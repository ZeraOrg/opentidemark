// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/cluster"
	"github.com/ZeraOrg/tidemark/internal/config"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/pipeline"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// Startup coordination of ClickHouse migrations across nodes (SPEC §17.5).
const (
	chMigrationLeaseTTL = 10 * time.Minute
	chMigrationPoll     = 2 * time.Second
)

// openMeta opens the metadata database of the configured driver with backups and downgrade
// protection (SPEC §8.4, §8.5).
func (a *App) openMeta(ctx context.Context) (*meta.DB, error) {
	opts := []meta.Option{meta.WithBinaryVersion(a.version), meta.WithBackupDir(a.cfg.BackupDir)}
	var (
		db  *meta.DB
		err error
	)
	if a.cfg.HA() {
		db, err = meta.OpenPostgres(ctx, a.cfg.Meta.DSN, opts...)
	} else {
		db, err = meta.Open(ctx, filepath.Join(a.cfg.DataDir, meta.DBFileName), opts...)
	}
	if err != nil {
		return nil, err
	}
	if rep := db.Migrations(); len(rep.Applied) > 0 {
		a.logger.Info("applied metadata migrations", "from", rep.FromVersion, "versions", rep.Applied, "backup", rep.BackupPath)
	}
	return db, nil
}

// migrateClickHouse applies the ClickHouse schema. In HA mode only the holder of the
// "clickhouse-migrations" lease migrates; the other nodes wait until nothing is pending.
func (a *App) migrateClickHouse(ctx context.Context, conn driver.Conn) error {
	m := ch.NewMigrator(conn, a.cfg.ClickHouse.Database, ch.WithCluster(a.cfg.ClickHouse.Cluster))
	if !a.cfg.HA() {
		return a.applyCH(ctx, m)
	}
	leases := meta.NewLeases(a.meta)
	deadline := time.Now().Add(clickhouseStartupWait)
	for {
		ok, err := leases.TryAcquire(ctx, cluster.LeaseCHMigrations, a.cfg.NodeID, time.Now(), chMigrationLeaseTTL)
		if err != nil {
			return fmt.Errorf("clickhouse migration lease: %w", err)
		}
		if ok {
			err := a.applyCH(ctx, m)
			if rerr := leases.Release(context.WithoutCancel(ctx), cluster.LeaseCHMigrations, a.cfg.NodeID, time.Now()); rerr != nil {
				a.logger.Warn("releasing the clickhouse migration lease failed", "err", rerr)
			}
			return err
		}
		pending, err := m.Pending(ctx)
		if err == nil && len(pending) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for another node to migrate clickhouse")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(chMigrationPoll):
		}
	}
}

func (a *App) applyCH(ctx context.Context, m *ch.Migrator) error {
	applied, err := m.Up(ctx)
	if err != nil {
		return fmt.Errorf("clickhouse migrations: %w", err)
	}
	if len(applied) > 0 {
		a.logger.Info("applied clickhouse migrations", "versions", applied)
	}
	return nil
}

// writerOptions returns the insert settings of the configured mode.
func (a *App) writerOptions() []ch.WriterOption { return writerOptionsFor(a.cfg.ClickHouse) }

// writerOptionsFor returns the writer options of c (insert_quorum in cluster mode).
func writerOptionsFor(c config.ClickHouse) []ch.WriterOption {
	if c.Cluster == "" {
		return nil
	}
	return []ch.WriterOption{ch.WithInsertQuorum(c.InsertQuorum)}
}

// openWAL opens the per-signal logs under TIDEMARK_WAL_DIR with one shared size budget.
func (a *App) openWAL() (*pipeline.WALJournals, error) {
	if !a.cfg.WAL.Enabled {
		return nil, nil
	}
	budget := wal.NewBudget(a.cfg.WAL.MaxBytes)
	opts := wal.Options{Sync: wal.SyncMode(a.cfg.WAL.Sync), Budget: budget}
	j := &pipeline.WALJournals{IsPermanent: ch.IsPermanent}
	for _, s := range []struct {
		name string
		dst  *pipeline.Journal
	}{{pipeline.SignalEvents, &j.Events}, {pipeline.SignalSpans, &j.Spans}, {pipeline.SignalMetrics, &j.Metrics}} {
		l, rep, err := wal.Open(filepath.Join(a.cfg.WAL.Dir, s.name), opts)
		if err != nil {
			a.closeJournals()
			return nil, fmt.Errorf("open wal %s: %w", s.name, err)
		}
		if rep.TruncatedBytes > 0 {
			a.logger.Warn("wal tail was torn or corrupt and has been truncated", "signal", s.name,
				"bytes", rep.TruncatedBytes, "at", rep.TruncatedAt)
		}
		a.journals = append(a.journals, l)
		a.metrics.WALBytes(s.name, l.Size)
		*s.dst = l
	}
	if a.cfg.WAL.Sync == string(wal.SyncNone) {
		a.logger.Warn("TIDEMARK_WAL_SYNC=none: acknowledged rows may be lost on power failure (testing only)")
	}
	return j, nil
}

// closeJournals closes WAL logs opened before a startup failure (the pipeline owns them after
// a successful start).
func (a *App) closeJournals() {
	for _, l := range a.journals {
		if err := l.Close(); err != nil {
			a.logger.Warn("close wal", "err", err)
		}
	}
	a.journals = nil
}

// buildCluster creates the leader elector and the invalidation bus (SPEC §17.5).
func (a *App) buildCluster() {
	a.elector = cluster.NewElector(meta.NewLeases(a.meta), auth.SystemClock{}, cluster.ElectorConfig{NodeID: a.cfg.NodeID}, a.logger)
	if a.cfg.HA() {
		a.bus = cluster.NewPGBus(a.cfg.Meta.DSN, a.meta, a.logger)
		return
	}
	a.bus = cluster.NewLocalBus()
}

// keyCacheOptions shortens the connection-key cache in HA mode.
func (a *App) keyCacheOptions() []auth.ConnAuthOption {
	if a.cfg.HA() {
		return []auth.ConnAuthOption{auth.WithKeyCacheTTL(auth.HAKeyCacheTTL)}
	}
	return nil
}

// Leader exposes lease-based leadership for singleton workers (alerting, retention, stats,
// purge, imports).
func (a *App) Leader() cluster.Leader { return a.elector }
