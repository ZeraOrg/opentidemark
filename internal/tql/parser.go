// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Keyword and function names (SPEC §10.2, §10.3).
const (
	kwAnd    = "and"
	kwOr     = "or"
	kwNot    = "not"
	kwIn     = "in"
	kwLike   = "like"
	kwCI     = "ci"
	kwSource = "source"
	kwBy     = "by"
	kwAs     = "as"
	kwSpan   = "span"
	kwAuto   = "auto"
	kwAsc    = "asc"
	kwDesc   = "desc"
	kwTrue   = "true"
	kwFalse  = "false"
	kwNull   = "null"

	stageStats  = "stats"
	stageSort   = "sort"
	stageLimit  = "limit"
	stageFields = "fields"
	stageWhere  = "where"
	stageTop    = "top"

	fnContains   = "contains"
	fnStartsWith = "startswith"
	fnEndsWith   = "endswith"
	fnHas        = "has"
	fnMatch      = "match"

	maxIntLiteral = 1_000_000_000
)

func stageNames() []string {
	return []string{stageStats, stageSort, stageLimit, stageFields, stageWhere, stageTop}
}

func funcNames() []string {
	return []string{fnContains, fnStartsWith, fnEndsWith, fnHas, fnMatch}
}

// Histogram aggregates (SPEC §10.5): per-series deltas of histogram (and summary) points.
const (
	aggHistMean     = "hist_mean"
	aggHistQuantile = "histogram_quantile"
)

func aggNames() []string {
	return []string{"count", "count_distinct", "sum", "avg", "min", "max", "p50", "p75", "p90",
		"p95", "p99", "rate", "first", "last", aggHistMean, aggHistQuantile}
}

// isHistAgg reports whether fn is a histogram aggregate.
func isHistAgg(fn string) bool { return fn == aggHistMean || fn == aggHistQuantile }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Parse parses a TQL query. If parsing fails and the input contains no TQL
// operators, quotes or colons, the whole input is taken as one free-text
// phrase and Query.InterpretedAsText is set (SPEC §10.6). A query whose
// canonical form would exceed MaxQueryBytes is rejected too, so that every
// accepted query round-trips through String. Errors are *Error.
func Parse(q string) (*Query, error) {
	if len(q) > MaxQueryBytes {
		return nil, errAt(MaxQueryBytes, len(q)-MaxQueryBytes, "query is too long (max %d bytes)", MaxQueryBytes)
	}
	if !utf8.ValidString(q) {
		return nil, errAt(invalidUTF8Pos(q), 1, "query is not valid UTF-8")
	}
	query, err := parseOrText(q)
	if err != nil {
		return nil, err
	}
	if n := len(query.String()); n > MaxQueryBytes {
		return nil, errAt(0, len(q), "query is too long in canonical form (%d bytes, max %d)", n, MaxQueryBytes)
	}
	return query, nil
}

// parseOrText parses q, falling back to one free-text phrase (see Parse).
func parseOrText(q string) (*Query, error) {
	query, err := newParser(q).parseQuery()
	if err == nil {
		return query, nil
	}
	if text := strings.TrimSpace(q); text != "" && !hasOperators(q) {
		start := strings.Index(q, text)
		return &Query{
			Source:            model.SourceLogs,
			Filter:            &FreeText{Position: Position{start, start + len(text)}, Text: text},
			InterpretedAsText: true,
			Text:              q,
		}, nil
	}
	return nil, err
}

func invalidUTF8Pos(q string) int {
	for i := 0; i < len(q); {
		r, n := utf8.DecodeRuneInString(q[i:])
		if r == utf8.RuneError && n <= 1 {
			return i
		}
		i += n
	}
	return 0
}

// hasOperators reports whether q contains TQL syntax that rules out the
// free-text fallback.
// A '!' counts only when used as an operator (before '=' or at the start of a
// word), so "Hello, world!" still falls back to free text.
func hasOperators(q string) bool {
	if strings.ContainsAny(q, ":=<>|()[]'\"`") || strings.Contains(q, "&&") {
		return true
	}
	for i := 0; i < len(q); i++ {
		if q[i] != '!' {
			continue
		}
		atWordStart := i == 0 || q[i-1] == ' ' || q[i-1] == '\t' || q[i-1] == '\n'
		if atWordStart && i+1 < len(q) && q[i+1] != ' ' {
			return true
		}
	}
	return false
}

type parser struct {
	src     string
	lx      lexer
	cur     token
	prevEnd int // end of the previously consumed token
	depth   int
}

func newParser(src string) *parser {
	p := &parser{src: src, lx: lexer{src: src}}
	p.cur = p.lx.next(0)
	return p
}

func (p *parser) advance() {
	p.prevEnd = p.cur.End
	p.cur = p.lx.next(p.cur.End)
}

func (p *parser) peek() token { return p.lx.next(p.cur.End) }

func (p *parser) resetAt(at int) {
	p.prevEnd = at
	p.cur = p.lx.next(at)
}

func isKw(t token, kw string) bool { return t.Kind == tIdent && strings.EqualFold(t.Text, kw) }

func describe(t token) string {
	switch t.Kind {
	case tEOF:
		return "end of query"
	case tString:
		return "string"
	default:
		return "'" + t.Text + "'"
	}
}

func (p *parser) unexpected(what string) *Error {
	if p.cur.Kind == tError {
		return errTok(p.cur, "%s", p.cur.Text)
	}
	return errTok(p.cur, "expected %s, found %s", what, describe(p.cur))
}

func (p *parser) parseQuery() (*Query, error) {
	q := &Query{Source: model.SourceLogs, Text: p.src}
	if err := p.parseSource(q); err != nil {
		return nil, err
	}
	filter, err := p.parseFilter()
	if err != nil {
		return nil, err
	}
	q.Filter = filter
	for p.cur.Kind == tPipe {
		pipe := p.cur
		p.advance()
		if len(q.Stages) == MaxStages {
			return nil, errTok(pipe, "too many stages (max %d)", MaxStages)
		}
		st, err := p.parseStage()
		if err != nil {
			return nil, err
		}
		q.Stages = append(q.Stages, st)
	}
	if p.cur.Kind != tEOF {
		return nil, p.unexpected("'|' or end of query")
	}
	return q, nil
}

func (p *parser) parseSource(q *Query) error {
	if !isKw(p.cur, kwSource) {
		return nil
	}
	name := p.peek()
	if name.Kind != tIdent {
		return nil
	}
	after := p.lx.next(name.End)
	if after.Kind != tPipe && after.Kind != tEOF {
		return nil
	}
	src, ok := parseSourceName(name.Text)
	if !ok {
		e := errTok(name, "unknown source '%s'", name.Text)
		return e.withHint(didYouMean(name.Text, sourceNames()))
	}
	q.Source = src
	p.cur = after
	if after.Kind == tPipe {
		p.advance()
	}
	return nil
}

func (p *parser) parseFilter() (Expr, error) {
	if p.cur.Kind == tPipe || p.cur.Kind == tEOF {
		return nil, nil
	}
	if p.cur.Kind == tStar {
		if n := p.peek(); n.Kind == tPipe || n.Kind == tEOF {
			p.advance()
			return nil, nil
		}
	}
	return p.parseOr()
}

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for isKw(p.cur, kwOr) || p.cur.Kind == tOrOr {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &Or{Position: span2(left, right), Left: left, Right: right}
	}
	return left, nil
}

func span2(a, b Node) Position {
	ap, _ := a.Bounds()
	_, be := b.Bounds()
	return Position{ap, be}
}

func (p *parser) startsPrimary() bool {
	switch p.cur.Kind {
	case tLParen, tBang, tWord, tNumber, tString, tBacktick, tStar, tError:
		return true
	case tIdent:
		return !isKw(p.cur, kwOr) && !isKw(p.cur, kwAnd)
	}
	return false
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		if isKw(p.cur, kwAnd) || p.cur.Kind == tAndAnd {
			p.advance()
		} else if !p.startsPrimary() {
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		left = &And{Position: span2(left, right), Left: left, Right: right}
	}
}

func (p *parser) enter() error {
	p.depth++
	if p.depth > MaxDepth {
		return errTok(p.cur, "expression is nested too deeply (max %d)", MaxDepth)
	}
	return nil
}

func (p *parser) parseUnary() (Expr, error) {
	if !isKw(p.cur, kwNot) && p.cur.Kind != tBang {
		return p.parsePrimary()
	}
	start := p.cur.Pos
	if err := p.enter(); err != nil {
		return nil, err
	}
	p.advance()
	x, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	p.depth--
	_, end := x.Bounds()
	return &Not{Position: Position{start, end}, X: x}, nil
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.cur
	switch t.Kind {
	case tLParen:
		return p.parseParen()
	case tStar:
		p.advance()
		return &MatchAll{Position: Position{t.Pos, t.End}}, nil
	case tString:
		p.advance()
		return &FreeText{Position: Position{t.Pos, t.End}, Text: t.Text, Quoted: true}, nil
	case tNumber, tWord:
		if n := p.peek(); n.Kind == tColon || n.Kind == tOp {
			return nil, errTok(t, "invalid field name '%s'", t.Text).withHint("quote unusual field names with backticks, e.g. `my key`")
		}
		p.advance()
		return &FreeText{Position: Position{t.Pos, t.End}, Text: t.Text}, nil
	case tIdent, tBacktick:
		if t.Kind == tIdent && (isKw(t, kwOr) || isKw(t, kwAnd)) {
			return nil, p.unexpected("an expression")
		}
		return p.parseFieldExpr()
	}
	return nil, p.unexpected("an expression")
}

func (p *parser) parseParen() (Expr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	p.advance()
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.cur.Kind != tRParen {
		return nil, p.unexpected("')'")
	}
	p.depth--
	p.advance()
	return e, nil
}

func fieldOf(t token) Field {
	return Field{Position: Position{t.Pos, t.End}, Name: t.Text, Quoted: t.Kind == tBacktick}
}

func (p *parser) parseFieldExpr() (Expr, error) {
	t := p.cur
	f := fieldOf(t)
	n := p.peek()
	if t.Kind == tIdent && n.Kind == tLParen && contains(funcNames(), strings.ToLower(t.Text)) {
		return p.parseFunc()
	}
	switch {
	case n.Kind == tColon:
		return p.parseFieldMatch(f, n)
	case n.Kind == tOp:
		p.cur = n
		return p.parseCompare(f, n.Text)
	case isKw(n, kwLike):
		p.cur = n
		return p.parseCompare(f, kwLike)
	case isKw(n, kwIn):
		p.cur = n
		return p.parseIn(f, false)
	case isKw(n, kwNot):
		n2 := p.lx.next(n.End)
		if isKw(n2, kwIn) {
			p.cur = n2
			return p.parseIn(f, true)
		}
		if isKw(n2, kwLike) {
			p.cur = n2
			return p.parseCompare(f, "not like")
		}
	}
	if t.Kind == tBacktick {
		return nil, errTok(n, "expected ':' or an operator after field %s, found %s", t.Text, describe(n))
	}
	p.advance()
	return &FreeText{Position: Position{t.Pos, t.End}, Text: t.Text}, nil
}

// valueOf converts a value-mode token into a Value.
func (p *parser) valueOf(t token) (Value, error) {
	v := Value{Position: Position{t.Pos, t.End}, Text: t.Text, Raw: p.src[t.Pos:t.End]}
	switch t.Kind {
	case tString:
		v.Kind = ValueString
	case tRegex:
		v.Kind = ValueRegex
	case tNumber:
		v.Kind, v.Num, v.Unit = ValueNumber, t.Num, t.Unit
	case tBare:
		classifyBare(&v)
	case tError:
		return v, errTok(t, "%s", t.Text)
	default:
		return v, errTok(t, "expected a value, found %s", describe(t))
	}
	if t.Wild {
		v.Kind, v.Pattern = ValueWildcard, t.Pattern
		if v.Raw == "*" {
			v.Kind, v.Pattern = ValueAny, ""
		}
	}
	return v, nil
}

func classifyBare(v *Value) {
	low := strings.ToLower(v.Text)
	switch {
	case v.Raw == v.Text && reVar.MatchString(v.Text):
		v.Kind, v.Text = ValueVar, v.Text[1:]
	case low == kwTrue || low == kwFalse:
		v.Kind, v.Text = ValueBool, low
	case low == kwNull:
		v.Kind = ValueNull
	default:
		v.Kind = ValueString
	}
}

func (p *parser) parseFieldMatch(f Field, colon token) (Expr, error) {
	t := p.lx.nextValue(colon.End)
	switch t.Kind {
	case tLParen:
		return p.parseValueGroup(f, t)
	case tEOF, tPipe, tRParen:
		return nil, errAt(t.Pos, t.End-t.Pos, "expected a value after ':'").withHint("use field:* to test that a field exists")
	}
	op := ""
	if t.Kind == tOp {
		op = t.Text
		t = p.lx.nextValue(t.End)
	}
	v, err := p.valueOf(t)
	if err != nil {
		return nil, err
	}
	if op != "" && v.Kind != ValueString && v.Kind != ValueNumber && v.Kind != ValueVar {
		return nil, errAt(v.Pos, v.End-v.Pos, "a range comparison needs a single number or string")
	}
	p.resetAt(t.End)
	return &FieldMatch{Position: Position{f.Pos, t.End}, Field: f, Op: op, Value: v}, nil
}

// isGroupOp reports whether t is a literal (unescaped, unquoted) `or`/`and`
// inside a value group.
func (p *parser) isGroupOp(t token) (string, bool) {
	if t.Kind != tBare || p.src[t.Pos:t.End] != t.Text {
		return "", false
	}
	low := strings.ToLower(t.Text)
	return low, low == kwOr || low == kwAnd
}

// parseValueGroup parses `(v1 or v2 and v3)` after `field:` and desugars it
// into an expression of FieldMatch nodes (and binds tighter than or).
func (p *parser) parseValueGroup(f Field, lp token) (Expr, error) {
	var ors []Expr
	var cur Expr
	pos := lp.End
	for {
		t := p.lx.nextValue(pos)
		if _, isOp := p.isGroupOp(t); isOp || t.Kind == tLParen || t.Kind == tOp {
			return nil, errTok(t, "expected a value, found %s", describe(t))
		}
		v, err := p.valueOf(t)
		if err != nil {
			return nil, err
		}
		if len(ors)+1 > MaxValues {
			return nil, errTok(t, "too many values in group (max %d)", MaxValues)
		}
		m := &FieldMatch{Position: v.Position, Field: f, Value: v}
		cur = joinAnd(cur, m)
		next := p.lx.nextValue(t.End)
		op, isOp := p.isGroupOp(next)
		switch {
		case next.Kind == tRParen:
			ors = append(ors, cur)
			p.resetAt(next.End)
			return groupExpr(ors, Position{f.Pos, next.End}), nil
		case !isOp:
			return nil, errTok(next, "expected 'or', 'and' or ')' in value group, found %s", describe(next))
		case op == kwOr:
			ors = append(ors, cur)
			cur = nil
		}
		pos = next.End
	}
}

func joinAnd(a Expr, b Expr) Expr {
	if a == nil {
		return b
	}
	return &And{Position: span2(a, b), Left: a, Right: b}
}

func groupExpr(ors []Expr, whole Position) Expr {
	e := ors[0]
	for _, x := range ors[1:] {
		e = &Or{Position: span2(e, x), Left: e, Right: x}
	}
	switch n := e.(type) {
	case *Or:
		n.Position = whole
	case *And:
		n.Position = whole
	}
	return e
}

// literal parses a normal-mode literal (right of a comparison, in lists and
// function arguments) at p.cur and advances past it.
func (p *parser) literal() (Value, error) {
	t := p.cur
	v := Value{Position: Position{t.Pos, t.End}, Text: t.Text, Raw: p.src[t.Pos:t.End]}
	switch t.Kind {
	case tString:
		v.Kind = ValueString
	case tNumber:
		v.Kind, v.Num, v.Unit = ValueNumber, t.Num, t.Unit
	case tIdent, tWord:
		classifyBare(&v)
	default:
		return v, p.unexpected("a value")
	}
	p.advance()
	return v, nil
}

func (p *parser) parseCompare(f Field, op string) (Expr, error) {
	p.advance()
	v, err := p.literal()
	if err != nil {
		return nil, err
	}
	c := &Compare{Position: Position{f.Pos, v.End}, Field: f, Op: op, Value: v}
	if isKw(p.cur, kwCI) {
		c.CI, c.End = true, p.cur.End
		p.advance()
	}
	return c, nil
}

func (p *parser) parseIn(f Field, not bool) (Expr, error) {
	p.advance()
	if p.cur.Kind != tLBrack {
		return nil, p.unexpected("'['")
	}
	p.advance()
	var vals []Value
	for {
		if len(vals) == MaxValues {
			return nil, errTok(p.cur, "too many values in list (max %d)", MaxValues)
		}
		v, err := p.literal()
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
		if p.cur.Kind == tRBrack {
			end := p.cur.End
			p.advance()
			return &In{Position: Position{f.Pos, end}, Field: f, Not: not, Values: vals}, nil
		}
		if p.cur.Kind != tComma {
			return nil, p.unexpected("',' or ']'")
		}
		p.advance()
	}
}

func (p *parser) parseFunc() (Expr, error) {
	name := strings.ToLower(p.cur.Text)
	start := p.cur.Pos
	p.advance() // (
	p.advance()
	if p.cur.Kind != tIdent && p.cur.Kind != tBacktick {
		return nil, p.unexpected("a field name")
	}
	fn := &Func{Name: name, Field: fieldOf(p.cur)}
	p.advance()
	if p.cur.Kind == tComma {
		p.advance()
		v, err := p.literal()
		if err != nil {
			return nil, err
		}
		fn.Arg = &v
	}
	if p.cur.Kind != tRParen {
		return nil, p.unexpected("')'")
	}
	fn.Position = Position{start, p.cur.End}
	if name == fnHas && fn.Arg != nil {
		return nil, errAt(start, fn.End-start, "has() takes only a field")
	}
	if name != fnHas && fn.Arg == nil {
		return nil, errAt(start, fn.End-start, "%s() needs a field and a value", name).withHint(name + "(field, 'text')")
	}
	p.advance()
	return fn, nil
}

func (p *parser) parseStage() (Stage, error) {
	t := p.cur
	if t.Kind != tIdent {
		return nil, p.unexpected("a stage (stats, sort, limit, fields, where, top)")
	}
	switch strings.ToLower(t.Text) {
	case stageStats:
		return p.parseStats()
	case stageSort:
		return p.parseSort()
	case stageLimit:
		p.advance()
		n, err := p.integer()
		if err != nil {
			return nil, err
		}
		return &Limit{Position: Position{t.Pos, p.prevEnd}, N: n}, nil
	case stageFields:
		return p.parseFields()
	case stageWhere:
		p.advance()
		p.depth = 0
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		_, end := e.Bounds()
		return &Where{Position: Position{t.Pos, end}, Expr: e}, nil
	case stageTop:
		return p.parseTop()
	}
	return nil, errTok(t, "unknown stage '%s'", t.Text).withHint(didYouMean(t.Text, stageNames()))
}

func (p *parser) integer() (int, error) {
	t := p.cur
	if t.Kind != tNumber || t.Unit != "" || t.Num != math.Trunc(t.Num) || t.Num < 1 || t.Num > maxIntLiteral {
		return 0, errTok(t, "expected a positive integer, found %s", describe(t))
	}
	p.advance()
	return int(t.Num), nil
}

func (p *parser) field() (Field, error) {
	if p.cur.Kind != tIdent && p.cur.Kind != tBacktick {
		return Field{}, p.unexpected("a field name")
	}
	f := fieldOf(p.cur)
	p.advance()
	return f, nil
}

func (p *parser) fieldList(limit int, what string) ([]Field, error) {
	var out []Field
	for {
		if len(out) == limit {
			return nil, errTok(p.cur, "too many %s (max %d)", what, limit)
		}
		f, err := p.field()
		if err != nil {
			return nil, err
		}
		out = append(out, f)
		if p.cur.Kind != tComma {
			return out, nil
		}
		p.advance()
	}
}

func (p *parser) parseStats() (Stage, error) {
	st := &Stats{Position: Position{Pos: p.cur.Pos}}
	p.advance()
	var bud aggBudget
	for {
		a, err := p.parseAgg(&bud)
		if err != nil {
			return nil, err
		}
		st.Aggs = append(st.Aggs, a)
		if p.cur.Kind != tComma {
			break
		}
		p.advance()
	}
	if isKw(p.cur, kwBy) {
		p.advance()
		by, err := p.fieldList(MaxByFields, "'by' fields")
		if err != nil {
			return nil, err
		}
		st.By = by
	}
	if isKw(p.cur, kwSpan) {
		sc, err := p.parseSpan()
		if err != nil {
			return nil, err
		}
		st.Span = sc
	}
	st.End = p.prevEnd
	return st, nil
}

// aggBudget counts the aggregates and terms of a stats stage against MaxAggregates and
// MaxAggTerms.
type aggBudget struct{ calls, terms int }

// parseAgg parses one stats column: an aggregate call or arithmetic between aggregates and
// numbers, optionally named with `as`.
func (p *parser) parseAgg(bud *aggBudget) (Agg, error) {
	start, calls := p.cur, bud.calls
	e, err := p.parseAggSum(bud)
	if err != nil {
		return Agg{}, err
	}
	var a Agg
	switch {
	case e.Call != nil:
		a = *e.Call
	case bud.calls == calls:
		return Agg{}, errAt(e.Pos, e.End-e.Pos, "arithmetic needs an aggregate").withHint("e.g. sum(x) / 2")
	default:
		a = Agg{Position: e.Position, Expr: e, Alias: aggExprText(e)}
	}
	if a.Expr != nil || a.Pos != start.Pos {
		a.Position = Position{start.Pos, p.prevEnd} // a parenthesized column spans its parentheses
	}
	a.AliasPos = a.Position
	if isKw(p.cur, kwAs) {
		p.advance()
		if p.cur.Kind != tIdent && p.cur.Kind != tBacktick {
			return a, p.unexpected("an alias name")
		}
		a.Alias, a.Explicit = p.cur.Text, true
		a.AliasPos = Position{p.cur.Pos, p.cur.End}
		a.End = p.cur.End
		p.advance()
	}
	return a, nil
}

// arithOp returns the arithmetic operator after the previous token, if any. The lexer reads
// + - * / as parts of words, so the operator is taken from the source and the parser
// continues lexing after it.
func (p *parser) arithOp(ops string) (string, bool) {
	pos := p.lx.skipSpace(p.prevEnd)
	if pos < len(p.src) && strings.IndexByte(ops, p.src[pos]) >= 0 {
		return p.src[pos : pos+1], true
	}
	return "", false
}

// parseAggSum parses agg_term { ("+" | "-") agg_term }.
func (p *parser) parseAggSum(bud *aggBudget) (*AggExpr, error) {
	return p.parseAggBinary(bud, "+-", p.parseAggProduct)
}

// parseAggProduct parses agg_atom { ("*" | "/") agg_atom }.
func (p *parser) parseAggProduct(bud *aggBudget) (*AggExpr, error) {
	return p.parseAggBinary(bud, "*/", p.parseAggAtom)
}

func (p *parser) parseAggBinary(bud *aggBudget, ops string, operand func(*aggBudget) (*AggExpr, error)) (*AggExpr, error) {
	left, err := operand(bud)
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.arithOp(ops)
		if !ok {
			return left, nil
		}
		p.resetAt(p.lx.skipSpace(p.prevEnd) + 1)
		right, err := operand(bud)
		if err != nil {
			return nil, err
		}
		left = &AggExpr{Position: Position{left.Pos, right.End}, Op: op, Left: left, Right: right}
	}
}

// parseAggAtom parses an aggregate call, a number or a parenthesized agg_expr.
func (p *parser) parseAggAtom(bud *aggBudget) (*AggExpr, error) {
	if p.cur.Kind == tLParen {
		return p.parseAggParen(bud)
	}
	if bud.terms == MaxAggTerms {
		return nil, errTok(p.cur, "too many terms in stats (max %d aggregates and numbers)", MaxAggTerms)
	}
	bud.terms++
	if v, ok := p.aggNumber(); ok {
		return &AggExpr{Position: v.Position, Num: &v}, nil
	}
	if bud.calls == MaxAggregates {
		return nil, errTok(p.cur, "too many aggregates (max %d)", MaxAggregates)
	}
	bud.calls++
	a, err := p.parseAggCall()
	if err != nil {
		return nil, err
	}
	return &AggExpr{Position: a.Position, Call: &a}, nil
}

func (p *parser) parseAggParen(bud *aggBudget) (*AggExpr, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	pos := p.cur.Pos
	p.advance()
	e, err := p.parseAggSum(bud)
	if err != nil {
		return nil, err
	}
	if p.cur.Kind != tRParen {
		return nil, p.unexpected("')'")
	}
	p.depth--
	p.advance()
	if e.Op != "" {
		e.Position = Position{pos, p.prevEnd}
	}
	return e, nil
}

// reNumberPrefix matches a number with optional unit at the start of a word, which in
// arithmetic may run into an operator (2*sum(x)).
var reNumberPrefix = regexp.MustCompile(`^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?(?:ns|us|ms|s|m|h|d)?`)

// aggNumber parses a number operand at the current token, cutting it before an operator.
func (p *parser) aggNumber() (Value, bool) {
	if p.cur.Kind != tNumber && p.cur.Kind != tWord {
		return Value{}, false
	}
	pos := p.cur.Pos
	n := len(reNumberPrefix.FindString(p.src[pos:p.cur.End]))
	if n == 0 || (pos+n < p.cur.End && strings.IndexByte("+-*/", p.src[pos+n]) < 0) {
		return Value{}, false
	}
	raw := p.src[pos : pos+n]
	num, unit, ok := parseNumber(raw)
	if !ok {
		return Value{}, false
	}
	p.resetAt(pos + n)
	return Value{Position: Position{pos, pos + n}, Kind: ValueNumber, Text: raw, Num: num, Unit: unit, Raw: raw}, true
}

// parseAggCall parses fn([field | number]) with its default alias.
func (p *parser) parseAggCall() (Agg, error) {
	t := p.cur
	name := strings.ToLower(t.Text)
	if t.Kind != tIdent || !contains(aggNames(), name) {
		e := errTok(t, "unknown aggregate %s", describe(t))
		return Agg{}, e.withHint(didYouMean(t.Text, aggNames()))
	}
	a := Agg{Position: Position{Pos: t.Pos}, Fn: name, Alias: name}
	p.advance()
	if p.cur.Kind != tLParen {
		return a, p.unexpected("'('")
	}
	p.advance()
	switch p.cur.Kind {
	case tIdent, tBacktick:
		f := fieldOf(p.cur)
		a.Field = &f
		a.Alias = name + "_" + f.Name
		p.advance()
	case tNumber:
		t := p.cur
		a.Arg = &Value{Position: Position{t.Pos, t.End}, Kind: ValueNumber, Text: t.Text, Num: t.Num, Unit: t.Unit,
			Raw: p.src[t.Pos:t.End]}
		a.Alias = numberAlias(name, t.Num)
		p.advance()
	}
	if p.cur.Kind != tRParen {
		return a, p.unexpected("')'")
	}
	a.End = p.cur.End
	p.advance()
	return a, nil
}

// numberAlias is the default alias of an aggregate with a number argument: fn_ and the number
// in shortest decimal form with '.' replaced by '_' (histogram_quantile_0_95), a plain
// identifier because '.' would read as a field path.
func numberAlias(fn string, num float64) string {
	return fn + "_" + strings.ReplaceAll(strconv.FormatFloat(num, 'f', -1, 64), ".", "_")
}

func unitDuration(unit string) time.Duration {
	switch unit {
	case "ns":
		return time.Nanosecond
	case "us":
		return time.Microsecond
	case "ms":
		return time.Millisecond
	case "s":
		return time.Second
	case "m":
		return time.Minute
	case "h":
		return time.Hour
	case "d":
		return 24 * time.Hour
	}
	return 0
}

// maxDurationValue bounds duration literals so conversion cannot overflow.
const maxDurationValue = float64(100 * 365 * 24 * time.Hour)

func toDuration(num float64, unit string) (time.Duration, bool) {
	d := num * float64(unitDuration(unit))
	if unit == "" || math.IsNaN(d) || d < 0 || d > maxDurationValue {
		return 0, false
	}
	return time.Duration(d), true
}

func (p *parser) parseSpan() (*SpanClause, error) {
	start := p.cur.Pos
	p.advance()
	t := p.cur
	if isKw(t, kwAuto) {
		p.advance()
		return &SpanClause{Position: Position{start, t.End}, Auto: true}, nil
	}
	if t.Kind != tNumber {
		return nil, p.unexpected("a duration such as 5m, or 'auto'")
	}
	d, ok := toDuration(t.Num, t.Unit)
	if !ok || d <= 0 {
		return nil, errTok(t, "invalid span duration '%s'", t.Text).withHint("use a positive duration with a unit, e.g. 1m, 5m, 1h")
	}
	p.advance()
	return &SpanClause{Position: Position{start, t.End}, Dur: d}, nil
}

func (p *parser) parseSort() (Stage, error) {
	st := &Sort{Position: Position{Pos: p.cur.Pos}}
	p.advance()
	for {
		if p.cur.Kind != tIdent && p.cur.Kind != tBacktick {
			return nil, p.unexpected("a sort key")
		}
		k := SortKey{Position: Position{p.cur.Pos, p.cur.End}, Name: p.cur.Text}
		p.advance()
		if isKw(p.cur, kwAsc) || isKw(p.cur, kwDesc) {
			k.Desc = isKw(p.cur, kwDesc)
			k.End = p.cur.End
			p.advance()
		}
		st.Keys = append(st.Keys, k)
		if p.cur.Kind != tComma {
			st.End = k.End
			return st, nil
		}
		p.advance()
	}
}

func (p *parser) parseFields() (Stage, error) {
	start := p.cur.Pos
	p.advance()
	fs, err := p.fieldList(MaxFields, "fields")
	if err != nil {
		return nil, err
	}
	return &Fields{Position: Position{start, fs[len(fs)-1].End}, Fields: fs}, nil
}

func (p *parser) parseTop() (Stage, error) {
	start := p.cur.Pos
	p.advance()
	n, err := p.integer()
	if err != nil {
		return nil, err
	}
	f, err := p.field()
	if err != nil {
		return nil, err
	}
	top := &Top{Position: Position{start, f.End}, N: n, Field: f}
	if isKw(p.cur, kwBy) {
		p.advance()
		by, err := p.field()
		if err != nil {
			return nil, err
		}
		top.By, top.End = &by, by.End
	}
	return top, nil
}
