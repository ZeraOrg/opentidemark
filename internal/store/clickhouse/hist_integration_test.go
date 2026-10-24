// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// histBase is the start of the query range of the histogram aggregate tests.
var histBase = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)

// histPoint builds one point of metric "lat" for service svc at histBase+at.
func histPoint(svc, kind, temporality string, at, start time.Duration, bounds []float64, counts []uint64, count uint64, sum float64) model.MetricPoint {
	p := model.MetricPoint{Timestamp: histBase.Add(at), Stream: "app", Service: svc, Name: "lat", Unit: "ms", Kind: kind,
		Temporality: temporality, Count: count, Sum: sum, BucketBounds: bounds, BucketCounts: counts,
		Attributes: map[string]any{"route": "/" + svc}}
	if start != 0 {
		p.StartTime = histBase.Add(start)
	}
	return p
}

// histFixture writes the series described in TestIntegrationHistogramAggregates; every batch is
// written separately so out-of-order arrival is part of the fixture.
func histFixture(t *testing.T) *Reader {
	t.Helper()
	const (
		h, eh, sm, cum, dlt = model.MetricHistogram, model.MetricExpHistogram, model.MetricSummary, model.TemporalityCumulative, model.TemporalityDelta
	)
	b100, b50, b10x50, b1 := []float64{10, 100}, []float64{50}, []float64{10, 50}, []float64{1}
	s := time.Second
	batches := [][]model.MetricPoint{
		// a: cumulative, previous point in the lookback, restart by start time at +1m30s.
		{histPoint("a", h, cum, -30*s, -time.Hour, b100, []uint64{1, 1, 0}, 2, 30),
			histPoint("a", h, cum, 30*s, -time.Hour, b100, []uint64{3, 2, 1}, 6, 400),
			histPoint("a", h, cum, 90*s, 70*s, b100, []uint64{1, 0, 0}, 1, 5),
			histPoint("a", h, cum, 110*s, 70*s, b100, []uint64{3, 2, 0}, 5, 65)},
		// b: cumulative without start time, written newest first; restart by decrease at +1m20s.
		{histPoint("b", h, cum, 80*s, 0, b100, []uint64{1, 1, 0}, 2, 60)},
		{histPoint("b", h, cum, 40*s, 0, b100, []uint64{6, 2, 0}, 8, 150)},
		{histPoint("b", h, cum, 10*s, 0, b100, []uint64{5, 0, 0}, 5, 20)},
		// c: delta temporality; d (same service c, other bounds): first point started in the range.
		{histPoint("c", h, dlt, 130*s, 0, b50, []uint64{1, 3}, 4, 300),
			histPoint("c", h, dlt, 160*s, 0, b50, []uint64{2, 0}, 2, 20),
			histPoint("c", eh, cum, 140*s, 125*s, b10x50, []uint64{2, 0, 0}, 2, 10)},
		// e: two equal cumulative points: nothing recorded in the bucket.
		{histPoint("e", h, cum, 190*s, -time.Hour, b1, []uint64{3, 0}, 3, 3),
			histPoint("e", h, cum, 200*s, -time.Hour, b1, []uint64{3, 0}, 3, 3)},
		// f: a summary (quantile values change every point) and a pre-upgrade summary point
		// without temporality, which histogram aggregates skip.
		{histPoint("f", sm, cum, 20*s, -time.Hour, nil, nil, 10, 100),
			histPoint("f", sm, cum, 50*s, -time.Hour, nil, nil, 14, 180),
			histPoint("f", sm, "", 55*s, 0, nil, nil, 1000, 1e6)},
		// other kinds are ignored.
		{histPoint("a", model.MetricGauge, "", 45*s, 0, nil, nil, 0, 0)},
	}
	batches[6][0].Attributes["quantile.0.99"] = 12.0
	batches[6][1].Attributes["quantile.0.99"] = 30.0
	batches[4][2].Attributes["route"] = "/d"
	return metricsReader(t, batches)
}

// metricsReader migrates a fresh database, writes each batch separately and returns a reader
// that uses the read-only user.
func metricsReader(t *testing.T, batches [][]model.MetricPoint) *Reader {
	t.Helper()
	conn, env := setup(t)
	ctx := context.Background()
	if _, err := NewMigrator(conn, env.Database).Up(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(conn, env.Database)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range batches {
		if err := w.WriteMetrics(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	rc, err := Open(ctx, Options{Addrs: env.Addrs, Database: env.Database, Username: env.ReaderUser, Password: env.ReaderPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	r, err := NewReader(rc, ReaderConfig{Database: env.Database, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runHist(t *testing.T, r *Reader, q string) store.Result {
	t.Helper()
	pq, err := tql.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tql.Compile(pq, tql.Options{Database: r.cfg.Database, From: histBase, To: histBase.Add(4 * time.Minute),
		Streams: model.AllStreams()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(context.Background(), c)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return res
}

// sameValues compares result values with hand-computed ones (nil = NULL).
func sameValues(got []any, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		w, wok := want[i].(float64)
		g, gok := got[i].(float64)
		if wok != gok || (wok && math.Abs(g-w) > 1e-9) || (!wok && got[i] != nil) {
			return false
		}
	}
	return true
}

// numbers converts integer result values to float64 so sameValues can compare them.
func numbers(row []any) []any {
	out := make([]any, len(row))
	for i, v := range row {
		switch n := v.(type) {
		case int64:
			out[i] = float64(n)
		case uint64:
			out[i] = float64(n)
		default:
			out[i] = v
		}
	}
	return out
}

// TestIntegrationHistogramAggregates checks hist_mean() and histogram_quantile(q) on real
// ClickHouse against hand-computed values (SPEC §10.5): lookback, restarts by start time and by
// decrease, a first point that is only a baseline, delta temporality, out-of-order arrival, mixed
// bounds, a bucket without observations (NULL), empty buckets (no row) and summaries.
func TestIntegrationHistogramAggregates(t *testing.T) {
	r := histFixture(t)
	t.Run("timeseries", func(t *testing.T) {
		res := runHist(t, r, "source metrics | name:lat | stats hist_mean(), histogram_quantile(0.5), "+
			"histogram_quantile(0.95) by service span 1m")
		m := func(minutes int) int64 { return histBase.Add(time.Duration(minutes) * time.Minute).UnixMilli() }
		want := map[string]map[int64][]any{
			"a": {m(0): {92.5, 10.0, 100.0}, m(1): {13.0, 10 * 2.5 / 3, 10 + 90*1.75/2}},
			"b": {m(0): {130.0 / 3, 10 + 90*0.5/2, 10 + 90*1.85/2}, m(1): {30.0, 10.0, 10 + 90*0.9}},
			"c": {m(2): {41.25, 10 + 40*2.0/3, 50.0}},
			"e": {m(3): {nil, nil, nil}},
			"f": {m(0): {20.0, nil, nil}},
		}
		seen := 0
		for _, s := range res.Series {
			svc, _ := s.Key["service"].(string)
			for _, p := range s.Points {
				ms, _ := p[0].(int64)
				exp, ok := want[svc][ms]
				if !ok {
					t.Errorf("unexpected point %s %v", svc, p)
					continue
				}
				if !sameValues(p[1:], exp) {
					t.Errorf("%s at +%dm = %v, want %v", svc, (ms-histBase.UnixMilli())/60000, p[1:], exp)
				}
				seen++
			}
		}
		if seen != 7 {
			t.Errorf("%d points, want 7: %+v", seen, res.Series)
		}
	})
	t.Run("table with where after stats", func(t *testing.T) {
		res := runHist(t, r, "source metrics | name:lat | stats hist_mean() as mean by service | where mean > 40 | sort mean desc")
		want := [][]any{{"a", 435.0 / 9}, {"c", 41.25}}
		if len(res.Rows) != len(want) {
			t.Fatalf("rows %v, want %v", res.Rows, want)
		}
		for i, row := range res.Rows {
			if row[0] != want[i][0] || !sameValues(row[1:], want[i][1:]) {
				t.Errorf("row %d = %v, want %v", i, row, want[i])
			}
		}
	})
	t.Run("quantile skips summaries", func(t *testing.T) {
		res := runHist(t, r, "source metrics | service:f | stats histogram_quantile(0.9) by service")
		if len(res.Rows) != 0 {
			t.Errorf("rows %v, want none", res.Rows)
		}
	})
	t.Run("arithmetic between aggregates", func(t *testing.T) {
		// e: two points with count 3 and sum 3 each; c: hist_mean 41.25 (see timeseries).
		for _, tc := range []struct {
			q    string
			want []any
		}{
			{"source metrics | service:e | stats sum(sum) / sum(count) as mean, count() - 3 as d, " +
				"count() / (count() - count()) as z, (sum(sum) + 1) * 2 / count()", []any{1.0, -1.0, nil, 7.0}},
			{"source metrics | service:c | stats hist_mean() * 1000 as us, hist_mean() / 0 as z, " +
				"histogram_quantile(0.95) / histogram_quantile(0.95)", []any{41250.0, nil, 1.0}},
		} {
			res := runHist(t, r, tc.q)
			if len(res.Rows) != 1 || !sameValues(numbers(res.Rows[0]), tc.want) {
				t.Errorf("%s = %v, want %v", tc.q, res.Rows, tc.want)
			}
		}
	})
	t.Run("empty range has no row", func(t *testing.T) {
		res := runHist(t, r, "source metrics | name:missing | stats hist_mean(), histogram_quantile(0.5)")
		if len(res.Rows) != 0 {
			t.Errorf("rows %v, want none", res.Rows)
		}
	})
}
