// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/retention"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

func setup(t *testing.T) (driver.Conn, testutil.ClickHouseEnv) {
	t.Helper()
	env, ok := testutil.ClickHouseFromEnv()
	if !ok {
		t.Skip("TIDEMARK_TEST_CLICKHOUSE_ADDR not set")
	}
	ctx := context.Background()
	conn, err := Open(ctx, Options{Addrs: env.Addrs, Username: env.WriterUser, Password: env.WriterPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+env.Database+" SYNC"); err != nil {
		t.Fatal(err)
	}
	return conn, env
}

func TestIntegrationStore(t *testing.T) {
	conn, env := setup(t)
	ctx := context.Background()
	db := env.Database

	m := NewMigrator(conn, db)
	if st, err := m.Status(ctx); err != nil || len(st) != 6 || st[0].Applied {
		t.Fatalf("status before migrating: %+v %v", st, err)
	}
	applied, err := m.Up(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) != 6 {
		t.Fatalf("applied %v", applied)
	}
	if st, err := m.Status(ctx); err != nil || !st[5].Applied {
		t.Fatalf("status after migrating: %+v %v", st, err)
	}
	if again, err := m.Up(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second run applied %v, %v", again, err)
	}

	w, err := NewWriter(conn, db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	ev := model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: now, IngestedAt: now, Stream: "app", Level: model.Warning,
		Service: "svc", Message: "hello", Protocol: "native", ConnectionID: "c1",
		Properties: map[string]any{"user": map[string]any{"id": int64(7)}, "ok": true, "tags": []any{"a"}}}
	if err := w.WriteEvents(ctx, []model.Event{ev}); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if err := w.WriteEvents(ctx, nil); err != nil {
		t.Fatal(err)
	}
	span := model.Span{TraceID: "5b8efff798038103d269b633813fc60c", SpanID: "eee19b7ec3c1b174", Stream: "app", Service: "svc",
		Name: "GET /", Kind: "server", Start: now, Duration: 25 * time.Millisecond, StatusCode: "ok",
		Attributes: map[string]any{"http.status_code": int64(200)}}
	if err := w.WriteSpans(ctx, []model.Span{span}); err != nil {
		t.Fatalf("write spans: %v", err)
	}
	mp := model.MetricPoint{Timestamp: now, Stream: "app", Service: "svc", Name: "cpu", Kind: "gauge", Value: 0.5,
		Attributes: map[string]any{"core": "1", "host": "a"}, BucketBounds: []float64{1, 2}, BucketCounts: []uint64{1, 2, 3}}
	if err := w.WriteMetrics(ctx, []model.MetricPoint{mp}); err != nil {
		t.Fatalf("write metrics: %v", err)
	}

	var (
		level, propsJSON, userID string
		levelNum                 uint8
		okNum                    float64
		gotID                    uuid.UUID
	)
	row := conn.QueryRow(ctx, "SELECT id, level, level_num, props['user.id'], props_num['ok'], props_json FROM "+db+".events")
	if err := row.Scan(&gotID, &level, &levelNum, &userID, &okNum, &propsJSON); err != nil {
		t.Fatal(err)
	}
	if gotID != ev.ID || level != "Warning" || levelNum != 3 || userID != "7" || okNum != 1 || !strings.Contains(propsJSON, `"tags":["a"]`) {
		t.Errorf("event row: %v %s %d %s %v %s", gotID, level, levelNum, userID, okNum, propsJSON)
	}
	var dur uint64
	var status string
	if err := conn.QueryRow(ctx, "SELECT duration_ns, attrs['http.status_code'] FROM "+db+".spans").Scan(&dur, &status); err != nil {
		t.Fatal(err)
	}
	if dur != 25_000_000 || status != "200" {
		t.Errorf("span row %d %s", dur, status)
	}
	var seriesID, chHash uint64
	err = conn.QueryRow(ctx, "SELECT series_id, cityHash64(concat(name, '\\0', 'core', '\\0', '1', '\\0', 'host', '\\0', 'a', '\\0', service)) FROM "+db+".metric_points").
		Scan(&seriesID, &chHash)
	if err != nil {
		t.Fatal(err)
	}
	if seriesID != chHash || seriesID != SeriesID("cpu", "svc", map[string]string{"core": "1", "host": "a"}) {
		t.Errorf("series id %d vs ClickHouse cityHash64 %d", seriesID, chHash)
	}
	var catalog uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM "+db+".field_catalog WHERE source = 'logs' AND key = 'user.id'").Scan(&catalog); err != nil || catalog == 0 {
		t.Errorf("field catalog: %d %v", catalog, err)
	}

	testRetention(t, conn, db)
	testReader(t, env)
}

func testRetention(t *testing.T, conn driver.Conn, db string) {
	ctx := context.Background()
	days := 90
	rules, err := retention.Build(retention.Policy{Defaults: retention.DefaultDefaults(), Streams: []retention.StreamPolicy{{
		Name: "api", LogsDays: &days, LevelRules: []retention.LevelRule{{Levels: []model.Level{model.Verbose, model.Debug}, Days: 3}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rm, _ := NewRetentionManager(conn, db)
	changed, err := rm.Apply(ctx, rules)
	if err != nil || len(changed) != 3 {
		t.Fatalf("apply: %v %v", changed, err)
	}
	if changed, err := rm.Apply(ctx, rules); err != nil || len(changed) != 0 {
		t.Fatalf("idempotent apply: %v %v", changed, err)
	}
	var ddl string
	if err := conn.QueryRow(ctx, "SELECT create_table_query FROM system.tables WHERE database = ? AND name = 'events'", db).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TTL", "toIntervalDay(3)", "level_num IN (0, 1)", "toIntervalDay(90)", "NOT IN ('api')"} {
		if !strings.Contains(ddl, want) {
			t.Errorf("events DDL lacks %q:\n%s", want, ddl)
		}
	}
	if err := rm.ApplyRetention(ctx, []retention.Rule{{Table: "users", TTL: "x"}}); err == nil {
		t.Error("unknown table accepted")
	}
}

func testReader(t *testing.T, env testutil.ClickHouseEnv) {
	ctx := context.Background()
	r, err := Open(ctx, Options{Addrs: env.Addrs, Database: env.Database, Username: env.ReaderUser, Password: env.ReaderPassword,
		QueryTimeout: 10 * time.Second, MaxMemoryBytes: DefaultReaderMemory, MaxResultRows: 1000})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var n uint64
	if err := r.QueryRow(ctx, "SELECT count() FROM events").Scan(&n); err != nil || n != 1 {
		t.Errorf("reader select: %d %v", n, err)
	}
	if err := r.Exec(ctx, "INSERT INTO events (message) VALUES ('x')"); err == nil {
		t.Error("reader could insert")
	}
	if err := r.Exec(ctx, "DROP TABLE events"); err == nil {
		t.Error("reader could drop")
	}
	if v, err := ServerVersion(ctx, r); err != nil || !strings.HasPrefix(v, "25.") {
		t.Errorf("version %q %v", v, err)
	}
	if err := (Pinger{Conn: r}).Ping(ctx); err != nil {
		t.Error(err)
	}
}
