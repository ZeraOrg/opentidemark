// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// FakeQuerier is an in-memory store.Querier for unit tests. It records the compiled queries
// and filters it receives and returns the canned values; Err (when set) is returned by every
// method. RunFunc, when set, answers Run.
type FakeQuerier struct {
	mu sync.Mutex

	Err       error
	RunFunc   func(c *tql.Compiled) (store.Result, error)
	Result    store.Result
	Hist      store.Histogram
	Sample    store.Sample
	FieldList []store.FieldInfo
	Events    map[uuid.UUID]model.Event
	Older     []model.Event
	Newer     []model.Event
	Tailed    []store.TailEvent
	TailTotal uint64
	Spans     []model.Span
	TraceEvs  []model.Event
	Summaries []store.TraceSummary
	Names     []store.MetricName

	Compiled  []*tql.Compiled
	Filters   []store.Filter
	HistSpans []time.Duration
	TailCalls [][2]time.Time
}

var _ store.Querier = (*FakeQuerier)(nil)

func (f *FakeQuerier) record(c *tql.Compiled, fl *store.Filter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c != nil {
		f.Compiled = append(f.Compiled, c)
	}
	if fl != nil {
		f.Filters = append(f.Filters, *fl)
	}
}

// Run implements store.Querier.
func (f *FakeQuerier) Run(_ context.Context, c *tql.Compiled) (store.Result, error) {
	f.record(c, nil)
	if f.Err != nil {
		return store.Result{}, f.Err
	}
	if f.RunFunc != nil {
		return f.RunFunc(c)
	}
	res := f.Result
	res.Kind = c.Kind
	return res, nil
}

// Histogram implements store.Querier.
func (f *FakeQuerier) Histogram(_ context.Context, fl store.Filter, span time.Duration) (store.Histogram, error) {
	f.record(nil, &fl)
	f.mu.Lock()
	f.HistSpans = append(f.HistSpans, span)
	f.mu.Unlock()
	h := f.Hist
	h.Span = span
	return h, f.Err
}

// Newest implements store.Querier.
func (f *FakeQuerier) Newest(_ context.Context, fl store.Filter, _ uint64) (store.Sample, error) {
	f.record(nil, &fl)
	return f.Sample, f.Err
}

// Fields implements store.Querier.
func (f *FakeQuerier) Fields(context.Context, model.Source, store.TimeRange) ([]store.FieldInfo, error) {
	return f.FieldList, f.Err
}

// Event implements store.Querier.
func (f *FakeQuerier) Event(_ context.Context, id uuid.UUID) (model.Event, error) {
	if f.Err != nil {
		return model.Event{}, f.Err
	}
	ev, ok := f.Events[id]
	if !ok {
		return model.Event{}, store.ErrNotFound
	}
	return ev, nil
}

// Context implements store.Querier.
func (f *FakeQuerier) Context(context.Context, model.Event, int, int) ([]model.Event, []model.Event, error) {
	return f.Older, f.Newer, f.Err
}

// Tail implements store.Querier.
func (f *FakeQuerier) Tail(_ context.Context, fl store.Filter, after, until time.Time, _ int) ([]store.TailEvent, uint64, error) {
	f.record(nil, &fl)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TailCalls = append(f.TailCalls, [2]time.Time{after, until})
	evs, total := f.Tailed, f.TailTotal
	f.Tailed, f.TailTotal = nil, 0
	return evs, total, f.Err
}

// Trace implements store.Querier.
func (f *FakeQuerier) Trace(context.Context, string) ([]model.Span, []model.Event, error) {
	if f.Err == nil && len(f.Spans) == 0 && len(f.TraceEvs) == 0 {
		return nil, nil, store.ErrNotFound
	}
	return f.Spans, f.TraceEvs, f.Err
}

// Traces implements store.Querier.
func (f *FakeQuerier) Traces(_ context.Context, fl store.Filter, _ int) ([]store.TraceSummary, error) {
	f.record(nil, &fl)
	return f.Summaries, f.Err
}

// MetricNames implements store.Querier.
func (f *FakeQuerier) MetricNames(context.Context, string, store.TimeRange, int) ([]store.MetricName, error) {
	return f.Names, f.Err
}

// LastCompiled returns the most recent compiled query (nil if none).
func (f *FakeQuerier) LastCompiled() *tql.Compiled {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Compiled) == 0 {
		return nil
	}
	return f.Compiled[len(f.Compiled)-1]
}

// LastFilter returns the most recent filter (zero if none).
func (f *FakeQuerier) LastFilter() store.Filter {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Filters) == 0 {
		return store.Filter{}
	}
	return f.Filters[len(f.Filters)-1]
}

// TailCallCount returns how many times Tail was called.
func (f *FakeQuerier) TailCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.TailCalls)
}
