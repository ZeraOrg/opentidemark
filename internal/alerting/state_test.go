// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func fptr(v float64) *float64 { return &v }

func ruleDoc(op string, threshold float64, forDur time.Duration, noData string, groupBy ...string) *RuleDoc {
	return &RuleDoc{Condition: Condition{Op: op, Threshold: threshold}, For: Duration(forDur), NoData: noData,
		GroupBy: groupBy, Window: Duration(5 * time.Minute), Every: Duration(time.Minute), Severity: SeverityWarning}
}

func TestConditionMet(t *testing.T) {
	tests := []struct {
		op   string
		v    float64
		want bool
	}{
		{">", 11, true}, {">", 10, false}, {">=", 10, true}, {">=", 9, false},
		{"<", 9, true}, {"<", 10, false}, {"<=", 10, true}, {"<=", 11, false},
		{"==", 10, true}, {"==", 11, false}, {"!=", 11, true}, {"!=", 10, false},
	}
	for _, tc := range tests {
		if got := (Condition{Op: tc.op, Threshold: 10}).Met(tc.v); got != tc.want {
			t.Errorf("%v %s 10 = %v", tc.v, tc.op, got)
		}
	}
}

// TestStepExhaustive covers every previous state × value kind × noData policy × for.
func TestStepExhaustive(t *testing.T) {
	const forDur = 2 * time.Minute
	now := t0.Add(10 * time.Minute)
	prevs := map[string]GroupState{
		"ok":            {State: StateOK, Since: t0},
		"pending-young": {State: StatePending, Since: now.Add(-time.Minute)},
		"pending-old":   {State: StatePending, Since: now.Add(-forDur)},
		"firing":        {State: StateFiring, Since: t0},
		"error":         {State: StateError, Since: t0},
	}
	values := map[string]*float64{"met": fptr(50), "unmet": fptr(1), "nodata": nil}
	type key struct{ prev, value, noData string }
	// expectations with for = 2m; "same" keeps the previous state and since.
	want := map[key]string{}
	for p := range prevs {
		for _, nd := range []string{NoDataOK, NoDataKeep, NoDataAlerting} {
			for v := range values {
				met := v == "met" || (v == "nodata" && nd == NoDataAlerting)
				var exp string
				switch {
				case v == "nodata" && nd == NoDataKeep:
					exp = "same"
				case !met && p == "ok":
					exp = "same"
				case !met:
					exp = "ok@now"
				case p == "firing", p == "pending-young":
					exp = "same"
				case p == "pending-old":
					exp = "firing@now"
				default: // ok, error
					exp = "pending@now"
				}
				want[key{p, v, nd}] = exp
			}
		}
	}
	for k, exp := range want {
		prev := prevs[k.prev]
		doc := ruleDoc(">", 10, forDur, k.noData)
		got := Step(prev, values[k.value], doc, now)
		var wantState GroupState
		switch exp {
		case "same":
			wantState = prev
		default:
			st, _, _ := strings.Cut(exp, "@")
			wantState = GroupState{State: State(st), Since: now}
		}
		if got.State != wantState.State || !got.Since.Equal(wantState.Since) {
			t.Errorf("%+v: got %s@%v want %s@%v", k, got.State, got.Since, wantState.State, wantState.Since)
		}
	}
}

func TestStepWithoutFor(t *testing.T) {
	doc := ruleDoc(">", 10, 0, NoDataOK)
	for _, prev := range []State{StateOK, StateError} {
		if got := Step(GroupState{State: prev}, fptr(11), doc, t0); got.State != StateFiring {
			t.Errorf("%s with for=0: %s", prev, got.State)
		}
	}
}

func TestTransitionNotify(t *testing.T) {
	tests := []struct {
		from, to State
		want     bool
	}{
		{StateOK, StatePending, false}, {StatePending, StateOK, false}, {StatePending, StateFiring, true},
		{StateOK, StateFiring, true}, {StateFiring, StateOK, true}, {StateOK, StateError, true},
		{StateFiring, StateError, true}, {StateError, StateOK, true}, {StateError, StatePending, false},
		{StateError, StateFiring, true},
	}
	for _, tc := range tests {
		if got := (Transition{From: tc.from, To: tc.to}).Notify(); got != tc.want {
			t.Errorf("%s→%s notify=%v", tc.from, tc.to, got)
		}
	}
}

func TestFailSequence(t *testing.T) {
	s := RuleState{Groups: map[string]GroupState{"": {State: StateFiring, Since: t0}}}
	boom := errors.New("clickhouse down")
	var tr []Transition
	for i := 1; i < ErrorThreshold; i++ {
		s, tr = s.Fail(boom, t0)
		if len(tr) != 0 || s.Aggregate() != StateFiring || s.Failures != i {
			t.Fatalf("failure %d: %+v %v", i, s, tr)
		}
	}
	s, tr = s.Fail(boom, t0.Add(time.Minute))
	if len(tr) != 1 || tr[0].From != StateFiring || tr[0].To != StateError || tr[0].Message != "clickhouse down" ||
		s.Aggregate() != StateError {
		t.Fatalf("third failure: %+v %v", s, tr)
	}
	s, tr = s.Fail(boom, t0.Add(2*time.Minute))
	if len(tr) != 0 || s.Failures != ErrorThreshold+1 {
		t.Errorf("stays in error silently: %+v %v", s, tr)
	}
	long := errors.New(strings.Repeat("é", 400))
	fresh := RuleState{Failures: 2, Groups: map[string]GroupState{}}
	_, tr = fresh.Fail(long, t0)
	if len(tr) != 1 || len(tr[0].Message) > maxMessageLen+len("…") || !strings.HasSuffix(tr[0].Message, "…") {
		t.Errorf("message not truncated: %d", len(tr[0].Message))
	}
}

func TestApplyUngrouped(t *testing.T) {
	doc := ruleDoc(">", 10, time.Minute, NoDataOK)
	s := DecodeRuleState(nil)
	steps := []struct {
		at        time.Duration
		value     *float64
		wantState State
		wantTrans string
	}{
		{0, fptr(5), StateOK, ""},
		{time.Minute, fptr(20), StatePending, "ok→pending"},
		{90 * time.Second, fptr(20), StatePending, ""},
		{2 * time.Minute, fptr(30), StateFiring, "pending→firing"},
		{3 * time.Minute, fptr(30), StateFiring, ""},
		{4 * time.Minute, nil, StateOK, "firing→ok"},
		{5 * time.Minute, fptr(1), StateOK, ""},
	}
	for i, st := range steps {
		var tr []Transition
		s, tr = s.Apply([]Sample{{Value: st.value}}, doc, t0.Add(st.at))
		got := ""
		if len(tr) == 1 {
			got = fmt.Sprintf("%s→%s", tr[0].From, tr[0].To)
		}
		if s.Aggregate() != st.wantState || got != st.wantTrans || len(tr) > 1 {
			t.Fatalf("step %d: state %s trans %v", i, s.Aggregate(), tr)
		}
	}
	if _, ok := s.Groups[""]; !ok {
		t.Error("ungrouped rules keep their ok group")
	}
}

func TestApplyGrouped(t *testing.T) {
	doc := ruleDoc(">=", 10, 0, NoDataOK, "service")
	a := Sample{Key: GroupKey(map[string]string{"service": "a"}), Labels: map[string]string{"service": "a"}}
	b := Sample{Key: GroupKey(map[string]string{"service": "b"}), Labels: map[string]string{"service": "b"}}
	s := DecodeRuleState([]byte(`{bad`))
	a.Value, b.Value = fptr(10), fptr(3)
	s, tr := s.Apply([]Sample{a, b}, doc, t0)
	if len(tr) != 1 || tr[0].Key != a.Key || tr[0].To != StateFiring || len(s.Groups) != 1 || s.Aggregate() != StateFiring {
		t.Fatalf("first: %+v %+v", s, tr)
	}
	if !strings.Contains(tr[0].Message, "value 10, condition >= 10") {
		t.Errorf("message %q", tr[0].Message)
	}
	// a disappears (no data ⇒ ok, resolved), b fires.
	b.Value = fptr(12)
	s, tr = s.Apply([]Sample{b}, doc, t0.Add(time.Minute))
	if len(tr) != 2 || s.Aggregate() != StateFiring || len(s.Groups) != 1 {
		t.Fatalf("second: %+v %+v", s, tr)
	}
	for _, x := range tr {
		if x.Key == a.Key && (x.To != StateOK || x.Value != nil || !strings.Contains(x.Message, "no data")) {
			t.Errorf("a: %+v", x)
		}
	}
	// keep policy holds a vanished group; alerting policy fires it.
	keep := ruleDoc(">=", 10, 0, NoDataKeep, "service")
	if s2, tr := s.Apply(nil, keep, t0.Add(2*time.Minute)); len(tr) != 0 || s2.Groups[b.Key].State != StateFiring {
		t.Errorf("keep: %+v %v", s2, tr)
	}
	alerting := ruleDoc(">=", 10, 0, NoDataAlerting, "service")
	if s2, tr := s.Apply(nil, alerting, t0.Add(2*time.Minute)); len(tr) != 0 || s2.Groups[b.Key].State != StateFiring {
		t.Errorf("alerting: %+v %v", s2, tr)
	}
}

func TestApplyRecoversFromError(t *testing.T) {
	grouped := ruleDoc(">", 10, 0, NoDataOK, "host")
	s := RuleState{Failures: 5, Groups: map[string]GroupState{"": {State: StateError, Since: t0}}}
	h := Sample{Key: GroupKey(map[string]string{"host": "x"}), Labels: map[string]string{"host": "x"}, Value: fptr(50)}
	next, tr := s.Apply([]Sample{h}, grouped, t0.Add(time.Minute))
	if len(tr) != 2 || tr[0].From != StateError || tr[0].To != StateOK || tr[0].Key != "" || tr[1].To != StateFiring ||
		next.Failures != 0 || next.Aggregate() != StateFiring {
		t.Fatalf("grouped recovery: %+v %+v", next, tr)
	}
	ungrouped := ruleDoc(">", 10, 0, NoDataOK)
	next, tr = s.Apply([]Sample{{Value: fptr(1)}}, ungrouped, t0.Add(time.Minute))
	if len(tr) != 1 || tr[0].From != StateError || tr[0].To != StateOK || next.Aggregate() != StateOK {
		t.Fatalf("ungrouped recovery: %+v %+v", next, tr)
	}
}

func TestGroupKeyAndDecode(t *testing.T) {
	if GroupKey(nil) != "" || GroupKey(map[string]string{"b": "2", "a": "1"}) != `{"a":"1","b":"2"}` {
		t.Error("group key not canonical")
	}
	s := DecodeRuleState([]byte(`{"failures":2,"groups":{"":{"state":"pending","since":"2026-10-13T12:00:00Z"}}}`))
	if s.Failures != 2 || s.Groups[""].State != StatePending || !s.Groups[""].Since.Equal(t0) {
		t.Errorf("%+v", s)
	}
	if FormatValue(0.25) != "0.25" || FormatValue(1e21) != "1e+21" {
		t.Error("format")
	}
}
