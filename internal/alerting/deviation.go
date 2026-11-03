// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// ErrBaselineGrid is returned when a baseline bucket does not lie on the window grid.
var ErrBaselineGrid = errors.New("baseline bucket off the window grid")

// ErrBaselineRows is returned when the baseline query reaches MaxBaselineRows.
var ErrBaselineRows = errors.New("baseline has too many rows")

// Baseline describes a group's baseline (SPEC §14.1): mean and sample standard deviation of its
// baseline values, their number, and the value's z-score (nil when the deviation is 0).
type Baseline struct {
	Mean    float64  `json:"mean"`
	StdDev  float64  `json:"stdDev"`
	Samples int      `json:"samples"`
	Z       *float64 `json:"z,omitempty"`
}

// groupBaseline is the baseline of one group: nil Baseline when it has too few values.
type groupBaseline struct {
	labels map[string]string
	stats  *baselineStats
}

// baselineStats are the baseline statistics before a value is compared with them.
type baselineStats struct {
	mean, stdDev float64
	samples      int
}

// For returns the Baseline of v.
func (b *baselineStats) For(v float64) *Baseline {
	out := &Baseline{Mean: b.mean, StdDev: b.stdDev, Samples: b.samples}
	if b.stdDev > 0 {
		z := (v - b.mean) / b.stdDev
		out.Z = &z
	}
	return out
}

// BaselineRange returns the baseline range [from, to) at now: the n whole windows before
// now − window, aligned to multiples of window since the Unix epoch (UTC).
func (d *RuleDoc) BaselineRange(now time.Time) (from, to time.Time) {
	end := floorTime(now.Add(-time.Duration(d.Window)), time.Duration(d.Window))
	return end.Add(-time.Duration(d.Condition.Baseline)), end
}

// CompileBaseline compiles the baseline query of a deviation rule at now: the rule query with
// `span <window>` over BaselineRange(now), at most MaxBaselineRows rows.
func (d *RuleDoc) CompileBaseline(now time.Time, database string, indexed map[model.Source]map[string]string,
	streams model.StreamScope) (*tql.Compiled, error) {
	q, err := tql.Parse(d.Query)
	if err != nil {
		return nil, err
	}
	stats := statsStage(q)
	if stats == nil {
		return nil, ruleErr("query", "needs a stats stage with one aggregate")
	}
	stats.Span = &tql.SpanClause{Position: stats.Position, Dur: time.Duration(d.Window)}
	from, to := d.BaselineRange(now)
	return tql.Compile(q, tql.Options{Database: database, From: from, To: to, Now: now, MaxRows: MaxBaselineRows,
		IndexedFields: indexed, Streams: streams})
}

// ZeroFills reports whether a missing baseline bucket or current row means 0 for the rule's
// aggregate: a single count, count_distinct, sum or rate() call (SPEC §14.1).
func (d *RuleDoc) ZeroFills() bool {
	q, err := tql.Parse(d.Query)
	if err != nil {
		return false
	}
	stats := statsStage(q)
	if stats == nil || len(stats.Aggs) != 1 || stats.Aggs[0].Expr != nil {
		return false
	}
	a := stats.Aggs[0]
	switch a.Fn {
	case "count", "count_distinct", "sum":
		return true
	case "rate":
		return a.Field == nil
	}
	return false
}

func statsStage(q *tql.Query) *tql.Stats {
	for _, st := range q.Stages {
		if s, ok := st.(*tql.Stats); ok {
			return s
		}
	}
	return nil
}

// baselines turns the baseline series of [from, to) into per-group statistics. names are the
// result's by-columns in order (labelled with groupBy); every point is [epoch ms, value]. With
// zeroFill a missing bucket counts as 0 from the group's first bucket with a point; otherwise
// missing buckets and NULL/NaN values are skipped.
func baselines(series []store.Series, names, groupBy []string, from, to time.Time, window time.Duration,
	zeroFill bool) (map[string]*groupBaseline, error) {
	points := 0
	for _, s := range series {
		points += len(s.Points)
	}
	if points >= MaxBaselineRows {
		return nil, fmt.Errorf("%w (%d); use a larger window or fewer groups", ErrBaselineRows, MaxBaselineRows)
	}
	n := int(to.Sub(from) / window)
	out := make(map[string]*groupBaseline, len(series))
	for _, s := range series {
		labels := make(map[string]string, len(groupBy))
		for i, g := range groupBy {
			if i < len(names) {
				labels[g] = labelString(s.Key[names[i]])
			}
		}
		values, first := map[int]float64{}, n
		for _, p := range s.Points {
			if len(p) < 2 {
				continue
			}
			idx, err := bucketIndex(p[0], from, window, n)
			if err != nil {
				return nil, err
			}
			first = min(first, idx)
			if v := toFloat(p[len(p)-1]); v != nil {
				values[idx] = *v
			}
		}
		if first == n {
			continue // no points
		}
		var vals []float64
		if zeroFill {
			for i := first; i < n; i++ {
				vals = append(vals, values[i]) // a missing bucket reads as 0
			}
		} else {
			for i := range n {
				if v, ok := values[i]; ok {
					vals = append(vals, v)
				}
			}
		}
		out[GroupKey(labels)] = &groupBaseline{labels: labels, stats: statsOf(vals)}
	}
	return out, nil
}

// bucketIndex returns the window index of a bucket time (epoch ms or time.Time) in [from, from +
// n·window); a bucket off that grid is ErrBaselineGrid.
func bucketIndex(t any, from time.Time, window time.Duration, n int) (int, error) {
	var at time.Time
	switch x := t.(type) {
	case int64:
		at = time.UnixMilli(x).UTC()
	case time.Time:
		at = x.UTC()
	default:
		return 0, fmt.Errorf("%w: bucket %v is not a time", ErrBaselineGrid, t)
	}
	off := at.Sub(from)
	if off < 0 || off%window != 0 || int(off/window) >= n {
		return 0, fmt.Errorf("%w: %s (window %s from %s)", ErrBaselineGrid, at.Format(time.RFC3339), window,
			from.UTC().Format(time.RFC3339))
	}
	return int(off / window), nil
}

// statsOf returns the mean and sample standard deviation, or nil with too few values.
func statsOf(vals []float64) *baselineStats {
	if len(vals) < MinBaselineSamples {
		return nil
	}
	var sum float64
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	var sq float64
	for _, v := range vals {
		sq += (v - mean) * (v - mean)
	}
	return &baselineStats{mean: mean, stdDev: math.Sqrt(sq / float64(len(vals)-1)), samples: len(vals)}
}

// withBaselines attaches the baselines to the current samples. With zeroFill, groups of the
// baseline without a current row get the value 0 and an ungrouped sample without a row too.
// At most MaxGroups samples: current groups first, then baseline-only groups by key.
func withBaselines(current []Sample, base map[string]*groupBaseline, zeroFill bool) []Sample {
	zero := 0.0
	out := make([]Sample, 0, len(current))
	seen := map[string]bool{}
	for _, s := range current {
		seen[s.Key] = true
		if s.Value == nil && zeroFill && base[s.Key] != nil {
			v := zero
			s.Value = &v
		}
		if b := base[s.Key]; b != nil && b.stats != nil && s.Value != nil {
			s.Baseline = b.stats.For(*s.Value)
		}
		out = append(out, s)
	}
	if zeroFill {
		var extra []string
		for key := range base {
			if !seen[key] {
				extra = append(extra, key)
			}
		}
		slices.Sort(extra)
		for _, key := range extra {
			b := base[key]
			v := zero
			s := Sample{Key: key, Labels: b.labels, Value: &v}
			if b.stats != nil {
				s.Baseline = b.stats.For(v)
			}
			out = append(out, s)
		}
	}
	if len(out) > MaxGroups {
		out = out[:MaxGroups]
	}
	return out
}

// deviationMet reports whether v deviates from b in the condition's direction (SPEC §14.1).
func (c Condition) deviationMet(v float64, b *Baseline) bool {
	delta := v - b.Mean
	above := delta > 0 && delta > c.Sigma*b.StdDev && delta >= c.MinDelta
	below := delta < 0 && -delta > c.Sigma*b.StdDev && -delta >= c.MinDelta
	switch c.Direction {
	case DirectionAbove:
		return above
	case DirectionBelow:
		return below
	}
	return above || below
}

// Bound returns the deviation bound on v's side of the mean as a threshold condition
// (`> mean + d` or `< mean − d` with d = max(sigma·σ, minDelta)), for notifications; "" without
// a baseline.
func (c Condition) Bound(v *float64, b *Baseline) (op string, threshold float64) {
	if b == nil {
		return "", 0
	}
	dist := max(c.Sigma*b.StdDev, c.MinDelta)
	upper := c.Direction == DirectionAbove || (c.Direction == DirectionBoth && (v == nil || *v >= b.Mean))
	if upper {
		return ">", b.Mean + dist
	}
	return "<", b.Mean - dist
}

// deviationText describes the condition: "3σ below the 24h baseline".
func (c Condition) deviationText() string {
	side := map[string]string{DirectionAbove: "above", DirectionBelow: "below", DirectionBoth: "outside"}[c.Direction]
	text := fmt.Sprintf("%sσ %s the %s baseline", shortNumber(c.Sigma), side, ShortDuration(time.Duration(c.Baseline)))
	if c.MinDelta > 0 {
		text += ", by at least " + shortNumber(c.MinDelta)
	}
	return text
}

// describeDeviation is the transition message of a deviation rule with a baseline.
func describeDeviation(st State, v float64, b *Baseline, c Condition) string {
	where := "is at"
	switch {
	case b.Z != nil && *b.Z > 0:
		where = fmt.Sprintf("is %sσ above", shortNumber(math.Abs(*b.Z)))
	case b.Z != nil && *b.Z < 0:
		where = fmt.Sprintf("is %sσ below", shortNumber(math.Abs(*b.Z)))
	case v > b.Mean:
		where = "is above"
	case v < b.Mean:
		where = "is below"
	}
	return fmt.Sprintf("%s: value %s %s the %s baseline (%s)", st, FormatValue(v), where,
		ShortDuration(time.Duration(c.Baseline)), b.Summary())
}

// Summary is "mean 80, σ 16.6, 288 samples".
func (b *Baseline) Summary() string {
	return fmt.Sprintf("mean %s, σ %s, %d samples", shortNumber(b.Mean), shortNumber(b.StdDev), b.Samples)
}

// shortNumber renders a statistic with at most 4 significant digits.
func shortNumber(v float64) string { return strconv.FormatFloat(v, 'g', 4, 64) }

// ShortDuration renders a duration without zero units: 7d (whole days from 2d), 24h, 1h30m,
// 5m, 1m30s.
func ShortDuration(d time.Duration) string {
	if d >= 2*day && d%day == 0 {
		return strconv.FormatInt(int64(d/day), 10) + "d"
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
