// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The repository contract suite (SPEC §8.4) runs against SQLite always and against PostgreSQL
// when TIDEMARK_TEST_POSTGRES_DSN is set (each test gets its own schema).

type opener func(t *testing.T) *DB

var contractCases = []struct {
	name string
	fn   func(t *testing.T, open opener)
}{
	{"users", contractUsers},
	{"user_delete_transfer", contractUserDeleteTransfer},
	{"sessions", contractSessions},
	{"connections", contractConnections},
	{"api_tokens", contractAPITokens},
	{"signals_dashboards_channels", contractSignalsDashboardsChannels},
	{"alert_rules", contractAlertRules},
	{"streams_indexed_purge", contractStreamsIndexedPurge},
	{"audit_settings", contractAuditAndSettings},
	{"teams", contractTeams},
	{"leases", contractLeases},
	{"import_jobs", contractImportJobs},
	{"imported_signals", contractImportedSignals},
	{"schema_info", contractSchemaInfo},
	{"export_import", contractExportImport},
}

func runContract(t *testing.T, open opener) {
	for _, c := range contractCases {
		t.Run(c.name, func(t *testing.T) { c.fn(t, open) })
	}
}

func TestContractSQLite(t *testing.T) { runContract(t, openTest) }

func TestContractPostgres(t *testing.T) {
	dsn := os.Getenv("TIDEMARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TIDEMARK_TEST_POSTGRES_DSN not set")
	}
	runContract(t, func(t *testing.T) *DB { return openPostgresTest(t, dsn, WithClock(func() time.Time { return t0 })) })
}

// openPostgresTest creates a fresh schema and opens the database with search_path set to it.
func openPostgresTest(t *testing.T, dsn string, opts ...Option) *DB {
	t.Helper()
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "tm_test_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		_ = admin.Close()
	})
	db, err := OpenPostgres(ctx, withSearchPath(t, dsn, schema), opts...)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}

func contractLeases(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewLeases(open(t))
	ttl := 30 * time.Second
	ok, err := r.TryAcquire(ctx, "alerting", "node-a", t0, ttl)
	if err != nil || !ok {
		t.Fatalf("first acquire: %v %v", ok, err)
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-b", t0.Add(10*time.Second), ttl); ok {
		t.Fatal("node-b acquired a live lease")
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-a", t0.Add(10*time.Second), ttl); !ok {
		t.Fatal("holder could not renew")
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-b", t0.Add(39*time.Second), ttl); ok {
		t.Fatal("renewed lease was taken before expiry")
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-b", t0.Add(40*time.Second), ttl); !ok {
		t.Fatal("expired lease was not taken over")
	}
	if err := r.Release(ctx, "alerting", "node-a", t0.Add(41*time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-a", t0.Add(41*time.Second), ttl); ok {
		t.Fatal("release by a non-holder must not free the lease")
	}
	if err := r.Release(ctx, "alerting", "node-b", t0.Add(42*time.Second)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.TryAcquire(ctx, "alerting", "node-a", t0.Add(42*time.Second), ttl); !ok {
		t.Fatal("released lease not available")
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 1 || list[0].Holder != "node-a" {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func contractImportJobs(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewImportJobs(open(t))
	j := &ImportJob{Kind: "seq", Name: "seq prod", Config: json.RawMessage(`{"url":"https://seq"}`), RequestedBy: "u1"}
	if err := r.Create(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(ctx, &ImportJob{Kind: "bogus", RequestedBy: "u1"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if err := r.MarkRunning(ctx, j.ID, t0); err != nil {
		t.Fatal(err)
	}
	p := ImportProgress{Checkpoint: json.RawMessage(`{"afterId":"event-1"}`), RowsImported: 5000, RowsTotal: 9000, Errors: 1, LastError: "x"}
	if err := r.SaveProgress(ctx, j.ID, p); err != nil {
		t.Fatal(err)
	}
	if err := r.SetWarnings(ctx, j.ID, []string{"older than retention"}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, j.ID)
	if err != nil || got.Status != ImportRunning || got.RowsImported != 5000 || got.RowsTotal != 9000 ||
		string(got.Checkpoint) != `{"afterId":"event-1"}` || len(got.Warnings) != 1 || got.StartedAt == nil {
		t.Fatalf("get: %+v %v", got, err)
	}
	runnable, _ := r.ListRunnable(ctx)
	if len(runnable) != 1 {
		t.Errorf("runnable = %d", len(runnable))
	}
	if err := r.RequestCancel(ctx, j.ID, t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(ctx, j.ID); !got.CancelRequested || got.Status != ImportRunning {
		t.Errorf("running job cancel: %+v", got)
	}
	if err := r.Finish(ctx, j.ID, ImportCancelled, "", t0); err != nil {
		t.Fatal(err)
	}
	q := &ImportJob{Kind: "elastic", RequestedBy: "u1"}
	_ = r.Create(ctx, q)
	_ = r.RequestCancel(ctx, q.ID, t0)
	if got, _ := r.Get(ctx, q.ID); got.Status != ImportCancelled || got.FinishedAt == nil {
		t.Errorf("queued job cancel: %+v", got)
	}
	if list, _ := r.List(ctx, 10); len(list) != 2 {
		t.Errorf("list = %d", len(list))
	}
	if err := r.RequestCancel(ctx, "missing", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel missing: %v", err)
	}
}

func contractImportedSignals(t *testing.T, open opener) {
	ctx := context.Background()
	r := NewSignals(open(t))
	s := &Signal{Name: "kibana: errors", TQL: "level:error", OwnerID: "u1", Shared: true, Source: "logs"}
	if err := r.CreateImported(ctx, s, true); err != nil {
		t.Fatal(err)
	}
	if d, err := r.IsDisabled(ctx, s.ID); err != nil || !d {
		t.Errorf("disabled = %v %v", d, err)
	}
	plain := &Signal{Name: "plain", TQL: "*", OwnerID: "u1", Source: "logs"}
	_ = r.Create(ctx, plain)
	if d, _ := r.IsDisabled(ctx, plain.ID); d {
		t.Error("regular signal is disabled")
	}
}

func contractSchemaInfo(t *testing.T, open opener) {
	ctx := context.Background()
	db := open(t)
	info, err := db.SchemaInfo(ctx)
	known, _ := LoadMigrations(db.Driver())
	if err != nil || info.SchemaVersion != known[len(known)-1].Version || info.MinCompatibleBinary == "" {
		t.Fatalf("schema info: %+v %v", info, err)
	}
	status, err := db.MigrationStatus(ctx)
	if err != nil || len(status) != len(known) {
		t.Fatalf("status: %+v %v", status, err)
	}
	for _, s := range status {
		if !s.Applied {
			t.Errorf("migration %d not applied", s.Version)
		}
	}
}

func contractExportImport(t *testing.T, open opener) {
	ctx := context.Background()
	src := open(t)
	u := &User{Username: "admin", PasswordHash: "h", Role: RoleAdmin}
	_ = NewUsers(src).Create(ctx, u)
	_ = NewSessions(src).Create(ctx, Session{ID: "s1", UserID: u.ID, CreatedAt: t0, LastSeenAt: t0, ExpiresAt: t0})
	_ = NewAuditLog(src).Append(ctx, &AuditEntry{Actor: u.ID, Action: "x"})
	v := 1.5
	rule := &AlertRule{Name: "r", Enabled: true, Doc: json.RawMessage(`{}`), State: "ok", LastValue: &v}
	_ = NewAlertRules(src).Create(ctx, rule)
	var buf bytes.Buffer
	if err := src.ExportJSON(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	dst := open(t)
	if err := dst.ImportJSON(ctx, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got, err := NewUsers(dst).GetByUsername(ctx, "ADMIN"); err != nil || got.ID != u.ID {
		t.Errorf("imported user: %+v %v", got, err)
	}
	if got, err := NewAlertRules(dst).Get(ctx, rule.ID); err != nil || got.LastValue == nil || *got.LastValue != v {
		t.Errorf("imported rule: %+v %v", got, err)
	}
	if err := dst.ImportJSON(ctx, bytes.NewReader(buf.Bytes())); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("second import: %v", err)
	}
	dir := t.TempDir()
	p, err := src.Backup(ctx, dir, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil || filepath.Dir(p) != dir {
		t.Errorf("backup %s: %v", p, err)
	}
}
