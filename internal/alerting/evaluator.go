// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// Evaluator settings (SPEC §14.1).
const (
	TickInterval   = 10 * time.Second
	DefaultWorkers = 4
	LeaderName     = "alerting"
	evalTimeout    = time.Minute
)

// ErrNoQuerier is returned when no query engine is configured.
var ErrNoQuerier = errors.New("alert evaluation needs the query engine (store.Querier)")

// Querier runs a compiled query (the Run method of store.Querier).
type Querier interface {
	Run(ctx context.Context, q *tql.Compiled) (store.Result, error)
}

// RuleStore persists rules, their state and history.
type RuleStore interface {
	List(ctx context.Context) ([]meta.AlertRule, error)
	SaveState(ctx context.Context, a meta.AlertRule) error
	AddHistory(ctx context.Context, h *meta.AlertHistory) error
}

// Leader reports whether this node runs the evaluator (SPEC §17.5).
type Leader interface {
	IsLeader(name string) bool
}

// Clock supplies the evaluation time.
type Clock interface{ Now() time.Time }

// Enqueuer accepts notifications for delivery.
type Enqueuer interface {
	Enqueue(n Notification, channelIDs []string)
}

// IndexedColumns provides the indexed-field columns for compilation (optional).
type IndexedColumns interface {
	Columns(ctx context.Context) (map[model.Source]map[string]string, error)
}

// EvaluatorDeps bundles the collaborators of the Evaluator.
type EvaluatorDeps struct {
	Rules     RuleStore
	Querier   Querier
	Notify    Enqueuer
	Clock     Clock
	Leader    Leader
	Indexed   IndexedColumns // may be nil
	Database  string
	PublicURL string // base URL for links to /alerts/{id}
	Workers   int    // 0 ⇒ DefaultWorkers
	Logger    *slog.Logger
}

// Evaluator runs due rules on every tick with a bounded worker pool.
type Evaluator struct{ d EvaluatorDeps }

// NewEvaluator returns an evaluator.
func NewEvaluator(d EvaluatorDeps) *Evaluator {
	if d.Workers <= 0 {
		d.Workers = DefaultWorkers
	}
	return &Evaluator{d: d}
}

// Run ticks every interval until ctx ends.
func (e *Evaluator) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

// Tick evaluates every enabled rule that is due (last evaluation at least `every` ago) and
// returns how many ran. Nothing runs unless this node leads "alerting".
func (e *Evaluator) Tick(ctx context.Context) int {
	if !e.d.Leader.IsLeader(LeaderName) {
		return 0
	}
	rules, err := e.d.Rules.List(ctx)
	if err != nil {
		e.d.Logger.Error("list alert rules", "err", err)
		return 0
	}
	now := e.d.Clock.Now().UTC()
	sem := make(chan struct{}, e.d.Workers)
	var wg sync.WaitGroup
	ran := 0
	for i := range rules {
		if !due(&rules[i], now) {
			continue
		}
		ran++
		wg.Add(1)
		sem <- struct{}{}
		go func(r meta.AlertRule) {
			defer func() { <-sem; wg.Done() }()
			e.evaluate(ctx, r, now)
		}(rules[i])
	}
	wg.Wait()
	return ran
}

// due reports whether an enabled rule should run at now.
func due(r *meta.AlertRule, now time.Time) bool {
	if !r.Enabled {
		return false
	}
	if r.LastEvalAt == nil {
		return true
	}
	doc, err := ParseRule(r.Doc)
	if err != nil {
		return true // evaluation records the error
	}
	return !now.Before(r.LastEvalAt.Add(time.Duration(doc.Every)))
}

// Outcome is the result of evaluating one rule.
type Outcome struct {
	Samples     []Sample
	State       RuleState
	Transitions []Transition
	Err         error
}

// Evaluate computes the next state of r at now without persisting or notifying (used by
// the evaluator and by POST /alerts/{id}/test).
func (e *Evaluator) Evaluate(ctx context.Context, r meta.AlertRule, now time.Time) Outcome {
	prev := DecodeRuleState(r.EvalState)
	doc, err := ParseRule(r.Doc)
	var samples []Sample
	if err == nil {
		samples, err = e.sample(ctx, doc, RuleScope(r.Streams), now)
	}
	if err != nil {
		st, tr := prev.Fail(err, now)
		return Outcome{State: st, Transitions: tr, Err: err}
	}
	st, tr := prev.Apply(samples, doc, now)
	return Outcome{Samples: samples, State: st, Transitions: tr}
}

func (e *Evaluator) evaluate(ctx context.Context, r meta.AlertRule, now time.Time) {
	out := e.Evaluate(ctx, r, now)
	prevAgg := DecodeRuleState(r.EvalState).Aggregate()
	agg := out.State.Aggregate()
	raw, err := json.Marshal(out.State)
	if err != nil {
		e.d.Logger.Error("encode alert state", "rule", r.ID, "err", err)
		return
	}
	r.EvalState, r.State, r.LastEvalAt, r.LastError = raw, string(agg), &now, ""
	if out.Err != nil {
		r.LastError = truncate(out.Err.Error())
	} else {
		r.LastValue = maxValue(out.Samples)
	}
	if agg != prevAgg || r.StateSince == nil {
		r.StateSince = &now
	}
	if err := e.d.Rules.SaveState(ctx, r); err != nil {
		e.d.Logger.Error("save alert state", "rule", r.ID, "err", err)
		return
	}
	doc, _ := ParseRule(r.Doc) // nil when invalid: only the error transition exists then
	for _, t := range out.Transitions {
		h := &meta.AlertHistory{RuleID: r.ID, At: t.At, FromState: string(t.From), ToState: string(t.To), Value: t.Value,
			Message: t.Message, GroupKey: t.Key}
		if err := e.d.Rules.AddHistory(ctx, h); err != nil {
			e.d.Logger.Error("alert history", "rule", r.ID, "err", err)
		}
		if t.Notify() && doc != nil && !doc.Muted(now) && len(doc.Channels) > 0 {
			e.d.Notify.Enqueue(e.notification(r, doc, t), doc.Channels)
		}
	}
}

func (e *Evaluator) notification(r meta.AlertRule, doc *RuleDoc, t Transition) Notification {
	return Notification{RuleID: r.ID, RuleName: r.Name, Severity: doc.Severity, Description: doc.Description,
		State: string(t.To), PreviousState: string(t.From), Value: t.Value, Op: doc.Condition.Op,
		Threshold: doc.Condition.Threshold, Group: t.Labels, At: t.At, URL: AlertURL(e.d.PublicURL, r.ID), Message: t.Message}
}

// AlertURL links to the rule in the UI.
func AlertURL(base, id string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/alerts/" + id
}

// RuleScope is the stream scope of a rule's stored streams: nil reads every stream, a list
// only those streams (SPEC §14.1). The rule keeps this scope when its author loses access.
func RuleScope(streams []string) model.StreamScope {
	if streams == nil {
		return model.AllStreams()
	}
	return model.OnlyStreams(streams...)
}

// sample runs the rule query over [now-window, now) in the rule's streams and converts the rows.
func (e *Evaluator) sample(ctx context.Context, doc *RuleDoc, streams model.StreamScope, now time.Time) ([]Sample, error) {
	var indexed map[model.Source]map[string]string
	if e.d.Indexed != nil {
		cols, err := e.d.Indexed.Columns(ctx)
		if err != nil {
			return nil, fmt.Errorf("indexed fields: %w", err)
		}
		indexed = cols
	}
	c, err := doc.Compile(now, e.d.Database, indexed, streams)
	if err != nil {
		return nil, err
	}
	if e.d.Querier == nil {
		return nil, ErrNoQuerier
	}
	qctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	res, err := e.d.Querier.Run(qctx, c)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return Samples(res.Rows, doc.GroupBy), nil
}

// Samples converts result rows (group columns then the value) into samples. Ungrouped rules
// always yield one sample ("" key; nil value when there is no row or the value is NULL/NaN).
func Samples(rows [][]any, groupBy []string) []Sample {
	if len(groupBy) == 0 {
		s := Sample{}
		if len(rows) > 0 && len(rows[0]) > 0 {
			s.Value = toFloat(rows[0][len(rows[0])-1])
		}
		return []Sample{s}
	}
	out := make([]Sample, 0, len(rows))
	for _, row := range rows {
		if len(row) != len(groupBy)+1 {
			continue
		}
		labels := make(map[string]string, len(groupBy))
		for i, g := range groupBy {
			labels[g] = labelString(row[i])
		}
		out = append(out, Sample{Key: GroupKey(labels), Labels: labels, Value: toFloat(row[len(groupBy)])})
	}
	return out
}

func labelString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case *string:
		if x == nil {
			return ""
		}
		return *x
	default:
		return fmt.Sprint(x)
	}
}

// toFloat converts numeric driver values; NULL, NaN and non-numbers are no data.
func toFloat(v any) *float64 {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case int:
		f = float64(x)
	case int8:
		f = float64(x)
	case int16:
		f = float64(x)
	case int32:
		f = float64(x)
	case int64:
		f = float64(x)
	case uint:
		f = float64(x)
	case uint8:
		f = float64(x)
	case uint16:
		f = float64(x)
	case uint32:
		f = float64(x)
	case uint64:
		f = float64(x)
	case *float64:
		if x == nil {
			return nil
		}
		f = *x
	case json.Number:
		var err error
		if f, err = x.Float64(); err != nil {
			return nil
		}
	default:
		return nil
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

// maxValue is the largest sample value (the rule's last value).
func maxValue(samples []Sample) *float64 {
	var out *float64
	for _, s := range samples {
		if s.Value != nil && (out == nil || *s.Value > *out) {
			v := *s.Value
			out = &v
		}
	}
	return out
}
