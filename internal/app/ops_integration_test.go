// SPDX-License-Identifier: Apache-2.0

//go:build integration

package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/importer"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

// TestIntegrationOps runs migrate, doctor, backup and restore against a real ClickHouse with a
// "backups" disk (SPEC §8.5 item 8).
func TestIntegrationOps(t *testing.T) {
	env, ok := testutil.ClickHouseFromEnv()
	if !ok {
		t.Skip("TIDEMARK_TEST_CLICKHOUSE_ADDR not set")
	}
	ctx := context.Background()
	o, out := testOps(t, config.MapEnv{config.VarCHAddr: strings.Join(env.Addrs, ","), config.VarCHDatabase: env.Database,
		config.VarCHWriterUser: env.WriterUser, config.VarCHWriterPassword: env.WriterPassword,
		config.VarCHReaderUser: env.ReaderUser, config.VarCHReaderPassword: env.ReaderPassword})
	conn, err := o.openWriter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+env.Database+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := o.Migrate(ctx, MigrateApply); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	if _, err := o.Doctor(ctx, false); err != nil {
		t.Fatalf("doctor on a healthy setup: %v\n%s", err, out)
	}
	w, err := ch.NewWriter(conn, env.Database)
	if err != nil {
		t.Fatal(err)
	}
	ev := model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: time.Now().UTC(), Stream: "app", Message: "backed up"}
	if err := w.WriteEvents(ctx, []model.Event{ev}); err != nil {
		t.Fatal(err)
	}
	dir, err := o.Backup(ctx, BackupOptions{Out: t.TempDir()})
	if err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	m, _ := ReadBackupManifest(dir)
	if m.ClickHouse == nil && len(m.Warnings) == 1 && strings.Contains(m.Warnings[0], "no 'backups' disk") {
		if m.Meta.Export == "" {
			t.Fatalf("metadata part missing: %+v", m)
		}
		t.Skip("the ClickHouse server has no 'backups' disk (e.g. a plain CI service container); metadata backup verified")
	}
	if m.ClickHouse == nil || len(m.ClickHouse.Tables) < 3 {
		t.Fatalf("clickhouse part missing: %+v\n%s", m, out)
	}
	if err := conn.Exec(ctx, "DROP DATABASE "+env.Database+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if err := o.Migrate(ctx, MigrateApply); err != nil { // the server recreates empty tables
		t.Fatal(err)
	}
	if err := o.Restore(ctx, RestoreOptions{From: dir, Force: true, WithClickHouse: true}); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	var n uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM "+env.Database+".events WHERE message = 'backed up'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("restored rows %d %v", n, err)
	}
	if err := o.Restore(ctx, RestoreOptions{From: dir, Force: true, WithClickHouse: true}); err == nil {
		t.Error("restore into non-empty tables accepted")
	}
}

// TestIntegrationImportResumeNoDuplicates runs `tidemark import clef` in the foreground against
// a real ClickHouse, then replays the whole job as if the node had crashed before storing any
// checkpoint: the deterministic page tokens make ClickHouse drop every repeated insert
// (SPEC §8.5 item 7, §9.9).
func TestIntegrationImportResumeNoDuplicates(t *testing.T) {
	env, ok := testutil.ClickHouseFromEnv()
	if !ok {
		t.Skip("TIDEMARK_TEST_CLICKHOUSE_ADDR not set")
	}
	ctx := context.Background()
	o, out := testOps(t, config.MapEnv{config.VarCHAddr: strings.Join(env.Addrs, ","), config.VarCHDatabase: env.Database,
		config.VarCHWriterUser: env.WriterUser, config.VarCHWriterPassword: env.WriterPassword,
		config.VarCHReaderUser: env.ReaderUser, config.VarCHReaderPassword: env.ReaderPassword})
	if err := o.Migrate(ctx, MigrateApply); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	const lines = 12_345 // three pages of 5,000 lines
	stream := fmt.Sprintf("import-%d", time.Now().UnixNano())
	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, `{"@t":"2026-01-01T00:00:%02d.%03dZ","@m":"line %d","n":%d}`+"\n", i%60, i%1000, i, i)
	}
	file := filepath.Join(t.TempDir(), "events.clef")
	if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := o.Import(ctx, ImportRequest{Kind: importer.KindCLEFFile, Files: []string{file}, Stream: stream})
	if err != nil || job.Status != meta.ImportCompleted || job.RowsImported != lines {
		t.Fatalf("import: %v %+v\n%s", err, job, out)
	}
	conn, err := o.openWriter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	count := func() uint64 {
		var n uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM "+env.Database+".events WHERE stream = ?", stream).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != lines {
		t.Fatalf("rows after import = %d", n)
	}

	ienv, err := o.openImport(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ienv.close()
	jobs := meta.NewImportJobs(ienv.db)
	if err := jobs.MarkRunning(ctx, job.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.SaveProgress(ctx, job.ID, meta.ImportProgress{Checkpoint: json.RawMessage("{}")}); err != nil {
		t.Fatal(err)
	}
	if err := ienv.runner.RunJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if again, _ := jobs.Get(ctx, job.ID); again.Status != meta.ImportCompleted {
		t.Fatalf("replayed job %+v", again)
	}
	if n := count(); n != lines {
		t.Fatalf("rows after replaying the job = %d, want %d (duplicates)", n, lines)
	}
}
