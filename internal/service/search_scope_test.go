// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// fixedScope is a ScopeResolver returning a settable scope.
type fixedScope struct {
	mu    sync.Mutex
	scope model.StreamScope
	err   error
}

func (f *fixedScope) Scope(context.Context, auth.Principal) (model.StreamScope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scope, f.err
}

func (f *fixedScope) set(s model.StreamScope) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scope = s
}

func newScopedSearchEnv(t *testing.T, s model.StreamScope) (*searchEnv, *fixedScope) {
	t.Helper()
	e := newSearchEnv(t)
	fs := &fixedScope{scope: s}
	e.s.d.Access = fs
	return e, fs
}

func TestSearchScopeNarrowedBySelection(t *testing.T) {
	e, _ := newScopedSearchEnv(t, model.OnlyStreams("a", "b"))
	ctx := context.Background()
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "*", Range: lastHour,
		Select: Selection{Streams: []string{"b", "c"}}}); err != nil {
		t.Fatal(err)
	}
	c := e.q.LastCompiled()
	if !strings.Contains(c.SQL, "stream IN (?)") || !hasArg(c.Args, "b") || hasArg(c.Args, "a") || hasArg(c.Args, "c") {
		t.Errorf("selection must narrow the scope, never widen it: %s %v", c.SQL, c.Args)
	}
	if _, err := e.s.Histogram(ctx, viewerUser, HistogramRequest{Q: "*", Range: lastHour, Buckets: 10,
		Select: Selection{Streams: []string{"c"}}}); err != nil {
		t.Fatal(err)
	}
	if f := e.q.LastFilter(); !f.Streams.Empty() {
		t.Errorf("a selection outside the scope must be empty: %v", f.Streams)
	}
	if _, err := e.s.Traces(ctx, viewerUser, TracesRequest{Q: "*", Range: lastHour, Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if f := e.q.LastFilter(); f.Streams.String() != model.OnlyStreams("a", "b").String() {
		t.Errorf("no selection keeps the scope: %v", f.Streams)
	}
}

func TestSearchSelectionValidation(t *testing.T) {
	e, _ := newScopedSearchEnv(t, model.AllStreams())
	ctx := context.Background()
	many := make([]string, MaxSelectedStreams+1)
	for i := range many {
		many[i] = fmt.Sprintf("s%d", i)
	}
	envs := make([]string, MaxSelectedEnvironments+1)
	for i := range envs {
		envs[i] = "e"
	}
	bad := []Selection{
		{Streams: many},
		{Streams: []string{"Bad Name"}},
		{Streams: []string{"a'--"}},
		{Environments: envs},
		{Environments: []string{""}},
		{Environments: []string{strings.Repeat("x", MaxEnvironmentLen+1)}},
	}
	for _, sel := range bad {
		var ve *ValidationError
		if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "*", Range: lastHour, Select: sel}); !errors.As(err, &ve) {
			t.Errorf("%+v accepted: %v", sel, err)
		}
	}
}

func TestSearchEnvironmentFiltersLogsOnly(t *testing.T) {
	e, _ := newScopedSearchEnv(t, model.AllStreams())
	ctx := context.Background()
	sel := Selection{Environments: []string{"prod", "staging"}}
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "*", Range: lastHour, Select: sel}); err != nil {
		t.Fatal(err)
	}
	if c := e.q.LastCompiled(); !strings.Contains(c.SQL, "environment") || !hasArg(c.Args, "prod") || !hasArg(c.Args, "staging") {
		t.Errorf("logs environment filter missing: %s %v", c.SQL, c.Args)
	}
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "source spans | *", Range: lastHour, Select: sel}); err != nil {
		t.Fatal(err)
	}
	if c := e.q.LastCompiled(); strings.Contains(c.SQL, "environment") {
		t.Errorf("environments must not apply to spans: %s", c.SQL)
	}
}

func TestSearchFixedReadsUseScope(t *testing.T) {
	e, _ := newScopedSearchEnv(t, model.OnlyStreams("a"))
	ctx := context.Background()
	hidden := model.Event{ID: uuid.Must(uuid.NewV7()), Stream: "b"}
	e.q.Events = map[uuid.UUID]model.Event{hidden.ID: hidden}
	if _, err := e.s.Event(ctx, viewerUser, hidden.ID.String()); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("event of a hidden stream: %v", err)
	}
	if _, err := e.s.Context(ctx, viewerUser, hidden.ID.String(), 1, 1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("context of a hidden event: %v", err)
	}
	e.q.Spans = []model.Span{{Stream: "b"}}
	if _, _, err := e.s.Trace(ctx, viewerUser, strings.Repeat("a", traceIDLen)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("trace of hidden spans: %v", err)
	}
	if _, err := e.s.Fields(ctx, viewerUser, model.SourceLogs, lastHour, Selection{Streams: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if s := e.q.LastScope(); !s.Empty() {
		t.Errorf("fields scope %v", s)
	}
	if _, err := e.s.MetricNames(ctx, viewerUser, "", lastHour, Selection{}); err != nil {
		t.Fatal(err)
	}
	if s := e.q.LastScope(); s.String() != model.OnlyStreams("a").String() {
		t.Errorf("metric names scope %v", s)
	}
	if _, err := e.s.Complete(ctx, viewerUser, "x", 1, model.SourceLogs, Selection{}); err != nil {
		t.Fatal(err)
	}
	if s := e.q.LastScope(); s.String() != model.OnlyStreams("a").String() {
		t.Errorf("complete scope %v", s)
	}
}

func TestSearchScopeErrorFailsClosed(t *testing.T) {
	e, fs := newScopedSearchEnv(t, model.AllStreams())
	fs.err = errors.New("meta down")
	ctx := context.Background()
	if _, err := e.s.Query(ctx, viewerUser, QueryRequest{Q: "*", Range: lastHour}); err == nil {
		t.Error("query ran without a scope")
	}
	if _, err := e.s.Event(ctx, viewerUser, uuid.NewString()); err == nil {
		t.Error("event read without a scope")
	}
	if len(e.q.Compiled)+len(e.q.Scopes) != 0 {
		t.Error("store reached without a scope")
	}
}

func TestTailResolvesScopeEveryPoll(t *testing.T) {
	e, fs := newScopedSearchEnv(t, model.OnlyStreams("a"))
	ctx := context.Background()
	tail, err := e.s.OpenTail(ctx, viewerUser, "*", nil, Selection{})
	if err != nil {
		t.Fatal(err)
	}
	defer tail.Close()
	e.now = e.now.Add(e.s.d.TailLag)
	if _, _, err := tail.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if f := e.q.LastFilter(); f.Streams.String() != model.OnlyStreams("a").String() {
		t.Errorf("first poll scope %v", f.Streams)
	}
	fs.set(model.NoStreams())
	e.now = e.now.Add(e.s.d.TailLag)
	if _, _, err := tail.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if f := e.q.LastFilter(); !f.Streams.Empty() {
		t.Errorf("revoked access must apply to an open tail: %v", f.Streams)
	}
}
