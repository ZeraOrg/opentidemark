// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), DBFileName), WithClock(func() time.Time { return t0 }))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DBFileName)
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	var n int
	known, err := LoadMigrations(DriverSQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := db2.sql.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(known) {
		t.Errorf("migrations recorded = %d, %v; want %d", n, err, len(known))
	}
}

func TestTimeHelpers(t *testing.T) {
	if encodeTimePtr(nil) != nil || encodeTimePtr(&time.Time{}) != nil {
		t.Error("nil time")
	}
	if !decodeTime("garbage").IsZero() {
		t.Error("garbage time")
	}
	if a, b := NewID(), NewID(); a == b {
		t.Error("ids collide")
	}
}
