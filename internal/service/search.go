// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// Search limits (SPEC §11.3).
const (
	DefaultHistogramBuckets = 80
	MaxHistogramBuckets     = 1000
	DefaultFacetTop         = 10
	MaxFacetTop             = 100
	MaxFacetFields          = 20
	FacetSampleRows         = 1_000_000
	MaxSignalsPerQuery      = 20
	DefaultTraceLimit       = 50
	MaxTraceLimit           = 500
	MaxMetricNames          = 1000
	MaxMetricNameFilter     = 256
	DefaultContextEvents    = 20
	MaxContextEvents        = 200
	ExportMaxEvents         = 100_000
	completeFieldsRange     = 24 * time.Hour
	completeValuesRange     = time.Hour
	completeValueCount      = 10
)

// ErrIndexedFields is wrapped when the indexed-field list cannot be loaded.
var ErrIndexedFields = errors.New("load indexed fields")

// SignalReader loads signals for queries.
type SignalReader interface {
	Get(ctx context.Context, id string) (meta.Signal, error)
}

// IndexedFieldRows lists the materialized property columns (SPEC §12.4).
type IndexedFieldRows interface {
	List(ctx context.Context) ([]meta.IndexedField, error)
}

// SearchDeps are the collaborators of Search.
type SearchDeps struct {
	Store    store.Querier
	Signals  SignalReader
	Indexed  IndexedFieldRows // optional
	Audit    *Auditor
	Database string
	MaxRows  int           // TIDEMARK_QUERY_MAX_ROWS
	TailLag  time.Duration // delay before ingested rows are considered complete (§11.3 tail)
	Now      func() time.Time
}

// Search runs TQL queries and the explorer read paths (SPEC §11.3).
type Search struct {
	d     SearchDeps
	tails *tailLimiter
}

// NewSearch returns the search service.
func NewSearch(d SearchDeps) *Search {
	if d.MaxRows <= 0 {
		d.MaxRows = tql.DefaultMaxRows
	}
	return &Search{d: d, tails: newTailLimiter(MaxTailsPerUser)}
}

// Range is a half-open request time range.
type Range struct {
	From time.Time
	To   time.Time
}

func (r Range) valid() error {
	if !r.From.Before(r.To) {
		return invalid("from", "must be before to")
	}
	return nil
}

func (r Range) store() store.TimeRange { return store.TimeRange{From: r.From, To: r.To} }

// prepared is a parsed query with its active signals.
type prepared struct {
	q       *tql.Query
	extra   []*tql.Query
	indexed map[model.Source]map[string]string
}

// prepare parses text (forcing src when it is set) and loads the signals visible to p.
func (s *Search) prepare(ctx context.Context, p auth.Principal, text string, src model.Source, signals []string) (*prepared, error) {
	var q *tql.Query
	var err error
	if src != "" {
		q, err = parseFor(src, text)
	} else {
		q, err = tql.Parse(text)
	}
	if err != nil {
		return nil, err
	}
	out := &prepared{q: q}
	if out.extra, err = s.signalFilters(ctx, p, q.Source, signals); err != nil {
		return nil, err
	}
	if out.indexed, err = s.indexedFields(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Search) signalFilters(ctx context.Context, p auth.Principal, src model.Source, ids []string) ([]*tql.Query, error) {
	if len(ids) > MaxSignalsPerQuery {
		return nil, invalid("signals", fmt.Sprintf("at most %d signals", MaxSignalsPerQuery))
	}
	out := make([]*tql.Query, 0, len(ids))
	for _, id := range ids {
		sig, err := s.d.Signals.Get(ctx, id)
		if errors.Is(err, meta.ErrNotFound) || (err == nil && !signalVisible(sig, p)) {
			return nil, invalid("signals", "unknown signal "+id)
		}
		if err != nil {
			return nil, err
		}
		if model.Source(sig.Source) != src {
			return nil, invalid("signals", fmt.Sprintf("signal %q is for %s, the query is for %s", sig.Name, sig.Source, src))
		}
		q, err := parseFor(src, sig.TQL)
		if err != nil {
			return nil, invalid("signals", fmt.Sprintf("signal %q is invalid: %v", sig.Name, err))
		}
		out = append(out, q)
	}
	return out, nil
}

func (s *Search) indexedFields(ctx context.Context) (map[model.Source]map[string]string, error) {
	if s.d.Indexed == nil {
		return nil, nil
	}
	list, err := s.d.Indexed.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndexedFields, err)
	}
	out := map[model.Source]map[string]string{}
	for _, f := range list {
		src := model.Source(f.Source)
		if out[src] == nil {
			out[src] = map[string]string{}
		}
		out[src][f.Key] = f.ColumnName
	}
	return out, nil
}

func (s *Search) options(pr *prepared, r Range) tql.Options {
	return tql.Options{Database: s.d.Database, From: r.From, To: r.To, Now: s.d.Now(), MaxRows: s.d.MaxRows,
		IndexedFields: pr.indexed, ExtraFilters: pr.extra}
}

// filter compiles the WHERE condition of pr for r.
func (s *Search) filter(pr *prepared, r Range) (store.Filter, error) {
	where, args, err := tql.CompileWhere(pr.q, s.options(pr, r))
	if err != nil {
		return store.Filter{}, err
	}
	return store.Filter{Source: pr.q.Source, Where: where, Args: args, Range: r.store()}, nil
}

// QueryRequest is the body of POST /query.
type QueryRequest struct {
	Q       string
	Range   Range
	Limit   int
	Cursor  *tql.Cursor
	Span    string // "", "auto" or a duration such as "5m"
	Signals []string
}

// Query compiles and runs a query.
func (s *Search) Query(ctx context.Context, p auth.Principal, req QueryRequest) (store.Result, error) {
	if err := req.Range.valid(); err != nil {
		return store.Result{}, err
	}
	if req.Limit < 0 || req.Limit > s.d.MaxRows {
		return store.Result{}, invalid("limit", fmt.Sprintf("must be between 1 and %d", s.d.MaxRows))
	}
	span, err := parseSpan(req.Span)
	if err != nil {
		return store.Result{}, err
	}
	pr, err := s.prepare(ctx, p, req.Q, "", req.Signals)
	if err != nil {
		return store.Result{}, err
	}
	opts := s.options(pr, req.Range)
	opts.Limit, opts.Cursor, opts.AutoSpan = req.Limit, req.Cursor, span
	c, err := tql.Compile(pr.q, opts)
	if err != nil {
		return store.Result{}, err
	}
	return s.d.Store.Run(ctx, c)
}

// parseSpan parses the span request parameter: "" or "auto" ⇒ 0 (server choice).
func parseSpan(v string) (time.Duration, error) {
	if v == "" || strings.EqualFold(v, "auto") {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second || d%time.Second != 0 {
		return 0, invalid("span", `must be "auto" or a whole number of seconds such as "30s" or "5m"`)
	}
	return d, nil
}

// Validation is the outcome of Validate.
type Validation struct {
	OK                bool
	Kind              tql.ResultKind
	Error             error
	InterpretedAsText bool
}

// Validate checks a query for the editor (POST /query/validate); query errors are reported
// in the result, not returned.
func (s *Search) Validate(q string, r Range) (Validation, error) {
	if err := r.valid(); err != nil {
		return Validation{}, err
	}
	return validation(s.compileOnly(q, r)), nil
}

// compileOnly parses and compiles q without signals (validation).
func (s *Search) compileOnly(q string, r Range) (*tql.Compiled, error) {
	pq, err := tql.Parse(q)
	if err != nil {
		return nil, err
	}
	return tql.Compile(pq, tql.Options{Database: s.d.Database, From: r.From, To: r.To, Now: s.d.Now(), MaxRows: s.d.MaxRows})
}

func validation(c *tql.Compiled, err error) Validation {
	if err != nil {
		return Validation{Error: err}
	}
	return Validation{OK: true, Kind: c.Kind, InterpretedAsText: c.InterpretedAsText}
}

// HistogramRequest is the input of Histogram.
type HistogramRequest struct {
	Q       string
	Range   Range
	Buckets int
	Signals []string
}

// Histogram counts matching rows over time with a span chosen so the bucket count is at most
// the requested one.
func (s *Search) Histogram(ctx context.Context, p auth.Principal, req HistogramRequest) (store.Histogram, error) {
	if err := req.Range.valid(); err != nil {
		return store.Histogram{}, err
	}
	if req.Buckets == 0 {
		req.Buckets = DefaultHistogramBuckets
	}
	if req.Buckets < 1 || req.Buckets > MaxHistogramBuckets {
		return store.Histogram{}, invalid("buckets", fmt.Sprintf("must be between 1 and %d", MaxHistogramBuckets))
	}
	pr, err := s.prepare(ctx, p, req.Q, "", req.Signals)
	if err != nil {
		return store.Histogram{}, err
	}
	f, err := s.filter(pr, req.Range)
	if err != nil {
		return store.Histogram{}, err
	}
	return s.d.Store.Histogram(ctx, f, tql.AutoSpan(req.Range.To.Sub(req.Range.From), req.Buckets))
}

// Fields lists built-in fields followed by the property keys seen in r (by hits).
func (s *Search) Fields(ctx context.Context, src model.Source, r Range) ([]store.FieldInfo, error) {
	if !src.Valid() {
		return nil, invalid("source", "must be logs, spans or metrics")
	}
	if err := r.valid(); err != nil {
		return nil, err
	}
	props, err := s.d.Store.Fields(ctx, src, r.store())
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []store.FieldInfo{}
	for _, b := range tql.BuiltinFields(src) {
		seen[strings.ToLower(b.Name)] = true
		out = append(out, store.FieldInfo{Name: b.Name, Builtin: true, Numeric: b.Numeric})
	}
	for _, f := range props {
		if !seen[strings.ToLower(f.Name)] {
			out = append(out, f)
		}
	}
	return out, nil
}

// Event returns one event by id.
func (s *Search) Event(ctx context.Context, id string) (model.Event, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return model.Event{}, invalid("id", "must be an event UUID")
	}
	return s.d.Store.Event(ctx, uid)
}

// EventContext is an event with its surrounding events.
type EventContext struct {
	Before []model.Event
	Event  model.Event
	After  []model.Event
}

// Context returns up to before/after events of the same service and host around an event.
func (s *Search) Context(ctx context.Context, id string, before, after int) (EventContext, error) {
	if before < 0 || before > MaxContextEvents || after < 0 || after > MaxContextEvents {
		return EventContext{}, invalid("before", fmt.Sprintf("before and after must be between 0 and %d", MaxContextEvents))
	}
	ev, err := s.Event(ctx, id)
	if err != nil {
		return EventContext{}, err
	}
	older, newer, err := s.d.Store.Context(ctx, ev, before, after)
	if err != nil {
		return EventContext{}, err
	}
	return EventContext{Before: older, Event: ev, After: newer}, nil
}

// TracesRequest is the input of Traces.
type TracesRequest struct {
	Q     string
	Range Range
	Limit int
}

// Traces searches traces with a span matching q (source spans).
func (s *Search) Traces(ctx context.Context, p auth.Principal, req TracesRequest) ([]store.TraceSummary, error) {
	if err := req.Range.valid(); err != nil {
		return nil, err
	}
	if req.Limit == 0 {
		req.Limit = DefaultTraceLimit
	}
	if req.Limit < 1 || req.Limit > MaxTraceLimit {
		return nil, invalid("limit", fmt.Sprintf("must be between 1 and %d", MaxTraceLimit))
	}
	pr, err := s.prepare(ctx, p, req.Q, model.SourceSpans, nil)
	if err != nil {
		return nil, err
	}
	f, err := s.filter(pr, req.Range)
	if err != nil {
		return nil, err
	}
	return s.d.Store.Traces(ctx, f, req.Limit)
}

// Trace returns the spans and events of one trace.
func (s *Search) Trace(ctx context.Context, traceID string) ([]model.Span, []model.Event, error) {
	id := strings.ToLower(traceID)
	if !isHex(id, traceIDLen) {
		return nil, nil, invalid("traceId", "must be 32 hexadecimal characters")
	}
	return s.d.Store.Trace(ctx, id)
}

const traceIDLen = 32

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// MetricNames lists metric names containing substr.
func (s *Search) MetricNames(ctx context.Context, substr string, r Range) ([]store.MetricName, error) {
	if err := r.valid(); err != nil {
		return nil, err
	}
	if len(substr) > MaxMetricNameFilter {
		return nil, invalid("q", fmt.Sprintf("must be at most %d bytes", MaxMetricNameFilter))
	}
	return s.d.Store.MetricNames(ctx, substr, r.store(), MaxMetricNames)
}
