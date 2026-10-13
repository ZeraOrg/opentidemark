// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/pipeline"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// Doctor thresholds (SPEC §8.5 item 8).
const (
	skewWarn         = 2 * time.Second
	skewFail         = 10 * time.Second // the lease safety margin (renew interval, §17.5)
	diskFailBytes    = 1 << 30
	diskWarnFraction = 0.10
	walWarnFraction  = 0.80
	percent          = 100
)

// Check statuses.
const (
	CheckOK   = "ok"
	CheckWarn = "warn"
	CheckFail = "fail"
)

// ErrDoctorFailed is returned when at least one check failed.
var ErrDoctorFailed = errors.New("doctor found problems")

// Check is one doctor result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Doctor checks connectivity, schema versions, WAL health, clock skew and disk space, prints
// the results (as JSON when asJSON) and returns ErrDoctorFailed when any check failed.
func (o *Ops) Doctor(ctx context.Context, asJSON bool) ([]Check, error) {
	var checks []Check
	add := func(name, status, format string, args ...any) {
		checks = append(checks, Check{Name: name, Status: status, Detail: fmt.Sprintf(format, args...)})
	}
	mode := "single node (sqlite)"
	if o.cfg.HA() {
		mode = "HA (postgres)"
	}
	add("config", CheckOK, "node %s, %s, clickhouse cluster %s, version %s", o.cfg.NodeID, mode, orNone(o.cfg.ClickHouse.Cluster), o.version)
	o.checkMeta(ctx, add)
	o.checkClickHouse(ctx, add)
	o.checkWAL(add)
	o.checkDisk(add)
	return checks, o.printChecks(checks, asJSON)
}

type addFunc func(name, status, format string, args ...any)

func (o *Ops) printChecks(checks []Check, asJSON bool) error {
	failed := false
	for _, c := range checks {
		failed = failed || c.Status == CheckFail
	}
	if asJSON {
		raw, err := json.MarshalIndent(checks, "", "  ")
		if err != nil {
			return fmt.Errorf("encode checks: %w", err)
		}
		o.printf("%s\n", raw)
	} else {
		for _, c := range checks {
			o.printf("[%-4s] %-22s %s\n", c.Status, c.Name, c.Detail)
		}
	}
	if failed {
		return ErrDoctorFailed
	}
	return nil
}

func (o *Ops) checkMeta(ctx context.Context, add addFunc) {
	cctx, cancel := context.WithTimeout(ctx, opsQueryTimeout)
	defer cancel()
	db, err := o.openMeta(cctx, false)
	if err != nil {
		add("metadata", CheckFail, "%v", err)
		return
	}
	defer closeQuietly(db)
	if err := db.Ping(cctx); err != nil {
		add("metadata", CheckFail, "%s unreachable: %v", db.Driver(), err)
		return
	}
	add("metadata", CheckOK, "%s reachable", db.Driver())
	o.checkMetaSchema(cctx, db, add)
	if t, ok, err := db.ServerTime(cctx); err != nil {
		add("clock skew (postgres)", CheckWarn, "%v", err)
	} else if ok {
		addSkew(add, "clock skew (postgres)", time.Since(t))
	}
}

func (o *Ops) checkMetaSchema(ctx context.Context, db *meta.DB, add addFunc) {
	states, err := db.MigrationStatus(ctx)
	if err != nil {
		add("metadata schema", CheckFail, "%v", err)
		return
	}
	pending := 0
	for _, s := range states {
		if !s.Applied {
			pending++
		}
	}
	info, _ := db.SchemaInfo(ctx) // zero before the first migration
	if err := meta.CheckBinaryCompatible(o.version, info.MinCompatibleBinary); err != nil {
		add("metadata schema", CheckFail, "%v", err)
		return
	}
	status := CheckOK
	if pending > 0 {
		status = CheckWarn
	}
	add("metadata schema", status, "version %d, %d pending (applied at next start or by `tidemark migrate`)", info.SchemaVersion, pending)
}

func (o *Ops) checkClickHouse(ctx context.Context, add addFunc) {
	c := o.cfg.ClickHouse
	for _, addr := range c.Addrs {
		conn, err := o.openWriterAt(ctx, []string{addr})
		if err != nil {
			add("clickhouse "+addr, CheckFail, "%v", err)
			continue
		}
		o.checkClickHouseNode(ctx, conn, addr, add)
		closeQuietly(conn)
	}
	rctx, cancel := context.WithTimeout(ctx, opsConnectTimeout)
	defer cancel()
	r, err := ch.Open(rctx, ch.Options{Addrs: c.Addrs, Database: c.Database, Username: c.ReaderUser, Password: c.ReaderPassword, TLS: c.TLS})
	if err != nil {
		add("clickhouse reader", CheckFail, "%v", err)
		return
	}
	closeQuietly(r)
	add("clickhouse reader", CheckOK, "user %s can connect", c.ReaderUser)
}

func (o *Ops) checkClickHouseNode(ctx context.Context, conn driver.Conn, addr string, add addFunc) {
	qctx, cancel := context.WithTimeout(ctx, opsQueryTimeout)
	defer cancel()
	var version string
	var now time.Time
	if err := conn.QueryRow(qctx, "SELECT version(), now64(3)").Scan(&version, &now); err != nil {
		add("clickhouse "+addr, CheckFail, "%v", err)
		return
	}
	add("clickhouse "+addr, CheckOK, "version %s", version)
	addSkew(add, "clock skew "+addr, time.Since(now))
	c := o.cfg.ClickHouse
	states, err := ch.NewMigrator(conn, c.Database, ch.WithCluster(c.Cluster)).Status(qctx)
	if err != nil {
		add("clickhouse schema "+addr, CheckFail, "%v", err)
		return
	}
	pending := 0
	for _, s := range states {
		if !s.Applied {
			pending++
		}
	}
	status := CheckOK
	if pending > 0 {
		status = CheckWarn
	}
	add("clickhouse schema "+addr, status, "%d of %d migrations applied", len(states)-pending, len(states))
	if c.Cluster != "" {
		var replicas uint64
		if err := conn.QueryRow(qctx, "SELECT count() FROM system.clusters WHERE cluster = ?", c.Cluster).Scan(&replicas); err != nil || replicas == 0 {
			add("clickhouse cluster "+addr, CheckFail, "cluster %q not defined on this server %v", c.Cluster, err)
			return
		}
		add("clickhouse cluster "+addr, CheckOK, "cluster %q has %d replicas", c.Cluster, replicas)
	}
}

func addSkew(add addFunc, name string, skew time.Duration) {
	abs := skew.Abs()
	status := CheckOK
	switch {
	case abs >= skewFail:
		status = CheckFail
	case abs >= skewWarn:
		status = CheckWarn
	}
	add(name, status, "%s (leases need < %s)", skew.Round(time.Millisecond), skewFail)
}

func (o *Ops) checkWAL(add addFunc) {
	if !o.cfg.WAL.Enabled {
		add("wal", CheckWarn, "disabled: acknowledged rows may be lost on crash")
		return
	}
	var total int64
	for _, signal := range []string{pipeline.SignalEvents, pipeline.SignalSpans, pipeline.SignalMetrics} {
		st, err := wal.Inspect(filepath.Join(o.cfg.WAL.Dir, signal))
		if err != nil {
			add("wal "+signal, CheckFail, "%v", err)
			continue
		}
		total += st.Bytes
		status := CheckOK
		if st.TornBytes > 0 {
			status = CheckWarn
		}
		add("wal "+signal, status, "%d segments, %d bytes not yet in clickhouse, %d torn bytes (truncated at next start), pending batch %t",
			st.Segments, st.Uncommitted, st.TornBytes, st.HasPending)
	}
	status := CheckOK
	if float64(total) >= walWarnFraction*float64(o.cfg.WAL.MaxBytes) {
		status = CheckWarn
	}
	add("wal size", status, "%d of %d bytes (%.1f%%); full ⇒ ingestion backpressure", total, o.cfg.WAL.MaxBytes,
		percent*float64(total)/float64(o.cfg.WAL.MaxBytes))
}

func (o *Ops) checkDisk(add addFunc) {
	dir := o.cfg.DataDir
	if _, err := os.Stat(dir); err != nil {
		add("disk "+dir, CheckFail, "%v", err)
		return
	}
	free, total, err := diskSpace(dir)
	if err != nil {
		add("disk "+dir, CheckWarn, "%v", err)
		return
	}
	status := CheckOK
	switch {
	case free < diskFailBytes:
		status = CheckFail
	case total > 0 && float64(free) < diskWarnFraction*float64(total):
		status = CheckWarn
	}
	add("disk "+dir, status, "%d MiB free of %d MiB", free>>20, total>>20)
}
