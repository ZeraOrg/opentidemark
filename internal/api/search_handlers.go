// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// defaultRange is the time range used when from/to are omitted (SPEC §11.1).
const defaultRange = time.Hour

// SearchAPI serves the search and analytics endpoints (SPEC §11.3).
type SearchAPI struct {
	Search       *service.Search
	Now          func() time.Time
	ClientIP     func(*http.Request) netip.Addr
	Logger       *slog.Logger
	Streams      *Streams      // ends live tails on shutdown (optional)
	TailInterval time.Duration // live-tail poll interval (0 ⇒ service.TailInterval)
}

// Routes implements Registrar.
func (a *SearchAPI) Routes() []Route {
	return []Route{
		{http.MethodPost, "/query", Viewer, a.query},
		{http.MethodPost, "/query/validate", Viewer, a.validate},
		{http.MethodGet, "/histogram", Viewer, a.histogram},
		{http.MethodGet, "/facets", Viewer, a.facets},
		{http.MethodGet, "/fields", Viewer, a.fields},
		{http.MethodGet, "/events/{id}", Viewer, a.event},
		{http.MethodGet, "/events/{id}/context", Viewer, a.eventContext},
		{http.MethodGet, "/tail", Viewer, a.tail},
		{http.MethodGet, "/traces", Viewer, a.traces},
		{http.MethodGet, "/traces/{traceId}", Viewer, a.trace},
		{http.MethodGet, "/metrics/names", Viewer, a.metricNames},
		{http.MethodGet, "/export", Editor, a.export},
		{http.MethodGet, "/tql/complete", Viewer, a.complete},
	}
}

// rangeOf parses from/to (RFC 3339 or relative) with the default of the last hour.
func (a *SearchAPI) rangeOf(from, to string) (service.Range, error) {
	now := a.Now().UTC()
	r := service.Range{From: now.Add(-defaultRange), To: now}
	var err error
	if from != "" {
		if r.From, err = ParseTimeParam(from, now); err != nil {
			return r, err
		}
	}
	if to != "" {
		if r.To, err = ParseTimeParam(to, now); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (a *SearchAPI) queryRange(q url.Values) (service.Range, error) {
	return a.rangeOf(q.Get("from"), q.Get("to"))
}

// intParam parses an optional integer query parameter (0 when absent).
func intParam(q url.Values, name string) (int, error) {
	v := q.Get(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errBadRequest("%s must be an integer", name)
	}
	return n, nil
}

// selectionParam reads the stream and environment selection of a GET search route
// (`streams=a,b`, `env=prod`; comma-separated and/or repeated, SPEC §11.3).
func selectionParam(q url.Values) service.Selection {
	return service.Selection{Streams: listParam(q, "streams"), Environments: listParam(q, "env")}
}

// listParam reads a comma-separated and/or repeated parameter.
func listParam(q url.Values, name string) []string {
	var out []string
	for _, v := range q[name] {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

type queryRequest struct {
	Q            string   `json:"q"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Limit        int      `json:"limit"`
	Cursor       string   `json:"cursor"`
	Span         string   `json:"span"`
	Signals      []string `json:"signals"`
	Streams      []string `json:"streams"`
	Environments []string `json:"environments"`
}

func (a *SearchAPI) query(w http.ResponseWriter, r *http.Request) error {
	var in queryRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	rng, err := a.rangeOf(in.From, in.To)
	if err != nil {
		return err
	}
	cur, err := service.DecodeCursor(in.Cursor)
	if err != nil {
		return err
	}
	res, err := a.Search.Query(r.Context(), principal(r), service.QueryRequest{Q: in.Q, Range: rng, Limit: in.Limit,
		Cursor: cur, Span: in.Span, Signals: in.Signals,
		Select: service.Selection{Streams: in.Streams, Environments: in.Environments}})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toQueryView(res))
	return nil
}

type validateRequest struct {
	Q    string `json:"q"`
	From string `json:"from"`
	To   string `json:"to"`
}

type validateView struct {
	OK                bool           `json:"ok"`
	Kind              string         `json:"kind,omitempty"`
	InterpretedAsText bool           `json:"interpretedAsText"`
	Error             *validateError `json:"error,omitempty"`
}

type validateError struct {
	Message string `json:"message"`
	Pos     int    `json:"pos"`
	Len     int    `json:"len"`
	Hint    string `json:"hint,omitempty"`
}

func (a *SearchAPI) validate(w http.ResponseWriter, r *http.Request) error {
	var in validateRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	rng, err := a.rangeOf(in.From, in.To)
	if err != nil {
		return err
	}
	v, err := a.Search.Validate(in.Q, rng)
	if err != nil {
		return err
	}
	out := validateView{OK: v.OK, Kind: string(v.Kind), InterpretedAsText: v.InterpretedAsText}
	if v.Error != nil {
		var qe *tql.Error
		if !errors.As(v.Error, &qe) {
			return v.Error
		}
		out.Error = &validateError{Message: qe.Msg, Pos: qe.Pos, Len: qe.Len, Hint: qe.Hint}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *SearchAPI) histogram(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	buckets, err := intParam(q, "buckets")
	if err != nil {
		return err
	}
	h, err := a.Search.Histogram(r.Context(), principal(r), service.HistogramRequest{Q: q.Get("q"), Range: rng,
		Buckets: buckets, Signals: listParam(q, "signals"), Select: selectionParam(q)})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toHistogramView(h))
	return nil
}

func (a *SearchAPI) facets(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	top, err := intParam(q, "top")
	if err != nil {
		return err
	}
	fs, err := a.Search.Facets(r.Context(), principal(r), service.FacetsRequest{Q: q.Get("q"), Range: rng,
		Fields: listParam(q, "fields"), Top: top, Signals: listParam(q, "signals"), Select: selectionParam(q)})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toFacetViews(fs))
	return nil
}

func sourceParam(q url.Values) model.Source {
	if v := q.Get("source"); v != "" {
		return model.Source(strings.ToLower(v))
	}
	return model.SourceLogs
}

func (a *SearchAPI) fields(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	fs, err := a.Search.Fields(r.Context(), principal(r), sourceParam(q), rng, selectionParam(q))
	if err != nil {
		return err
	}
	out := make([]fieldView, 0, len(fs))
	for _, f := range fs {
		out = append(out, fieldView{Name: f.Name, Builtin: f.Builtin, Numeric: f.Numeric, Hits: f.Hits})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *SearchAPI) event(w http.ResponseWriter, r *http.Request) error {
	ev, err := a.Search.Event(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toEventView(ev))
	return nil
}

type contextView struct {
	Before []eventView `json:"before"`
	Event  eventView   `json:"event"`
	After  []eventView `json:"after"`
}

func (a *SearchAPI) eventContext(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	before, after := service.DefaultContextEvents, service.DefaultContextEvents
	var err error
	if q.Has("before") {
		if before, err = intParam(q, "before"); err != nil {
			return err
		}
	}
	if q.Has("after") {
		if after, err = intParam(q, "after"); err != nil {
			return err
		}
	}
	c, err := a.Search.Context(r.Context(), principal(r), r.PathValue("id"), before, after)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, contextView{Before: toEventViews(c.Before), Event: toEventView(c.Event), After: toEventViews(c.After)})
	return nil
}

func (a *SearchAPI) traces(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	limit, err := intParam(q, "limit")
	if err != nil {
		return err
	}
	ts, err := a.Search.Traces(r.Context(), principal(r), service.TracesRequest{Q: q.Get("q"), Range: rng, Limit: limit,
		Select: selectionParam(q)})
	if err != nil {
		return err
	}
	out := make([]traceSummaryView, 0, len(ts))
	for _, t := range ts {
		out = append(out, traceSummaryView{TraceID: t.TraceID, RootService: t.RootService, RootName: t.RootName,
			Start: t.Start, DurationMs: t.DurationMs, SpanCount: t.SpanCount, ErrorCount: t.ErrorCount, Services: nonNil(t.Services)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type traceView struct {
	Spans  []spanView  `json:"spans"`
	Events []eventView `json:"events"`
}

func (a *SearchAPI) trace(w http.ResponseWriter, r *http.Request) error {
	spans, events, err := a.Search.Trace(r.Context(), principal(r), r.PathValue("traceId"))
	if err != nil {
		return err
	}
	v := traceView{Spans: make([]spanView, 0, len(spans)), Events: toEventViews(events)}
	for _, s := range spans {
		v.Spans = append(v.Spans, toSpanView(s))
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (a *SearchAPI) metricNames(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	names, err := a.Search.MetricNames(r.Context(), principal(r), q.Get("q"), rng, selectionParam(q))
	if err != nil {
		return err
	}
	out := make([]metricNameView, 0, len(names))
	for _, n := range names {
		out = append(out, metricNameView{Name: n.Name, Kind: n.Kind, Unit: n.Unit, Description: n.Description, Monotonic: n.Monotonic})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *SearchAPI) complete(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	text := q.Get("q")
	pos := len(text)
	if q.Has("pos") {
		var err error
		if pos, err = intParam(q, "pos"); err != nil {
			return err
		}
	}
	c, err := a.Search.Complete(r.Context(), principal(r), text, pos, sourceParam(q), selectionParam(q))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}
