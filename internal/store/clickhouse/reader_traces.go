// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// Trace read limits.
const (
	maxTraceSpans = 10000
	// traceSlack widens the trace search range when summarizing, so spans of a matching
	// trace that start slightly outside the requested range are included.
	traceSlack = time.Hour
)

const spanSelect = "start_time, trace_id, span_id, parent_span_id, stream, service, name, kind, duration_ns, " +
	"status_code, status_message, connection_id, attrs, attrs_num, resource_json, events_json, links_json"

// Trace returns the spans of a trace (by start time) and the events with its trace id, both
// limited to scope; ErrNotFound when nothing is visible.
func (r *Reader) Trace(ctx context.Context, scope model.StreamScope, traceID string) ([]model.Span, []model.Event, error) {
	cond, args, err := streamCond(scope)
	if err != nil {
		return nil, nil, err
	}
	spans, err := r.traceSpans(ctx, cond, concatArgs(args, []any{traceID}))
	if err != nil {
		return nil, nil, err
	}
	events, err := r.readEvents(ctx, maxTraceEvents, "SELECT "+logEventSelect+" FROM "+r.cfg.Database+
		".events WHERE "+cond+" AND trace_id = ? ORDER BY timestamp ASC, id ASC LIMIT ?", append(args, traceID, maxTraceEvents)...)
	if err != nil {
		return nil, nil, err
	}
	if len(spans) == 0 && len(events) == 0 {
		return nil, nil, store.ErrNotFound
	}
	return spans, events, nil
}

// traceSpans reads the spans matching cond (the stream condition) and trace_id; args holds
// the arguments of cond followed by the trace id.
func (r *Reader) traceSpans(ctx context.Context, cond string, args []any) ([]model.Span, error) {
	rows, cancel, err := r.query(ctx, maxTraceSpans, nil, model.SourceSpans, "SELECT "+spanSelect+" FROM "+r.cfg.Database+
		".spans WHERE "+cond+" AND trace_id = ? ORDER BY start_time ASC, span_id ASC LIMIT ?", append(args, maxTraceSpans))
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	out := []model.Span{}
	for rows.Next() {
		var s model.Span
		var durNs uint64
		var attrs map[string]string
		var nums map[string]float64
		var resource, events, links string
		if err := rows.Scan(&s.Start, &s.TraceID, &s.SpanID, &s.ParentSpanID, &s.Stream, &s.Service, &s.Name, &s.Kind,
			&durNs, &s.StatusCode, &s.StatusMessage, &s.ConnectionID, &attrs, &nums, &resource, &events, &links); err != nil {
			return nil, fmt.Errorf("scan span: %w", err)
		}
		s.Start = s.Start.UTC()
		if durNs > math.MaxInt64 {
			durNs = math.MaxInt64
		}
		s.Duration = time.Duration(durNs)
		s.Attributes = typedAttributes(attrs, nums)
		s.ResourceAttributes = decodeObject(resource)
		s.Events = decodeList[model.SpanEvent](events)
		s.Links = decodeList[model.SpanLink](links)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read spans: %w", err)
	}
	return out, nil
}

// typedAttributes rebuilds typed span attributes from the flattened maps: numeric keys
// become numbers (or booleans for "true"/"false"), others stay strings.
func typedAttributes(strs map[string]string, nums map[string]float64) map[string]any {
	out := make(map[string]any, len(strs))
	for k, s := range strs {
		n, isNum := nums[k]
		switch {
		case !isNum:
			out[k] = s
		case s == "true" || s == "false":
			out[k] = s == "true"
		case json.Valid([]byte(s)):
			out[k] = json.Number(s)
		default:
			out[k] = n
		}
	}
	return out
}

func decodeList[T any](s string) []T {
	var out []T
	if err := json.Unmarshal([]byte(s), &out); err != nil || out == nil {
		return []T{}
	}
	return out
}

// Traces finds the newest traces with a span matching f and summarizes each over all of its
// spans inside f.Streams in the (slightly widened) range.
func (r *Reader) Traces(ctx context.Context, f store.Filter, limit int) ([]store.TraceSummary, error) {
	cond, scopeArgs, err := streamCond(f.Streams)
	if err != nil {
		return nil, err
	}
	ids, err := r.matchingTraces(ctx, f, limit)
	if err != nil || len(ids) == 0 {
		return []store.TraceSummary{}, err
	}
	const rootCond = "parent_span_id = ''"
	sql := "SELECT trace_id, " +
		"if(countIf(" + rootCond + ") > 0, argMinIf(service, start_time, " + rootCond + "), argMin(service, start_time)), " +
		"if(countIf(" + rootCond + ") > 0, argMinIf(name, start_time, " + rootCond + "), argMin(name, start_time)), " +
		"min(start_time), (max(toUnixTimestamp64Nano(start_time) + toInt64(duration_ns)) - " +
		"toUnixTimestamp64Nano(min(start_time))) / 1e6, count(), countIf(status_code = 'error'), " +
		"arraySort(groupUniqArray(service)) FROM " + r.cfg.Database + ".spans " +
		"WHERE " + cond + " AND has(?, trace_id) AND start_time >= toDateTime64(?, 9, 'UTC') AND start_time < toDateTime64(?, 9, 'UTC') " +
		"GROUP BY trace_id"
	rows, cancel, err := r.query(ctx, len(ids), nil, model.SourceSpans, sql,
		append(scopeArgs, ids, f.Range.From.Add(-traceSlack), f.Range.To.Add(traceSlack)))
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	byID := map[string]store.TraceSummary{}
	for rows.Next() {
		var t store.TraceSummary
		if err := rows.Scan(&t.TraceID, &t.RootService, &t.RootName, &t.Start, &t.DurationMs, &t.SpanCount,
			&t.ErrorCount, &t.Services); err != nil {
			return nil, fmt.Errorf("scan trace summary: %w", err)
		}
		t.Start = t.Start.UTC()
		byID[t.TraceID] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trace summaries: %w", err)
	}
	out := make([]store.TraceSummary, 0, len(ids))
	for _, id := range ids {
		if t, ok := byID[id]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// matchingTraces returns the ids of the newest traces having a span that matches f.
func (r *Reader) matchingTraces(ctx context.Context, f store.Filter, limit int) ([]string, error) {
	sql := "SELECT trace_id, max(start_time) AS t FROM " + r.cfg.Database + ".spans WHERE " + f.Where +
		" GROUP BY trace_id ORDER BY t DESC, trace_id LIMIT ?"
	rows, cancel, err := r.query(ctx, limit, nil, model.SourceSpans, sql, append(append([]any{}, f.Args...), limit))
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return nil, fmt.Errorf("scan trace id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read trace ids: %w", err)
	}
	return ids, nil
}
