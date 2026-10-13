// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// sqlBuf accumulates SQL text from trusted fragments and positional args.
// User input only ever enters through arg.
type sqlBuf struct {
	sb   strings.Builder
	args []any
}

// w writes trusted SQL fragments (constants and allow-listed identifiers).
func (b *sqlBuf) w(parts ...string) {
	for _, p := range parts {
		b.sb.WriteString(p)
	}
}

// arg writes a positional parameter placeholder and records its value.
func (b *sqlBuf) arg(v any) {
	b.sb.WriteByte('?')
	b.args = append(b.args, v)
}

func (b *sqlBuf) String() string { return b.sb.String() }

type frag func(b *sqlBuf)

func lit(s string) frag { return func(b *sqlBuf) { b.w(s) } }

// mapGet renders col[?] with the key as a parameter.
func mapGet(col, key string) frag {
	return func(b *sqlBuf) {
		b.w(col, "[")
		b.arg(key)
		b.w("]")
	}
}

func wrap(prefix string, f frag, suffix string) frag {
	return func(b *sqlBuf) {
		b.w(prefix)
		f(b)
		b.w(suffix)
	}
}

// accessor knows how to render a resolved field in each SQL role.
type accessor struct {
	typ         fieldType
	str         frag   // string-valued expression
	num         frag   // numeric expression for predicates (ns for durations)
	numNullable bool   // num may be NULL (wrap predicates in ifNull)
	propNum     string // numeric property map column: predicates use mapContains form
	key         string // property key (with propNum)
	val         frag   // value for grouping, projection, first/last
	aggNum      frag   // numeric value for sum/avg/quantiles (ms for durations)
	exists      frag
	missing     frag
	lowerStored bool
	uuid        bool
	levelByName bool          // level predicates compare level names on str (HAVING)
	durUnit     time.Duration // unit of num for durations
}

func stringAccessor(e frag) *accessor {
	n := wrap("toFloat64OrNull(", e, ")")
	return &accessor{typ: typeString, str: e, num: n, numNullable: true, val: e, aggNum: n,
		exists: wrap("", e, " != ''"), missing: wrap("", e, " = ''")}
}

func numberAccessor(e frag) *accessor {
	return &accessor{typ: typeNumber, str: wrap("toString(", e, ")"), num: e, val: e, aggNum: e,
		exists: lit("1"), missing: lit("0")}
}

// builtinAccessor renders a built-in column.
func (c *compiler) builtinAccessor(bi *builtin) *accessor {
	col := lit(bi.column)
	switch bi.typ {
	case typeNumber:
		return numberAccessor(col)
	case typeLevel:
		a := stringAccessor(lit("level"))
		a.typ, a.num, a.numNullable, a.aggNum = typeLevel, col, false, col
		return a
	case typeTime:
		a := numberAccessor(col)
		a.typ = typeTime
		return a
	case typeDuration:
		ms := lit(bi.column + " / 1e6")
		return &accessor{typ: typeDuration, num: col, val: ms, aggNum: ms, exists: lit("1"), missing: lit("0"),
			str: wrap("toString(", ms, ")"), durUnit: time.Nanosecond}
	case typeUUID:
		a := stringAccessor(lit("toString(" + bi.column + ")"))
		a.typ, a.uuid, a.lowerStored, a.exists, a.missing = typeUUID, true, true, lit("1"), lit("0")
		return a
	}
	a := stringAccessor(col)
	a.lowerStored = bi.lowerStored
	return a
}

// propertyAccessor renders a property (map) lookup or its indexed column.
func (c *compiler) propertyAccessor(key string) *accessor {
	s := c.s
	if s.numCol == "" { // metrics: attributes are text only
		a := stringAccessor(mapGet(s.propsCol, key))
		a.exists = c.mapExists(s.propsCol, key, a.str)
		a.missing = c.mapMissing(s.propsCol, key)
		return a
	}
	strExpr := mapGet(s.propsCol, key)
	indexed := false
	if col, ok := c.indexed[key]; ok {
		strExpr, indexed = lit(col), true
	}
	numVal := func(b *sqlBuf) {
		b.w("if(mapContains(", s.numCol, ", ")
		b.arg(key)
		b.w("), ")
		mapGet(s.numCol, key)(b)
		b.w(", NULL)")
	}
	a := stringAccessor(strExpr)
	a.aggNum, a.num, a.propNum, a.key = numVal, numVal, s.numCol, key
	if !indexed {
		a.exists = c.mapExists(s.propsCol, key, strExpr)
		a.missing = c.mapMissing(s.propsCol, key)
	}
	return a
}

func (c *compiler) mapExists(col, key string, strExpr frag) frag {
	return func(b *sqlBuf) {
		b.w("(mapContains(", col, ", ")
		b.arg(key)
		b.w(") AND ")
		strExpr(b)
		b.w(" != '')")
	}
}

func (c *compiler) mapMissing(col, key string) frag {
	return func(b *sqlBuf) {
		b.w("NOT mapContains(", col, ", ")
		b.arg(key)
		b.w(")")
	}
}

// access resolves a field in the current context (filter or HAVING).
func (c *compiler) access(f Field) *accessor {
	if c.scope != nil {
		if ref, ok := c.scope.lookup(f.Name); ok {
			return c.scopeAccessor(ref)
		}
	}
	r, _ := c.s.resolve(f) // already validated by analyze
	if r.bi != nil {
		return c.builtinAccessor(r.bi)
	}
	return c.propertyAccessor(r.key)
}

// scopeAccessor renders an aggregate alias or a by field inside HAVING.
func (c *compiler) scopeAccessor(ref scopeRef) *accessor {
	if ref.agg >= 0 {
		return numberAccessor(lit(aggAlias(ref.agg)))
	}
	g := lit(groupAlias(ref.by))
	r, _ := c.s.resolve(c.scope.sp.by[ref.by])
	switch r.typ() {
	case typeNumber:
		return numberAccessor(g)
	case typeDuration:
		a := numberAccessor(g)
		a.typ, a.durUnit = typeDuration, time.Millisecond
		return a
	case typeLevel:
		a := stringAccessor(g)
		a.typ, a.levelByName = typeLevel, true
		return a
	}
	return stringAccessor(g)
}

func aggAlias(i int) string   { return "a" + strconv.Itoa(i) }
func groupAlias(i int) string { return "g" + strconv.Itoa(i) }

// expr renders a checked filter expression.
func (c *compiler) expr(b *sqlBuf, e Expr) {
	switch n := e.(type) {
	case *And:
		c.binary(b, n.Left, n.Right, " AND ")
	case *Or:
		c.binary(b, n.Left, n.Right, " OR ")
	case *Not:
		b.w("NOT (")
		c.expr(b, n.X)
		b.w(")")
	case *MatchAll:
		b.w("1")
	case *FreeText:
		c.freeText(b, n.Text)
	case *FieldMatch:
		c.fieldMatch(b, n)
	case *Compare:
		c.compare(b, n)
	case *In:
		c.in(b, n)
	case *Func:
		c.function(b, n)
	}
}

func (c *compiler) binary(b *sqlBuf, l, r Expr, op string) {
	b.w("(")
	c.expr(b, l)
	b.w(op)
	c.expr(b, r)
	b.w(")")
}

func (c *compiler) freeText(b *sqlBuf, text string) {
	b.w("(")
	for i, col := range c.s.textCols {
		if i > 0 {
			b.w(" OR ")
		}
		b.w("positionCaseInsensitiveUTF8(", col, ", ")
		b.arg(text)
		b.w(") > 0")
	}
	b.w(")")
}

func (c *compiler) fieldMatch(b *sqlBuf, n *FieldMatch) {
	a := c.access(n.Field)
	v := n.Value
	if n.Op != "" {
		c.ordered(b, a, n.Op, v, false)
		return
	}
	switch v.Kind {
	case ValueAny:
		a.exists(b)
	case ValueNull:
		a.missing(b)
	case ValueWildcard:
		a.str(b)
		b.w(" ILIKE ")
		b.arg(v.Pattern)
	case ValueRegex:
		b.w("match(")
		a.str(b)
		b.w(", ")
		b.arg(v.Text)
		b.w(")")
	default:
		c.equal(b, a, v, true)
	}
}

// equal renders field = literal; ci selects case-insensitive text matching.
func (c *compiler) equal(b *sqlBuf, a *accessor, v Value, ci bool) {
	switch a.typ {
	case typeLevel:
		l, _ := levelLiteral(v)
		c.levelPred(b, a, "=", l)
	case typeNumber, typeDuration, typeTime:
		c.ordered(b, a, "=", v, false)
	default:
		if v.Kind == ValueNumber && a.propNum != "" {
			b.w("(")
			c.numPred(b, a, "=", v.Num)
			b.w(" OR ")
			c.eqText(b, a, v.Text, true)
			b.w(")")
			return
		}
		c.eqText(b, a, v.Text, ci)
	}
}

func (c *compiler) eqText(b *sqlBuf, a *accessor, text string, ci bool) {
	switch {
	case a.uuid:
		b.w("id = toUUIDOrZero(")
		b.arg(strings.ToLower(text))
		b.w(")")
	case a.lowerStored && ci:
		a.str(b)
		b.w(" = ")
		b.arg(strings.ToLower(text))
	case ci:
		b.w("lower(")
		a.str(b)
		b.w(") = lower(")
		b.arg(text)
		b.w(")")
	default:
		a.str(b)
		b.w(" = ")
		b.arg(text)
	}
}

// numPred renders a numeric comparison that is false (not NULL) when missing.
func (c *compiler) numPred(b *sqlBuf, a *accessor, op string, x float64) {
	switch {
	case a.propNum != "":
		b.w("(mapContains(", a.propNum, ", ")
		b.arg(a.key)
		b.w(") AND ")
		mapGet(a.propNum, a.key)(b)
		b.w(" ", op, " ")
		b.arg(x)
		b.w(")")
	case a.numNullable:
		b.w("ifNull(")
		a.num(b)
		b.w(" ", op, " ")
		b.arg(x)
		b.w(", 0)")
	default:
		a.num(b)
		b.w(" ", op, " ")
		b.arg(x)
	}
}

// levelPred renders a level comparison by ordinal, or by name set in HAVING.
func (c *compiler) levelPred(b *sqlBuf, a *accessor, op string, l model.Level) {
	if !a.levelByName {
		a.num(b)
		b.w(" ", op, " ")
		b.arg(uint8(l))
		return
	}
	var names []any
	for _, x := range model.AllLevels() {
		if levelOpHolds(x, op, l) {
			names = append(names, x.String())
		}
	}
	a.str(b)
	b.w(" IN (")
	writeArgs(b, names)
	b.w(")")
}

func levelOpHolds(x model.Level, op string, l model.Level) bool {
	switch op {
	case "<":
		return x < l
	case "<=":
		return x <= l
	case ">":
		return x > l
	case ">=":
		return x >= l
	case "!=":
		return x != l
	}
	return x == l
}

func writeArgs(b *sqlBuf, vals []any) {
	for i, v := range vals {
		if i > 0 {
			b.w(", ")
		}
		b.arg(v)
	}
}

// durationValue converts a duration literal to the accessor's unit (a bare
// number means milliseconds, matching results).
func durationValue(v Value, unit time.Duration) float64 {
	v, _ = durationLiteral(v)
	d := v.Num * float64(time.Millisecond)
	if v.Unit != "" {
		d = v.Num * float64(unitDuration(v.Unit))
	}
	return d / float64(unit)
}

// ordered renders field op literal for <,<=,>,>= and typed equality.
func (c *compiler) ordered(b *sqlBuf, a *accessor, op string, v Value, ci bool) {
	switch a.typ {
	case typeLevel:
		l, _ := levelLiteral(v)
		c.levelPred(b, a, op, l)
		return
	case typeTime:
		t, _ := timeLiteral(v, c.o.Now)
		a.num(b)
		b.w(" ", op, " ")
		b.arg(t)
		return
	case typeDuration:
		c.numPred(b, a, op, durationValue(v, a.durUnit))
		return
	}
	if v.Kind == ValueNumber {
		c.numPred(b, a, op, v.Num)
		return
	}
	if ci {
		b.w("lower(")
		a.str(b)
		b.w(") ", op, " lower(")
		b.arg(v.Text)
		b.w(")")
		return
	}
	a.str(b)
	b.w(" ", op, " ")
	b.arg(v.Text)
}

func normalizeOp(op string) string {
	switch op {
	case "==":
		return "="
	case "<>":
		return "!="
	}
	return op
}

func (c *compiler) compare(b *sqlBuf, n *Compare) {
	a := c.access(n.Field)
	op := normalizeOp(n.Op)
	switch {
	case n.Value.Kind == ValueNull && op == "=":
		a.missing(b)
	case n.Value.Kind == ValueNull:
		a.exists(b)
	case op == kwLike || op == "not like":
		c.like(b, a, op, n.Value.Text, n.CI)
	case op == "=":
		c.equal(b, a, n.Value, n.CI)
	case op == "!=":
		b.w("NOT (")
		c.equal(b, a, n.Value, n.CI)
		b.w(")")
	default:
		c.ordered(b, a, op, n.Value, n.CI)
	}
}

func (c *compiler) like(b *sqlBuf, a *accessor, op, pattern string, ci bool) {
	if op != kwLike {
		b.w("NOT ")
	}
	a.str(b)
	if ci {
		b.w(" ILIKE ")
	} else {
		b.w(" LIKE ")
	}
	b.arg(pattern)
}

func allNumbers(vs []Value) bool {
	for _, v := range vs {
		if v.Kind != ValueNumber {
			return false
		}
	}
	return true
}

func (c *compiler) in(b *sqlBuf, n *In) {
	a := c.access(n.Field)
	if n.Not {
		b.w("NOT (")
		defer b.w(")")
	}
	vals := make([]any, 0, len(n.Values))
	switch {
	case a.typ == typeLevel:
		for _, v := range n.Values {
			l, _ := levelLiteral(v)
			vals = append(vals, levelInValue(a, l))
		}
		c.inList(b, levelInExpr(a), vals)
	case a.typ == typeNumber || a.typ == typeDuration:
		for _, v := range n.Values {
			vals = append(vals, numericInValue(a, v))
		}
		c.inList(b, a.num, vals)
	case a.uuid:
		c.uuidIn(b, n.Values)
	case a.propNum != "" && allNumbers(n.Values):
		for _, v := range n.Values {
			vals = append(vals, v.Num)
		}
		b.w("(mapContains(", a.propNum, ", ")
		b.arg(a.key)
		b.w(") AND ")
		c.inList(b, mapGet(a.propNum, a.key), vals)
		b.w(")")
	default:
		for _, v := range n.Values {
			vals = append(vals, v.Text)
		}
		c.inList(b, a.str, vals)
	}
}

func levelInExpr(a *accessor) frag {
	if a.levelByName {
		return a.str
	}
	return a.num
}

func levelInValue(a *accessor, l model.Level) any {
	if a.levelByName {
		return l.String()
	}
	return uint8(l)
}

func numericInValue(a *accessor, v Value) any {
	if a.typ == typeDuration {
		return durationValue(v, a.durUnit)
	}
	return v.Num
}

func (c *compiler) inList(b *sqlBuf, e frag, vals []any) {
	e(b)
	b.w(" IN (")
	writeArgs(b, vals)
	b.w(")")
}

func (c *compiler) uuidIn(b *sqlBuf, vs []Value) {
	b.w("id IN (")
	for i, v := range vs {
		if i > 0 {
			b.w(", ")
		}
		b.w("toUUIDOrZero(")
		b.arg(strings.ToLower(v.Text))
		b.w(")")
	}
	b.w(")")
}

func (c *compiler) function(b *sqlBuf, n *Func) {
	a := c.access(n.Field)
	if n.Name == fnHas {
		a.exists(b)
		return
	}
	text := n.Arg.Text
	switch n.Name {
	case fnContains:
		b.w("positionCaseInsensitiveUTF8(")
		a.str(b)
		b.w(", ")
		b.arg(text)
		b.w(") > 0")
	case fnStartsWith, fnEndsWith:
		fn := "startsWith"
		if n.Name == fnEndsWith {
			fn = "endsWith"
		}
		b.w(fn, "(lower(")
		a.str(b)
		b.w("), lower(")
		b.arg(text)
		b.w("))")
	case fnMatch:
		b.w("match(")
		a.str(b)
		b.w(", ")
		b.arg(text)
		b.w(")")
	}
}

// relative time literal: now, now-15m, now+1h (units s m h d w).
var reRelTime = regexp.MustCompile(`^(?i)now(?:([+-])(\d{1,9})(s|m|h|d|w))?$`)

// timeLayouts are the accepted absolute time formats (ISO-8601 subsets).
func timeLayouts() []string {
	return []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"}
}

// timeLiteral parses an @timestamp literal relative to now.
func timeLiteral(v Value, now time.Time) (time.Time, error) {
	if v.Kind == ValueString {
		if m := reRelTime.FindStringSubmatch(v.Text); m != nil {
			return relativeTime(now, m), nil
		}
		for _, layout := range timeLayouts() {
			if t, err := time.Parse(layout, v.Text); err == nil {
				return t.UTC(), nil
			}
		}
	}
	e := nodeErr(v, "invalid time '%s'", v.Raw)
	return time.Time{}, e.withHint("use now, now-15m, or an ISO-8601 time such as '2026-10-13T08:00:00Z'")
}

func relativeTime(now time.Time, m []string) time.Time {
	if m[1] == "" {
		return now.UTC()
	}
	n := 0
	for _, ch := range m[2] {
		n = n*10 + int(ch-'0')
	}
	unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour,
		"d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[strings.ToLower(m[3])]
	d := time.Duration(math.Min(float64(n)*float64(unit), float64(maxDurationValue)))
	if m[1] == "-" {
		d = -d
	}
	return now.Add(d).UTC()
}
