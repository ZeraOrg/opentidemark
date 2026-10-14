// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeQuerier returns programmed rows per rule query text; it tracks concurrency.
type fakeQuerier struct {
	mu       sync.Mutex
	rows     map[string][][]any
	errs     map[string]error
	calls    int
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
	lastSQL  string
	lastArgs []any
}

func (q *fakeQuerier) set(query string, rows [][]any, err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.rows[query], q.errs[query] = rows, err
}

func (q *fakeQuerier) Run(_ context.Context, c *tql.Compiled) (store.Result, error) {
	n := q.inFlight.Add(1)
	defer q.inFlight.Add(-1)
	for {
		m := q.maxSeen.Load()
		if n <= m || q.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	if q.delay > 0 {
		time.Sleep(q.delay)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	q.lastSQL, q.lastArgs = c.SQL, c.Args
	if c.Limit > MaxGroups {
		return store.Result{}, errors.New("row limit above MaxGroups")
	}
	for query, rows := range q.rows {
		if strings.Contains(c.SQL, query) || query == "*" {
			return store.Result{Kind: tql.KindTable, Rows: rows}, q.errs[query]
		}
	}
	return store.Result{Kind: tql.KindTable}, q.errs["*"]
}

// memRules is an in-memory RuleStore.
type memRules struct {
	mu      sync.Mutex
	rules   map[string]meta.AlertRule
	history []meta.AlertHistory
	listErr error
}

func (m *memRules) List(context.Context) ([]meta.AlertRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []meta.AlertRule
	for _, r := range m.rules {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b meta.AlertRule) int { return strings.Compare(a.ID, b.ID) })
	return out, m.listErr
}

func (m *memRules) SaveState(_ context.Context, a meta.AlertRule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules[a.ID] = a
	return nil
}

func (m *memRules) AddHistory(_ context.Context, h *meta.AlertHistory) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.history = append(m.history, *h)
	return nil
}

func (m *memRules) get(id string) meta.AlertRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rules[id]
}

type sent struct {
	n        Notification
	channels []string
}

type fakeEnqueuer struct {
	mu   sync.Mutex
	sent []sent
}

func (f *fakeEnqueuer) Enqueue(n Notification, ch []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{n, ch})
}

func (f *fakeEnqueuer) states() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, s := range f.sent {
		out = append(out, s.n.PreviousState+"→"+s.n.State)
	}
	return out
}

type leaderFlag struct{ on atomic.Bool }

func (l *leaderFlag) IsLeader(name string) bool { return name == LeaderName && l.on.Load() }

type fakeIndexed struct{ err error }

func (f fakeIndexed) Columns(context.Context) (map[model.Source]map[string]string, error) {
	return map[model.Source]map[string]string{model.SourceLogs: {"user.id": "f_user_id"}}, f.err
}

type evalEnv struct {
	clock  *fakeClock
	q      *fakeQuerier
	rules  *memRules
	out    *fakeEnqueuer
	leader *leaderFlag
	ev     *Evaluator
}

func newEvalEnv() *evalEnv {
	e := &evalEnv{clock: &fakeClock{now: t0}, q: &fakeQuerier{rows: map[string][][]any{}, errs: map[string]error{}},
		rules: &memRules{rules: map[string]meta.AlertRule{}}, out: &fakeEnqueuer{}, leader: &leaderFlag{}}
	e.leader.on.Store(true)
	e.ev = NewEvaluator(EvaluatorDeps{Rules: e.rules, Querier: e.q, Notify: e.out, Clock: e.clock, Leader: e.leader,
		Database: "tidemark", PublicURL: "https://tm.example.com/", Logger: quiet()})
	return e
}

func (e *evalEnv) addRule(id string, enabled bool, doc string) {
	e.rules.rules[id] = meta.AlertRule{ID: id, Name: "rule " + id, Enabled: enabled, Doc: json.RawMessage(doc), State: "ok"}
}

func simpleRule(query, op string, threshold float64, forDur, every string, extra string) string {
	return `{"query":"` + query + `","condition":{"op":"` + op + `","threshold":` + FormatValue(threshold) +
		`},"window":"5m","every":"` + every + `","for":"` + forDur + `","channels":["c1","c2"]` + extra + `}`
}

// TestEvaluatorLifecycle drives one ungrouped rule through every state with a fake clock.
func TestEvaluatorLifecycle(t *testing.T) {
	e := newEvalEnv()
	e.addRule("r1", true, simpleRule("level:error | stats count()", ">", 10, "1m", "1m", `,"severity":"critical"`))
	steps := []struct {
		advance   time.Duration
		value     any
		err       error
		ran       int
		wantState State
		wantSent  []string
	}{
		{0, uint64(3), nil, 1, StateOK, nil},
		{30 * time.Second, uint64(50), nil, 0, StateOK, nil}, // not due yet
		{30 * time.Second, uint64(50), nil, 1, StatePending, nil},
		{time.Minute, uint64(60), nil, 1, StateFiring, []string{"pending→firing"}},
		{time.Minute, uint64(70), nil, 1, StateFiring, nil}, // de-duplicated: no new transition
		{time.Minute, nil, errors.New("timeout"), 1, StateFiring, nil},
		{time.Minute, nil, errors.New("timeout"), 1, StateFiring, nil},
		{time.Minute, nil, errors.New("timeout"), 1, StateError, []string{"firing→error"}},
		{time.Minute, nil, errors.New("timeout"), 1, StateError, nil},
		{time.Minute, uint64(1), nil, 1, StateOK, []string{"error→ok"}},
		{time.Minute, float64(20), nil, 1, StatePending, nil},
		{time.Minute, nil, nil, 1, StateOK, nil}, // NULL ⇒ no data ⇒ ok (pending→ok is silent)
	}
	for i, st := range steps {
		e.clock.Advance(st.advance)
		var rows [][]any
		if st.value != nil {
			rows = [][]any{{st.value}}
		}
		e.q.set("*", rows, st.err)
		before := len(e.out.states())
		if ran := e.ev.Tick(context.Background()); ran != st.ran {
			t.Fatalf("step %d: ran %d want %d", i, ran, st.ran)
		}
		r := e.rules.get("r1")
		if State(r.State) != st.wantState {
			t.Fatalf("step %d: state %s want %s (err %q)", i, r.State, st.wantState, r.LastError)
		}
		if got := e.out.states()[before:]; !slices.Equal(got, st.wantSent) && (len(got) != 0 || len(st.wantSent) != 0) {
			t.Fatalf("step %d: sent %v want %v", i, got, st.wantSent)
		}
		if st.err != nil && r.LastError == "" {
			t.Errorf("step %d: last error not recorded", i)
		}
	}
	first := e.out.sent[0]
	if first.n.URL != "https://tm.example.com/alerts/r1" || first.n.Severity != SeverityCritical || *first.n.Value != 60 ||
		!slices.Equal(first.channels, []string{"c1", "c2"}) || first.n.Op != ">" || first.n.Threshold != 10 {
		t.Errorf("notification %+v", first)
	}
	if len(e.rules.history) != 6 {
		var hs []string
		for _, h := range e.rules.history {
			hs = append(hs, h.FromState+"→"+h.ToState)
		}
		t.Errorf("history %v", hs)
	}
	if !strings.Contains(e.q.lastSQL, "tidemark.events") {
		t.Errorf("sql %s", e.q.lastSQL)
	}
}

func TestEvaluatorGroupsAndMute(t *testing.T) {
	e := newEvalEnv()
	until := t0.Add(10 * time.Minute).Format(time.RFC3339)
	e.addRule("g", true, `{"query":"level:error | stats count() by service","condition":{"op":">=","threshold":5},
		"window":"5m","every":"10s","for":"0s","groupBy":["service"],"channels":["c"],"muteUntil":"`+until+`"}`)
	e.q.set("*", [][]any{{"api", uint64(9)}, {"web", uint64(1)}, {nil, int64(7)}}, nil)
	e.ev.Tick(context.Background())
	r := e.rules.get("g")
	st := DecodeRuleState(r.EvalState)
	if r.State != "firing" || len(st.Groups) != 2 || *r.LastValue != 9 {
		t.Fatalf("groups %+v state %s", st, r.State)
	}
	if len(e.out.states()) != 0 || len(e.rules.history) != 2 {
		t.Errorf("muted rule notified %v; history %d", e.out.states(), len(e.rules.history))
	}
	e.clock.Advance(11 * time.Minute) // mute expired
	e.q.set("*", [][]any{{"api", uint64(9)}}, nil)
	e.ev.Tick(context.Background())
	got := e.out.states()
	if len(got) != 1 || got[0] != "firing→ok" || e.out.sent[0].n.Group["service"] != "" {
		t.Errorf("resolved group: %v %+v", got, e.out.sent)
	}
}

func TestEvaluatorSkips(t *testing.T) {
	e := newEvalEnv()
	e.addRule("off", false, simpleRule("* | stats count()", ">", 1, "0s", "10s", ""))
	e.addRule("on", true, simpleRule("* | stats count()", ">", 1, "0s", "10s", ""))
	e.leader.on.Store(false)
	if ran := e.ev.Tick(context.Background()); ran != 0 || e.q.calls != 0 {
		t.Errorf("follower ran %d", ran)
	}
	e.leader.on.Store(true)
	if ran := e.ev.Tick(context.Background()); ran != 1 {
		t.Errorf("leader ran %d", ran)
	}
	e.rules.listErr = errors.New("db down")
	e.clock.Advance(time.Minute)
	if ran := e.ev.Tick(context.Background()); ran != 0 {
		t.Errorf("list error ran %d", ran)
	}
}

func TestEvaluatorInvalidDocErrors(t *testing.T) {
	e := newEvalEnv()
	e.addRule("bad", true, `{"query":"level:error"}`)
	for range ErrorThreshold {
		e.ev.Tick(context.Background())
		e.clock.Advance(time.Second)
	}
	r := e.rules.get("bad")
	if r.State != "error" || r.LastError == "" || len(e.out.states()) != 0 {
		t.Errorf("invalid doc: %+v sent %v", r, e.out.states())
	}
}

func TestEvaluatorWorkerPoolBound(t *testing.T) {
	e := newEvalEnv()
	e.q.delay = 20 * time.Millisecond
	for i := range 12 {
		e.addRule(string(rune('a'+i)), true, simpleRule("* | stats count()", ">", 1, "0s", "10s", ""))
	}
	if ran := e.ev.Tick(context.Background()); ran != 12 {
		t.Fatalf("ran %d", ran)
	}
	if m := e.q.maxSeen.Load(); m > DefaultWorkers || m < 2 {
		t.Errorf("max concurrent evaluations %d", m)
	}
}

func TestEvaluatorIndexedAndRun(t *testing.T) {
	e := newEvalEnv()
	e.ev.d.Indexed = fakeIndexed{}
	e.addRule("i", true, simpleRule("user.id:42 | stats count()", ">", 0, "0s", "10s", ""))
	e.ev.Tick(context.Background())
	if !strings.Contains(e.q.lastSQL, "f_user_id") {
		t.Errorf("indexed column not used: %s", e.q.lastSQL)
	}
	e.ev.d.Indexed = fakeIndexed{err: errors.New("meta down")}
	out := e.ev.Evaluate(context.Background(), e.rules.get("i"), t0)
	if out.Err == nil {
		t.Error("indexed error swallowed")
	}
	noQ := NewEvaluator(EvaluatorDeps{Rules: e.rules, Clock: e.clock, Leader: e.leader, Logger: quiet()})
	if out := noQ.Evaluate(context.Background(), e.rules.get("i"), t0); !errors.Is(out.Err, ErrNoQuerier) {
		t.Errorf("no querier: %v", out.Err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.ev.Run(ctx, time.Millisecond); close(done) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	<-done
}

func TestSamplesConversion(t *testing.T) {
	v := 2.5
	vals := []any{float64(1), float32(1), 1, int8(1), int16(1), int32(1), int64(1), uint(1), uint8(1), uint16(1), uint32(1),
		uint64(1), &v, json.Number("1")}
	for _, x := range vals {
		if f := toFloat(x); f == nil {
			t.Errorf("%T not converted", x)
		}
	}
	var nilF *float64
	for _, x := range []any{nil, "1", nilF, json.Number("x"), float64(0) / zero()} {
		if toFloat(x) != nil {
			t.Errorf("%v converted", x)
		}
	}
	s := "x"
	var nilS *string
	got := Samples([][]any{{&s, 1}, {nilS, 2}, {"short"}}, []string{"g"})
	if len(got) != 2 || got[0].Labels["g"] != "x" || got[1].Labels["g"] != "" {
		t.Errorf("%+v", got)
	}
	if got := Samples(nil, nil); len(got) != 1 || got[0].Value != nil {
		t.Errorf("empty ungrouped %+v", got)
	}
	if AlertURL("", "x") != "" {
		t.Error("url without base")
	}
}

func zero() float64 { return 0 }

// TestEvaluatorRuleStreams checks that a rule reads its stored streams: every stream when
// unset, the listed streams otherwise, nothing for an empty list (SPEC §14.1).
func TestEvaluatorRuleStreams(t *testing.T) {
	tests := []struct {
		streams []string
		want    string
	}{
		{nil, ""},
		{[]string{"web", "api"}, "stream IN (?, ?)"},
		{[]string{}, "AND 0"},
	}
	for _, tt := range tests {
		e := newEvalEnv()
		e.addRule("s", true, simpleRule("* | stats count()", ">", 0, "0s", "10s", ""))
		r := e.rules.get("s")
		r.Streams = tt.streams
		if out := e.ev.Evaluate(context.Background(), r, t0); out.Err != nil {
			t.Fatal(out.Err)
		}
		if tt.want == "" && strings.Contains(e.q.lastSQL, "stream IN") || tt.want != "" && !strings.Contains(e.q.lastSQL, tt.want) {
			t.Errorf("%v: %s", tt.streams, e.q.lastSQL)
		}
		if len(tt.streams) == 2 && !slices.Contains(e.q.lastArgs, any("api")) {
			t.Errorf("stream names must be arguments: %v", e.q.lastArgs)
		}
	}
	if s := RuleScope(nil); !s.All() {
		t.Errorf("nil streams: %v", s)
	}
}
