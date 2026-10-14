// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

type adminEnv struct {
	conn   driver.Conn // writer
	reader driver.Conn
	admin  *Admin
	db     string
	day0   time.Time // two whole UTC days ago
	day1   time.Time // yesterday
}

func setupAdmin(t *testing.T) *adminEnv {
	t.Helper()
	conn, env := setup(t)
	ctx := context.Background()
	if _, err := NewMigrator(conn, env.Database).Up(ctx); err != nil {
		t.Fatal(err)
	}
	reader, err := Open(ctx, Options{Addrs: env.Addrs, Database: env.Database, Username: env.ReaderUser,
		Password: env.ReaderPassword, QueryTimeout: 30 * time.Second, MaxMemoryBytes: DefaultReaderMemory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	admin, err := NewAdmin(conn, env.Database, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	e := &adminEnv{conn: conn, reader: reader, admin: admin, db: env.Database, day0: today.Add(-48 * time.Hour), day1: today.Add(-24 * time.Hour)}
	e.seed(t)
	return e
}

func (e *adminEnv) seed(t *testing.T) {
	t.Helper()
	w, err := NewWriter(e.conn, e.db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ev := func(stream string, at time.Time, level model.Level, svc string, props map[string]any) model.Event {
		return model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: at, IngestedAt: now, Stream: stream, Level: level,
			Service: svc, Message: "m", Protocol: "native", Properties: props}
	}
	h := 10 * time.Hour
	events := []model.Event{
		ev("api", e.day1.Add(h), model.Debug, "checkout", map[string]any{"user": map[string]any{"id": int64(42)}}),
		ev("api", e.day1.Add(h+time.Minute), model.Debug, "checkout", map[string]any{"user": map[string]any{"id": int64(7)}}),
		ev("api", e.day1.Add(h+2*time.Minute), model.Error, "checkout", map[string]any{"user": map[string]any{"id": int64(42)}}),
		ev("web", e.day1.Add(h), model.Information, "frontend", map[string]any{"path": "/"}),
		ev("web", e.day0.Add(h), model.Information, "frontend", map[string]any{"path": "/a"}),
		ev("api", e.day0.Add(h), model.Warning, "checkout", nil),
	}
	if err := w.WriteEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
	spans := []model.Span{{TraceID: "5b8efff798038103d269b633813fc60c", SpanID: "eee19b7ec3c1b174", Stream: "api", Service: "checkout",
		Name: "GET /", Kind: "server", Start: e.day1.Add(h), Duration: time.Millisecond, StatusCode: "ok"}}
	if err := w.WriteSpans(context.Background(), spans); err != nil {
		t.Fatal(err)
	}
	points := []model.MetricPoint{{Timestamp: e.day1.Add(h), Stream: "web", Service: "frontend", Name: "cpu", Kind: "gauge", Value: 1}}
	if err := w.WriteMetrics(context.Background(), points); err != nil {
		t.Fatal(err)
	}
}

func (e *adminEnv) count(t *testing.T, table, where string, args ...any) uint64 {
	t.Helper()
	var n uint64
	if err := e.conn.QueryRow(context.Background(), "SELECT count() FROM "+e.db+"."+table+" WHERE "+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *adminEnv) createTable(t *testing.T, table string) string {
	t.Helper()
	var ddl string
	if err := e.conn.QueryRow(context.Background(), "SHOW CREATE TABLE "+e.db+"."+table).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	return ddl
}

func TestIntegrationStreamStatsAndMappings(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	stats, err := e.admin.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]store.StreamStats{}
	for _, s := range stats {
		byKey[s.Stream+"/"+string(s.Source)] = s
	}
	api := byKey["api/logs"]
	if api.Rows != 4 || !api.Estimated || api.CompressedBytes == 0 || api.UncompressedBytes == 0 || api.RatePer5Min <= 0 ||
		!api.Oldest.Equal(e.day0.Add(10*time.Hour)) || !slices.Equal(api.TopServices, []string{"checkout"}) {
		t.Errorf("api logs %+v", api)
	}
	if byKey["web/logs"].Rows != 2 || byKey["api/spans"].Rows != 1 || byKey["web/metrics"].Rows != 1 {
		t.Errorf("stats %+v", stats)
	}
	var total uint64
	if err := e.conn.QueryRow(ctx, "SELECT sum(data_compressed_bytes) FROM system.parts WHERE database = ? AND table = 'events' AND active", e.db).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if sum := api.CompressedBytes + byKey["web/logs"].CompressedBytes; sum > total || sum < total-2 {
		t.Errorf("apportioned %d of %d", sum, total)
	}
	maps, err := e.admin.Mappings(ctx, model.AllStreams(), "api")
	if err != nil || len(maps) != 1 || maps[0].Key != "user.id" || maps[0].Type != "number" || maps[0].Hits != 3 || maps[0].Source != model.SourceLogs {
		t.Errorf("mappings %+v %v", maps, err)
	}
	if maps, err := e.admin.Mappings(ctx, model.OnlyStreams("web"), "api"); err != nil || len(maps) != 0 {
		t.Errorf("mappings outside scope %+v %v", maps, err)
	}
	if _, err := e.admin.Mappings(ctx, model.StreamScope{}, "api"); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("mappings without scope: %v", err)
	}
}

// TestIntegrationRetentionThroughService applies stream retention through the service layer
// and checks the TTL in SHOW CREATE TABLE.
func TestIntegrationRetentionThroughService(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	db, err := meta.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	streams := meta.NewStreams(db)
	registry := service.NewStreamRegistry(streams, 10, logger)
	if err := registry.Load(ctx); err != nil {
		t.Fatal(err)
	}
	sync := service.NewRetentionSync(meta.NewSettings(db), streams, e.admin, logger)
	audit := service.NewAuditor(meta.NewAuditLog(db), logger, time.Now)
	svc := service.NewStreamAdmin(service.StreamAdminDeps{Repo: streams, Registry: registry, Storage: e.admin, Grants: meta.NewTeams(db), Retention: sync,
		Indexed: service.NewIndexedFields(meta.NewIndexedFields(db), e.admin, audit), Audit: audit, Now: time.Now, Logger: logger})
	days, spanDays := 90, 14
	dp, sp := &days, &spanDays
	if _, err := svc.Create(ctx, service.SystemActor, service.StreamInput{Name: "api", RetentionDays: &dp, SpansRetentionDays: &sp,
		LevelRules: &[]meta.LevelRule{{Levels: []string{"verbose", "debug"}, Days: 3}}}); err != nil {
		t.Fatal(err)
	}
	events, spans := e.createTable(t, "events"), e.createTable(t, "spans")
	for _, want := range []string{"TTL", "toIntervalDay(3)", "level_num IN (0, 1)", "toIntervalDay(90)", "stream = 'api'", "NOT IN ('api')"} {
		if !strings.Contains(events, want) {
			t.Errorf("events TTL lacks %q:\n%s", want, events)
		}
	}
	if !strings.Contains(spans, "toIntervalDay(14)") || !strings.Contains(spans, "toIntervalDay(7)") {
		t.Errorf("spans TTL:\n%s", spans)
	}
	var cleared *int
	if _, err := svc.Update(ctx, service.SystemActor, "api", service.StreamInput{RetentionDays: &cleared, SpansRetentionDays: &cleared,
		LevelRules: &[]meta.LevelRule{}}); err != nil {
		t.Fatal(err)
	}
	if ddl := e.createTable(t, "events"); strings.Contains(ddl, "'api'") || !strings.Contains(ddl, "toIntervalDay(30)") {
		t.Errorf("TTL after clearing overrides:\n%s", ddl)
	}
	if err := svc.Delete(ctx, service.SystemActor, "api", "api"); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, "events", "stream = 'api'"); n != 0 {
		t.Errorf("deleted stream still has %d events", n)
	}
	if n := e.count(t, "spans", "stream = 'api'"); n != 0 {
		t.Errorf("deleted stream still has %d spans", n)
	}
	if n := e.count(t, "events", "stream = 'web'"); n != 2 {
		t.Errorf("other stream lost rows: %d", n)
	}
}

func TestIntegrationIndexedField(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	f := store.IndexedField{Source: model.SourceLogs, Key: "user.id", Column: "f_user_id"}
	if err := e.admin.AddIndexedField(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.AddIndexedField(ctx, f); err != nil {
		t.Fatalf("idempotent add: %v", err)
	}
	ddl := e.createTable(t, "events")
	for _, want := range []string{"`f_user_id` String MATERIALIZED props['user.id']", "INDEX idx_f_user_id f_user_id TYPE bloom_filter(0.01) GRANULARITY 4"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL lacks %q:\n%s", want, ddl)
		}
	}
	waitMutations(t, e)
	q, err := tql.Parse("user.id:42")
	if err != nil {
		t.Fatal(err)
	}
	opts := tql.Options{From: e.day0, To: time.Now(), Streams: model.AllStreams(), IndexedFields: map[model.Source]map[string]string{model.SourceLogs: {"user.id": "f_user_id"}}}
	c, err := tql.Compile(q, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "f_user_id") {
		t.Fatalf("compiler ignores the indexed column: %s", c.SQL)
	}
	rows, err := e.reader.Query(ctx, c.SQL, c.Args...)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	if n != 2 {
		t.Errorf("indexed query returned %d rows", n)
	}
	// The bloom filter serves exact string equality; case-insensitive ":" reads the column but
	// cannot use the skip index (SPEC §12.4).
	exact, err := tql.Parse(`user.id = "42"`)
	if err != nil {
		t.Fatal(err)
	}
	where, args, err := tql.CompileWhere(exact, opts)
	if err != nil {
		t.Fatal(err)
	}
	plan := explainIndexes(t, e, "SELECT count() FROM events WHERE "+where, args...)
	if !strings.Contains(plan, "idx_f_user_id") {
		t.Errorf("skip index not used:\n%s", plan)
	}
	if err := e.admin.DropIndexedField(ctx, f); err != nil {
		t.Fatal(err)
	}
	if ddl := e.createTable(t, "events"); strings.Contains(ddl, "f_user_id") {
		t.Errorf("column not dropped:\n%s", ddl)
	}
}

func explainIndexes(t *testing.T, e *adminEnv, query string, args ...any) string {
	t.Helper()
	rows, err := e.reader.Query(context.Background(), "EXPLAIN indexes = 1 "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func waitMutations(t *testing.T, e *adminEnv) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var pending uint64
		if err := e.conn.QueryRow(context.Background(), "SELECT count() FROM system.mutations WHERE database = ? AND NOT is_done", e.db).
			Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("mutations did not finish")
}

func TestIntegrationPurge(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	q, err := tql.Parse("level:debug")
	if err != nil {
		t.Fatal(err)
	}
	from, to := e.day0, time.Now().UTC()
	where, args, err := tql.CompileWhere(q, tql.Options{From: from, To: to, Streams: model.AllStreams()})
	if err != nil {
		t.Fatal(err)
	}
	req := store.PurgeRequest{Stream: "api", Source: model.SourceLogs, Where: where, Args: args, From: from, To: to, DryRun: true}
	job, err := e.admin.Purge(ctx, req)
	if err != nil || job.RowsEstimate != 2 {
		t.Fatalf("dry run %+v %v", job, err)
	}
	if n := e.count(t, "events", "1"); n != 6 {
		t.Fatalf("dry run deleted rows: %d", n)
	}
	req.DryRun = false
	job, err = e.admin.Purge(ctx, req)
	if err != nil || !job.LightweightDeleted || len(job.DroppedPartitions) != 0 {
		t.Fatalf("purge %+v %v", job, err)
	}
	if n := e.count(t, "events", "level_num = 1"); n != 0 {
		t.Errorf("debug rows left: %d", n)
	}
	if n := e.count(t, "events", "1"); n != 4 {
		t.Errorf("rows after filtered purge: %d", n)
	}
	// Whole-day purge without stream or filter drops day0's partition.
	whole := store.PurgeRequest{Source: model.SourceLogs, From: e.day0, To: e.day1}
	job, err = e.admin.Purge(ctx, whole)
	if err != nil || !slices.Equal(job.DroppedPartitions, []string{e.day0.Format("20060102")}) {
		t.Fatalf("whole-day purge %+v %v", job, err)
	}
	if n := e.count(t, "events", "timestamp < ?", e.day1); n != 0 {
		t.Errorf("day0 rows left: %d", n)
	}
	if n := e.count(t, "events", "timestamp >= ?", e.day1); n != 2 {
		t.Errorf("day1 rows lost: %d", n)
	}
	// A partial range with a stream deletes only matching rows of that stream.
	partial := store.PurgeRequest{Stream: "web", Source: model.SourceLogs, From: e.day1, To: e.day1.Add(11 * time.Hour)}
	if job, err = e.admin.Purge(ctx, partial); err != nil || len(job.DroppedPartitions) != 0 {
		t.Fatalf("partial purge %+v %v", job, err)
	}
	if n := e.count(t, "events", "stream = 'web'"); n != 0 {
		t.Errorf("web rows left: %d", n)
	}
	if n := e.count(t, "events", "stream = 'api'"); n != 1 {
		t.Errorf("api rows: %d", n)
	}
	if n := e.count(t, "spans", "1"); n != 1 {
		t.Errorf("logs purge touched spans: %d", n)
	}
}
