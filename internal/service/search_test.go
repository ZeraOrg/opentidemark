// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/testutil"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

var (
	searchNow  = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	lastHour   = Range{From: searchNow.Add(-time.Hour), To: searchNow}
	viewerUser = auth.Principal{UserID: "u-viewer", Role: auth.RoleViewer}
	editorUser = auth.Principal{UserID: "u-editor", Role: auth.RoleEditor}
	adminUser  = auth.Principal{UserID: "u-admin", Role: auth.RoleAdmin}
)

type searchEnv struct {
	db      *meta.DB
	q       *testutil.FakeQuerier
	s       *Search
	signals *Signals
	now     time.Time
}

func newSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	db := openDB(t)
	e := &searchEnv{db: db, q: &testutil.FakeQuerier{}, now: searchNow}
	e.s = NewSearch(SearchDeps{Store: e.q, Access: NewAccess(meta.NewTeams(db), meta.NewSettings(db)), Signals: meta.NewSignals(db), Indexed: meta.NewIndexedFields(db),
		Audit: NewAuditor(meta.NewAuditLog(db), quiet(), time.Now), Database: "tidemark", MaxRows: 100,
		TailLag: 2 * time.Second, Now: func() time.Time { return e.now }})
	e.signals = NewSignals(meta.NewSignals(db))
	return e
}

func TestQueryOptions(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	if err := meta.NewIndexedFields(e.db).Create(ctx, &meta.IndexedField{Source: "logs", Key: "RequestPath", ColumnName: "f_requestpath"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "RequestPath:/a", Range: lastHour}); err != nil {
		t.Fatal(err)
	}
	if c := e.q.LastCompiled(); !strings.Contains(c.SQL, "f_requestpath") || !strings.Contains(c.SQL, "tidemark.events") {
		t.Errorf("indexed field / database not applied: %s", c.SQL)
	}
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "* | stats count() span auto", Range: lastHour, Span: "auto"}); err != nil {
		t.Fatal(err)
	}
	if c := e.q.LastCompiled(); c.Span != time.Minute {
		t.Errorf("auto span %v", c.Span)
	}
	bad := []QueryRequest{
		{Q: "*", Range: Range{From: searchNow, To: searchNow}},
		{Q: "*", Range: lastHour, Limit: 101},
		{Q: "*", Range: lastHour, Limit: -1},
		{Q: "*", Range: lastHour, Span: "0s"},
		{Q: "*", Range: lastHour, Span: "abc"},
		{Q: "*", Range: lastHour, Signals: make([]string, MaxSignalsPerQuery+1)},
	}
	for _, req := range bad {
		var ve *ValidationError
		if _, err := e.s.Query(ctx, viewerUser, req); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	var qe *tql.Error
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "level:nope", Range: lastHour}); !errors.As(err, &qe) {
		t.Errorf("query error: %v", err)
	}
}

func TestQuerySignals(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	shared, err := e.signals.Create(ctx, editorUser, SignalInput{Name: "errors", TQL: "level >= error", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	spans, _ := e.signals.Create(ctx, editorUser, SignalInput{Name: "slow", TQL: "duration > 100", Source: "spans", Shared: true})
	private, _ := e.signals.Create(ctx, editorUser, SignalInput{Name: "mine", TQL: "service:x"})
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "service:api", Range: lastHour, Signals: []string{shared.ID}}); err != nil {
		t.Fatal(err)
	}
	if c := e.q.LastCompiled(); !strings.Contains(c.SQL, "level_num >= ?") {
		t.Errorf("signal filter missing: %s", c.SQL)
	}
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "source spans | *", Range: lastHour, Signals: []string{spans.ID}}); err != nil {
		t.Errorf("spans signal: %v", err)
	}
	for name, ids := range map[string][]string{"private": {private.ID}, "other source": {spans.ID}, "missing": {"x"}} {
		var ve *ValidationError
		if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "*", Range: lastHour, Signals: ids}); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := e.s.Query(ctx, editorUser, QueryRequest{Q: "*", Range: lastHour, Signals: []string{private.ID}}); err != nil {
		t.Errorf("owner's private signal: %v", err)
	}
}

func TestHistogramAndFacets(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	if _, err := e.s.Histogram(ctx, viewerUser, HistogramRequest{Q: "*", Range: lastHour}); err != nil {
		t.Fatal(err)
	}
	if got := e.q.HistSpans[0]; got != time.Minute {
		t.Errorf("default buckets span %v", got)
	}
	if _, err := e.s.Histogram(ctx, viewerUser, HistogramRequest{Q: "*", Range: lastHour, Buckets: 4}); err != nil || e.q.HistSpans[1] != 30*time.Minute {
		t.Errorf("4 buckets: %v %v", e.q.HistSpans, err)
	}
	if _, err := e.s.Histogram(ctx, viewerUser, HistogramRequest{Q: "*", Range: lastHour, Buckets: MaxHistogramBuckets + 1}); err == nil {
		t.Error("too many buckets accepted")
	}

	cutoff := searchNow.Add(-5 * time.Minute)
	e.q.Sample = store.Sample{Total: 2_000_000, Sampled: true, Cutoff: cutoff}
	e.q.RunFunc = func(c *tql.Compiled) (store.Result, error) {
		if len(c.Columns) == 1 {
			return store.Result{Rows: [][]any{{"25"}}}, nil
		}
		return store.Result{Rows: [][]any{{"a", uint64(10)}, {2.5, int64(5)}, {nil, float64(1)}}}, nil
	}
	fs, err := e.s.Facets(ctx, viewerUser, FacetsRequest{Q: "level:error | where service:x", Range: lastHour, Fields: []string{"service", "level", "weird key"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 3 || !fs[0].Sampled || fs[0].OtherCount != 9 || fs[0].Values[1].Value != "2.5" || fs[0].Values[2].Value != "" {
		t.Errorf("facets %+v", fs)
	}
	for _, c := range e.q.Compiled {
		if !hasArg(c.Args, cutoff) {
			t.Errorf("facet query not restricted to the sample: %v", c.Args)
		}
	}
	levelSQL := e.q.Compiled[2].SQL
	if strings.Contains(levelSQL, "level_num != ''") {
		t.Errorf("level facet has an existence filter: %s", levelSQL)
	}
	for _, req := range []FacetsRequest{
		{Q: "*", Range: lastHour},
		{Q: "*", Range: lastHour, Fields: []string{"a"}, Top: MaxFacetTop + 1},
		{Q: "*", Range: Range{}, Fields: []string{"a"}},
	} {
		if _, err := e.s.Facets(ctx, viewerUser, req); err == nil {
			t.Errorf("%+v accepted", req)
		}
	}
	e.q.Err = errors.New("down")
	if _, err := e.s.Facets(ctx, viewerUser, FacetsRequest{Q: "*", Range: lastHour, Fields: []string{"a"}}); err == nil {
		t.Error("store error swallowed")
	}
}

func hasArg(args []any, want any) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestFieldsEventsTraces(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	e.q.FieldList = []store.FieldInfo{{Name: "Message", Hits: 3}, {Name: "user.id", Numeric: true, Hits: 2}}
	fs, err := e.s.Fields(ctx, viewerUser, model.SourceSpans, lastHour, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if !fs[0].Builtin || fs[len(fs)-1].Name != "user.id" || fs[len(fs)-2].Name != "Message" {
		t.Errorf("fields %+v", fs)
	}
	if _, err := e.s.Fields(ctx, viewerUser, "nope", lastHour, Selection{}); err == nil {
		t.Error("bad source accepted")
	}
	if _, err := e.s.Event(ctx, viewerUser, "nope"); err == nil {
		t.Error("bad id accepted")
	}
	if _, err := e.s.Context(ctx, viewerUser, "nope", 1, MaxContextEvents+1); err == nil {
		t.Error("context limit not enforced")
	}
	if _, _, err := e.s.Trace(ctx, viewerUser, "ABC"); err == nil {
		t.Error("short trace id accepted")
	}
	if _, err := e.s.Traces(ctx, viewerUser, TracesRequest{Q: "status:error", Range: lastHour}); err != nil {
		t.Fatal(err)
	}
	if f := e.q.LastFilter(); f.Source != model.SourceSpans || !strings.Contains(f.Where, "status_code") {
		t.Errorf("traces filter %+v", f)
	}
	if _, err := e.s.Traces(ctx, viewerUser, TracesRequest{Q: "*", Range: lastHour, Limit: MaxTraceLimit + 1}); err == nil {
		t.Error("trace limit not enforced")
	}
	if _, err := e.s.MetricNames(ctx, viewerUser, strings.Repeat("x", MaxMetricNameFilter+1), lastHour, Selection{}); err == nil {
		t.Error("metric filter length not enforced")
	}
}

func TestValidateQuery(t *testing.T) {
	e := newSearchEnv(t)
	v, err := e.s.Validate("* | stats count() by service span 1m", lastHour)
	if err != nil || !v.OK || v.Kind != tql.KindTimeseries {
		t.Errorf("valid: %+v %v", v, err)
	}
	if v, _ := e.s.Validate("* | stats count() span 1s", Range{From: searchNow.Add(-24 * time.Hour), To: searchNow}); v.OK {
		t.Error("too many buckets validated")
	}
	if _, err := e.s.Validate("*", Range{}); err == nil {
		t.Error("empty range accepted")
	}
}

func TestTailPolling(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	tail, err := e.s.OpenTail(ctx, viewerUser, "service:api", nil, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	if evs, dropped, err := tail.Poll(ctx); err != nil || evs != nil || dropped != 0 {
		t.Errorf("poll before time advanced: %v %d %v", evs, dropped, err)
	}
	e.now = e.now.Add(time.Second)
	e.q.Tailed, e.q.TailTotal = []store.TailEvent{{}}, 60
	evs, dropped, err := tail.Poll(ctx)
	if err != nil || len(evs) != 1 || dropped != 59 {
		t.Errorf("poll: %d %d %v", len(evs), dropped, err)
	}
	call := e.q.TailCalls[0]
	if !call[0].Equal(searchNow.Add(-2*time.Second)) || !call[1].Equal(searchNow.Add(-time.Second)) {
		t.Errorf("tail window %v", call)
	}
	if f := e.q.LastFilter(); !strings.Contains(f.Where, "service") {
		t.Errorf("tail filter %s", f.Where)
	}
	tail.Close()
	tail.Close()
	if _, err := e.s.OpenTail(ctx, viewerUser, "source spans | *", nil, Selection{}); err == nil {
		t.Error("spans tail accepted")
	}
	if _, err := e.s.OpenTail(ctx, viewerUser, "level:", nil, Selection{}); err == nil {
		t.Error("invalid tail query accepted")
	}
	e.q.Err = errors.New("down")
	t2, _ := e.s.OpenTail(ctx, viewerUser, "*", nil, Selection{})
	e.now = e.now.Add(time.Second)
	if _, _, err := t2.Poll(ctx); err == nil {
		t.Error("poll error swallowed")
	}
}

func TestExportPages(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	pages := 0
	e.q.RunFunc = func(c *tql.Compiled) (store.Result, error) {
		pages++
		res := store.Result{Kind: c.Kind, Events: make([]model.Event, c.Limit)}
		if pages < 3 {
			res.NextCursor = &tql.Cursor{Timestamp: searchNow, ID: "x"}
		}
		return res, nil
	}
	rows := 0
	n, err := e.s.Export(ctx, SystemActor, viewerUser, ExportRequest{Q: "*", Range: lastHour, Format: ExportCSV},
		func(r store.Result) error { rows += len(r.Events); return nil })
	if err != nil || n != 300 || rows != 300 || pages != 3 {
		t.Errorf("export n=%d rows=%d pages=%d err=%v", n, rows, pages, err)
	}
	if _, err := e.s.Export(ctx, SystemActor, viewerUser, ExportRequest{Q: "*", Range: lastHour, Format: ExportNDJSON},
		func(store.Result) error { return errors.New("client gone") }); err == nil {
		t.Error("emit error swallowed")
	}
	entries, _ := meta.NewAuditLog(e.db).List(ctx, meta.AuditFilter{Action: ActionExport})
	if len(entries) != 2 {
		t.Errorf("audit entries %d", len(entries))
	}
}

func TestCompleteValues(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	e.q.FieldList = []store.FieldInfo{{Name: "RequestPath", Hits: 2}}
	e.q.RunFunc = func(*tql.Compiled) (store.Result, error) {
		return store.Result{Rows: [][]any{{"/api/a b", uint64(3)}, {"true", uint64(1)}, {"zeta", uint64(1)}}}, nil
	}
	q := "source spans | RequestPath:/api"
	c, err := e.s.Complete(ctx, viewerUser, q, len(q), model.SourceLogs, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Suggestions) == 0 || c.Suggestions[0].InsertText != `"/api/a b"` || c.Suggestions[0].Detail != "3" ||
		c.Suggestions[0].Kind != tql.SuggestValue {
		t.Errorf("values %+v", c)
	}
	c, _ = e.s.Complete(ctx, viewerUser, "level:", 6, model.SourceLogs, Selection{})
	if len(c.Suggestions) == 0 || len(e.q.Compiled) != 2 {
		t.Errorf("level values should not query: %+v (%d queries)", c, len(e.q.Compiled))
	}
	if c, err := e.s.Complete(ctx, viewerUser, "@timestamp:", 11, model.SourceLogs, Selection{}); err != nil || len(c.Suggestions) != 1 {
		t.Errorf("unfaceted field: %+v %v", c, err)
	}
	if _, err := e.s.Complete(ctx, viewerUser, strings.Repeat("a", tql.MaxQueryBytes+1), 0, model.SourceLogs, Selection{}); err == nil {
		t.Error("long query accepted")
	}
	if _, err := e.s.Complete(ctx, viewerUser, "x", 1, "nope", Selection{}); err == nil {
		t.Error("bad source accepted")
	}
}

func TestTQLValueAndCursor(t *testing.T) {
	for in, want := range map[string]string{"api": "api", "a b": `"a b"`, `x"*\`: `"x\"\*\\"`, "OR": `"OR"`, "1.5": "1.5"} {
		if got := tqlValue(in); got != want {
			t.Errorf("tqlValue(%q) = %s, want %s", in, got, want)
		}
	}
	c := &tql.Cursor{Timestamp: searchNow.Add(123), ID: "id-1"}
	got, err := DecodeCursor(EncodeCursor(c))
	if err != nil || !got.Timestamp.Equal(c.Timestamp) || got.ID != c.ID {
		t.Errorf("cursor round trip %+v %v", got, err)
	}
	if got, err := DecodeCursor(""); got != nil || err != nil {
		t.Error("empty cursor")
	}
	for _, bad := range []string{"!!", "e30", strings.Repeat("A", 1000)} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Errorf("cursor %q accepted", bad)
		}
	}
	if EncodeCursor(nil) != "" {
		t.Error("nil cursor")
	}
	if _, err := parseFor(model.SourceSpans, "source logs | *"); err == nil {
		t.Error("conflicting source accepted")
	}
}

func TestSignalsService(t *testing.T) {
	e := newSearchEnv(t)
	ctx := context.Background()
	s, err := e.signals.Create(ctx, editorUser, SignalInput{Name: "  Errors ", TQL: "level:error", Color: "#ff0000"})
	if err != nil || s.Name != "Errors" || s.Source != "logs" {
		t.Fatalf("create %+v %v", s, err)
	}
	if _, err := e.signals.Get(ctx, viewerUser, s.ID); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("private signal visible: %v", err)
	}
	upd, err := e.signals.Update(ctx, adminUser, s.ID, SignalInput{Name: "E", TQL: "level:error", Shared: true}, s.UpdatedAt)
	if err != nil || !upd.Shared {
		t.Fatalf("admin update %+v %v", upd, err)
	}
	if _, err := e.signals.Update(ctx, viewerUser, s.ID, SignalInput{Name: "E", TQL: "*"}, upd.UpdatedAt); !errors.Is(err, ErrForbidden) {
		t.Errorf("foreign update: %v", err)
	}
	if _, err := e.signals.Update(ctx, editorUser, s.ID, SignalInput{Name: "E", TQL: "*"}, time.Time{}); err == nil {
		t.Error("update without updatedAt")
	}
	list, _ := e.signals.List(ctx, viewerUser)
	if len(list) != 1 {
		t.Errorf("visible %d", len(list))
	}
	if err := e.signals.Delete(ctx, viewerUser, s.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("foreign delete: %v", err)
	}
	if err := e.signals.Delete(ctx, editorUser, s.ID); err != nil {
		t.Errorf("owner delete: %v", err)
	}
	long := strings.Repeat("x", tql.MaxQueryBytes+1)
	for _, in := range []SignalInput{{Name: "x", TQL: long}, {Name: "x", TQL: " "}, {Name: strings.Repeat("n", 129), TQL: "*"}} {
		if _, err := e.signals.Create(ctx, editorUser, in); err == nil {
			t.Errorf("%.40v accepted", in)
		}
	}
}
