// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/config"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/testutil"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// unreachableCH is a closed local port: ClickHouse checks fail fast.
const unreachableCH = "127.0.0.1:1"

func testOps(t *testing.T, env config.MapEnv) (*Ops, *bytes.Buffer) {
	t.Helper()
	if _, ok := env[config.VarDataDir]; !ok {
		env[config.VarDataDir] = t.TempDir()
	}
	if _, ok := env[config.VarCHAddr]; !ok {
		env[config.VarCHAddr] = unreachableCH
	}
	cfg, err := config.Load(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	return NewOps(cfg, "0.7.0", &out, slog.New(slog.NewTextHandler(io.Discard, nil))), &out
}

func seedUser(t *testing.T, db *meta.DB, name string) {
	t.Helper()
	if err := meta.NewUsers(db).Create(context.Background(), &meta.User{Username: name, PasswordHash: "h", Role: meta.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
}

func userNames(t *testing.T, o *Ops) []string {
	t.Helper()
	db, err := o.OpenMeta(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	users, err := meta.NewUsers(db).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func TestBackupAndRestoreSQLite(t *testing.T) {
	ctx := context.Background()
	o, out := testOps(t, config.MapEnv{})
	if _, err := o.Backup(ctx, BackupOptions{Out: t.TempDir(), MetaOnly: true}); !errors.Is(err, ErrNoMetadata) {
		t.Fatalf("backup before first start: %v", err)
	}
	db, err := o.OpenMeta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seedUser(t, db, "alice")
	_ = db.Close()

	if _, err := o.Backup(ctx, BackupOptions{Out: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "--meta-only") {
		t.Fatalf("backup with unreachable clickhouse: %v", err)
	}
	dir, err := o.Backup(ctx, BackupOptions{Out: t.TempDir(), MetaOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	m, err := ReadBackupManifest(dir)
	if err != nil || m.Meta.Driver != meta.DriverSQLite || m.Meta.NativeFile == "" || m.ClickHouse != nil || m.Meta.SchemaVersion == 0 {
		t.Fatalf("manifest %+v %v", m, err)
	}
	if !strings.Contains(out.String(), dir) {
		t.Errorf("output %q", out.String())
	}

	db, _ = o.OpenMeta(ctx)
	seedUser(t, db, "bob") // created after the backup
	_ = db.Close()
	if err := o.Restore(ctx, RestoreOptions{From: dir}); !errors.Is(err, ErrRestoreTargetExists) {
		t.Fatalf("restore over existing db without --force: %v", err)
	}
	if err := o.Restore(ctx, RestoreOptions{From: dir, Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := userNames(t, o); len(got) != 1 || got[0] != "alice" {
		t.Errorf("users after restore: %v", got)
	}
	safety, _ := filepath.Glob(filepath.Join(o.cfg.BackupDir, "meta-pre-restore-*.db"))
	if len(safety) != 1 {
		t.Errorf("safety copies: %v", safety)
	}
	db, _ = o.OpenMeta(ctx)
	seedUser(t, db, "erin")
	_ = db.Close()
	if err := o.Restore(ctx, RestoreOptions{From: dir, Force: true, WithClickHouse: true}); !errors.Is(err, ErrBadBackup) {
		t.Errorf("restore clickhouse from a meta-only backup: %v", err)
	}
	if got := userNames(t, o); len(got) != 2 {
		t.Errorf("a refused restore must not touch the metadata: %v", got)
	}
}

// TestRestoreFromExport restores a backup without a native file (taken from PostgreSQL) into
// SQLite through the JSON export.
func TestRestoreFromExport(t *testing.T) {
	ctx := context.Background()
	src, _ := testOps(t, config.MapEnv{})
	db, _ := src.OpenMeta(ctx)
	seedUser(t, db, "carol")
	_ = db.Close()
	dir, err := src.Backup(ctx, BackupOptions{Out: t.TempDir(), MetaOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	stripNativeFile(t, dir)
	dst, _ := testOps(t, config.MapEnv{})
	if err := dst.Restore(ctx, RestoreOptions{From: dir}); err != nil {
		t.Fatal(err)
	}
	if got := userNames(t, dst); len(got) != 1 || got[0] != "carol" {
		t.Errorf("users: %v", got)
	}
}

func stripNativeFile(t *testing.T, dir string) {
	t.Helper()
	m, err := ReadBackupManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, m.Meta.NativeFile))
	m.Meta.NativeFile, m.Meta.Driver = "", meta.DriverPostgres
	_ = os.Remove(filepath.Join(dir, backupManifestName))
	if err := writeJSONFile(filepath.Join(dir, backupManifestName), m); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreSQLiteToPostgres moves single-node metadata to PostgreSQL (the path to HA).
func TestRestoreSQLiteToPostgres(t *testing.T) {
	dsn := testutil.PostgresDSN(t)
	ctx := context.Background()
	src, _ := testOps(t, config.MapEnv{})
	db, _ := src.OpenMeta(ctx)
	seedUser(t, db, "dave")
	_ = db.Close()
	dir, err := src.Backup(ctx, BackupOptions{Out: t.TempDir(), MetaOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	pg, _ := testOps(t, config.MapEnv{config.VarMetaDriver: "postgres", config.VarMetaDSN: testutil.PostgresSchemaDSN(t, dsn)})
	if err := pg.Restore(ctx, RestoreOptions{From: dir}); err != nil {
		t.Fatal(err)
	}
	if got := userNames(t, pg); len(got) != 1 || got[0] != "dave" {
		t.Errorf("users: %v", got)
	}
	if err := pg.Restore(ctx, RestoreOptions{From: dir}); !errors.Is(err, meta.ErrNotEmpty) {
		t.Errorf("restore into non-empty postgres: %v", err)
	}
	pgDir, err := pg.Backup(ctx, BackupOptions{Out: t.TempDir(), MetaOnly: true})
	if m, _ := ReadBackupManifest(pgDir); err != nil || m.Meta.Driver != meta.DriverPostgres || m.Meta.NativeFile != "" {
		t.Errorf("postgres backup %+v %v", m, err)
	}
}

func TestReadBackupManifestRejectsHostileNames(t *testing.T) {
	good := BackupManifest{Format: BackupFormat, Meta: BackupMeta{Driver: "sqlite", Export: backupExportName},
		ClickHouse: &BackupClickHouse{Database: "tidemark", Disk: chBackupDisk, Name: "tidemark-20261013T101010Z", Tables: []string{"events"}}}
	mutations := map[string]func(m *BackupManifest){
		"format":        func(m *BackupManifest) { m.Format = "other/1" },
		"export path":   func(m *BackupManifest) { m.Meta.Export = "../../etc/passwd" },
		"native path":   func(m *BackupManifest) { m.Meta.NativeFile = "../x.db" },
		"database":      func(m *BackupManifest) { m.ClickHouse.Database = "x; DROP DATABASE y" },
		"disk":          func(m *BackupManifest) { m.ClickHouse.Disk = "default" },
		"name":          func(m *BackupManifest) { m.ClickHouse.Name = "x') SETTINGS a=1 --" },
		"table":         func(m *BackupManifest) { m.ClickHouse.Tables = []string{"events, TABLE system.users"} },
		"no tables":     func(m *BackupManifest) { m.ClickHouse.Tables = nil },
		"valid control": nil,
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := good
			c := *good.ClickHouse
			m.ClickHouse = &c
			if mutate != nil {
				mutate(&m)
			}
			dir := t.TempDir()
			if err := writeJSONFile(filepath.Join(dir, backupManifestName), m); err != nil {
				t.Fatal(err)
			}
			_, err := ReadBackupManifest(dir)
			if (err == nil) != (mutate == nil) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, err := ReadBackupManifest(t.TempDir()); !errors.Is(err, ErrBadBackup) {
		t.Errorf("missing manifest: %v", err)
	}
}

func TestMigrateStatusAndDryRun(t *testing.T) {
	ctx := context.Background()
	o, out := testOps(t, config.MapEnv{})
	if err := o.Migrate(ctx, MigrateStatus); err == nil {
		t.Error("unreachable clickhouse not reported")
	}
	if !strings.Contains(out.String(), "does not exist yet") {
		t.Errorf("status before first start: %q", out.String())
	}
	if err := os.WriteFile(o.sqlitePath(), nil, 0o600); err != nil { // an empty file is an empty SQLite database
		t.Fatal(err)
	}
	out.Reset()
	_ = o.Migrate(ctx, MigrateDryRun)
	if !strings.Contains(out.String(), "0001") || !strings.Contains(out.String(), "would run") ||
		!strings.Contains(out.String(), "CREATE TABLE") {
		t.Errorf("dry run output %q", out.String())
	}
	if st, _ := os.Stat(o.cfg.BackupDir); st != nil {
		t.Error("dry run wrote a backup")
	}
	_ = o.Migrate(ctx, MigrateApply) // metadata applies; clickhouse is unreachable
	out.Reset()
	_ = o.Migrate(ctx, MigrateStatus)
	if strings.Contains(out.String(), "pending") || !strings.Contains(out.String(), "applied") {
		t.Errorf("status after apply %q", out.String())
	}
}

func TestDoctor(t *testing.T) {
	ctx := context.Background()
	o, out := testOps(t, config.MapEnv{})
	db, _ := o.OpenMeta(ctx)
	_ = db.Close()
	l, _, err := wal.Open(filepath.Join(o.cfg.WAL.Dir, "events"), wal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(ctx, []byte("pending row")); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	checks, err := o.Doctor(ctx, true)
	if !errors.Is(err, ErrDoctorFailed) {
		t.Fatalf("unreachable clickhouse must fail: %v", err)
	}
	var printed []Check
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil || len(printed) != len(checks) {
		t.Fatalf("json output: %v %q", err, out.String())
	}
	status := map[string]string{}
	for _, c := range checks {
		status[c.Name] = c.Status
	}
	want := map[string]string{"config": CheckOK, "metadata": CheckOK, "metadata schema": CheckOK,
		"clickhouse " + unreachableCH: CheckFail, "clickhouse reader": CheckFail, "wal events": CheckOK, "wal size": CheckOK}
	for name, s := range want {
		if status[name] != s {
			t.Errorf("%s = %q, want %q (all: %v)", name, status[name], s, checks)
		}
	}
	for _, c := range checks {
		if c.Name == "wal events" && !strings.Contains(c.Detail, "19 bytes not yet in clickhouse") {
			t.Errorf("wal detail %q", c.Detail)
		}
	}
}
