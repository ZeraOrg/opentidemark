// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"strings"
)

// AnyValue is the variable value that removes the predicate using it.
const AnyValue = "*"

// Substitute replaces dashboard variables ($name) on the AST (SPEC §13.1).
// Values are injected as literals (a number when the text is numeric,
// otherwise a string — never re-parsed as TQL). A multi-value variable
// becomes a value group (field:(a or b)); `=` becomes an OR of comparisons
// and `!=` an AND; in-lists are expanded in place. A variable whose values
// are empty or contain "*" removes its predicate: the predicate becomes
// "match all", absorbing an enclosing `or` and dropping an enclosing `not`.
// Unknown variables are an error. q is not modified.
func Substitute(q *Query, vars map[string][]string) (*Query, error) {
	if q == nil {
		return nil, errAt(0, 0, "empty query")
	}
	s := &substituter{vars: vars}
	out := &Query{Source: q.Source, InterpretedAsText: q.InterpretedAsText}
	if q.Filter != nil {
		f, err := s.expr(q.Filter)
		if err != nil {
			return nil, err
		}
		out.Filter = f
	}
	for _, st := range q.Stages {
		w, ok := st.(*Where)
		if !ok {
			out.Stages = append(out.Stages, st)
			continue
		}
		e, err := s.expr(w.Expr)
		if err != nil {
			return nil, err
		}
		if e != nil {
			out.Stages = append(out.Stages, &Where{Position: w.Position, Expr: e})
		}
	}
	out.Text = out.String()
	return out, nil
}

type substituter struct{ vars map[string][]string }

// lookup returns the values of v; removed reports a match-all variable.
func (s *substituter) lookup(v Value) (vals []Value, removed bool, err error) {
	raw, ok := s.vars[v.Text]
	if !ok {
		return nil, false, nodeErr(v, "unknown variable $%s", v.Text)
	}
	if len(raw) == 0 {
		return nil, true, nil
	}
	if len(raw) > MaxValues {
		return nil, false, nodeErr(v, "variable $%s has too many values (max %d)", v.Text, MaxValues)
	}
	for _, r := range raw {
		if r == AnyValue {
			return nil, true, nil
		}
		vals = append(vals, literalValue(r, v.Position))
	}
	return vals, false, nil
}

// literalValue builds a literal from variable text: a plain number becomes a
// number, anything else (including "250ms") a string. Duration fields accept
// numeric strings with a unit (see durationLiteral).
func literalValue(text string, pos Position) Value {
	t := strings.TrimSpace(text)
	if n, unit, ok := parseNumber(t); ok && unit == "" {
		return Value{Position: pos, Kind: ValueNumber, Text: t, Num: n}
	}
	return Value{Position: pos, Kind: ValueString, Text: text}
}

// expr returns the substituted expression, or nil when it was removed.
func (s *substituter) expr(e Expr) (Expr, error) {
	switch n := e.(type) {
	case *And:
		return s.binary(n.Left, n.Right, n.Position, false)
	case *Or:
		return s.binary(n.Left, n.Right, n.Position, true)
	case *Not:
		x, err := s.expr(n.X)
		if x == nil || err != nil {
			return nil, err
		}
		return &Not{Position: n.Position, X: x}, nil
	case *FieldMatch:
		return s.fieldMatch(n)
	case *Compare:
		return s.compare(n)
	case *In:
		return s.in(n)
	case *Func:
		return s.function(n)
	}
	return e, nil
}

func (s *substituter) binary(l, r Expr, pos Position, isOr bool) (Expr, error) {
	left, err := s.expr(l)
	if err != nil {
		return nil, err
	}
	right, err := s.expr(r)
	if err != nil {
		return nil, err
	}
	switch {
	case isOr && (left == nil || right == nil):
		return nil, nil
	case left == nil:
		return right, nil
	case right == nil:
		return left, nil
	case isOr:
		return &Or{Position: pos, Left: left, Right: right}, nil
	}
	return &And{Position: pos, Left: left, Right: right}, nil
}

// fold joins expressions with or/and.
func fold(es []Expr, isOr bool, pos Position) Expr {
	out := es[0]
	for _, e := range es[1:] {
		if isOr {
			out = &Or{Position: pos, Left: out, Right: e}
		} else {
			out = &And{Position: pos, Left: out, Right: e}
		}
	}
	return out
}

func (s *substituter) fieldMatch(n *FieldMatch) (Expr, error) {
	if n.Value.Kind != ValueVar {
		return n, nil
	}
	vals, removed, err := s.lookup(n.Value)
	if removed || err != nil {
		return nil, err
	}
	if n.Op != "" && len(vals) > 1 {
		return nil, nodeErr(n.Value, "variable $%s has several values; a range needs one", n.Value.Text)
	}
	es := make([]Expr, len(vals))
	for i, v := range vals {
		es[i] = &FieldMatch{Position: n.Position, Field: n.Field, Op: n.Op, Value: v}
	}
	return fold(es, true, n.Position), nil
}

func (s *substituter) compare(n *Compare) (Expr, error) {
	if n.Value.Kind != ValueVar {
		return n, nil
	}
	vals, removed, err := s.lookup(n.Value)
	if removed || err != nil {
		return nil, err
	}
	op := normalizeOp(n.Op)
	if len(vals) > 1 && op != "=" && op != "!=" {
		return nil, nodeErr(n.Value, "variable $%s has several values; '%s' needs one", n.Value.Text, n.Op)
	}
	es := make([]Expr, len(vals))
	for i, v := range vals {
		es[i] = &Compare{Position: n.Position, Field: n.Field, Op: n.Op, Value: v, CI: n.CI}
	}
	return fold(es, op != "!=", n.Position), nil
}

func (s *substituter) in(n *In) (Expr, error) {
	var out []Value
	changed := false
	for _, v := range n.Values {
		if v.Kind != ValueVar {
			out = append(out, v)
			continue
		}
		vals, removed, err := s.lookup(v)
		if removed || err != nil {
			return nil, err
		}
		out, changed = append(out, vals...), true
	}
	if !changed {
		return n, nil
	}
	if len(out) > MaxValues {
		return nil, nodeErr(n, "too many values in list (max %d)", MaxValues)
	}
	return &In{Position: n.Position, Field: n.Field, Not: n.Not, Values: out}, nil
}

func (s *substituter) function(n *Func) (Expr, error) {
	if n.Arg == nil || n.Arg.Kind != ValueVar {
		return n, nil
	}
	vals, removed, err := s.lookup(*n.Arg)
	if removed || err != nil {
		return nil, err
	}
	es := make([]Expr, len(vals))
	for i := range vals {
		es[i] = &Func{Position: n.Position, Name: n.Name, Field: n.Field, Arg: &vals[i]}
	}
	return fold(es, true, n.Position), nil
}
