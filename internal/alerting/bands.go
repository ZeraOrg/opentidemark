// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// Band limits (SPEC §14.4): windows of one band query, groups drawn, and statistic work.
const (
	MaxBandWindows = tql.MaxBuckets
	MaxBandGroups  = 20
	maxBandWork    = 20_000_000
)

// ErrBandRange is returned when a chart range is too long for a rolling band.
var ErrBandRange = errors.New("range too long for the rule's baseline band")

// BandPoint is the normal range of one window: the mean and the bounds at which the rule's
// condition holds (Lower nil for direction above, Upper nil for below).
type BandPoint struct {
	T     time.Time
	Mean  float64
	Lower *float64
	Upper *float64
}

// GroupBand is the rolling band of one group; Values are the by-field values in by order.
type GroupBand struct {
	Labels map[string]string
	Values []string
	Points []BandPoint
}

// CanonicalQuery is the canonical text of q without the span of its stats stage, used to
// match a chart's query with rule queries (SPEC §14.4); ok is false when q does not parse.
func CanonicalQuery(q string) (string, bool) {
	parsed, err := tql.Parse(q)
	if err != nil {
		return "", false
	}
	if st := statsStage(parsed); st != nil {
		st.Span = nil
	}
	return parsed.String(), true
}

// Bands computes the rolling band of a deviation rule for every window starting in [from, to):
// the window's value is compared with the n windows before it, as the evaluator does at the
// end of that window (SPEC §14.1). The groups are those of the result in key order, at most
// MaxBandGroups.
func (e *Evaluator) Bands(ctx context.Context, doc *RuleDoc, streams model.StreamScope, from, to time.Time) ([]GroupBand, error) {
	if !doc.Condition.IsDeviation() {
		return nil, nil
	}
	if e.d.Querier == nil {
		return nil, ErrNoQuerier
	}
	w := time.Duration(doc.Window)
	n := int(time.Duration(doc.Condition.Baseline) / w)
	start := floorTime(from, w)
	end := floorTime(to.Add(w-time.Nanosecond), w)
	qFrom := start.Add(-time.Duration(doc.Condition.Baseline))
	total := int(end.Sub(qFrom) / w)
	if total > MaxBandWindows || !start.Before(end) {
		return nil, fmt.Errorf("%w: at most %d windows of %s including the baseline", ErrBandRange, MaxBandWindows,
			ShortDuration(w))
	}
	var indexed map[model.Source]map[string]string
	if e.d.Indexed != nil {
		cols, err := e.d.Indexed.Columns(ctx)
		if err != nil {
			return nil, fmt.Errorf("indexed fields: %w", err)
		}
		indexed = cols
	}
	q, err := tql.Parse(doc.Query)
	if err != nil {
		return nil, err
	}
	stats := statsStage(q)
	if stats == nil {
		return nil, ruleErr("query", "needs a stats stage with one aggregate")
	}
	stats.Span = &tql.SpanClause{Position: stats.Position, Dur: w}
	c, err := tql.Compile(q, tql.Options{Database: e.d.Database, From: qFrom, To: end, Now: end, MaxRows: MaxBaselineRows,
		IndexedFields: indexed, Streams: streams})
	if err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, evalTimeout)
	defer cancel()
	res, err := e.d.Querier.Run(qctx, c)
	if err != nil {
		return nil, fmt.Errorf("band query: %w", err)
	}
	return bandsFrom(res.Series, byColumns(c), doc, qFrom, total, n)
}

// byColumns are the by-columns of a bucketed stats result (bucket, by-columns, value).
func byColumns(c *tql.Compiled) []string {
	var names []string
	for _, col := range c.Columns[1:] {
		names = append(names, col.Name)
	}
	if len(names) > 0 {
		names = names[:len(names)-1]
	}
	return names
}

// bandsFrom computes the bands of windows n…total−1 from the series of total windows that
// start at qFrom.
func bandsFrom(series []store.Series, names []string, doc *RuleDoc, qFrom time.Time, total, n int) ([]GroupBand, error) {
	points := 0
	for _, s := range series {
		points += len(s.Points)
	}
	if points >= MaxBaselineRows {
		return nil, fmt.Errorf("%w (%d); use a shorter range or fewer groups", ErrBaselineRows, MaxBaselineRows)
	}
	w := time.Duration(doc.Window)
	zeroFill := doc.ZeroFills()
	type group struct {
		band   GroupBand
		values map[int]float64
		first  int
	}
	var groups []*group
	for _, s := range series {
		g := &group{band: GroupBand{Labels: map[string]string{}}, values: map[int]float64{}, first: total}
		for i, name := range names {
			v := labelString(s.Key[name])
			g.band.Values = append(g.band.Values, v)
			if i < len(doc.GroupBy) {
				g.band.Labels[doc.GroupBy[i]] = v
			}
		}
		for _, p := range s.Points {
			if len(p) < 2 {
				continue
			}
			idx, err := bucketIndex(p[0], qFrom, w, total)
			if err != nil {
				return nil, err
			}
			g.first = min(g.first, idx)
			if v := toFloat(p[len(p)-1]); v != nil {
				g.values[idx] = *v
			}
		}
		groups = append(groups, g)
	}
	slices.SortFunc(groups, func(a, b *group) int {
		return strings.Compare(strings.Join(a.band.Values, "\x00"), strings.Join(b.band.Values, "\x00"))
	})
	if len(groups) > MaxBandGroups {
		groups = groups[:MaxBandGroups]
	}
	if len(groups)*(total-n)*n > maxBandWork {
		return nil, fmt.Errorf("%w: use a shorter range or fewer groups", ErrBandRange)
	}
	c := doc.Condition
	out := make([]GroupBand, 0, len(groups))
	for _, g := range groups {
		vals := make([]float64, 0, n)
		for j := n; j < total; j++ {
			vals = vals[:0]
			if zeroFill {
				for i := max(g.first, j-n); i < j; i++ {
					vals = append(vals, g.values[i])
				}
			} else {
				for i := j - n; i < j; i++ {
					if v, ok := g.values[i]; ok {
						vals = append(vals, v)
					}
				}
			}
			st := statsOf(vals)
			if st == nil {
				continue
			}
			dist := max(c.Sigma*st.stdDev, c.MinDelta)
			p := BandPoint{T: qFrom.Add(time.Duration(j) * w), Mean: st.mean}
			if c.Direction != DirectionAbove {
				lo := st.mean - dist
				p.Lower = &lo
			}
			if c.Direction != DirectionBelow {
				hi := st.mean + dist
				p.Upper = &hi
			}
			g.band.Points = append(g.band.Points, p)
		}
		out = append(out, g.band)
	}
	return out, nil
}

// floorTime rounds t down to a multiple of d since the Unix epoch (UTC).
func floorTime(t time.Time, d time.Duration) time.Time {
	ns, w := t.UnixNano(), int64(d)
	return time.Unix(0, ns-((ns%w)+w)%w).UTC()
}
