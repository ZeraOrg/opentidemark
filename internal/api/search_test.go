// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/testutil"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

const testMaxRows = 1000

// searchModules wires the search and signals APIs on a fake querier. Live tails end at once
// (closed Streams) so route tables can call every route.
func searchModules(db *meta.DB, q *testutil.FakeQuerier, audit *service.Auditor, ip func(*http.Request) netip.Addr,
	logger *slog.Logger) []Registrar {
	signals := meta.NewSignals(db)
	streams := NewStreams()
	streams.Close()
	return []Registrar{
		&SearchAPI{Search: newTestSearch(db, q, audit), Now: time.Now, ClientIP: ip, Logger: logger, Streams: streams,
			TailInterval: time.Millisecond},
		&SignalsAPI{Signals: service.NewSignals(signals)},
	}
}

func newTestSearch(db *meta.DB, q *testutil.FakeQuerier, audit *service.Auditor) *service.Search {
	return service.NewSearch(service.SearchDeps{Store: q, Access: service.NewAccess(meta.NewTeams(db), meta.NewSettings(db)), Signals: meta.NewSignals(db), Indexed: meta.NewIndexedFields(db),
		Audit: audit, Database: "tidemark", MaxRows: testMaxRows, TailLag: time.Second, Now: time.Now})
}

func sampleEvent(msg string) model.Event {
	return model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: time.Date(2026, 10, 13, 11, 0, 0, 5, time.UTC), Stream: "app",
		Level: model.Error, Service: "api", Host: "h1", Message: msg, Protocol: "native",
		Properties: map[string]any{"status": 500, "formula": "=cmd()"}}
}

func TestQueryKinds(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	ev := sampleEvent("boom")
	e.querier.Result = store.Result{Events: []model.Event{ev}, NextCursor: &tql.Cursor{Timestamp: ev.Timestamp, ID: ev.ID.String()},
		Stats: store.QueryStats{ElapsedMs: 3, RowsRead: 10}}
	w := e.do(viewer, "POST", "/query", map[string]any{"q": "level:error", "from": "now-15m", "limit": 50})
	got := decodeBody[map[string]any](t, w)
	events, _ := got["events"].([]any)
	if w.Code != 200 || got["kind"] != "events" || len(events) != 1 || got["nextCursor"] == nil {
		t.Fatalf("events: %d %s", w.Code, w.Body.String())
	}
	first := events[0].(map[string]any)
	if first["message"] != "boom" || first["level"] != "Error" || first["properties"].(map[string]any)["status"] != float64(500) {
		t.Errorf("event view %v", first)
	}
	c := e.querier.LastCompiled()
	if c.Limit != 50 || c.Kind != tql.KindEvents {
		t.Errorf("compiled %+v", c)
	}
	// The cursor round-trips into the compiled keyset condition.
	e.do(viewer, "POST", "/query", map[string]any{"q": "level:error", "cursor": got["nextCursor"]})
	if c := e.querier.LastCompiled(); !strings.Contains(c.SQL, "(timestamp, id) < (") {
		t.Errorf("cursor not applied: %s", c.SQL)
	}

	e.querier.Result = store.Result{Columns: []tql.Column{{Name: "service", Type: tql.ColumnString}, {Name: "count", Type: tql.ColumnNumber}},
		Rows: [][]any{{"api", 3}}}
	got = decodeBody[map[string]any](t, e.do(viewer, "POST", "/query", map[string]any{"q": "* | stats count() by service"}))
	if got["kind"] != "table" || len(got["rows"].([]any)) != 1 || len(got["columns"].([]any)) != 2 {
		t.Errorf("table %v", got)
	}
	e.querier.Result = store.Result{Series: []store.Series{{Key: map[string]any{"level": "Error"}, Points: [][]any{{1, 2}}}}}
	got = decodeBody[map[string]any](t, e.do(viewer, "POST", "/query",
		map[string]any{"q": "* | stats count() by level span auto", "span": "10m", "from": "now-6h"}))
	if got["kind"] != "timeseries" || len(got["series"].([]any)) != 1 {
		t.Errorf("timeseries %v", got)
	}
	if c := e.querier.LastCompiled(); c.Span != 10*time.Minute {
		t.Errorf("span %v", c.Span)
	}
	e.querier.Result = store.Result{Records: []map[string]any{{"name": "GET /"}}, Columns: []tql.Column{{Name: "name"}}}
	got = decodeBody[map[string]any](t, e.do(viewer, "POST", "/query", map[string]any{"q": "source spans | *"}))
	if rec := got["events"].([]any)[0].(map[string]any); rec["name"] != "GET /" || got["columns"] == nil {
		t.Errorf("records %v", got)
	}
}

func TestQueryErrors(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	tests := []struct {
		name string
		body map[string]any
		code string
	}{
		{"syntax", map[string]any{"q": "level:error | stats"}, "invalid_query"},
		{"unknown level", map[string]any{"q": "level:erorr"}, "invalid_query"},
		{"limit", map[string]any{"q": "*", "limit": testMaxRows + 1}, "bad_request"},
		{"span", map[string]any{"q": "*", "span": "1.5s"}, "bad_request"},
		{"cursor", map[string]any{"q": "*", "cursor": "%%%"}, "bad_request"},
		{"range", map[string]any{"q": "*", "from": "now", "to": "now-1h"}, "bad_request"},
		{"time", map[string]any{"q": "*", "from": "yesterday"}, "bad_request"},
		{"signal", map[string]any{"q": "*", "signals": []string{"nope"}}, "bad_request"},
		{"unknown field", map[string]any{"q": "*", "bogus": 1}, "bad_request"},
	}
	for _, tt := range tests {
		w := e.do(viewer, "POST", "/query", tt.body)
		body := decodeBody[errorBody](t, w)
		if w.Code != 400 || body.Error.Code != tt.code {
			t.Errorf("%s: %d %s", tt.name, w.Code, w.Body.String())
		}
	}
	body := decodeBody[errorBody](t, e.do(viewer, "POST", "/query", map[string]any{"q": "level:erorr"}))
	if body.Error.Details["hint"] != "did you mean 'error'?" || body.Error.Details["pos"] != float64(6) {
		t.Errorf("details %+v", body.Error)
	}
	e.querier.Err = errors.New("clickhouse down")
	if w := e.do(viewer, "POST", "/query", map[string]any{"q": "*"}); w.Code != 500 || strings.Contains(w.Body.String(), "clickhouse") {
		t.Errorf("internal error leaked: %d %s", w.Code, w.Body.String())
	}
}

func TestValidate(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	got := decodeBody[validateView](t, e.do(viewer, "POST", "/query/validate", map[string]string{"q": "* | stats count() by service"}))
	if !got.OK || got.Kind != "table" || got.Error != nil {
		t.Errorf("valid %+v", got)
	}
	got = decodeBody[validateView](t, e.do(viewer, "POST", "/query/validate", map[string]string{"q": "level:erorr"}))
	if got.OK || got.Error == nil || got.Error.Pos != 6 || got.Error.Hint == "" {
		t.Errorf("invalid %+v", got)
	}
	got = decodeBody[validateView](t, e.do(viewer, "POST", "/query/validate", map[string]string{"q": "connection and"}))
	if !got.OK || !got.InterpretedAsText {
		t.Errorf("text %+v", got)
	}
}

func TestHistogramFacetsFields(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	e.querier.Hist = store.Histogram{Buckets: []store.HistogramBucket{{T: time.Unix(0, 0), Counts: map[string]uint64{"Error": 2}}}}
	h := decodeBody[histogramView](t, e.do(viewer, "GET", "/histogram?q=level:error&from=now-1h&to=now&buckets=60", nil))
	if h.Span != time.Minute.Milliseconds() || len(h.Buckets) != 1 || h.Buckets[0].Counts["Error"] != 2 {
		t.Errorf("histogram %+v", h)
	}
	if w := e.do(viewer, "GET", "/histogram?buckets=x", nil); w.Code != 400 {
		t.Errorf("bad buckets %d", w.Code)
	}
	if w := e.do(viewer, "GET", "/histogram?buckets=5000", nil); w.Code != 400 {
		t.Errorf("too many buckets %d", w.Code)
	}

	e.querier.Sample = store.Sample{Total: 3_000_000, Sampled: true, Cutoff: time.Now().Add(-10 * time.Minute)}
	e.querier.RunFunc = func(c *tql.Compiled) (store.Result, error) {
		if len(c.Columns) == 1 {
			return store.Result{Rows: [][]any{{uint64(10)}}}, nil
		}
		return store.Result{Rows: [][]any{{"api", uint64(6)}, {"web", uint64(3)}}}, nil
	}
	fs := decodeBody[[]facetView](t, e.do(viewer, "GET", "/facets?q=*&fields=service,level&top=5", nil))
	if len(fs) != 2 || fs[0].Field != "service" || len(fs[0].Values) != 2 || fs[0].OtherCount != 1 || !fs[0].Sampled {
		t.Errorf("facets %+v", fs)
	}
	if w := e.do(viewer, "GET", "/facets?fields=", nil); w.Code != 400 {
		t.Errorf("no fields %d", w.Code)
	}
	if w := e.do(viewer, "GET", "/facets?fields=@nope", nil); w.Code != 400 {
		t.Errorf("unknown builtin %d", w.Code)
	}

	e.querier.FieldList = []store.FieldInfo{{Name: "RequestPath", Hits: 4}, {Name: "service", Hits: 1}}
	fields := decodeBody[[]fieldView](t, e.do(viewer, "GET", "/fields?source=logs", nil))
	if len(fields) < 3 || !fields[0].Builtin || fields[len(fields)-1].Name != "RequestPath" {
		t.Errorf("fields %+v", fields)
	}
	if w := e.do(viewer, "GET", "/fields?source=nope", nil); w.Code != 400 {
		t.Errorf("bad source %d", w.Code)
	}
}

func TestEventsTracesMetrics(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	ev := sampleEvent("one")
	e.querier.Events = map[uuid.UUID]model.Event{ev.ID: ev}
	got := decodeBody[eventView](t, e.do(viewer, "GET", "/events/"+ev.ID.String(), nil))
	if got.ID != ev.ID.String() || got.Message != "one" {
		t.Errorf("event %+v", got)
	}
	if w := e.do(viewer, "GET", "/events/not-a-uuid", nil); w.Code != 400 {
		t.Errorf("bad id %d", w.Code)
	}
	if w := e.do(viewer, "GET", "/events/"+uuid.NewString(), nil); w.Code != 404 {
		t.Errorf("missing %d", w.Code)
	}
	e.querier.Older, e.querier.Newer = []model.Event{sampleEvent("before")}, []model.Event{}
	cv := decodeBody[contextView](t, e.do(viewer, "GET", "/events/"+ev.ID.String()+"/context?before=5&after=5", nil))
	if len(cv.Before) != 1 || cv.Event.ID != ev.ID.String() || cv.After == nil {
		t.Errorf("context %+v", cv)
	}
	if w := e.do(viewer, "GET", "/events/"+ev.ID.String()+"/context?before=1000", nil); w.Code != 400 {
		t.Errorf("context limit %d", w.Code)
	}

	e.querier.Summaries = []store.TraceSummary{{TraceID: "abc", SpanCount: 2}}
	ts := decodeBody[[]traceSummaryView](t, e.do(viewer, "GET", "/traces?q=service:api&limit=5", nil))
	if len(ts) != 1 || ts[0].Services == nil {
		t.Errorf("traces %+v", ts)
	}
	if f := e.querier.LastFilter(); f.Source != model.SourceSpans {
		t.Errorf("traces source %s", f.Source)
	}
	if w := e.do(viewer, "GET", "/traces?q=source+logs+|+*", nil); w.Code != 400 {
		t.Errorf("logs traces %d", w.Code)
	}
	traceID := strings.Repeat("ab", 16)
	if w := e.do(viewer, "GET", "/traces/"+traceID, nil); w.Code != 404 {
		t.Errorf("missing trace %d", w.Code)
	}
	e.querier.Spans = []model.Span{{TraceID: traceID, SpanID: "s1", Duration: 1500 * time.Microsecond}}
	tv := decodeBody[traceView](t, e.do(viewer, "GET", "/traces/"+strings.ToUpper(traceID), nil))
	if len(tv.Spans) != 1 || tv.Spans[0].DurationMs != 1.5 || tv.Events == nil {
		t.Errorf("trace %+v", tv)
	}
	if w := e.do(viewer, "GET", "/traces/xyz", nil); w.Code != 400 {
		t.Errorf("bad trace id %d", w.Code)
	}
	e.querier.Names = []store.MetricName{{Name: "cpu"}}
	if names := decodeBody[[]metricNameView](t, e.do(viewer, "GET", "/metrics/names?q=cp", nil)); len(names) != 1 {
		t.Errorf("names %+v", names)
	}
}

func TestExport(t *testing.T) {
	e := newEnv(t)
	editor := e.sessions["editor"]
	ev := sampleEvent("exported")
	e.querier.Result = store.Result{Events: []model.Event{ev}}
	w := e.do(editor, "GET", "/export?q=*&format=ndjson", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(w.Body.String(), `"message":"exported"`) {
		t.Errorf("ndjson %d %q", w.Code, w.Body.String())
	}
	w = e.do(editor, "GET", "/export?q=*&format=csv", nil)
	rows, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 || rows[0][0] != "timestamp" || rows[1][6] != "exported" {
		t.Fatalf("csv %v %q", err, w.Body.String())
	}
	e.querier.Result = store.Result{Records: []map[string]any{{"name": "=1+1", "start_time": time.Unix(0, 0)}},
		Columns: []tql.Column{{Name: "start_time"}, {Name: "name"}}}
	w = e.do(editor, "GET", "/export?q=source+spans+|+*&format=csv", nil)
	if !strings.Contains(w.Body.String(), "start_time,name") || !strings.Contains(w.Body.String(), "'=1+1") {
		t.Errorf("records csv %q", w.Body.String())
	}
	if w := e.do(editor, "GET", "/export?q=*+|+stats+count()", nil); w.Code != 400 {
		t.Errorf("stats export %d", w.Code)
	}
	if w := e.do(editor, "GET", "/export?format=xml", nil); w.Code != 400 {
		t.Errorf("bad format %d", w.Code)
	}
	audit := decodeBody[[]auditView](t, e.do(e.sessions["admin"], "GET", "/audit?action=export", nil))
	if len(audit) != 3 {
		t.Errorf("export audit entries %d", len(audit))
	}
}

func TestComplete(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	e.querier.FieldList = []store.FieldInfo{{Name: "RequestPath", Hits: 9}}
	c := decodeBody[tql.Completion](t, e.do(viewer, "GET", "/tql/complete?q=Req&source=logs", nil))
	if len(c.Suggestions) == 0 || c.Suggestions[0].Label != "RequestPath" {
		t.Errorf("fields %+v", c)
	}
	e.querier.RunFunc = func(c *tql.Compiled) (store.Result, error) {
		return store.Result{Rows: [][]any{{"checkout api", uint64(4)}, {"cart", uint64(2)}}}, nil
	}
	c = decodeBody[tql.Completion](t, e.do(viewer, "GET", "/tql/complete?q=service:c&pos=9", nil))
	if c.Field != "service" || len(c.Suggestions) != 2 || c.Suggestions[0].InsertText != `"checkout api"` {
		t.Errorf("values %+v", c)
	}
	if w := e.do(viewer, "GET", "/tql/complete?q=x&pos=y", nil); w.Code != 400 {
		t.Errorf("bad pos %d", w.Code)
	}
}

func TestTailStream(t *testing.T) {
	e := newEnv(t)
	q := &testutil.FakeQuerier{}
	logger := slog.New(slog.DiscardHandler)
	api := &SearchAPI{Search: newTestSearch(e.db, q, nil), Now: time.Now, Logger: logger, Streams: NewStreams(),
		TailInterval: 5 * time.Millisecond}
	ev := sampleEvent("live")
	q.Tailed, q.TailTotal = []store.TailEvent{{Event: ev, IngestedAt: time.Now()}}, 3
	ctx, cancel := context.WithCancel(auth.WithPrincipal(context.Background(), auth.Principal{UserID: "u1", Role: auth.RoleViewer}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/tail?q=service:api", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan error)
	go func() { done <- api.tail(rec, req) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if q.TailCallCount() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var names []string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if n, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			names = append(names, n)
		}
	}
	if rec.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" || len(names) != 2 || names[0] != "events" ||
		names[1] != "dropped" || !strings.Contains(rec.Body.String(), "data: 2\n") {
		t.Errorf("stream %q", rec.Body.String())
	}

	// Too many tails for one user ⇒ 429; stats queries and spans are rejected.
	s := newTestSearch(e.db, q, nil)
	p := auth.Principal{UserID: "u2"}
	for range service.MaxTailsPerUser {
		if _, err := s.OpenTail(context.Background(), p, "*", nil, service.Selection{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.OpenTail(context.Background(), p, "*", nil, service.Selection{}); !errors.Is(err, service.ErrTooManyTails) {
		t.Errorf("tail limit: %v", err)
	}
	if w := e.do(e.sessions["viewer"], "GET", "/tail?q=*+|+stats+count()", nil); w.Code != 400 {
		t.Errorf("stats tail %d", w.Code)
	}
}

func TestSignals(t *testing.T) {
	e := newEnv(t)
	editor, viewer, admin := e.sessions["editor"], e.sessions["viewer"], e.sessions["admin"]
	w := e.do(editor, "POST", "/signals", map[string]any{"name": "Errors", "tql": "level >= error", "color": "danger", "shared": true})
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	sig := decodeBody[signalView](t, w)
	private := decodeBody[signalView](t, e.do(editor, "POST", "/signals", map[string]any{"name": "Mine", "tql": "service:x"}))
	for _, bad := range []map[string]any{
		{"name": "", "tql": "*"},
		{"name": "x", "tql": "* | stats count()"},
		{"name": "x", "tql": "level:erorr"},
		{"name": "x", "tql": "*", "source": "files"},
		{"name": "x", "tql": "*", "color": "red;"},
	} {
		if w := e.do(editor, "POST", "/signals", bad); w.Code != 400 {
			t.Errorf("%v: %d", bad, w.Code)
		}
	}
	if list := decodeBody[[]signalView](t, e.do(viewer, "GET", "/signals", nil)); len(list) != 1 || list[0].ID != sig.ID {
		t.Errorf("viewer sees %+v", list)
	}
	if w := e.do(viewer, "GET", "/signals/"+private.ID, nil); w.Code != 404 {
		t.Errorf("private visible: %d", w.Code)
	}
	// The signal is ANDed into queries.
	e.do(viewer, "POST", "/query", map[string]any{"q": "service:api", "signals": []string{sig.ID}})
	if c := e.querier.LastCompiled(); !strings.Contains(c.SQL, "level_num >= ?") {
		t.Errorf("signal not applied: %s", c.SQL)
	}
	if w := e.do(viewer, "POST", "/query", map[string]any{"q": "*", "signals": []string{private.ID}}); w.Code != 400 {
		t.Errorf("private signal usable: %d", w.Code)
	}
	upd := map[string]any{"name": "Errors+", "tql": "level >= error", "shared": true, "updatedAt": sig.UpdatedAt}
	if w := e.do(e.login("editor1", testPassword), "PUT", "/signals/"+sig.ID, upd); w.Code != 200 {
		t.Fatalf("update %d %s", w.Code, w.Body.String())
	}
	if w := e.do(editor, "PUT", "/signals/"+sig.ID, upd); w.Code != 409 {
		t.Errorf("stale update %d", w.Code)
	}
	if w := e.do(editor, "PUT", "/signals/"+sig.ID, map[string]any{"name": "x", "tql": "*"}); w.Code != 400 {
		t.Errorf("missing updatedAt %d", w.Code)
	}
	other := decodeBody[signalView](t, e.do(admin, "POST", "/signals", map[string]any{"name": "Admin's", "tql": "*", "shared": true}))
	if w := e.do(editor, "DELETE", "/signals/"+other.ID, nil); w.Code != 403 {
		t.Errorf("delete foreign %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/signals/"+sig.ID, nil); w.Code != 204 {
		t.Errorf("admin delete %d", w.Code)
	}
	if w := e.do(editor, "GET", "/signals/"+sig.ID, nil); w.Code != 404 {
		t.Errorf("deleted %d", w.Code)
	}
}

func TestSearchSelectionParams(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	if w := e.do(viewer, "POST", "/query", map[string]any{"q": "*", "streams": []string{"app"},
		"environments": []string{"prod"}}); w.Code != 200 {
		t.Fatalf("query %d %s", w.Code, w.Body.String())
	}
	if c := e.querier.LastCompiled(); !strings.Contains(c.SQL, "stream IN (?)") || !hasArg(c.Args, "app") || !hasArg(c.Args, "prod") {
		t.Errorf("selection not applied: %s %v", c.SQL, c.Args)
	}
	if w := e.do(viewer, "GET", "/histogram?q=*&streams=app,web&env=prod&buckets=10", nil); w.Code != 200 {
		t.Fatalf("histogram %d", w.Code)
	}
	if f := e.querier.LastFilter(); f.Streams.String() != model.OnlyStreams("app", "web").String() || !strings.Contains(f.Where, "environment") {
		t.Errorf("histogram selection %v %s", f.Streams, f.Where)
	}
	for _, path := range []string{"/histogram?streams=Bad", "/facets?fields=service&streams=a%27b", "/fields?source=logs&streams=Bad",
		"/metrics/names?streams=Bad", "/traces?streams=Bad", "/tql/complete?q=x&streams=Bad", "/tail?q=*&streams=Bad"} {
		if w := e.do(viewer, "GET", path, nil); w.Code != 400 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := e.do(e.sessions["editor"], "GET", "/export?q=*&streams=Bad", nil); w.Code != 400 {
		t.Errorf("export %d", w.Code)
	}
	if w := e.do(viewer, "POST", "/query", map[string]any{"q": "*", "environments": []string{""}}); w.Code != 400 {
		t.Errorf("empty environment %d", w.Code)
	}
}

func hasArg(args []any, want any) bool {
	return slices.Contains(args, want)
}
