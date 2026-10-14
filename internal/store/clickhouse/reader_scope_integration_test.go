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
)

// testStreamScope adds data of a second stream ("other": an event next to events[10], a span
// of traceA, a metric) and checks that every read honours the stream scope (SPEC §15.10).
func (f *readerFixture) testStreamScope(t *testing.T) {
	ctx := context.Background()
	w := f.w
	near := f.events[10]
	other := model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: near.Timestamp.Add(time.Microsecond), IngestedAt: near.IngestedAt,
		Stream: "other", Level: model.Error, Service: near.Service, Host: near.Host, Protocol: "native", Message: "secret",
		TraceID: traceA, Properties: map[string]any{"secret_key": "x"}}
	if err := w.WriteEvents(ctx, []model.Event{other}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSpans(ctx, []model.Span{{TraceID: traceA, SpanID: "a000000000000009", ParentSpanID: "a000000000000002",
		Stream: "other", Service: "vault", Name: "secret", Kind: "client", Start: f.base.Add(20 * time.Millisecond),
		Duration: time.Millisecond, StatusCode: "ok"}}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteMetrics(ctx, []model.MetricPoint{{Timestamp: f.base, Stream: "other", Service: "vault", Name: "vault.secrets",
		Kind: "gauge", Value: 1, Attributes: map[string]any{"vault_key": "x"}}}); err != nil {
		t.Fatal(err)
	}
	s := scopes{app: model.OnlyStreams("app"), other: model.OnlyStreams("other")}
	t.Run("event", func(t *testing.T) { f.scopedEvent(t, s, other) })
	t.Run("context", func(t *testing.T) { f.scopedContext(t, s, near, other) })
	t.Run("trace", func(t *testing.T) { f.scopedTrace(t, s, other) })
	t.Run("traces", func(t *testing.T) { f.scopedTraces(t, s) })
	t.Run("fields and metric names", func(t *testing.T) { f.scopedCatalog(t, s) })
	t.Run("filters need a scope", func(t *testing.T) { f.scopedFilters(t, s) })
}

type scopes struct{ app, other model.StreamScope }

var unsetScope model.StreamScope

func (f *readerFixture) scopedEvent(t *testing.T, s scopes, other model.Event) {
	for _, tc := range []struct {
		scope model.StreamScope
		want  error
	}{
		{model.AllStreams(), nil}, {s.other, nil}, {s.app, store.ErrNotFound}, {model.NoStreams(), store.ErrNotFound},
		{unsetScope, store.ErrNoScope},
	} {
		if _, err := f.r.Event(context.Background(), tc.scope, other.ID); !errors.Is(err, tc.want) {
			t.Errorf("Event(%s) = %v, want %v", tc.scope, err, tc.want)
		}
	}
}

func (f *readerFixture) scopedContext(t *testing.T, s scopes, near, other model.Event) {
	ctx := context.Background()
	older, newer, err := f.r.Context(ctx, s.app, near, 0, 5)
	if err != nil || len(older) != 0 || len(newer) != 5 || newer[0].ID == other.ID {
		t.Errorf("app context %v %v", ids(newer), err)
	}
	if _, newer, err := f.r.Context(ctx, model.AllStreams(), near, 0, 1); err != nil || len(newer) != 1 || newer[0].ID != other.ID {
		t.Errorf("all context %v %v", ids(newer), err)
	}
	if _, _, err := f.r.Context(ctx, unsetScope, near, 1, 1); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("unset context: %v", err)
	}
}

func (f *readerFixture) scopedTrace(t *testing.T, s scopes, other model.Event) {
	ctx := context.Background()
	spans, events, err := f.r.Trace(ctx, s.app, traceA)
	if err != nil || len(spans) != 2 || len(events) != 1 || events[0].Stream != "app" {
		t.Errorf("app trace %d spans %d events %v", len(spans), len(events), err)
	}
	spans, events, err = f.r.Trace(ctx, s.other, traceA)
	if err != nil || len(spans) != 1 || spans[0].Service != "vault" || len(events) != 1 || events[0].ID != other.ID {
		t.Errorf("other trace %+v %d %v", spans, len(events), err)
	}
	if _, _, err := f.r.Trace(ctx, model.NoStreams(), traceA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("trace of no stream: %v", err)
	}
	if _, _, err := f.r.Trace(ctx, unsetScope, traceA); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("unset trace: %v", err)
	}
}

func (f *readerFixture) scopedTraces(t *testing.T, s scopes) {
	ctx := context.Background()
	sums, err := f.r.Traces(ctx, f.scopedFilter(t, "source spans | *", s.app), 10)
	if err != nil || len(sums) != 2 || sums[1].SpanCount != 2 || len(sums[1].Services) != 2 {
		t.Errorf("app traces %+v %v", sums, err)
	}
	if all, err := f.r.Traces(ctx, f.filter(t, "source spans | *"), 10); err != nil || len(all) != 2 || all[1].SpanCount != 3 {
		t.Errorf("all traces %+v %v", all, err)
	}
	if none, err := f.r.Traces(ctx, f.scopedFilter(t, "source spans | *", model.NoStreams()), 10); err != nil || len(none) != 0 {
		t.Errorf("traces of no stream %+v %v", none, err)
	}
}

func (f *readerFixture) scopedCatalog(t *testing.T, s scopes) {
	ctx := context.Background()
	rng := store.TimeRange{From: f.from, To: f.to}
	for _, src := range []model.Source{model.SourceLogs, model.SourceMetrics} {
		fields, err := f.r.Fields(ctx, s.app, src, rng)
		if err != nil || len(fields) == 0 {
			t.Fatalf("%s fields %v", src, err)
		}
		for _, fi := range fields {
			if fi.Name == "secret_key" || fi.Name == "vault_key" {
				t.Errorf("%s fields leak %s", src, fi.Name)
			}
		}
		if none, err := f.r.Fields(ctx, model.NoStreams(), src, rng); err != nil || len(none) != 0 {
			t.Errorf("%s fields of no stream %+v %v", src, none, err)
		}
	}
	names, err := f.r.MetricNames(ctx, s.other, "", rng, 10)
	if err != nil || len(names) != 1 || names[0].Name != "vault.secrets" {
		t.Errorf("other metric names %+v %v", names, err)
	}
	if _, err := f.r.MetricNames(ctx, unsetScope, "", rng, 10); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("unset metric names: %v", err)
	}
}

func (f *readerFixture) scopedFilters(t *testing.T, s scopes) {
	ctx := context.Background()
	fl := f.filter(t, "*")
	fl.Streams = unsetScope
	if _, err := f.r.Histogram(ctx, fl, time.Minute); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("histogram: %v", err)
	}
	if _, err := f.r.Newest(ctx, fl, 1); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("newest: %v", err)
	}
	if _, _, err := f.r.Tail(ctx, fl, f.base, f.base.Add(time.Second), 1); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("tail: %v", err)
	}
	if _, err := f.r.Traces(ctx, fl, 1); !errors.Is(err, store.ErrNoScope) {
		t.Errorf("traces: %v", err)
	}
	if sample, err := f.r.Newest(ctx, f.scopedFilter(t, "*", s.other), 10); err != nil || sample.Total != 1 {
		t.Errorf("other newest %+v %v", sample, err)
	}
}
