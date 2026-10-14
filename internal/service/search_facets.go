// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// FacetValue is one value of a facet with its count.
type FacetValue struct {
	Value string
	Count uint64
}

// Facet lists the top values of a field (SPEC §11.3).
type Facet struct {
	Field      string
	Values     []FacetValue
	OtherCount uint64
	Sampled    bool
}

// FacetsRequest is the input of Facets.
type FacetsRequest struct {
	Q       string
	Range   Range
	Fields  []string
	Top     int
	Signals []string
	Select  Selection
}

// reIdentifier matches field names that need no backticks (SPEC §10.2).
var reIdentifier = regexp.MustCompile(`^[A-Za-z_@][A-Za-z0-9_.\-@]*$`)

// Facets returns the top values of each field among the rows matching the query. Above
// FacetSampleRows matching rows only the newest FacetSampleRows are counted (Sampled).
func (s *Search) Facets(ctx context.Context, p auth.Principal, req FacetsRequest) ([]Facet, error) {
	if err := req.Range.valid(); err != nil {
		return nil, err
	}
	if len(req.Fields) == 0 || len(req.Fields) > MaxFacetFields {
		return nil, invalid("fields", fmt.Sprintf("give 1 to %d fields", MaxFacetFields))
	}
	if req.Top == 0 {
		req.Top = DefaultFacetTop
	}
	if req.Top < 1 || req.Top > MaxFacetTop {
		return nil, invalid("top", fmt.Sprintf("must be between 1 and %d", MaxFacetTop))
	}
	pr, err := s.prepare(ctx, p, req.Q, "", req.Signals, req.Select)
	if err != nil {
		return nil, err
	}
	f, err := s.filter(pr, req.Range)
	if err != nil {
		return nil, err
	}
	sample, err := s.d.Store.Newest(ctx, f, FacetSampleRows)
	if err != nil {
		return nil, err
	}
	rng := req.Range
	if sample.Sampled && sample.Cutoff.After(rng.From) {
		rng.From = sample.Cutoff
	}
	out := make([]Facet, 0, len(req.Fields))
	for _, field := range req.Fields {
		fc, err := s.facet(ctx, pr, rng, field, req.Top)
		if err != nil {
			return nil, err
		}
		fc.Sampled = sample.Sampled
		out = append(out, fc)
	}
	return out, nil
}

// facet computes the top values of one field and the count of the remaining rows having it.
func (s *Search) facet(ctx context.Context, pr *prepared, rng Range, field string, top int) (Facet, error) {
	fd := tql.Field{Name: field, Quoted: !reIdentifier.MatchString(field)}
	topRes, err := s.runFacetQuery(ctx, pr, rng, fd, &tql.Top{N: top, Field: fd})
	if err != nil {
		return Facet{}, err
	}
	totalRes, err := s.runFacetQuery(ctx, pr, rng, fd, &tql.Stats{Aggs: []tql.Agg{{Fn: "count", Alias: "count"}}})
	if err != nil {
		return Facet{}, err
	}
	fc := Facet{Field: field, Values: []FacetValue{}}
	var sum uint64
	for _, row := range topRes.Rows {
		if len(row) < 2 {
			continue
		}
		n := toUint(row[1])
		sum += n
		fc.Values = append(fc.Values, FacetValue{Value: facetText(row[0]), Count: n})
	}
	if len(totalRes.Rows) == 1 && len(totalRes.Rows[0]) == 1 {
		if total := toUint(totalRes.Rows[0][0]); total > sum {
			fc.OtherCount = total - sum
		}
	}
	return fc, nil
}

// runFacetQuery runs `<filter> and field:* | <where stages> | stage` built on the AST.
func (s *Search) runFacetQuery(ctx context.Context, pr *prepared, rng Range, fd tql.Field, stage tql.Stage) (store.Result, error) {
	filter := pr.q.Filter
	if !isLevelField(fd.Name) {
		var exists tql.Expr = &tql.FieldMatch{Field: fd, Value: tql.Value{Kind: tql.ValueAny, Text: "*", Raw: "*"}}
		if filter != nil {
			exists = &tql.And{Left: filter, Right: exists}
		}
		filter = exists
	}
	q := &tql.Query{Source: pr.q.Source, Filter: filter, Stages: append(leadingWheres(pr.q.Stages), stage), Text: pr.q.Text}
	c, err := tql.Compile(q, s.options(pr, rng))
	if err != nil {
		return store.Result{}, err
	}
	return s.d.Store.Run(ctx, c)
}

// isLevelField reports whether name is the level built-in (always present, so it needs no
// existence filter).
func isLevelField(name string) bool {
	switch strings.ToLower(name) {
	case "level", "@level", "@l":
		return true
	}
	return false
}

// leadingWheres returns the where stages before the first non-where stage.
func leadingWheres(stages []tql.Stage) []tql.Stage {
	var out []tql.Stage
	for _, st := range stages {
		w, ok := st.(*tql.Where)
		if !ok {
			break
		}
		out = append(out, w)
	}
	return out
}

func toUint(v any) uint64 {
	switch x := v.(type) {
	case uint64:
		return x
	case int64:
		if x < 0 {
			return 0
		}
		return uint64(x)
	case float64:
		return uint64(max(x, 0))
	case string:
		n, _ := strconv.ParseUint(x, 10, 64) // counts beyond 2^53 arrive as text; invalid ⇒ 0
		return n
	}
	return 0
}

func facetText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return fmt.Sprint(v)
}

// reSourceName extracts the source of a leading source clause for autocomplete.
var reSourceName = regexp.MustCompile(`(?i)^\s*source\s+(logs|spans|metrics)\b`)

// Complete returns autocomplete suggestions (SPEC §10.7): the syntactic suggestions of
// tql.Complete with catalog fields, plus the top values of the field in value position, both
// from the streams p may see (narrowed by sel).
func (s *Search) Complete(ctx context.Context, p auth.Principal, q string, pos int, src model.Source,
	sel Selection) (tql.Completion, error) {
	if len(q) > tql.MaxQueryBytes {
		return tql.Completion{}, invalid("q", "query too long")
	}
	if m := reSourceName.FindStringSubmatch(q); m != nil {
		src = model.Source(strings.ToLower(m[1]))
	}
	if !src.Valid() {
		return tql.Completion{}, invalid("source", "must be logs, spans or metrics")
	}
	scope, err := s.scope(ctx, p, sel)
	if err != nil {
		return tql.Completion{}, err
	}
	now := s.d.Now()
	props, err := s.d.Store.Fields(ctx, scope, src, store.TimeRange{From: now.Add(-completeFieldsRange), To: now})
	if err != nil {
		return tql.Completion{}, err
	}
	fields := make([]tql.FieldInfo, 0, len(props))
	for _, f := range props {
		fields = append(fields, tql.FieldInfo{Name: f.Name, Numeric: f.Numeric, Hits: f.Hits})
	}
	comp := tql.Complete(q, pos, src, fields)
	if comp.Field == "" || isLevelField(comp.Field) {
		return comp, nil
	}
	values, err := s.completeValues(ctx, p, comp, q[comp.From:max(comp.From, min(pos, len(q)))], src, now, sel)
	if err != nil {
		return tql.Completion{}, err
	}
	comp.Suggestions = append(values, comp.Suggestions...)
	if len(comp.Suggestions) > tql.MaxSuggestions {
		comp.Suggestions = comp.Suggestions[:tql.MaxSuggestions]
	}
	return comp, nil
}

func (s *Search) completeValues(ctx context.Context, p auth.Principal, comp tql.Completion, typed string,
	src model.Source, now time.Time, sel Selection) ([]tql.Suggestion, error) {
	pr, err := s.prepare(ctx, p, "*", src, nil, sel)
	if err != nil {
		return nil, err
	}
	fc, err := s.facet(ctx, pr, Range{From: now.Add(-completeValuesRange), To: now}, comp.Field, completeValueCount)
	if err != nil {
		var qe *tql.Error
		if errors.As(err, &qe) {
			return nil, nil // the field cannot be faceted (e.g. @timestamp): no value suggestions
		}
		return nil, err
	}
	prefix := strings.ToLower(strings.TrimLeft(typed, `"'`))
	out := []tql.Suggestion{}
	for _, v := range fc.Values {
		if !strings.HasPrefix(strings.ToLower(v.Value), prefix) {
			continue
		}
		out = append(out, tql.Suggestion{Kind: tql.SuggestValue, Label: v.Value, InsertText: tqlValue(v.Value),
			Detail: strconv.FormatUint(v.Count, 10)})
	}
	return out, nil
}
