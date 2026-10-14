// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// FakeQuerier is an in-memory store.Querier for unit tests. It records the compiled queries,
// filters and stream scopes it receives and returns the canned values; Err (when set) is
// returned by every method. RunFunc, when set, answers Run. Like the real reader it rejects an
// unset scope with store.ErrNoScope and hides events and spans outside the scope (SPEC §15.10).
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
	Scopes    []model.StreamScope // scopes of the fixed reads (Fields, Event, Context, Trace, MetricNames)
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

// scope records a fixed-read scope and rejects an unset one.
func (f *FakeQuerier) scope(s model.StreamScope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scopes = append(f.Scopes, s)
	if !s.IsSet() {
		return store.ErrNoScope
	}
	return nil
}

// filter records fl and rejects an unset scope.
func (f *FakeQuerier) filter(fl store.Filter) error {
	f.record(nil, &fl)
	if !fl.Streams.IsSet() {
		return store.ErrNoScope
	}
	return nil
}

// Histogram implements store.Querier.
func (f *FakeQuerier) Histogram(_ context.Context, fl store.Filter, span time.Duration) (store.Histogram, error) {
	if err := f.filter(fl); err != nil {
		return store.Histogram{}, err
	}
	f.mu.Lock()
	f.HistSpans = append(f.HistSpans, span)
	f.mu.Unlock()
	h := f.Hist
	h.Span = span
	return h, f.Err
}

// Newest implements store.Querier.
func (f *FakeQuerier) Newest(_ context.Context, fl store.Filter, _ uint64) (store.Sample, error) {
	if err := f.filter(fl); err != nil {
		return store.Sample{}, err
	}
	return f.Sample, f.Err
}

// Fields implements store.Querier.
func (f *FakeQuerier) Fields(_ context.Context, s model.StreamScope, _ model.Source, _ store.TimeRange) ([]store.FieldInfo, error) {
	if err := f.scope(s); err != nil {
		return nil, err
	}
	return f.FieldList, f.Err
}

// Event implements store.Querier.
func (f *FakeQuerier) Event(_ context.Context, s model.StreamScope, id uuid.UUID) (model.Event, error) {
	if err := f.scope(s); err != nil {
		return model.Event{}, err
	}
	if f.Err != nil {
		return model.Event{}, f.Err
	}
	ev, ok := f.Events[id]
	if !ok || !s.Allows(ev.Stream) {
		return model.Event{}, store.ErrNotFound
	}
	return ev, nil
}

// Context implements store.Querier.
func (f *FakeQuerier) Context(_ context.Context, s model.StreamScope, _ model.Event, _, _ int) ([]model.Event, []model.Event, error) {
	if err := f.scope(s); err != nil {
		return nil, nil, err
	}
	return visibleEvents(s, f.Older), visibleEvents(s, f.Newer), f.Err
}

func visibleEvents(s model.StreamScope, evs []model.Event) []model.Event {
	out := []model.Event{}
	for _, ev := range evs {
		if s.Allows(ev.Stream) {
			out = append(out, ev)
		}
	}
	return out
}

// Tail implements store.Querier.
func (f *FakeQuerier) Tail(_ context.Context, fl store.Filter, after, until time.Time, _ int) ([]store.TailEvent, uint64, error) {
	if err := f.filter(fl); err != nil {
		return nil, 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TailCalls = append(f.TailCalls, [2]time.Time{after, until})
	evs, total := f.Tailed, f.TailTotal
	f.Tailed, f.TailTotal = nil, 0
	return evs, total, f.Err
}

// Trace implements store.Querier.
func (f *FakeQuerier) Trace(_ context.Context, s model.StreamScope, _ string) ([]model.Span, []model.Event, error) {
	if err := f.scope(s); err != nil {
		return nil, nil, err
	}
	if f.Err != nil {
		return nil, nil, f.Err
	}
	spans := []model.Span{}
	for _, sp := range f.Spans {
		if s.Allows(sp.Stream) {
			spans = append(spans, sp)
		}
	}
	events := visibleEvents(s, f.TraceEvs)
	if len(spans) == 0 && len(events) == 0 {
		return nil, nil, store.ErrNotFound
	}
	return spans, events, nil
}

// Traces implements store.Querier.
func (f *FakeQuerier) Traces(_ context.Context, fl store.Filter, _ int) ([]store.TraceSummary, error) {
	if err := f.filter(fl); err != nil {
		return nil, err
	}
	return f.Summaries, f.Err
}

// MetricNames implements store.Querier.
func (f *FakeQuerier) MetricNames(_ context.Context, s model.StreamScope, _ string, _ store.TimeRange, _ int) ([]store.MetricName, error) {
	if err := f.scope(s); err != nil {
		return nil, err
	}
	return f.Names, f.Err
}

// LastScope returns the scope of the most recent fixed read (unset if none).
func (f *FakeQuerier) LastScope() model.StreamScope {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Scopes) == 0 {
		return model.StreamScope{}
	}
	return f.Scopes[len(f.Scopes)-1]
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
