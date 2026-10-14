// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reopener opens the same underlying database (SQLite file or PostgreSQL schema) repeatedly.
type reopener func(opts ...Option) (*DB, error)

type storeFactory func(t *testing.T) reopener

func sqliteStore(t *testing.T) reopener {
	path := filepath.Join(t.TempDir(), DBFileName)
	return func(opts ...Option) (*DB, error) { return Open(context.Background(), path, opts...) }
}

func postgresStore(dsn string) storeFactory {
	return func(t *testing.T) reopener {
		db := openPostgresTest(t, dsn, WithoutMigrations())
		var schema string
		if err := db.sql.QueryRowContext(context.Background(), `SELECT current_schema()`).Scan(&schema); err != nil {
			t.Fatal(err)
		}
		scoped := withSearchPath(t, dsn, schema)
		return func(opts ...Option) (*DB, error) { return OpenPostgres(context.Background(), scoped, opts...) }
	}
}

var migrationCases = []struct {
	name string
	fn   func(t *testing.T, store storeFactory)
}{
	{"rollback_on_failure", migrationRollback},
	{"downgrade_protection", migrationDowngrade},
	{"backup_before_migration", migrationBackup},
	{"backup_failure_aborts", migrationBackupFailure},
}

func TestMigrationsSQLite(t *testing.T) {
	for _, c := range migrationCases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, sqliteStore) })
	}
}

func TestMigrationsPostgres(t *testing.T) {
	dsn := os.Getenv("TIDEMARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TIDEMARK_TEST_POSTGRES_DSN not set")
	}
	for _, c := range migrationCases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, postgresStore(dsn)) })
	}
}

func mustOpen(t *testing.T, open reopener, opts ...Option) *DB {
	t.Helper()
	db, err := open(opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

// withMigrationSet replaces the embedded migrations (tests only).
func withMigrationSet(ms []Migration) Option { return func(db *DB) { db.opts.migrations = ms } }

func headPlus(t *testing.T, driver string, extra Migration) []Migration {
	t.Helper()
	ms, err := LoadMigrations(driver)
	if err != nil {
		t.Fatal(err)
	}
	extra.Version = ms[len(ms)-1].Version + 1
	return append(ms, extra)
}

func migrationRollback(t *testing.T, store storeFactory) {
	ctx := context.Background()
	open := store(t)
	db := mustOpen(t, open)
	driver := db.Driver()
	_ = db.Close()
	bad := Migration{Name: "9999_bad.sql", MinBinary: "0.1.0",
		SQL: "CREATE TABLE half_done (id TEXT PRIMARY KEY);\nINSERT INTO no_such_table VALUES (1);"}
	_, err := open(withMigrationSet(headPlus(t, driver, bad)))
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("failing migration: %v", err)
	}
	db = mustOpen(t, open)
	defer db.Close()
	ok, err := db.tableExists(ctx, "half_done")
	if err != nil || ok {
		t.Errorf("partial migration left table behind: %v %v", ok, err)
	}
	status, _ := db.MigrationStatus(ctx)
	for _, s := range status {
		if s.Name == "" {
			t.Errorf("failed migration %d recorded as applied", s.Version)
		}
	}
}

func migrationDowngrade(t *testing.T, store storeFactory) {
	ctx := context.Background()
	open := store(t)
	db := mustOpen(t, open, WithBinaryVersion("0.2.1"))
	info, err := db.SchemaInfo(ctx)
	if err != nil || info.LastMigratedBy != "0.2.1" {
		t.Fatalf("schema info: %+v %v", info, err)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE schema_info SET min_compatible_binary = ?`, "9.0.0"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := open(WithBinaryVersion("0.2.1")); !errors.Is(err, ErrIncompatibleBinary) {
		t.Fatalf("older binary: %v", err)
	}
	for _, v := range []string{"9.0.0", "v9.1.0-rc1", "dev", ""} {
		db, err := open(WithBinaryVersion(v))
		if err != nil {
			t.Errorf("binary %q refused: %v", v, err)
			continue
		}
		_ = db.Close()
	}
}

func migrationBackup(t *testing.T, store storeFactory) {
	ctx := context.Background()
	open := store(t)
	dir := t.TempDir()
	d0 := mustOpen(t, open, WithBackupDir(dir))
	driver := d0.Driver()
	if d0.Migrations().BackupPath != "" {
		t.Errorf("a fresh database needs no backup: %+v", d0.Migrations())
	}
	_ = d0.Close()
	first, err := LoadMigrations(driver)
	if err != nil {
		t.Fatal(err)
	}
	extra := Migration{Name: "9999_extra.sql", MinBinary: "0.1.0", SQL: "CREATE TABLE extra_t (id TEXT PRIMARY KEY);"}
	db, err := open(withMigrationSet(headPlus(t, driver, extra)), WithBackupDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rep := db.Migrations()
	if rep.BackupPath == "" || len(rep.Applied) != 1 || rep.FromVersion != first[len(first)-1].Version {
		t.Fatalf("report: %+v", rep)
	}
	want := fmt.Sprintf("meta-%04d-", rep.FromVersion)
	if !strings.HasPrefix(filepath.Base(rep.BackupPath), want) {
		t.Errorf("backup name %s, want prefix %s", rep.BackupPath, want)
	}
	if st, err := os.Stat(rep.BackupPath); err != nil || st.Size() == 0 {
		t.Errorf("backup file: %v", err)
	}
	if db.Driver() == DriverSQLite {
		assertSQLiteBackupLacks(ctx, t, rep.BackupPath, "extra_t")
	}
}

func assertSQLiteBackupLacks(ctx context.Context, t *testing.T, path, table string) {
	t.Helper()
	b, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&n); err != nil || n != 0 {
		t.Errorf("backup contains %s (n=%d, %v)", table, n, err)
	}
}

func migrationBackupFailure(t *testing.T, store storeFactory) {
	open := store(t)
	db := mustOpen(t, open)
	driver := db.Driver()
	_ = db.Close()
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := Migration{Name: "9999_extra.sql", MinBinary: "0.1.0", SQL: "CREATE TABLE extra_t (id TEXT PRIMARY KEY);"}
	if _, err := open(withMigrationSet(headPlus(t, driver, extra)), WithBackupDir(notADir)); err == nil ||
		!strings.Contains(err.Error(), "backup") {
		t.Fatalf("backup failure must abort: %v", err)
	}
	db = mustOpen(t, open)
	defer db.Close()
	if ok, _ := db.tableExists(context.Background(), "extra_t"); ok {
		t.Error("migration ran although the backup failed")
	}
}

func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 13 {
		p := filepath.Join(dir, fmt.Sprintf("meta-0001-%02d.db", i))
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		_ = os.Chtimes(p, mt, mt)
	}
	_ = os.WriteFile(filepath.Join(dir, "other.txt"), []byte("x"), 0o600)
	if err := PruneBackups(dir, DefaultKeepBackups); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != DefaultKeepBackups+1 {
		t.Fatalf("kept %d entries", len(entries))
	}
	if _, err := os.Stat(filepath.Join(dir, "meta-0001-00.db")); !errors.Is(err, os.ErrNotExist) {
		t.Error("oldest backup kept")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.4.0", "0.4.0", 0}, {"0.4.0", "0.10.0", -1}, {"v1.2.3", "1.2.2", 1}, {"1.0.0-rc1", "1.0.0", 0},
		{"dev", "1.0.0", 0}, {"1.0", "1.0.0", 0},
	}
	for _, tc := range tests {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
	if err := CheckBinaryCompatible("0.3.0", "0.4.0"); !errors.Is(err, ErrIncompatibleBinary) {
		t.Errorf("old binary: %v", err)
	}
}

func TestLoadMigrationsDialectsMatch(t *testing.T) {
	lite, err := LoadMigrations(DriverSQLite)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := LoadMigrations(DriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	if len(lite) != len(pg) {
		t.Fatalf("sqlite has %d migrations, postgres %d", len(lite), len(pg))
	}
	for i := range lite {
		if lite[i].Version != pg[i].Version || lite[i].Name != pg[i].Name || lite[i].MinBinary != pg[i].MinBinary {
			t.Errorf("migration %d differs: %s/%s vs %s/%s", i, lite[i].Name, lite[i].MinBinary, pg[i].Name, pg[i].MinBinary)
		}
	}
	if _, err := LoadMigrations("oracle"); err == nil {
		t.Error("unknown driver accepted")
	}
}
