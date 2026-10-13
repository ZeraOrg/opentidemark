// SPDX-License-Identifier: Apache-2.0

// Package store declares the storage ports consumed by services (SPEC §6.2). The ClickHouse
// implementation lives in internal/store/clickhouse.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/retention"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// ErrNotFound is returned when an event, trace or job does not exist.
var ErrNotFound = errors.New("not found")

// EventWriter persists events.
type EventWriter interface {
	WriteEvents(ctx context.Context, ev []model.Event) error
}

// SpanWriter persists spans.
type SpanWriter interface {
	WriteSpans(ctx context.Context, sp []model.Span) error
}

// MetricWriter persists metric points.
type MetricWriter interface {
	WriteMetrics(ctx context.Context, mp []model.MetricPoint) error
}

// Querier runs compiled TQL and the explorer read paths (SPEC §11.3). It is implemented by
// the query layer of internal/store/clickhouse using the read-only connection. Filters come
// from tql.CompileWhere, so every user value is a positional argument.
type Querier interface {
	// Run executes a compiled query; events pages carry NextCursor when the page is full.
	Run(ctx context.Context, q *tql.Compiled) (Result, error)
	// Histogram counts matching rows per bucket of span (by level for logs, status for spans).
	Histogram(ctx context.Context, f Filter, span time.Duration) (Histogram, error)
	// Newest counts matching rows and, above n rows, returns the time of the n-th newest row
	// so facets can be restricted to the newest n rows (SPEC §11.3).
	Newest(ctx context.Context, f Filter, n uint64) (Sample, error)
	// Fields lists the property keys seen in r (field_catalog; metric attribute keys).
	Fields(ctx context.Context, src model.Source, r TimeRange) ([]FieldInfo, error)
	// Event returns one event by id; ErrNotFound when absent.
	Event(ctx context.Context, id uuid.UUID) (model.Event, error)
	// Context returns up to before older and after newer events of the same service and host.
	Context(ctx context.Context, ev model.Event, before, after int) (older, newer []model.Event, err error)
	// Tail returns up to limit events ingested in (after, until] in ingestion order and the
	// total number of such events.
	Tail(ctx context.Context, f Filter, after, until time.Time, limit int) ([]TailEvent, uint64, error)
	// Trace returns every span of a trace and the events carrying its trace id.
	Trace(ctx context.Context, traceID string) ([]model.Span, []model.Event, error)
	// Traces summarizes the most recent traces having a span that matches f.
	Traces(ctx context.Context, f Filter, limit int) ([]TraceSummary, error)
	// MetricNames lists metric names containing substr (case-insensitive) seen in r.
	MetricNames(ctx context.Context, substr string, r TimeRange, limit int) ([]MetricName, error)
}

// Filter is a compiled WHERE condition (tql.CompileWhere) for one source; it already contains
// the time range Range.
type Filter struct {
	Source model.Source
	Where  string
	Args   []any
	Range  TimeRange
}

// Sample is the outcome of Querier.Newest. When Sampled is set, only rows at or after
// Cutoff belong to the newest n rows.
type Sample struct {
	Total   uint64
	Cutoff  time.Time
	Sampled bool
}

// TailEvent is an event with its ingestion time (the live-tail position).
type TailEvent struct {
	Event      model.Event
	IngestedAt time.Time
}

// TraceSummary describes one trace for the trace search (SPEC §11.3).
type TraceSummary struct {
	TraceID     string
	RootService string
	RootName    string
	Start       time.Time
	DurationMs  float64
	SpanCount   uint64
	ErrorCount  uint64
	Services    []string
}

// MetricName describes one metric for the metric browser.
type MetricName struct {
	Name        string
	Kind        string
	Unit        string
	Description string
}

// StreamAdmin manages stream data (SPEC §12). It uses the writer connection.
type StreamAdmin interface {
	Stats(ctx context.Context) ([]StreamStats, error)
	ApplyRetention(ctx context.Context, rules []retention.Rule) error
	Purge(ctx context.Context, p PurgeRequest) (PurgeJob, error)
	AddIndexedField(ctx context.Context, f IndexedField) error
	DropIndexedField(ctx context.Context, f IndexedField) error
	Mappings(ctx context.Context, stream string) ([]FieldMapping, error)
	DeleteStream(ctx context.Context, name string) error
}

// RetentionApplier is the subset of StreamAdmin used at startup and on settings changes.
type RetentionApplier interface {
	ApplyRetention(ctx context.Context, rules []retention.Rule) error
}

// Pinger checks storage reachability (readiness).
type Pinger interface {
	Ping(ctx context.Context) error
}

// TimeRange is a half-open [From, To) interval.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// QueryStats describes the cost of a query.
type QueryStats struct {
	ElapsedMs         int64
	RowsRead          uint64
	BytesRead         uint64
	InterpretedAsText bool
}

// Series is one time series of a timeseries result.
type Series struct {
	Key    map[string]any
	Points [][]any // [tsMs, value…]
}

// Result is the outcome of Run; the populated fields depend on Kind. Events holds log events
// with the default columns; Records holds every other events-kind row (spans, metric points,
// `fields` projections) keyed by column name. Table rows and series points contain JSON-ready
// values (times as RFC 3339 strings or epoch milliseconds, non-finite numbers as nil).
type Result struct {
	Kind       tql.ResultKind
	Events     []model.Event
	Records    []map[string]any
	Columns    []tql.Column
	Rows       [][]any
	Span       time.Duration
	Series     []Series
	NextCursor *tql.Cursor
	Stats      QueryStats
}

// HistogramBucket counts rows per level (logs) or status (spans) in one bucket.
type HistogramBucket struct {
	T      time.Time
	Counts map[string]uint64
}

// Histogram is the result of a histogram query.
type Histogram struct {
	Span    time.Duration
	Buckets []HistogramBucket
}

// FieldInfo describes a field for the field list and autocomplete.
type FieldInfo struct {
	Name    string
	Builtin bool
	Numeric bool
	Hits    uint64
}

// StreamStats are storage statistics of one stream and source (SPEC §12.2).
type StreamStats struct {
	Stream            string
	Source            model.Source
	Rows              uint64
	Oldest            time.Time
	Newest            time.Time
	RatePer5Min       float64
	RatePer24h        float64
	CompressedBytes   uint64
	UncompressedBytes uint64
	Estimated         bool
	TopServices       []string
}

// PurgeRequest deletes data of one source in [From, To) (SPEC §12.5). Where/Args is the
// optional TQL filter condition from tql.CompileWhere (it repeats the time range); Stream ""
// means every stream.
type PurgeRequest struct {
	Stream string
	Source model.Source
	Where  string
	Args   []any
	From   time.Time
	To     time.Time
	DryRun bool
}

// PurgeJob is the outcome of a purge request: the estimate for a dry run, otherwise the
// partitions dropped and whether a lightweight DELETE was issued.
type PurgeJob struct {
	RowsEstimate       uint64
	DroppedPartitions  []string
	LightweightDeleted bool
}

// IndexedField describes a materialized property column (SPEC §12.4).
type IndexedField struct {
	Source model.Source
	Key    string
	Column string
}

// FieldMapping is one property key of a stream from the field catalog (SPEC §12.4).
type FieldMapping struct {
	Source    model.Source
	Key       string
	Type      string // string | number | mixed
	Hits      uint64
	FirstSeen time.Time
	LastSeen  time.Time
}

// ServerInfo describes the storage server (GET /api/v1/system).
type ServerInfo struct {
	Version string     `json:"version"`
	Disks   []DiskInfo `json:"disks"`
}

// DiskInfo is one storage disk.
type DiskInfo struct {
	Name       string `json:"name"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}
