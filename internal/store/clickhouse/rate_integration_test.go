// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// ratePoint builds one point of counter "req" for service svc at histBase+at.
func ratePoint(svc, temporality string, at, start time.Duration, value float64) model.MetricPoint {
	p := model.MetricPoint{Timestamp: histBase.Add(at), Stream: "app", Service: svc, Name: "req", Kind: model.MetricSum,
		Temporality: temporality, IsMonotonic: true, Value: value}
	if start != 0 {
		p.StartTime = histBase.Add(start)
	}
	return p
}

// TestIntegrationMetricRate checks rate(value) on real ClickHouse against hand-computed values
// (SPEC §10.5): the increase between the last point of one bucket and the first point of the
// next counts, the first bucket takes its delta from a point in the lookback, restarts (new start
// time, decrease) count the new value, delta points count as they are, and a series that started
// inside the read range counts its first point.
func TestIntegrationMetricRate(t *testing.T) {
	const cum, dlt = model.TemporalityCumulative, model.TemporalityDelta
	s := time.Second
	r := metricsReader(t, [][]model.MetricPoint{
		// a: lookback point, then +60 (bucket 0), +60 and +30 (bucket 1), restart by start time (bucket 3).
		{ratePoint("a", cum, -30*s, -time.Hour, 100), ratePoint("a", cum, 30*s, -time.Hour, 160),
			ratePoint("a", cum, 70*s, -time.Hour, 220), ratePoint("a", cum, 110*s, -time.Hour, 250),
			ratePoint("a", cum, 200*s, 190*s, 4)},
		// b: no start time, written newest first: baseline, +30 (bucket 0), reset to 20, +30 (bucket 1).
		{ratePoint("b", cum, 100*s, 0, 50)},
		{ratePoint("b", cum, 80*s, 0, 20)},
		{ratePoint("b", cum, 10*s, 0, 50), ratePoint("b", cum, 40*s, 0, 80)},
		// c: delta points (bucket 2); d: started in the range, so its first point counts whole.
		{ratePoint("c", dlt, 130*s, 0, 12), ratePoint("c", dlt, 160*s, 0, 6),
			ratePoint("d", cum, 140*s, 125*s, 9), ratePoint("d", cum, 170*s, 125*s, 15)},
	})
	t.Run("timeseries", func(t *testing.T) {
		res := runHist(t, r, "source metrics | name:req | stats rate(value) * 60 as per_minute by service span 1m")
		m := func(minutes int) int64 { return histBase.Add(time.Duration(minutes) * time.Minute).UnixMilli() }
		want := map[string]map[int64]float64{ // increase per minute
			"a": {m(0): 60, m(1): 90, m(3): 4},
			"b": {m(0): 30, m(1): 50},
			"c": {m(2): 18},
			"d": {m(2): 15},
		}
		seen := 0
		for _, sr := range res.Series {
			svc, _ := sr.Key["service"].(string)
			for _, p := range sr.Points {
				ms, _ := p[0].(int64)
				inc, ok := want[svc][ms]
				if !ok {
					t.Errorf("unexpected point %s %v", svc, p)
					continue
				}
				if !sameValues(p[1:], []any{inc}) {
					t.Errorf("%s at +%dm = %v per minute, want %v", svc, (ms-histBase.UnixMilli())/60000, p[1:], inc)
				}
				seen++
			}
		}
		if seen != 7 {
			t.Errorf("%d points, want 7: %+v", seen, res.Series)
		}
	})
	t.Run("table over the range", func(t *testing.T) {
		res := runHist(t, r, "source metrics | name:req | stats rate(value) as rps by service | where rps > 0.1 | sort rps desc")
		want := [][]any{{"a", 154.0 / 240}, {"b", 80.0 / 240}}
		if len(res.Rows) != len(want) {
			t.Fatalf("rows %v, want %v", res.Rows, want)
		}
		for i, row := range res.Rows {
			if row[0] != want[i][0] || !sameValues(row[1:], want[i][1:]) {
				t.Errorf("row %d = %v, want %v", i, row, want[i])
			}
		}
	})
}
