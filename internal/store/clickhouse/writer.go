// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/go-faster/city"

	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// Insert quorum values (SPEC §17.5).
const (
	QuorumAuto = "auto"
	maxQuorum  = 64
)

// Column lists of the insert statements.
const (
	eventColumns = "timestamp, ingested_at, id, stream, level, level_num, service, environment, host, " +
		"message_template, message, exception, event_type, trace_id, span_id, protocol, connection_id, props, props_num, props_json"
	spanColumns = "start_time, trace_id, span_id, parent_span_id, stream, service, name, kind, duration_ns, " +
		"status_code, status_message, connection_id, attrs, attrs_num, resource_json, events_json, links_json"
	metricColumns = "timestamp, stream, service, name, description, unit, kind, temporality, is_monotonic, value, " +
		"count, sum, min, max, bucket_bounds, bucket_counts, attrs, series_id, connection_id, start_time"
)

// Writer inserts batches with the writer connection; it implements the store writer ports.
// Every insert carries the batch's deduplication token when the context has one
// (store.WithDedupToken, SPEC §8.5 item 7) and the configured insert quorum (cluster mode).
type Writer struct {
	conn   driver.Conn
	db     string
	quorum string
}

// WriterOption configures a Writer.
type WriterOption func(*Writer)

// WithInsertQuorum sets insert_quorum ("auto" = majority of replicas, or a number of replicas);
// "" keeps the server default (no quorum).
func WithInsertQuorum(q string) WriterOption { return func(w *Writer) { w.quorum = q } }

// NewWriter returns a writer for database db (must be a valid identifier).
func NewWriter(conn driver.Conn, db string, opts ...WriterOption) (*Writer, error) {
	if !ValidDatabase(db) {
		return nil, ErrInvalidDatabase
	}
	w := &Writer{conn: conn, db: db}
	for _, o := range opts {
		o(w)
	}
	if w.quorum != "" && !ValidQuorum(w.quorum) {
		return nil, fmt.Errorf("invalid insert quorum %q", w.quorum)
	}
	return w, nil
}

// InsertSettings returns the per-insert settings for ctx: deduplication token and quorum.
func (w *Writer) InsertSettings(ctx context.Context) ch.Settings {
	s := ch.Settings{}
	if tok := store.DedupToken(ctx); tok != "" {
		s["insert_deduplication_token"] = tok
		s["insert_deduplicate"] = 1
	}
	if w.quorum != "" {
		s["insert_quorum"] = w.quorum
	}
	return s
}

// ValidQuorum reports whether q is "auto" or a replica count between 1 and 64.
func ValidQuorum(q string) bool {
	if q == QuorumAuto {
		return true
	}
	n, err := strconv.Atoi(q)
	return err == nil && n >= 1 && n <= maxQuorum
}

// WriteEvents inserts events.
func (w *Writer) WriteEvents(ctx context.Context, events []model.Event) error {
	return w.insert(ctx, "events", eventColumns, len(events), func(b driver.Batch, i int) error {
		return b.Append(EventRow(events[i])...)
	})
}

// EventRow returns the column values of one event, in eventColumns order.
func EventRow(ev model.Event) []any {
	props := ev.Properties
	flat := Flatten(props)
	if flat.Truncated {
		props = withTruncatedFlag(props)
	}
	return []any{ev.Timestamp, ev.IngestedAt, ev.ID, ev.Stream, ev.Level.String(), uint8(ev.Level), ev.Service,
		ev.Environment, ev.Host, ev.MessageTemplate, ev.Message, ev.Exception, ev.EventType, ev.TraceID, ev.SpanID,
		ev.Protocol, ev.ConnectionID, flat.Strings, flat.Numbers, jsonText(props)}
}

func withTruncatedFlag(props map[string]any) map[string]any {
	out := make(map[string]any, len(props)+1)
	for k, v := range props {
		out[k] = v
	}
	out["_tidemark.truncated"] = true
	return out
}

// WriteSpans inserts spans.
func (w *Writer) WriteSpans(ctx context.Context, spans []model.Span) error {
	return w.insert(ctx, "spans", spanColumns, len(spans), func(b driver.Batch, i int) error {
		return b.Append(SpanRow(spans[i])...)
	})
}

// SpanRow returns the column values of one span, in spanColumns order.
func SpanRow(s model.Span) []any {
	flat := Flatten(s.Attributes)
	dur := uint64(0)
	if s.Duration > 0 {
		dur = uint64(s.Duration.Nanoseconds()) //nolint:gosec // guarded above: Duration > 0
	}
	return []any{s.Start, s.TraceID, s.SpanID, s.ParentSpanID, s.Stream, s.Service, s.Name, s.Kind, dur,
		s.StatusCode, s.StatusMessage, s.ConnectionID, flat.Strings, flat.Numbers, jsonText(s.ResourceAttributes),
		jsonText(s.Events), jsonText(s.Links)}
}

// WriteMetrics inserts metric points.
func (w *Writer) WriteMetrics(ctx context.Context, points []model.MetricPoint) error {
	return w.insert(ctx, "metric_points", metricColumns, len(points), func(b driver.Batch, i int) error {
		return b.Append(MetricRow(points[i])...)
	})
}

// MetricRow returns the column values of one metric point, in metricColumns order.
func MetricRow(p model.MetricPoint) []any {
	attrs := Flatten(p.Attributes).Strings
	return []any{p.Timestamp, p.Stream, p.Service, p.Name, p.Description, p.Unit, p.Kind, p.Temporality,
		p.IsMonotonic, p.Value, p.Count, p.Sum, p.Min, p.Max, nonNilF(p.BucketBounds), nonNilU(p.BucketCounts),
		attrs, SeriesID(p.Name, p.Service, seriesAttrs(p.Kind, attrs)), p.ConnectionID, startTime(p.StartTime)}
}

// seriesAttrs returns the attributes that identify a series: all of them, except the quantile
// values of a summary point, which change with every point (SPEC §8.2).
func seriesAttrs(kind string, attrs map[string]string) map[string]string {
	if kind != model.MetricSummary {
		return attrs
	}
	out := make(map[string]string, len(attrs))
	for k, v := range attrs {
		if !strings.HasPrefix(k, model.SummaryQuantilePrefix) {
			out[k] = v
		}
	}
	return out
}

// startTime maps an unknown start time (zero) to the column default, the Unix epoch.
func startTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return t.UTC()
}

func (w *Writer) insert(ctx context.Context, table, columns string, n int, appendRow func(driver.Batch, int) error) error {
	if n == 0 {
		return nil
	}
	if settings := w.InsertSettings(ctx); len(settings) > 0 {
		ctx = ch.Context(ctx, ch.WithSettings(settings))
	}
	batch, err := w.conn.PrepareBatch(ctx, "INSERT INTO "+w.db+"."+table+" ("+columns+")")
	if err != nil {
		return fmt.Errorf("prepare %s insert: %w", table, err)
	}
	for i := range n {
		if err := appendRow(batch, i); err != nil {
			_ = batch.Abort() // the append error is the one to report
			return fmt.Errorf("%w: append %s row: %w", ErrRowRejected, table, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("send %s batch: %w", table, err)
	}
	return nil
}

// SeriesID is cityHash64 (ClickHouse variant) of name, the sorted attributes and service,
// joined as name \x00 k1 \x00 v1 … \x00 service.
func SeriesID(name, service string, attrs map[string]string) uint64 {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	for _, k := range keys {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(attrs[k])
	}
	b.WriteByte(0)
	b.WriteString(service)
	return city.CH64([]byte(b.String()))
}

func jsonText(v any) string {
	switch x := v.(type) {
	case nil:
		return "{}"
	case map[string]any:
		if x == nil {
			return "{}"
		}
		return jsonx.Compact(Sanitize(x))
	default:
		return jsonx.Compact(v)
	}
}

func nonNilF(s []float64) []float64 {
	if s == nil {
		return []float64{}
	}
	return s
}

func nonNilU(s []uint64) []uint64 {
	if s == nil {
		return []uint64{}
	}
	return s
}
