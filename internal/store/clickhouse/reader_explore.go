// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"fmt"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// Explorer read limits.
const (
	maxCatalogFields = 1000
	// HistogramCountKey is the single histogram key for metrics.
	HistogramCountKey = "count"
)

// HistogramKeys lists the count keys of a histogram bucket per source (SPEC §11.3): level
// names for logs, span status codes for spans, "count" for metrics.
func HistogramKeys(src model.Source) []string {
	switch src {
	case model.SourceSpans:
		return []string{model.StatusUnset, model.StatusOK, model.StatusError}
	case model.SourceMetrics:
		return []string{HistogramCountKey}
	default:
		keys := make([]string, 0, len(model.AllLevels()))
		for _, l := range model.AllLevels() {
			keys = append(keys, l.String())
		}
		return keys
	}
}

// histogramGroup is the trusted group expression per source.
func histogramGroup(src model.Source) string {
	switch src {
	case model.SourceSpans:
		return "toString(status_code)"
	case model.SourceMetrics:
		return "'" + HistogramCountKey + "'"
	default:
		return "toString(level)"
	}
}

// Histogram counts rows per bucket; every bucket of the range is present (zero filled).
func (r *Reader) Histogram(ctx context.Context, f store.Filter, span time.Duration) (store.Histogram, error) {
	if err := checkFilter(f); err != nil {
		return store.Histogram{}, err
	}
	if span < time.Second || span%time.Second != 0 {
		return store.Histogram{}, fmt.Errorf("histogram: span must be whole seconds")
	}
	table, timeCol := r.table(f.Source)
	sql := "SELECT toStartOfInterval(" + timeCol + ", toIntervalSecond(?)) AS t, " + histogramGroup(f.Source) +
		" AS k, count() FROM " + table + " WHERE " + f.Where + " GROUP BY t, k ORDER BY t"
	rows, cancel, err := r.query(ctx, 0, nil, f.Source, sql, append([]any{int64(span / time.Second)}, f.Args...))
	if err != nil {
		return store.Histogram{}, err
	}
	defer cancel()
	defer rows.Close()
	h := emptyHistogram(f.Source, f.Range, span)
	index := make(map[int64]int, len(h.Buckets))
	for i, b := range h.Buckets {
		index[b.T.Unix()] = i
	}
	for rows.Next() {
		var t time.Time
		var k string
		var n uint64
		if err := rows.Scan(&t, &k, &n); err != nil {
			return store.Histogram{}, fmt.Errorf("scan histogram: %w", err)
		}
		if i, ok := index[t.Unix()]; ok {
			h.Buckets[i].Counts[k] += n
		}
	}
	if err := rows.Err(); err != nil {
		return store.Histogram{}, fmt.Errorf("read histogram: %w", err)
	}
	return h, nil
}

// emptyHistogram returns zero buckets aligned like toStartOfInterval (multiples of span since
// the Unix epoch) covering [From, To).
func emptyHistogram(src model.Source, rng store.TimeRange, span time.Duration) store.Histogram {
	sec := int64(span / time.Second)
	from := rng.From.Unix()
	start := from - mod(from, sec)
	h := store.Histogram{Span: span, Buckets: []store.HistogramBucket{}}
	keys := HistogramKeys(src)
	end := rng.To.Add(time.Second - time.Nanosecond).Unix() // ceil to whole seconds
	for t := start; t < end; t += sec {
		counts := make(map[string]uint64, len(keys))
		for _, k := range keys {
			counts[k] = 0
		}
		h.Buckets = append(h.Buckets, store.HistogramBucket{T: time.Unix(t, 0).UTC(), Counts: counts})
	}
	return h
}

func mod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

// Newest counts the rows matching f and, when there are more than n, finds the time of the
// n-th newest row.
func (r *Reader) Newest(ctx context.Context, f store.Filter, n uint64) (store.Sample, error) {
	if err := checkFilter(f); err != nil {
		return store.Sample{}, err
	}
	table, timeCol := r.table(f.Source)
	var s store.Sample
	if err := r.count(ctx, f.Source, "SELECT count() FROM "+table+" WHERE "+f.Where, f.Args, &s.Total); err != nil {
		return store.Sample{}, err
	}
	if s.Total <= n {
		return s, nil
	}
	sql := "SELECT min(t) FROM (SELECT " + timeCol + " AS t FROM " + table + " WHERE " + f.Where +
		" ORDER BY " + timeCol + " DESC LIMIT ?)"
	if err := r.count(ctx, f.Source, sql, append(append([]any{}, f.Args...), n), &s.Cutoff); err != nil {
		return store.Sample{}, err
	}
	s.Cutoff = s.Cutoff.UTC()
	s.Sampled = true
	return s, nil
}

// Fields lists property keys seen in r inside scope: field_catalog for logs and spans,
// attribute keys of metric points for metrics. Ordered by hits, at most maxCatalogFields.
func (r *Reader) Fields(ctx context.Context, scope model.StreamScope, src model.Source, rng store.TimeRange) ([]store.FieldInfo, error) {
	cond, scopeArgs, err := streamCond(scope)
	if err != nil {
		return nil, err
	}
	sql := "SELECT key, max(numeric) = 1, sum(hits) AS h FROM " + r.cfg.Database + ".field_catalog " +
		"WHERE " + cond + " AND source = ? AND hour >= toStartOfHour(toDateTime64(?, 9, 'UTC')) AND hour < toDateTime64(?, 9, 'UTC') " +
		"GROUP BY key ORDER BY h DESC, key LIMIT ?"
	args := concatArgs(scopeArgs, []any{string(src), rng.From, rng.To, maxCatalogFields})
	if src == model.SourceMetrics {
		sql = "SELECT k, false, count() AS h FROM " + r.cfg.Database + ".metric_points ARRAY JOIN mapKeys(attrs) AS k " +
			"WHERE " + cond + " AND timestamp >= toDateTime64(?, 3, 'UTC') AND timestamp < toDateTime64(?, 3, 'UTC') " +
			"GROUP BY k ORDER BY h DESC, k LIMIT ?"
		args = concatArgs(scopeArgs, []any{rng.From, rng.To, maxCatalogFields})
	}
	rows, cancel, err := r.query(ctx, maxCatalogFields, nil, src, sql, args)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	out := []store.FieldInfo{}
	for rows.Next() {
		var fi store.FieldInfo
		if err := rows.Scan(&fi.Name, &fi.Numeric, &fi.Hits); err != nil {
			return nil, fmt.Errorf("scan fields: %w", err)
		}
		out = append(out, fi)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read fields: %w", err)
	}
	return out, nil
}

// MetricNames lists metric names containing substr seen in rng inside scope.
func (r *Reader) MetricNames(ctx context.Context, scope model.StreamScope, substr string, rng store.TimeRange, limit int) ([]store.MetricName, error) {
	cond, args, err := streamCond(scope)
	if err != nil {
		return nil, err
	}
	sql := "SELECT name, any(kind), any(unit), any(description) FROM " + r.cfg.Database + ".metric_points " +
		"WHERE " + cond + " AND timestamp >= toDateTime64(?, 3, 'UTC') AND timestamp < toDateTime64(?, 3, 'UTC') " +
		"AND positionCaseInsensitiveUTF8(name, ?) > 0 GROUP BY name ORDER BY name LIMIT ?"
	rows, cancel, err := r.query(ctx, limit, nil, model.SourceMetrics, sql, append(args, rng.From, rng.To, substr, limit))
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	out := []store.MetricName{}
	for rows.Next() {
		var m store.MetricName
		if err := rows.Scan(&m.Name, &m.Kind, &m.Unit, &m.Description); err != nil {
			return nil, fmt.Errorf("scan metric names: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read metric names: %w", err)
	}
	return out, nil
}
