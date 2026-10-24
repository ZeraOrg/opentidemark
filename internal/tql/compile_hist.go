// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"strconv"
	"strings"
	"time"
)

// maxDeltaLookback caps how far before the request range histogram aggregates and metrics
// rate(value) read, so the first bucket has a previous point to take its delta from (SPEC §10.5).
const maxDeltaLookback = 10 * time.Minute

// deltaLookback is one span (or the whole range without span), capped at maxDeltaLookback.
func (c *compiler) deltaLookback(span time.Duration) time.Duration {
	if span <= 0 {
		span = c.o.To.Sub(c.o.From)
	}
	return min(span, maxDeltaLookback)
}

// histNeeds reports which merged values the stage's histogram aggregates use.
func histNeeds(sp *statsPlan) (buckets, mean bool) {
	for _, a := range sp.calls() {
		buckets = buckets || a.Fn == aggHistQuantile
		mean = mean || a.Fn == aggHistMean
	}
	return buckets, mean
}

// histStats renders histogram aggregates in three levels (SPEC §10.6):
//   - points: per series (window over stream, series_id by time) the delta of each point
//     against the previous one, over the request range plus the lookback;
//   - groups: the deltas of the points in the request range, merged per bucket and by-fields;
//   - outer: the aggregate values computed from the merged deltas.
func (c *compiler) histStats(b *sqlBuf, sp *statsPlan, span time.Duration) {
	keys := groupByList(sp, span)
	b.w("SELECT ")
	if keys != "" {
		b.w(keys, ", ")
	}
	call := 0 // calls are numbered in written order across columns, as in histGroups
	for i, a := range sp.aggs {
		if i > 0 {
			b.w(", ")
		}
		column(b, a, func(a Agg) {
			histAgg(b, a, call)
			call++
		})
		b.w(" AS ", aggAlias(i))
	}
	b.w(" FROM (SELECT ")
	if keys != "" {
		b.w(keys, ", ")
	}
	c.histGroups(b, sp)
	b.w(" FROM (")
	c.histPoints(b, sp, span)
	b.w(") WHERE ", c.s.timeCol, " >= ")
	b.arg(c.o.From.UTC())
	if keys != "" {
		b.w(" GROUP BY ", keys)
	}
	b.w(" HAVING count() > 0)")
}

// histGroups renders the merged values: delta sum and count, and for quantiles the buckets
// merged by upper bound (sumMap over the bounds with +Inf appended; the third array of ones
// keeps buckets whose counts sum to 0, which sumMap would drop) with their cumulative counts,
// and per quantile aggregate i its rank h_r<i> and the first bucket reaching it h_i<i>.
func (c *compiler) histGroups(b *sqlBuf, sp *statsPlan) {
	b.w("sum(h_ds) AS h_s, sum(h_dn) AS h_n")
	if buckets, _ := histNeeds(sp); !buckets {
		return
	}
	b.w(", sumMap(h_qk, h_qv, h_qn) AS h_b, h_b.1 AS h_k, arrayCumSum(h_b.2) AS h_c")
	for i, a := range sp.calls() {
		if a.Fn != aggHistQuantile {
			continue
		}
		r, x := histAlias("h_r", i), histAlias("h_i", i)
		b.w(", ")
		b.arg(a.Arg.Num)
		b.w(" * h_c[-1] AS ", r, ", arrayFirstIndex(x -> x >= ", r, ", h_c) AS ", x)
	}
}

func histAlias(prefix string, i int) string { return prefix + strconv.Itoa(i) }

// histQuantileExpr interpolates linearly inside bucket {x}, the first whose cumulative count
// reaches the rank {r}: the +Inf bucket yields the highest finite bound, the lowest bucket starts
// at 0 when its upper bound is positive (else its upper bound is the result).
const histQuantileExpr = "multiIf(h_c[-1] <= 0, NULL, isInfinite(h_k[{x}]), if({x} = 1, NULL, h_k[{x} - 1]), " +
	"{x} = 1, if(h_k[1] <= 0, h_k[1], h_k[1] * {r} / h_c[1]), " +
	"h_k[{x} - 1] + (h_k[{x}] - h_k[{x} - 1]) * ({r} - h_c[{x} - 1]) / (h_c[{x}] - h_c[{x} - 1]))"

// histAgg renders histogram aggregate i over the merged values; no observations ⇒ NULL.
func histAgg(b *sqlBuf, a Agg, i int) {
	switch a.Fn {
	case aggHistMean:
		b.w("if(h_n = 0, NULL, h_s / h_n)")
	case aggHistQuantile:
		b.w(strings.NewReplacer("{r}", histAlias("h_r", i), "{x}", histAlias("h_i", i)).Replace(histQuantileExpr))
	}
}

// histPoints renders the per-point deltas. A point counts whole when its temporality is delta,
// when the series restarted (both start times known and different, the count decreased, or a
// bucket decreased under unchanged bounds), or when it is the first point of its series and its
// start time lies in the read range. Any other first point is only the baseline of the next.
// Buckets need unchanged bounds for a delta: after a layout change without restart the point
// adds its count and sum delta but no buckets.
func (c *compiler) histPoints(b *sqlBuf, sp *statsPlan, span time.Duration) {
	readFrom := c.o.From.Add(-c.deltaLookback(span))
	buckets, mean := histNeeds(sp)
	b.w("SELECT ")
	c.keys(b, sp, span)
	if span > 0 || len(sp.by) > 0 {
		b.w(", ")
	}
	b.w(c.s.timeCol, ", row_number() OVER w = 1 AS h_first, ",
		"toUnixTimestamp64Nano(start_time) AS h_st, lagInFrame(toUnixTimestamp64Nano(start_time)) OVER w AS h_pst, ",
		"lagInFrame(`count`) OVER w AS h_pn, lagInFrame(bucket_counts) OVER w AS h_pc, ",
		"bucket_bounds = lagInFrame(bucket_bounds) OVER w AS h_same, ",
		"temporality = 'delta' OR (NOT h_first AND ((h_st != 0 AND h_pst != 0 AND h_st != h_pst) OR `count` < h_pn ",
		"OR (h_same AND arrayExists((x, y) -> x < y, bucket_counts, arrayResize(h_pc, length(bucket_counts)))))) ",
		"OR (h_first AND h_st != 0 AND start_time >= ")
	b.arg(readFrom.UTC())
	b.w(") AS h_whole, ",
		"multiIf(h_whole, toInt64(`count`), h_first, 0, toInt64(`count`) - toInt64(h_pn)) AS h_dn, ",
		"multiIf(h_whole, `sum`, h_first, 0, `sum` - lagInFrame(`sum`) OVER w) AS h_ds")
	if buckets {
		b.w(", multiIf(h_whole, arrayMap(x -> toInt64(x), bucket_counts), h_first OR NOT h_same, emptyArrayInt64(), ",
			"arrayMap((x, y) -> toInt64(x) - toInt64(y), bucket_counts, arrayResize(h_pc, length(bucket_counts)))) AS h_dc, ",
			"length(h_dc) > 0 AND length(h_dc) = length(bucket_bounds) + 1 AS h_hasb, ",
			"if(h_hasb, arrayPushBack(bucket_bounds, inf), []) AS h_qk, if(h_hasb, h_dc, []) AS h_qv, ",
			"arrayMap(x -> toUInt8(1), h_qk) AS h_qn")
	}
	b.w(" FROM ", c.table(), " WHERE ")
	c.whereFrom(b, readFrom)
	b.w(" AND (kind IN ('histogram', 'exp_histogram')")
	if mean {
		b.w(" OR (kind = 'summary' AND temporality != '')")
	}
	b.w(") WINDOW w AS (PARTITION BY stream, series_id ORDER BY ", c.s.timeCol,
		" ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)")
}
