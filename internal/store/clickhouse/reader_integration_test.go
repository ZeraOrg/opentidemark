// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// readerFixture writes a small dataset and returns a Reader on it.
type readerFixture struct {
	r      *Reader
	w      *Writer // writer connection, for data added by later subtests
	base   time.Time
	events []model.Event
	from   time.Time
	to     time.Time
}

const (
	traceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	traceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newReaderFixture(t *testing.T) *readerFixture {
	t.Helper()
	conn, env := setup(t)
	ctx := context.Background()
	if _, err := NewMigrator(conn, env.Database).Up(ctx); err != nil {
		t.Fatal(err)
	}
	w, _ := NewWriter(conn, env.Database)
	base := time.Now().UTC().Add(-30 * time.Minute).Truncate(time.Minute)
	f := &readerFixture{w: w, base: base, from: base.Add(-time.Hour), to: base.Add(time.Hour)}
	levels := []model.Level{model.Information, model.Warning, model.Error}
	for i := range 30 {
		ts := base.Add(time.Duration(i) * 100 * time.Millisecond).Add(time.Duration(i) * time.Microsecond) // sub-second keyset
		ev := model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: ts, IngestedAt: base.Add(time.Duration(i) * time.Millisecond),
			Stream: "app", Level: levels[i%3], Service: []string{"api", "worker"}[i%2], Host: "h1", Protocol: "native",
			Message: "request done", Properties: map[string]any{"status": int64(200 + i%2*300), "route": "/r" + string(rune('a'+i%3))}}
		if i == 7 {
			ev.TraceID, ev.SpanID = traceA, "a000000000000001"
		}
		f.events = append(f.events, ev)
	}
	if err := w.WriteEvents(ctx, f.events); err != nil {
		t.Fatal(err)
	}
	spans := []model.Span{
		{TraceID: traceA, SpanID: "a000000000000001", Stream: "app", Service: "api", Name: "GET /orders", Kind: "server",
			Start: base, Duration: 120 * time.Millisecond, StatusCode: "ok", Attributes: map[string]any{"http.status_code": int64(200), "cached": true}},
		{TraceID: traceA, SpanID: "a000000000000002", ParentSpanID: "a000000000000001", Stream: "app", Service: "db", Name: "SELECT",
			Kind: "client", Start: base.Add(10 * time.Millisecond), Duration: 150 * time.Millisecond, StatusCode: "error",
			Events: []model.SpanEvent{{Time: base, Name: "exception"}}},
		{TraceID: traceB, SpanID: "b000000000000001", Stream: "app", Service: "worker", Name: "job", Kind: "internal",
			Start: base.Add(time.Minute), Duration: time.Second, StatusCode: "unset"},
	}
	if err := w.WriteSpans(ctx, spans); err != nil {
		t.Fatal(err)
	}
	points := []model.MetricPoint{
		{Timestamp: base, Stream: "app", Service: "api", Name: "http.server.duration", Unit: "ms", Kind: "histogram", Value: 3,
			Attributes: map[string]any{"route": "/a"}},
		{Timestamp: base, Stream: "app", Service: "api", Name: "process.cpu", Kind: "gauge", Value: 0.5, Description: "cpu"},
		{Timestamp: base, Stream: "app", Service: "api", Name: "process.restarts", Kind: "sum", Temporality: "cumulative",
			IsMonotonic: true, Value: 2},
	}
	if err := w.WriteMetrics(ctx, points); err != nil {
		t.Fatal(err)
	}
	rc, err := Open(ctx, Options{Addrs: env.Addrs, Database: env.Database, Username: env.ReaderUser, Password: env.ReaderPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	f.r, err = NewReader(rc, ReaderConfig{Database: env.Database, QueryTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *readerFixture) compile(t *testing.T, q string, limit int, cur *tql.Cursor) *tql.Compiled {
	t.Helper()
	pq, err := tql.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	c, err := tql.Compile(pq, tql.Options{Database: f.r.cfg.Database, From: f.from, To: f.to, Limit: limit, Cursor: cur,
		Streams: model.AllStreams()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *readerFixture) filter(t *testing.T, q string) store.Filter {
	t.Helper()
	return f.scopedFilter(t, q, model.AllStreams())
}

func (f *readerFixture) scopedFilter(t *testing.T, q string, scope model.StreamScope) store.Filter {
	t.Helper()
	pq, err := tql.Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	where, args, err := tql.CompileWhere(pq, tql.Options{Database: f.r.cfg.Database, From: f.from, To: f.to, Streams: scope})
	if err != nil {
		t.Fatal(err)
	}
	return store.Filter{Source: pq.Source, Where: where, Args: args, Range: store.TimeRange{From: f.from, To: f.to}, Streams: scope}
}

func TestIntegrationReader(t *testing.T) {
	f := newReaderFixture(t)
	t.Run("events pages", f.testEventPages)
	t.Run("table and timeseries", f.testStats)
	t.Run("records", f.testRecords)
	t.Run("histogram", f.testHistogram)
	t.Run("newest and fields", f.testNewestFields)
	t.Run("event and context", f.testEventContext)
	t.Run("tail", f.testTail)
	t.Run("traces", f.testTraces)
	t.Run("metric names", f.testMetricNames)
	t.Run("stream scope", f.testStreamScope)
}

func (f *readerFixture) testEventPages(t *testing.T) {
	ctx := context.Background()
	var seen []uuid.UUID
	var cur *tql.Cursor
	for page := 0; page < 10; page++ {
		res, err := f.r.Run(ctx, f.compile(t, "service:api", 4, cur))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range res.Events {
			seen = append(seen, ev.ID)
		}
		if res.NextCursor == nil {
			break
		}
		cur = res.NextCursor
	}
	if len(seen) != 15 {
		t.Fatalf("paged %d events, want 15", len(seen))
	}
	if seen[0] != f.events[28].ID || seen[14] != f.events[0].ID {
		t.Errorf("order: first %v last %v", seen[0], seen[14])
	}
	res, err := f.r.Run(ctx, f.compile(t, "status >= 500 and level:error", 100, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range res.Events {
		if ev.Level != model.Error || ev.Properties["status"].(interface{ String() string }).String() != "500" {
			t.Errorf("unexpected event %+v", ev)
		}
	}
	if len(res.Events) != 5 || res.Stats.RowsRead == 0 {
		t.Errorf("filtered %d events, stats %+v", len(res.Events), res.Stats)
	}
}

func (f *readerFixture) testStats(t *testing.T) {
	ctx := context.Background()
	res, err := f.r.Run(ctx, f.compile(t, "* | stats count() by service | sort service", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "api" || res.Rows[0][1] != uint64(15) {
		t.Errorf("table rows %v", res.Rows)
	}
	res, err = f.r.Run(ctx, f.compile(t, "* | stats count(), avg(status) by level span 1m", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != tql.KindTimeseries || len(res.Series) != 3 || res.Span != time.Minute {
		t.Fatalf("series %+v", res)
	}
	var total uint64
	for _, s := range res.Series {
		if _, ok := s.Key["level"]; !ok || len(s.Points[0]) != 3 {
			t.Errorf("series %+v", s)
		}
		for _, p := range s.Points {
			total += p[1].(uint64)
		}
	}
	if total != 30 {
		t.Errorf("timeseries total %d", total)
	}
}

func (f *readerFixture) testRecords(t *testing.T) {
	ctx := context.Background()
	res, err := f.r.Run(ctx, f.compile(t, "source spans | service:api", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0]["name"] != "GET /orders" || res.Records[0]["duration_ms"] != float64(120) {
		t.Errorf("span records %+v", res.Records)
	}
	res, err = f.r.Run(ctx, f.compile(t, "source metrics | *", 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.NextCursor == nil {
		t.Fatalf("metric page %+v", res)
	}
	res2, err := f.r.Run(ctx, f.compile(t, "source metrics | *", 1, res.NextCursor))
	if err != nil || len(res2.Records) != 1 || res2.Records[0]["name"] == res.Records[0]["name"] {
		t.Errorf("second metric page %+v %v", res2.Records, err)
	}
	res, err = f.r.Run(ctx, f.compile(t, "level:error | fields service, route", 2, nil))
	if err != nil || len(res.Records) != 2 || res.Records[0]["route"] == nil || res.NextCursor == nil {
		t.Errorf("projection %+v %v", res, err)
	}
}

func (f *readerFixture) testHistogram(t *testing.T) {
	h, err := f.r.Histogram(context.Background(), f.filter(t, "*"), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Buckets) < 12 || len(h.Buckets) > 13 || h.Buckets[0].T.After(f.from) {
		t.Fatalf("%d buckets", len(h.Buckets))
	}
	var errs, total uint64
	for _, b := range h.Buckets {
		errs += b.Counts["Error"]
		for _, n := range b.Counts {
			total += n
		}
	}
	if errs != 10 || total != 30 {
		t.Errorf("histogram errors %d total %d", errs, total)
	}
	h, err = f.r.Histogram(context.Background(), f.filter(t, "source spans | *"), time.Hour)
	var spanErrs uint64
	for _, b := range h.Buckets {
		spanErrs += b.Counts["error"]
	}
	if err != nil || len(h.Buckets) < 2 || len(h.Buckets) > 3 || spanErrs != 1 {
		t.Errorf("span histogram %+v %v", h, err)
	}
}

func (f *readerFixture) testNewestFields(t *testing.T) {
	ctx := context.Background()
	s, err := f.r.Newest(ctx, f.filter(t, "*"), 10)
	if err != nil || s.Total != 30 || !s.Sampled || !s.Cutoff.Equal(f.events[20].Timestamp) {
		t.Errorf("newest %+v %v (want cutoff %v)", s, err, f.events[20].Timestamp)
	}
	s, err = f.r.Newest(ctx, f.filter(t, "*"), 100)
	if err != nil || s.Sampled || s.Total != 30 {
		t.Errorf("unsampled %+v %v", s, err)
	}
	fields, err := f.r.Fields(ctx, model.AllStreams(), model.SourceLogs, store.TimeRange{From: f.from, To: f.to})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]store.FieldInfo{}
	for _, fi := range fields {
		got[fi.Name] = fi
	}
	if !got["status"].Numeric || got["route"].Numeric || got["status"].Hits != 30 {
		t.Errorf("fields %+v", fields)
	}
	mf, err := f.r.Fields(ctx, model.AllStreams(), model.SourceMetrics, store.TimeRange{From: f.from, To: f.to})
	if err != nil || len(mf) != 1 || mf[0].Name != "route" {
		t.Errorf("metric fields %+v %v", mf, err)
	}
}

func (f *readerFixture) testEventContext(t *testing.T) {
	ctx := context.Background()
	want := f.events[10]
	ev, err := f.r.Event(ctx, model.AllStreams(), want.ID)
	if err != nil || ev.ID != want.ID || !ev.Timestamp.Equal(want.Timestamp) || ev.Level != want.Level {
		t.Fatalf("event %+v %v", ev, err)
	}
	if _, err := f.r.Event(ctx, model.AllStreams(), uuid.Must(uuid.NewV7())); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing event: %v", err)
	}
	older, newer, err := f.r.Context(ctx, model.AllStreams(), ev, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 2 || older[0].ID != f.events[6].ID || older[1].ID != f.events[8].ID ||
		len(newer) != 3 || newer[0].ID != f.events[12].ID {
		t.Errorf("context older %v newer %v", ids(older), ids(newer))
	}
}

func ids(evs []model.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Service + "@" + e.Timestamp.Format(time.RFC3339Nano)
	}
	return out
}

func (f *readerFixture) testTail(t *testing.T) {
	ctx := context.Background()
	evs, total, err := f.r.Tail(ctx, f.filter(t, "service:worker"), f.base.Add(-time.Second), f.base.Add(time.Second), 5)
	if err != nil {
		t.Fatal(err)
	}
	if total != 15 || len(evs) != 5 || evs[0].Event.ID != f.events[1].ID || !evs[0].IngestedAt.Equal(f.events[1].IngestedAt) {
		t.Errorf("tail total %d events %d first %+v", total, len(evs), evs)
	}
	evs, total, err = f.r.Tail(ctx, f.filter(t, "service:worker"), evs[4].IngestedAt, f.base.Add(time.Second), 50)
	if err != nil || total != 10 || len(evs) != 10 {
		t.Errorf("tail continuation %d %d %v", total, len(evs), err)
	}
	if _, _, err := f.r.Tail(ctx, f.filter(t, "source spans | *"), f.base, f.base, 1); err == nil {
		t.Error("tail of spans accepted")
	}
}

func (f *readerFixture) testTraces(t *testing.T) {
	ctx := context.Background()
	spans, events, err := f.r.Trace(ctx, model.AllStreams(), traceA)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].SpanID != "a000000000000001" || spans[0].Duration != 120*time.Millisecond ||
		spans[0].Attributes["cached"] != true || len(spans[1].Events) != 1 || len(events) != 1 {
		t.Errorf("trace %+v events %d", spans, len(events))
	}
	if _, _, err := f.r.Trace(ctx, model.AllStreams(), "cccccccccccccccccccccccccccccccc"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("missing trace: %v", err)
	}
	sums, err := f.r.Traces(ctx, f.filter(t, "source spans | *"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 2 || sums[0].TraceID != traceB {
		t.Fatalf("traces %+v", sums)
	}
	a := sums[1]
	if a.RootService != "api" || a.RootName != "GET /orders" || a.SpanCount != 2 || a.ErrorCount != 1 ||
		a.DurationMs != 160 || len(a.Services) != 2 {
		t.Errorf("trace summary %+v", a)
	}
	sums, err = f.r.Traces(ctx, f.filter(t, "source spans | service:db"), 10)
	if err != nil || len(sums) != 1 || sums[0].SpanCount != 2 {
		t.Errorf("filtered traces %+v %v", sums, err)
	}
}

func (f *readerFixture) testMetricNames(t *testing.T) {
	names, err := f.r.MetricNames(context.Background(), model.AllStreams(), "HTTP", store.TimeRange{From: f.from, To: f.to}, 10)
	if err != nil || len(names) != 1 || names[0].Name != "http.server.duration" || names[0].Unit != "ms" {
		t.Errorf("metric names %+v %v", names, err)
	}
	names, err = f.r.MetricNames(context.Background(), model.AllStreams(), "process.", store.TimeRange{From: f.from, To: f.to}, 10)
	if err != nil || len(names) != 2 || names[0].Monotonic || !names[1].Monotonic || names[1].Kind != "sum" {
		t.Errorf("monotonic metric names %+v %v", names, err)
	}
}
