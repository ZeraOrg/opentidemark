// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// logEventSelect is the event column list; it equals the default events columns of the TQL
// compiler (SPEC §10.6) so Run and the fixed queries share scanEvent.
const logEventSelect = "timestamp, id, stream, level, level_num, service, environment, host, message_template, " +
	"message, exception, event_type, trace_id, span_id, protocol, connection_id, props_json"

// Event read limits.
const (
	// contextWindow bounds the time searched for surrounding events (partition pruning).
	contextWindow = 24 * time.Hour
	// maxContextEvents caps before/after.
	maxContextEvents = 200
	// maxTraceEvents caps the events returned with a trace.
	maxTraceEvents = 1000
	// eventLookupRows caps the rows of a by-id lookup.
	eventLookupRows = 1
)

// isLogEventColumns reports whether cols are the default log event columns.
func isLogEventColumns(cols []tql.Column) bool {
	names := strings.Split(logEventSelect, ", ")
	if len(cols) != len(names) {
		return false
	}
	for i, c := range cols {
		if c.Name != names[i] {
			return false
		}
	}
	return true
}

// scanEvent scans the logEventSelect columns followed by extra destinations.
func scanEvent(rows driver.Rows, extra ...any) (model.Event, error) {
	var ev model.Event
	var levelName, props string
	var levelNum uint8
	dest := append([]any{&ev.Timestamp, &ev.ID, &ev.Stream, &levelName, &levelNum, &ev.Service, &ev.Environment,
		&ev.Host, &ev.MessageTemplate, &ev.Message, &ev.Exception, &ev.EventType, &ev.TraceID, &ev.SpanID,
		&ev.Protocol, &ev.ConnectionID, &props}, extra...)
	if err := rows.Scan(dest...); err != nil {
		return model.Event{}, fmt.Errorf("scan event: %w", err)
	}
	ev.Timestamp = ev.Timestamp.UTC()
	ev.Level = model.Level(levelNum)
	if !ev.Level.Valid() {
		ev.Level = model.Information
	}
	ev.Properties = decodeObject(props)
	return ev, nil
}

// readEvents runs a fixed event query.
func (r *Reader) readEvents(ctx context.Context, limit int, sql string, args ...any) ([]model.Event, error) {
	rows, cancel, err := r.query(ctx, limit, nil, model.SourceLogs, sql, args)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	return out, nil
}

// Event returns one event by id; an event outside scope is not found.
func (r *Reader) Event(ctx context.Context, scope model.StreamScope, id uuid.UUID) (model.Event, error) {
	cond, args, err := streamCond(scope)
	if err != nil {
		return model.Event{}, err
	}
	evs, err := r.readEvents(ctx, eventLookupRows, "SELECT "+logEventSelect+" FROM "+r.cfg.Database+
		".events WHERE "+cond+" AND id = toUUID(?) LIMIT 1", append(args, id.String())...)
	if err != nil {
		return model.Event{}, err
	}
	if len(evs) == 0 {
		return model.Event{}, store.ErrNotFound
	}
	return evs[0], nil
}

// Context returns events of the same service and host around ev inside scope: older
// newest-last, newer oldest-first, both within contextWindow of ev.
func (r *Reader) Context(ctx context.Context, scope model.StreamScope, ev model.Event, before, after int) ([]model.Event, []model.Event, error) {
	cond, scopeArgs, err := streamCond(scope)
	if err != nil {
		return nil, nil, err
	}
	before, after = clamp(before, 0, maxContextEvents), clamp(after, 0, maxContextEvents)
	base := "SELECT " + logEventSelect + " FROM " + r.cfg.Database + ".events WHERE " + cond + " AND service = ? AND host = ? AND "
	args := func(rest ...any) []any { return concatArgs(scopeArgs, []any{ev.Service, ev.Host}, rest) }
	older, newer := []model.Event{}, []model.Event{}
	if before > 0 {
		older, err = r.readEvents(ctx, before, base+"timestamp >= toDateTime64(?, 9, 'UTC') AND (timestamp, id) < (toDateTime64(?, 9, 'UTC'), toUUID(?)) "+
			"ORDER BY timestamp DESC, id DESC LIMIT ?", args(ev.Timestamp.Add(-contextWindow), ev.Timestamp, ev.ID.String(), before)...)
		if err != nil {
			return nil, nil, err
		}
		slices.Reverse(older)
	}
	if after > 0 {
		newer, err = r.readEvents(ctx, after, base+"timestamp < toDateTime64(?, 9, 'UTC') AND (timestamp, id) > (toDateTime64(?, 9, 'UTC'), toUUID(?)) "+
			"ORDER BY timestamp ASC, id ASC LIMIT ?", args(ev.Timestamp.Add(contextWindow), ev.Timestamp, ev.ID.String(), after)...)
		if err != nil {
			return nil, nil, err
		}
	}
	return older, newer, nil
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }

// Tail returns events ingested in (after, until] that match f, oldest first, plus the total
// count of such events (the caller reports the excess as dropped).
func (r *Reader) Tail(ctx context.Context, f store.Filter, after, until time.Time, limit int) ([]store.TailEvent, uint64, error) {
	if err := checkFilter(f); err != nil {
		return nil, 0, err
	}
	if f.Source != model.SourceLogs {
		return nil, 0, errors.New("tail: only logs can be tailed")
	}
	window := " AND ingested_at > toDateTime64(?, 3, 'UTC') AND ingested_at <= toDateTime64(?, 3, 'UTC')"
	args := append(append([]any{}, f.Args...), formatTimeArg(after, milliDigits), formatTimeArg(until, milliDigits))
	var total uint64
	if err := r.count(ctx, f.Source, "SELECT count() FROM "+r.cfg.Database+".events WHERE "+f.Where+window, args, &total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	rows, cancel, err := r.query(ctx, limit, nil, f.Source, "SELECT "+logEventSelect+", ingested_at FROM "+r.cfg.Database+
		".events WHERE "+f.Where+window+" ORDER BY ingested_at ASC, id ASC LIMIT ?", append(args, limit))
	if err != nil {
		return nil, 0, err
	}
	defer cancel()
	defer rows.Close()
	out := []store.TailEvent{}
	for rows.Next() {
		var te store.TailEvent
		if te.Event, err = scanEvent(rows, &te.IngestedAt); err != nil {
			return nil, 0, err
		}
		te.Event.IngestedAt = te.IngestedAt.UTC()
		out = append(out, te)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read tail: %w", err)
	}
	return out, total, nil
}

// count runs a single-value query (max_result_rows is lifted: it would also cap subqueries).
func (r *Reader) count(ctx context.Context, src model.Source, sql string, args []any, out any) error {
	qctx, cancel := r.queryCtx(ctx, 0, nil)
	defer cancel()
	if err := r.conn.QueryRow(qctx, sql, bindArgs(src, args)...).Scan(out); err != nil {
		return fmt.Errorf("count: %w", err)
	}
	return nil
}
