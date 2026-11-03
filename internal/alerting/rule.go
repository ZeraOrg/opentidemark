// SPDX-License-Identifier: Apache-2.0

// Package alerting evaluates alert rules and delivers notifications (SPEC §14): rule
// validation, the per-group state machine, the evaluator (10 s tick, bounded worker pool,
// singleton via Leader) and the webhook, Slack, Teams and email notifiers with retries.
package alerting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// Rule limits (SPEC §14.1).
const (
	MinEvery       = 10 * time.Second
	MaxEvery       = 24 * time.Hour
	MinWindow      = 10 * time.Second
	MaxWindow      = 7 * 24 * time.Hour
	MaxFor         = 24 * time.Hour
	MaxChannels    = 20
	MaxGroups      = 1000
	maxDescription = 1024
	maxQueryLen    = 8192
	day            = 24 * time.Hour
)

// Deviation rule limits (SPEC §14.1).
const (
	MaxSigma           = 100
	MinBaselineBuckets = 5
	MaxBaselineBuckets = 1000
	MaxBaseline        = 30 * day
	MinBaselineSamples = 5
	MaxBaselineRows    = 100_000
)

// Severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// NoData policies: treat a missing value as "condition false", keep the state, or treat it
// as "condition true".
const (
	NoDataOK       = "ok"
	NoDataKeep     = "keep"
	NoDataAlerting = "alerting"
)

var (
	severities = []string{SeverityInfo, SeverityWarning, SeverityCritical}
	noDataOpts = []string{NoDataOK, NoDataKeep, NoDataAlerting}
	operators  = []string{">", ">=", "<", "<=", "==", "!="}
)

// ErrInvalidRule is wrapped by every rule validation error.
var ErrInvalidRule = errors.New("invalid alert rule")

// RuleError is a validation error of one field of a rule document.
type RuleError struct {
	Field string
	Msg   string
}

func (e *RuleError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Msg) }

// Unwrap lets errors.Is match ErrInvalidRule.
func (e *RuleError) Unwrap() error { return ErrInvalidRule }

func ruleErr(field, format string, args ...any) error {
	return &RuleError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

// Duration is a JSON duration string ("5m").
type Duration time.Duration

// MarshalJSON encodes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON decodes a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// Condition types (SPEC §14.1): a fixed threshold, or a deviation from the value's baseline.
const (
	ConditionThreshold = "threshold"
	ConditionDeviation = "deviation"
)

// Deviation directions.
const (
	DirectionAbove = "above"
	DirectionBelow = "below"
	DirectionBoth  = "both"
)

var directions = []string{DirectionAbove, DirectionBelow, DirectionBoth}

// Condition compares the rule value with a threshold (Op, Threshold) or, for Type deviation,
// with the baseline of the value's recent history (Direction, Sigma, Baseline, MinDelta). Type
// "" means threshold. The fields of the other form must be zero.
type Condition struct {
	Type      string   `json:"type"`
	Op        string   `json:"op"`
	Threshold float64  `json:"threshold"`
	Direction string   `json:"direction"`
	Sigma     float64  `json:"sigma"`
	Baseline  Duration `json:"baseline"`
	MinDelta  float64  `json:"minDelta"`
}

// MarshalJSON writes the canonical form of the condition's type: {op, threshold} for threshold
// conditions (as before deviation rules existed) and {type, direction, sigma, baseline,
// minDelta} for deviation conditions.
func (c Condition) MarshalJSON() ([]byte, error) {
	if c.IsDeviation() {
		return json.Marshal(struct {
			Type      string   `json:"type"`
			Direction string   `json:"direction"`
			Sigma     float64  `json:"sigma"`
			Baseline  Duration `json:"baseline"`
			MinDelta  float64  `json:"minDelta"`
		}{ConditionDeviation, c.Direction, c.Sigma, c.Baseline, c.MinDelta})
	}
	return json.Marshal(struct {
		Op        string  `json:"op"`
		Threshold float64 `json:"threshold"`
	}{c.Op, c.Threshold})
}

// IsDeviation reports whether the condition compares with a baseline.
func (c Condition) IsDeviation() bool { return c.Type == ConditionDeviation }

// Met reports whether v satisfies a threshold condition.
func (c Condition) Met(v float64) bool {
	switch c.Op {
	case ">":
		return v > c.Threshold
	case ">=":
		return v >= c.Threshold
	case "<":
		return v < c.Threshold
	case "<=":
		return v <= c.Threshold
	case "==":
		return v == c.Threshold
	default:
		return v != c.Threshold
	}
}

// RuleDoc is the alert rule document (SPEC §14.1).
type RuleDoc struct {
	Description string     `json:"description"`
	Query       string     `json:"query"`
	Condition   Condition  `json:"condition"`
	Window      Duration   `json:"window"`
	Every       Duration   `json:"every"`
	For         Duration   `json:"for"`
	Severity    string     `json:"severity"`
	Channels    []string   `json:"channels"`
	NoData      string     `json:"noData"`
	GroupBy     []string   `json:"groupBy"`
	MuteUntil   *time.Time `json:"muteUntil"`
}

// Muted reports whether notifications are suppressed at now.
func (d *RuleDoc) Muted(now time.Time) bool { return d.MuteUntil != nil && now.Before(*d.MuteUntil) }

// ParseRule strictly decodes and validates a rule document. Defaults: severity warning,
// noData ok, empty channels and groupBy.
func ParseRule(raw json.RawMessage) (*RuleDoc, error) {
	if len(raw) == 0 {
		return nil, ruleErr("doc", "is required")
	}
	if err := jsonx.CheckDepth(raw, jsonx.MaxDepth); err != nil {
		return nil, ruleErr("doc", "is nested too deeply")
	}
	var d RuleDoc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, ruleErr("doc", "%s", err.Error())
	}
	if dec.More() {
		return nil, ruleErr("doc", "unexpected data after the document")
	}
	if d.Severity == "" {
		d.Severity = SeverityWarning
	}
	if d.NoData == "" {
		d.NoData = NoDataOK
	}
	if d.Channels == nil {
		d.Channels = []string{}
	}
	if d.GroupBy == nil {
		d.GroupBy = []string{}
	}
	if d.Condition.Type == ConditionThreshold {
		d.Condition.Type = ""
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// Validate checks the limits and that the query yields one numeric column per group.
func (d *RuleDoc) Validate() error {
	switch {
	case len(d.Description) > maxDescription:
		return ruleErr("description", "is too long")
	case time.Duration(d.Window) < MinWindow || time.Duration(d.Window) > MaxWindow:
		return ruleErr("window", "must be between %s and %s", MinWindow, MaxWindow)
	case time.Duration(d.Every) < MinEvery || time.Duration(d.Every) > MaxEvery:
		return ruleErr("every", "must be between %s and %s", MinEvery, MaxEvery)
	case d.For < 0 || time.Duration(d.For) > MaxFor:
		return ruleErr("for", "must be between 0s and %s", MaxFor)
	case !slices.Contains(severities, d.Severity):
		return ruleErr("severity", "must be info, warning or critical")
	case !slices.Contains(noDataOpts, d.NoData):
		return ruleErr("noData", "must be ok, keep or alerting")
	case len(d.Channels) > MaxChannels:
		return ruleErr("channels", "at most %d channels", MaxChannels)
	}
	for i, c := range d.Channels {
		if c == "" || slices.Contains(d.Channels[:i], c) {
			return ruleErr("channels", "ids must be non-empty and unique")
		}
	}
	if err := d.validateCondition(); err != nil {
		return err
	}
	epoch := time.Unix(0, 0).UTC()
	if _, err := d.Compile(epoch, "", nil, model.NoStreams()); err != nil {
		return err
	}
	if d.Condition.IsDeviation() {
		if _, err := d.CompileBaseline(epoch, "", nil, model.NoStreams()); err != nil {
			return err
		}
	}
	return nil
}

func (d *RuleDoc) validateCondition() error {
	c := d.Condition
	switch c.Type {
	case "":
		return validateThreshold(c)
	case ConditionDeviation:
		return d.validateDeviation()
	}
	return ruleErr("condition.type", "must be threshold or deviation")
}

func validateThreshold(c Condition) error {
	switch {
	case !slices.Contains(operators, c.Op):
		return ruleErr("condition.op", "must be one of %s", strings.Join(operators, " "))
	case !finite(c.Threshold):
		return ruleErr("condition.threshold", "must be a finite number")
	case c.Direction != "" || c.Sigma != 0 || c.Baseline != 0 || c.MinDelta != 0:
		return ruleErr("condition", "direction, sigma, baseline and minDelta need type deviation")
	}
	return nil
}

func (d *RuleDoc) validateDeviation() error {
	c, w := d.Condition, time.Duration(d.Window)
	switch {
	case c.Op != "" || c.Threshold != 0:
		return ruleErr("condition", "op and threshold are not used by type deviation")
	case !slices.Contains(directions, c.Direction):
		return ruleErr("condition.direction", "must be above, below or both")
	case !finite(c.Sigma) || c.Sigma <= 0 || c.Sigma > MaxSigma:
		return ruleErr("condition.sigma", "must be greater than 0 and at most %d", MaxSigma)
	case !finite(c.MinDelta) || c.MinDelta < 0:
		return ruleErr("condition.minDelta", "must be a finite number of at least 0")
	case w%time.Second != 0 || day%w != 0:
		return ruleErr("window", "must divide 24h for deviation rules (e.g. 1m, 5m, 15m, 1h, 6h, 24h)")
	}
	b := time.Duration(c.Baseline)
	if b <= 0 || b%w != 0 || b/w < MinBaselineBuckets || b/w > MaxBaselineBuckets || b > MaxBaseline {
		return ruleErr("condition.baseline", "must be a multiple of window giving %d to %d windows, at most %s",
			MinBaselineBuckets, MaxBaselineBuckets, MaxBaseline)
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Compile parses the query and compiles it for the window ending at now. The query must be
// a single stats stage without span whose by-fields equal groupBy and whose only aggregate
// is numeric (SPEC §14.1). streams is the stream scope the query reads (SPEC §15.10).
func (d *RuleDoc) Compile(now time.Time, database string, indexed map[model.Source]map[string]string,
	streams model.StreamScope) (*tql.Compiled, error) {
	if strings.TrimSpace(d.Query) == "" || len(d.Query) > maxQueryLen {
		return nil, ruleErr("query", "is required (at most 8 KiB)")
	}
	q, err := tql.Parse(d.Query)
	if err != nil {
		return nil, err
	}
	if err := d.checkShape(q); err != nil {
		return nil, err
	}
	c, err := tql.Compile(q, tql.Options{Database: database, From: now.Add(-time.Duration(d.Window)), To: now, Now: now,
		MaxRows: MaxGroups, IndexedFields: indexed, Streams: streams})
	if err != nil {
		return nil, err
	}
	if c.Kind != tql.KindTable || len(c.Columns) != len(d.GroupBy)+1 || c.Columns[len(c.Columns)-1].Type != tql.ColumnNumber {
		return nil, ruleErr("query", "must yield exactly one numeric column (optionally grouped by groupBy)")
	}
	return c, nil
}

func (d *RuleDoc) checkShape(q *tql.Query) error {
	var stats *tql.Stats
	for _, st := range q.Stages {
		switch s := st.(type) {
		case *tql.Stats:
			stats = s
		case *tql.Top:
			return ruleErr("query", "top is not allowed; use stats with one aggregate")
		case *tql.Where:
			if stats != nil && d.Condition.IsDeviation() {
				return ruleErr("query", "where after stats is not allowed in deviation rules (it would drop baseline windows)")
			}
		case *tql.Limit:
			if d.Condition.IsDeviation() {
				return ruleErr("query", "limit is not allowed in deviation rules (it would drop baseline windows)")
			}
		}
	}
	if stats == nil {
		return ruleErr("query", "needs a stats stage with one aggregate")
	}
	if len(stats.Aggs) != 1 || stats.Span != nil {
		return ruleErr("query", "stats must have exactly one aggregate and no span")
	}
	if len(stats.By) != len(d.GroupBy) {
		return ruleErr("groupBy", "must list the stats by-fields in order")
	}
	for i, f := range stats.By {
		if !strings.EqualFold(f.Name, d.GroupBy[i]) {
			return ruleErr("groupBy", "must list the stats by-fields in order")
		}
	}
	return nil
}
