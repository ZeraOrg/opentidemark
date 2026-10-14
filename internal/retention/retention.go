// SPDX-License-Identifier: Apache-2.0

// Package retention builds ClickHouse TTL clauses from retention settings (SPEC §12.3).
// DDL cannot take parameters, so every stream name is validated against the stream regex
// before it is placed in a clause.
package retention

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Retention limits.
const (
	MinDays = 1
	MaxDays = 36500
)

// Default retention per source (SPEC §12.3).
const (
	DefaultLogsDays    = 30
	DefaultSpansDays   = 7
	DefaultMetricsDays = 30
)

// Table names and their time columns.
const (
	TableEvents  = "events"
	TableSpans   = "spans"
	TableMetrics = "metric_points"
)

// ErrInvalid is wrapped by every validation error.
var ErrInvalid = errors.New("invalid retention policy")

// Defaults are the retention days applied to streams without overrides.
type Defaults struct {
	LogsDays    int
	SpansDays   int
	MetricsDays int
}

// DefaultDefaults returns the built-in defaults.
func DefaultDefaults() Defaults {
	return Defaults{LogsDays: DefaultLogsDays, SpansDays: DefaultSpansDays, MetricsDays: DefaultMetricsDays}
}

// LevelRule shortens retention of some log levels in one stream.
type LevelRule struct {
	Levels []model.Level
	Days   int
}

// StreamPolicy holds per-stream overrides (nil = use the default).
type StreamPolicy struct {
	Name        string
	LogsDays    *int
	SpansDays   *int
	MetricsDays *int
	LevelRules  []LevelRule
}

// Policy is the complete retention configuration.
type Policy struct {
	Defaults Defaults
	Streams  []StreamPolicy
}

// Rule is the TTL expression for one table (the text after "MODIFY TTL").
type Rule struct {
	Table string
	TTL   string
}

// tableSpec describes how to build the rule of one table.
type tableSpec struct {
	table   string
	column  string
	defDays func(Defaults) int
	days    func(StreamPolicy) *int
	levels  bool
}

var tables = []tableSpec{
	{TableEvents, "timestamp", func(d Defaults) int { return d.LogsDays }, func(s StreamPolicy) *int { return s.LogsDays }, true},
	{TableSpans, "start_time", func(d Defaults) int { return d.SpansDays }, func(s StreamPolicy) *int { return s.SpansDays }, false},
	{TableMetrics, "timestamp", func(d Defaults) int { return d.MetricsDays }, func(s StreamPolicy) *int { return s.MetricsDays }, false},
}

// Build validates p and returns one TTL rule per table, in a deterministic order.
func Build(p Policy) ([]Rule, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	streams := slices.Clone(p.Streams)
	sort.Slice(streams, func(i, j int) bool { return streams[i].Name < streams[j].Name })
	rules := make([]Rule, 0, len(tables))
	for _, t := range tables {
		rules = append(rules, Rule{Table: t.table, TTL: buildTable(t, p.Defaults, streams)})
	}
	return rules, nil
}

func buildTable(t tableSpec, d Defaults, streams []StreamPolicy) string {
	var parts, overridden []string
	for _, s := range streams {
		if t.levels {
			for _, lr := range s.LevelRules {
				parts = append(parts, fmt.Sprintf("%s DELETE WHERE stream = %s AND level_num IN (%s)",
					expiry(t.column, lr.Days), quote(s.Name), levelList(lr.Levels)))
			}
		}
		if days := t.days(s); days != nil {
			parts = append(parts, fmt.Sprintf("%s DELETE WHERE stream = %s", expiry(t.column, *days), quote(s.Name)))
			overridden = append(overridden, quote(s.Name))
		}
	}
	def := expiry(t.column, t.defDays(d))
	if len(parts) == 0 {
		return def
	}
	if len(overridden) == 0 {
		return strings.Join(append(parts, def), ",\n    ")
	}
	parts = append(parts, fmt.Sprintf("%s DELETE WHERE stream NOT IN (%s)", def, strings.Join(overridden, ", ")))
	return strings.Join(parts, ",\n    ")
}

func expiry(column string, days int) string {
	return fmt.Sprintf("toDateTime(%s) + toIntervalDay(%d)", column, days)
}

func levelList(levels []model.Level) string {
	sorted := slices.Clone(levels)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	out := make([]string, len(sorted))
	for i, l := range sorted {
		out[i] = strconv.Itoa(int(l))
	}
	return strings.Join(out, ",")
}

// quote renders a validated stream name as a SQL string literal. Validation guarantees the
// name contains only [a-z0-9._-], so no escaping is needed.
func quote(name string) string { return "'" + name + "'" }

// Validate checks every value that ends up in DDL.
func Validate(p Policy) error {
	for _, d := range []int{p.Defaults.LogsDays, p.Defaults.SpansDays, p.Defaults.MetricsDays} {
		if err := checkDays(d); err != nil {
			return fmt.Errorf("%w: default: %w", ErrInvalid, err)
		}
	}
	seen := map[string]bool{}
	for _, s := range p.Streams {
		if err := validateStream(s); err != nil {
			return fmt.Errorf("%w: stream %q: %w", ErrInvalid, s.Name, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("%w: duplicate stream %q", ErrInvalid, s.Name)
		}
		seen[s.Name] = true
	}
	return nil
}

func validateStream(s StreamPolicy) error {
	if !model.ValidStreamName(s.Name) {
		return errors.New("invalid stream name")
	}
	for _, d := range []*int{s.LogsDays, s.SpansDays, s.MetricsDays} {
		if d != nil {
			if err := checkDays(*d); err != nil {
				return err
			}
		}
	}
	for _, lr := range s.LevelRules {
		if err := checkDays(lr.Days); err != nil {
			return fmt.Errorf("level rule: %w", err)
		}
		if len(lr.Levels) == 0 {
			return errors.New("level rule without levels")
		}
		for _, l := range lr.Levels {
			if !l.Valid() {
				return fmt.Errorf("invalid level %d", l)
			}
		}
	}
	return nil
}

func checkDays(d int) error {
	if d < MinDays || d > MaxDays {
		return fmt.Errorf("days must be between %d and %d, got %d", MinDays, MaxDays, d)
	}
	return nil
}
