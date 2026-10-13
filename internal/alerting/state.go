// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"
	"unicode/utf8"
)

// State is the evaluation state of a rule or group (SPEC §14.1).
type State string

// States.
const (
	StateOK      State = "ok"
	StatePending State = "pending"
	StateFiring  State = "firing"
	StateError   State = "error"
)

// ErrorThreshold is the number of consecutive failed evaluations that set the error state.
const ErrorThreshold = 3

// maxMessageLen bounds error messages stored in history and notifications.
const maxMessageLen = 512

// GroupState is the state of one group (key "" for ungrouped rules).
type GroupState struct {
	State  State             `json:"state"`
	Since  time.Time         `json:"since"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  *float64          `json:"value,omitempty"`
}

// RuleState is the persisted evaluation state of a rule (alert_rules.eval_state).
type RuleState struct {
	Failures int                   `json:"failures"`
	Groups   map[string]GroupState `json:"groups"`
}

// Sample is one evaluated value; Value nil means no data.
type Sample struct {
	Key    string
	Labels map[string]string
	Value  *float64
}

// Transition is one state change of a group.
type Transition struct {
	Key     string
	Labels  map[string]string
	From    State
	To      State
	Value   *float64
	At      time.Time
	Message string
}

// Notify reports whether the transition is announced: entering firing or error, and
// resolving (ok after firing or error). pending and pending ⇒ ok are silent.
func (t Transition) Notify() bool {
	return t.To == StateFiring || t.To == StateError || (t.To == StateOK && (t.From == StateFiring || t.From == StateError))
}

// DecodeRuleState reads persisted state; empty or invalid input yields a fresh state.
func DecodeRuleState(raw []byte) RuleState {
	var s RuleState
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s.Groups == nil {
		return RuleState{Groups: map[string]GroupState{}}
	}
	return s
}

// GroupKey returns the canonical key of a label set ("" when there are no labels).
func GroupKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	b, err := json.Marshal(labels) // map keys are sorted, so the key is canonical
	if err != nil {
		return ""
	}
	return string(b)
}

// Aggregate is the overall state: error > firing > pending > ok.
func (s RuleState) Aggregate() State {
	rank := map[State]int{StateOK: 0, StatePending: 1, StateFiring: 2, StateError: 3}
	out := StateOK
	for _, g := range s.Groups {
		if rank[g.State] > rank[out] {
			out = g.State
		}
	}
	return out
}

// Fail records a failed evaluation; the third consecutive failure moves the rule to error
// (one transition, replacing all groups).
func (s RuleState) Fail(err error, now time.Time) (RuleState, []Transition) {
	next := RuleState{Failures: s.Failures + 1, Groups: s.Groups}
	prev := s.Aggregate()
	if next.Failures < ErrorThreshold || prev == StateError {
		return next, nil
	}
	next.Groups = map[string]GroupState{"": {State: StateError, Since: now}}
	return next, []Transition{{From: prev, To: StateError, At: now, Message: truncate(err.Error())}}
}

// Apply records a successful evaluation of samples under doc (SPEC §14.1): per group
// ok → pending (condition true, shorter than `for`) → firing → ok; groups missing from the
// samples are treated as no data; ok groups of grouped rules are forgotten.
func (s RuleState) Apply(samples []Sample, doc *RuleDoc, now time.Time) (RuleState, []Transition) {
	grouped := len(doc.GroupBy) > 0
	next := RuleState{Groups: map[string]GroupState{}}
	var out []Transition
	prevGroups := s.Groups
	if eg, ok := prevGroups[""]; ok && grouped && eg.State == StateError {
		out = append(out, Transition{From: StateError, To: StateOK, At: now, Message: "evaluation succeeded again"})
		prevGroups = map[string]GroupState{}
	}
	seen := map[string]bool{}
	for _, smp := range samples {
		seen[smp.Key] = true
		prev, ok := prevGroups[smp.Key]
		if !ok {
			prev = GroupState{State: StateOK, Labels: smp.Labels}
		}
		out = next.record(smp.Key, prev, smp.Labels, smp.Value, doc, now, out)
	}
	for key, prev := range prevGroups {
		if !seen[key] {
			out = next.record(key, prev, prev.Labels, nil, doc, now, out)
		}
	}
	if grouped {
		for key, g := range next.Groups {
			if g.State == StateOK {
				delete(next.Groups, key)
			}
		}
	}
	return next, out
}

// record steps one group and appends its transition.
func (s RuleState) record(key string, prev GroupState, labels map[string]string, v *float64, doc *RuleDoc,
	now time.Time, out []Transition) []Transition {
	ns := Step(prev, v, doc, now)
	ns.Labels, ns.Value = labels, v
	s.Groups[key] = ns
	if ns.State == prev.State {
		return out
	}
	return append(out, Transition{Key: key, Labels: labels, From: prev.State, To: ns.State, Value: v, At: now,
		Message: describe(ns.State, v, doc)})
}

// Step computes the next state of one group for value v (nil = no data).
func Step(prev GroupState, v *float64, doc *RuleDoc, now time.Time) GroupState {
	var met bool
	switch {
	case v != nil:
		met = doc.Condition.Met(*v)
	case doc.NoData == NoDataKeep:
		return prev
	default:
		met = doc.NoData == NoDataAlerting
	}
	if !met {
		if prev.State == StateOK {
			return prev
		}
		return GroupState{State: StateOK, Since: now}
	}
	switch prev.State {
	case StateFiring:
		return prev
	case StatePending:
		if now.Sub(prev.Since) >= time.Duration(doc.For) {
			return GroupState{State: StateFiring, Since: now}
		}
		return prev
	default:
		if doc.For == 0 {
			return GroupState{State: StateFiring, Since: now}
		}
		return GroupState{State: StatePending, Since: now}
	}
}

func describe(st State, v *float64, doc *RuleDoc) string {
	if v == nil {
		return fmt.Sprintf("%s: no data (noData=%s)", st, doc.NoData)
	}
	return fmt.Sprintf("%s: value %s, condition %s %s", st, FormatValue(*v), doc.Condition.Op,
		FormatValue(doc.Condition.Threshold))
}

// FormatValue renders a value compactly.
func FormatValue(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func truncate(s string) string {
	if len(s) <= maxMessageLen {
		return s
	}
	n := maxMessageLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
