// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// lastSchemaWithoutStartTime is the newest ClickHouse migration before metric_points.start_time.
const lastSchemaWithoutStartTime = 6

// TestIntegrationMetricStartTimeUpgrade migrates a database at the 0006 schema that holds a
// metric point (SPEC §8.5 item 1): migration 0007 adds start_time, the old row reads 0 and new
// rows store the OTLP start time.
func TestIntegrationMetricStartTimeUpgrade(t *testing.T) {
	conn, env := setup(t)
	ctx := context.Background()
	db := env.Database
	m := NewMigrator(conn, db)
	if err := m.bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	migrations, err := m.Plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, mig := range migrations {
		if mig.Version > lastSchemaWithoutStartTime {
			break
		}
		for _, stmt := range mig.Statements {
			if err := conn.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", mig.Name, err)
			}
		}
		if err := conn.Exec(ctx, "INSERT INTO "+db+".schema_migrations (version, applied_at) VALUES (?, now())", mig.Version); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	if err := conn.Exec(ctx, "INSERT INTO "+db+".metric_points (timestamp, stream, name, kind, temporality, count, series_id) "+
		"VALUES (?, 'app', 'old', 'histogram', 'cumulative', 3, 1)", old); err != nil {
		t.Fatalf("insert at the old schema: %v", err)
	}
	applied, err := m.Up(ctx)
	if err != nil || !slices.Equal(applied, []uint32{7}) {
		t.Fatalf("upgrade applied %v, %v", applied, err)
	}
	w, err := NewWriter(conn, db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 7, 59, 0, 123456789, time.UTC)
	p := model.MetricPoint{Timestamp: old.Add(time.Minute), StartTime: start, Stream: "app", Name: "new", Kind: model.MetricHistogram}
	if err := w.WriteMetrics(ctx, []model.MetricPoint{p}); err != nil {
		t.Fatalf("write after upgrade: %v", err)
	}
	var count uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM "+db+".metric_points").Scan(&count); err != nil || count != 2 {
		t.Fatalf("rows after upgrade %d, %v", count, err)
	}
	for name, want := range map[string]int64{"old": 0, "new": start.UnixNano()} {
		var got int64
		if err := conn.QueryRow(ctx, "SELECT toUnixTimestamp64Nano(start_time) FROM "+db+".metric_points WHERE name = ?", name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s start_time = %d ns, want %d", name, got, want)
		}
	}
}
