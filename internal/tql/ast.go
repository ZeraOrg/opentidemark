// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Query is a parsed TQL query (the AST root). It is immutable after Parse;
// Substitute returns a new Query.
type Query struct {
	Source            model.Source // logs when the query has no source clause
	Filter            Expr         // nil = match all
	Stages            []Stage      // pipe stages in order
	InterpretedAsText bool         // the input failed to parse and was taken as free text (§10.6)
	Text              string       // original query text
}

// Position is the byte range [Pos, End) of a node in the query text.
type Position struct {
	Pos int
	End int
}

// Bounds returns the byte range of the node.
func (p Position) Bounds() (int, int) { return p.Pos, p.End }

// Node is any AST node.
type Node interface{ Bounds() (int, int) }

// Expr is a boolean filter expression.
type Expr interface {
	Node
	exprNode()
}

// Field is a field reference as written (resolution happens in Check).
type Field struct {
	Position
	Name   string // as written, without backticks
	Quoted bool   // written as `…`
}

// ValueKind classifies a literal value.
type ValueKind int

// Value kinds.
const (
	ValueString   ValueKind = iota // text
	ValueNumber                    // number, optionally with a duration Unit
	ValueBool                      // true / false
	ValueNull                      // null
	ValueRegex                     // /…/
	ValueWildcard                  // text containing an unescaped *
	ValueAny                       // lone * (exists)
	ValueVar                       // $name (dashboard variable)
)

// Value is a literal on the right of ':' or a comparison operator.
type Value struct {
	Position
	Kind    ValueKind
	Text    string  // string text, number text, regex body, "true"/"false", variable name (without $)
	Pattern string  // ILIKE pattern for ValueWildcard (* ⇒ %, ? ⇒ _, % _ \ escaped)
	Num     float64 // ValueNumber
	Unit    string  // duration unit for ValueNumber: ns us ms s m h d, or ""
	Raw     string  // source text ("" for synthesized values)
}

// And is a conjunction (explicit `and`/`&&` or adjacency).
type And struct {
	Position
	Left, Right Expr
}

// Or is a disjunction.
type Or struct {
	Position
	Left, Right Expr
}

// Not negates X.
type Not struct {
	Position
	X Expr
}

// MatchAll is `*` (or an expression removed by variable substitution).
type MatchAll struct{ Position }

// FieldMatch is KQL-style `field:value` or `field: >= value` (Op is "" or a range operator).
type FieldMatch struct {
	Position
	Field Field
	Op    string // "" (match), "<", "<=", ">", ">="
	Value Value
}

// Compare is a Seq-style comparison `field op literal [ci]`.
type Compare struct {
	Position
	Field Field
	Op    string // = == != <> < <= > >= like "not like"
	Value Value
	CI    bool
}

// In is `field [not] in [v1, v2, …]`.
type In struct {
	Position
	Field  Field
	Not    bool
	Values []Value
}

// Func is contains/startswith/endswith/has/match(field[, literal]).
type Func struct {
	Position
	Name  string // lower case
	Field Field
	Arg   *Value
}

// FreeText is a bare word or quoted phrase searched in the message text.
type FreeText struct {
	Position
	Text   string
	Quoted bool
}

func (*And) exprNode()        {}
func (*Or) exprNode()         {}
func (*Not) exprNode()        {}
func (*MatchAll) exprNode()   {}
func (*FieldMatch) exprNode() {}
func (*Compare) exprNode()    {}
func (*In) exprNode()         {}
func (*Func) exprNode()       {}
func (*FreeText) exprNode()   {}

// Stage is a pipe stage.
type Stage interface {
	Node
	stageNode()
}

// Stats is `stats agg, … [by f, …] [span d|auto]`.
type Stats struct {
	Position
	Aggs []Agg
	By   []Field
	Span *SpanClause
}

// Agg is one stats column: an aggregate call, or arithmetic between aggregates (Expr set; Fn,
// Field and Arg empty). Alias is always set after parsing (default fn, fn_field or fn_number,
// or for arithmetic its canonical text).
type Agg struct {
	Position
	Fn       string // lower case
	Field    *Field
	Arg      *Value   // number literal argument (ValueNumber) instead of a field, e.g. histogram_quantile(0.95)
	Expr     *AggExpr // arithmetic between aggregates, e.g. sum(sum) / sum(count); nil for a call
	Alias    string
	AliasPos Position
	Explicit bool // alias given with `as`
}

// AggExpr is a node of arithmetic between aggregates: a binary operation (Op set), an
// aggregate call (Call) or a number literal (Num).
type AggExpr struct {
	Position
	Op          string // "+", "-", "*" or "/" for a binary operation
	Left, Right *AggExpr
	Call        *Agg   // aggregate call; its Alias is unused
	Num         *Value // number literal (ValueNumber)
}

// Calls returns the aggregate calls of the column in written order: the column itself, or
// the calls of its arithmetic.
func (a *Agg) Calls() []*Agg {
	if a.Expr == nil {
		return []*Agg{a}
	}
	return a.Expr.calls(nil)
}

func (e *AggExpr) calls(out []*Agg) []*Agg {
	switch {
	case e.Call != nil:
		return append(out, e.Call)
	case e.Op != "":
		return e.Right.calls(e.Left.calls(out))
	}
	return out
}

// SpanClause is the stats time bucket: a duration or `auto`.
type SpanClause struct {
	Position
	Auto bool
	Dur  time.Duration
}

// Sort is `sort key [asc|desc], …`.
type Sort struct {
	Position
	Keys []SortKey
}

// SortKey is one sort key.
type SortKey struct {
	Position
	Name string
	Desc bool
}

// Limit is `limit n`.
type Limit struct {
	Position
	N int
}

// Fields is `fields f, …` (projection for event results).
type Fields struct {
	Position
	Fields []Field
}

// Where is `where expr`; after stats it filters aggregate rows (HAVING).
type Where struct {
	Position
	Expr Expr
}

// Top is `top n field [by field]`: stats count() by field, sorted by count
// descending, limited to n (per `by` value when given).
type Top struct {
	Position
	N     int
	Field Field
	By    *Field
}

func (*Stats) stageNode()  {}
func (*Sort) stageNode()   {}
func (*Limit) stageNode()  {}
func (*Fields) stageNode() {}
func (*Where) stageNode()  {}
func (*Top) stageNode()    {}
