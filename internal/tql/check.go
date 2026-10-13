// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// ResultKind is the shape of a query result (SPEC §10.5).
type ResultKind string

// Result kinds.
const (
	KindEvents     ResultKind = "events"     // no stats
	KindTable      ResultKind = "table"      // stats without span
	KindTimeseries ResultKind = "timeseries" // stats with span
)

// BucketColumn is the result column name of the time bucket.
const BucketColumn = "@bucket"

// plan is the checked, normalized form of a query used by the compiler.
type plan struct {
	q       *Query
	schema  *sourceSchema
	filters []Expr // filter + where stages before stats
	stats   *statsPlan
	having  []Expr
	sort    []SortKey
	limit   int // 0 = none
	fields  []Field
	kind    ResultKind
}

// statsPlan is a normalized stats or top stage.
type statsPlan struct {
	aggs       []Agg
	by         []Field
	span       *SpanClause
	topN       int  // > 0 for a top stage
	topBy      int  // index into by for `top … by`, or -1
	metricRate bool // metrics rate(value): per-series deltas
	pos        Position
}

// Check parses nothing; it validates a parsed query: field resolution per
// source, level literals, value types, durations, regexes, stage order and
// the rule that `where` after stats references only aggregate aliases and
// by-fields. Errors are *Error with positions.
func Check(q *Query) error {
	_, err := analyze(q)
	return err
}

func analyze(q *Query) (*plan, error) {
	if q == nil {
		return nil, errAt(0, 0, "empty query")
	}
	src := q.Source
	if src == "" {
		src = model.SourceLogs
	}
	if !src.Valid() {
		return nil, errAt(0, 0, "unknown source '%s'", src)
	}
	p := &plan{q: q, schema: schemaFor(src), kind: KindEvents}
	c := &checker{schema: p.schema}
	if q.Filter != nil {
		if err := c.expr(q.Filter); err != nil {
			return nil, err
		}
		p.filters = append(p.filters, q.Filter)
	}
	for _, st := range q.Stages {
		if err := c.stage(p, st); err != nil {
			return nil, err
		}
	}
	return p, nil
}

type checker struct {
	schema *sourceSchema
	scope  *havingScope // non-nil while checking `where` after stats
}

func nodeErr(n Node, format string, args ...any) *Error {
	pos, end := n.Bounds()
	return errAt(pos, end-pos, format, args...)
}

func (c *checker) stage(p *plan, st Stage) error {
	switch s := st.(type) {
	case *Where:
		return c.where(p, s)
	case *Stats:
		return c.statsStage(p, s)
	case *Top:
		return c.topStage(p, s)
	case *Sort:
		return c.sortStage(p, s)
	case *Limit:
		if p.limit != 0 {
			return nodeErr(s, "duplicate limit stage")
		}
		p.limit = s.N
	case *Fields:
		return c.fieldsStage(p, s)
	}
	return nil
}

func (c *checker) where(p *plan, w *Where) error {
	if p.stats == nil {
		if err := c.expr(w.Expr); err != nil {
			return err
		}
		p.filters = append(p.filters, w.Expr)
		return nil
	}
	hc := &checker{schema: c.schema, scope: newHavingScope(c.schema, p.stats)}
	if err := hc.expr(w.Expr); err != nil {
		return err
	}
	p.having = append(p.having, w.Expr)
	return nil
}

func (c *checker) precheckStats(p *plan, n Node) error {
	switch {
	case p.stats != nil:
		return nodeErr(n, "only one stats or top stage is allowed")
	case p.fields != nil:
		return nodeErr(n, "fields cannot be combined with stats or top")
	case p.limit != 0:
		return nodeErr(n, "limit must come after stats or top")
	}
	return nil
}

func (c *checker) statsStage(p *plan, s *Stats) error {
	if err := c.precheckStats(p, s); err != nil {
		return err
	}
	sp := &statsPlan{aggs: s.Aggs, by: s.By, span: s.Span, topBy: -1, pos: s.Position}
	if err := c.checkStats(sp); err != nil {
		return err
	}
	p.stats = sp
	p.kind = KindTable
	if s.Span != nil {
		p.kind = KindTimeseries
	}
	return nil
}

func (c *checker) topStage(p *plan, t *Top) error {
	if err := c.precheckStats(p, t); err != nil {
		return err
	}
	sp := &statsPlan{aggs: []Agg{{Position: t.Position, Fn: "count", Alias: "count"}}, topN: t.N, topBy: -1, pos: t.Position}
	if t.By != nil {
		sp.by = append(sp.by, *t.By)
		sp.topBy = 0
	}
	sp.by = append(sp.by, t.Field)
	if err := c.checkStats(sp); err != nil {
		return err
	}
	p.stats, p.kind = sp, KindTable
	return nil
}

func (c *checker) checkStats(sp *statsPlan) error {
	names := map[string]bool{strings.ToLower(BucketColumn): true}
	for _, f := range sp.by {
		r, err := c.schema.resolve(f)
		if err != nil {
			return err
		}
		if r.typ() == typeTime {
			return nodeErr(f, "cannot group by %s", f.Name).withHint("use 'span <duration>' to group by time")
		}
		low := strings.ToLower(f.Name)
		if names[low] {
			return nodeErr(f, "duplicate by field '%s'", f.Name)
		}
		names[low] = true
	}
	for i := range sp.aggs {
		if err := c.checkAgg(sp, &sp.aggs[i]); err != nil {
			return err
		}
		low := strings.ToLower(sp.aggs[i].Alias)
		if names[low] {
			return errAt(sp.aggs[i].AliasPos.Pos, sp.aggs[i].AliasPos.End-sp.aggs[i].AliasPos.Pos,
				"duplicate column name '%s'", sp.aggs[i].Alias).withHint("give the aggregate a different name with 'as'")
		}
		names[low] = true
	}
	if sp.span != nil && !sp.span.Auto && (sp.span.Dur < time.Second || sp.span.Dur%time.Second != 0) {
		return nodeErr(sp.span, "span must be a whole number of seconds (at least 1s)")
	}
	return nil
}

func needsField(fn string) bool {
	switch fn {
	case "count", "rate":
		return false
	}
	return true
}

func isNumericAgg(fn string) bool {
	switch fn {
	case "sum", "avg", "min", "max", "p50", "p75", "p90", "p95", "p99":
		return true
	}
	return false
}

func (c *checker) checkAgg(sp *statsPlan, a *Agg) error {
	if a.Field == nil {
		if needsField(a.Fn) {
			return nodeErr(a, "%s() needs a field", a.Fn).withHint(a.Fn + "(field)")
		}
		return nil
	}
	r, err := c.schema.resolve(*a.Field)
	if err != nil {
		return err
	}
	if a.Fn == "rate" {
		return c.checkRate(sp, a, r)
	}
	if isNumericAgg(a.Fn) && (r.typ() == typeTime || r.typ() == typeUUID) {
		return nodeErr(*a.Field, "%s() needs a numeric field; %s is not numeric", a.Fn, a.Field.Name)
	}
	return nil
}

func (c *checker) checkRate(sp *statsPlan, a *Agg, r resolvedField) error {
	if c.schema.src != model.SourceMetrics || r.bi == nil || r.bi.name != "value" {
		return nodeErr(a, "rate() takes a field only as rate(value) on source metrics").withHint("use rate() for events per second")
	}
	if len(sp.aggs) != 1 {
		return nodeErr(a, "rate(value) must be the only aggregate in its stats stage")
	}
	sp.metricRate = true
	return nil
}

func (c *checker) sortStage(p *plan, s *Sort) error {
	switch {
	case p.stats == nil:
		return nodeErr(s, "sort needs a stats or top stage before it").withHint("events are always sorted newest first")
	case p.sort != nil:
		return nodeErr(s, "duplicate sort stage")
	case p.stats.topN > 0:
		return nodeErr(s, "top already sorts by count")
	}
	scope := newHavingScope(p.schema, p.stats)
	for _, k := range s.Keys {
		if _, ok := scope.lookup(k.Name); !ok {
			return nodeErr(k, "unknown sort key '%s'", k.Name).withHint(didYouMean(k.Name, scope.names()))
		}
	}
	p.sort = s.Keys
	return nil
}

func (c *checker) fieldsStage(p *plan, s *Fields) error {
	if p.stats != nil {
		return nodeErr(s, "fields cannot be combined with stats or top")
	}
	if p.fields != nil {
		return nodeErr(s, "duplicate fields stage")
	}
	for _, f := range s.Fields {
		if _, err := c.schema.resolve(f); err != nil {
			return err
		}
	}
	p.fields = s.Fields
	return nil
}

// havingScope resolves names visible to `where`/`sort` after stats.
type havingScope struct {
	schema *sourceSchema
	sp     *statsPlan
}

// scopeRef is a name visible after stats: an aggregate (agg >= 0), a by field
// (by >= 0) or the time bucket.
type scopeRef struct {
	agg, by int
	bucket  bool
}

func newHavingScope(s *sourceSchema, sp *statsPlan) *havingScope {
	return &havingScope{schema: s, sp: sp}
}

func (h *havingScope) lookup(name string) (scopeRef, bool) {
	for i, a := range h.sp.aggs {
		if strings.EqualFold(a.Alias, name) {
			return scopeRef{agg: i, by: -1}, true
		}
	}
	for i, f := range h.sp.by {
		if strings.EqualFold(f.Name, name) {
			return scopeRef{agg: -1, by: i}, true
		}
	}
	if h.sp.span != nil && strings.EqualFold(name, BucketColumn) {
		return scopeRef{agg: -1, by: -1, bucket: true}, true
	}
	return scopeRef{}, false
}

func (h *havingScope) names() []string {
	var out []string
	for _, a := range h.sp.aggs {
		out = append(out, a.Alias)
	}
	for _, f := range h.sp.by {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

// field resolves a field reference in the current context.
func (c *checker) field(f Field) (resolvedField, error) {
	if c.scope == nil {
		return c.schema.resolve(f)
	}
	ref, ok := c.scope.lookup(f.Name)
	if !ok || ref.bucket {
		e := nodeErr(f, "after stats, where can only use aggregate names or by fields; '%s' is neither", f.Name)
		return resolvedField{}, e.withHint(didYouMean(f.Name, c.scope.names()))
	}
	if ref.agg >= 0 {
		return resolvedField{name: f.Name, bi: &builtin{name: f.Name, typ: typeNumber}}, nil
	}
	return c.schema.resolve(c.scope.sp.by[ref.by])
}

func (c *checker) expr(e Expr) error {
	switch n := e.(type) {
	case *And:
		if err := c.expr(n.Left); err != nil {
			return err
		}
		return c.expr(n.Right)
	case *Or:
		if err := c.expr(n.Left); err != nil {
			return err
		}
		return c.expr(n.Right)
	case *Not:
		return c.expr(n.X)
	case *MatchAll:
		return nil
	case *FreeText:
		if c.scope != nil {
			return nodeErr(n, "free text is not allowed after stats").withHint("compare an aggregate, e.g. where count > 10")
		}
		return nil
	case *FieldMatch:
		return c.fieldMatch(n)
	case *Compare:
		return c.compare(n)
	case *In:
		return c.in(n)
	case *Func:
		return c.function(n)
	}
	return nodeErr(e, "unsupported expression")
}

func varErr(v Value) *Error {
	return nodeErr(v, "unresolved variable $%s", v.Text).withHint("variables are replaced by dashboards before the query runs")
}

func (c *checker) fieldMatch(n *FieldMatch) error {
	r, err := c.field(n.Field)
	if err != nil {
		return err
	}
	v := n.Value
	if v.Kind == ValueVar {
		return varErr(v)
	}
	if v.Kind == ValueRegex {
		if _, err := regexp.Compile(v.Text); err != nil {
			return nodeErr(v, "invalid regular expression: %s", regexErrText(err))
		}
	}
	if n.Op != "" {
		return c.literalFor(r, v, n.Op)
	}
	return c.matchValue(r, n)
}

func regexErrText(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i >= 0 {
		return msg[i+2:]
	}
	return msg
}

// matchValue checks a `field:value` value (no range operator).
func (c *checker) matchValue(r resolvedField, n *FieldMatch) error {
	v := n.Value
	switch r.typ() {
	case typeTime:
		return nodeErr(n, "%s needs a comparison", n.Field.Name).withHint("e.g. @timestamp > now-15m")
	case typeNumber, typeDuration:
		if v.Kind == ValueAny {
			return nil
		}
		return c.literalFor(r, v, "=")
	case typeLevel:
		switch v.Kind {
		case ValueAny, ValueWildcard, ValueRegex:
			return nil
		case ValueNull, ValueBool:
			return nodeErr(v, "level expects a level name such as error or warning")
		}
		return c.literalFor(r, v, "=")
	}
	if v.Kind == ValueNumber && v.Unit != "" {
		return durationErr(v)
	}
	return nil
}

func durationErr(v Value) *Error {
	return nodeErr(v, "durations are only allowed for span duration").withHint("remove the unit or compare against duration in source spans")
}

// literalFor checks a single literal compared to a field with op.
func (c *checker) literalFor(r resolvedField, v Value, op string) error {
	switch v.Kind {
	case ValueVar:
		return varErr(v)
	case ValueAny, ValueWildcard, ValueRegex:
		return nodeErr(v, "'%s' cannot be used with %s", v.Raw, op)
	}
	switch r.typ() {
	case typeLevel:
		_, err := levelLiteral(v)
		return err
	case typeTime:
		_, err := timeLiteral(v, time.Time{})
		return err
	case typeNumber:
		if v.Kind != ValueNumber || v.Unit != "" {
			return nodeErr(v, "%s expects a number", r.name)
		}
	case typeDuration:
		if _, ok := durationLiteral(v); !ok {
			return nodeErr(v, "duration expects a number such as 250ms or 1.5s")
		}
	default:
		if v.Kind == ValueNumber && v.Unit != "" {
			return durationErr(v)
		}
	}
	return nil
}

func (c *checker) compare(n *Compare) error {
	r, err := c.field(n.Field)
	if err != nil {
		return err
	}
	if n.Value.Kind == ValueNull {
		if n.Op == "=" || n.Op == "==" || n.Op == "!=" || n.Op == "<>" {
			return nil
		}
		return nodeErr(n.Value, "null can only be compared with = or !=")
	}
	if strings.HasSuffix(n.Op, kwLike) {
		switch r.typ() {
		case typeTime, typeNumber, typeDuration:
			return nodeErr(n, "like needs a text field")
		}
		if n.Value.Kind == ValueVar {
			return varErr(n.Value)
		}
		return nil
	}
	return c.literalFor(r, n.Value, n.Op)
}

func (c *checker) in(n *In) error {
	r, err := c.field(n.Field)
	if err != nil {
		return err
	}
	if r.typ() == typeTime {
		return nodeErr(n, "in cannot be used with %s", n.Field.Name)
	}
	for _, v := range n.Values {
		if v.Kind == ValueNull {
			return nodeErr(v, "null is not allowed in a list")
		}
		if err := c.literalFor(r, v, kwIn); err != nil {
			return err
		}
	}
	return nil
}

func (c *checker) function(n *Func) error {
	r, err := c.field(n.Field)
	if err != nil {
		return err
	}
	if n.Name == fnHas {
		return nil
	}
	switch r.typ() {
	case typeTime, typeNumber, typeDuration:
		return nodeErr(n, "%s() needs a text field", n.Name)
	}
	a := *n.Arg
	switch a.Kind {
	case ValueVar:
		return varErr(a)
	case ValueString, ValueNumber:
	default:
		return nodeErr(a, "%s() needs a text argument", n.Name)
	}
	if n.Name == fnMatch {
		if _, err := regexp.Compile(a.Text); err != nil {
			return nodeErr(a, "invalid regular expression: %s", regexErrText(err))
		}
	}
	return nil
}

// levelLiteral parses a level literal with a did-you-mean hint.
func levelLiteral(v Value) (model.Level, error) {
	if v.Kind == ValueString || v.Kind == ValueNumber {
		if l, ok := model.ParseLevel(v.Text); ok {
			return l, nil
		}
	}
	e := nodeErr(v, "unknown level '%s'", v.Text)
	return 0, e.withHint(didYouMean(v.Text, canonicalLevelNames()))
}

// canonicalLevelNames are the hint candidates: canonical names plus the most
// common short forms (rarer aliases such as "eror" would make poor hints).
func canonicalLevelNames() []string {
	out := make([]string, 0, len(model.AllLevels())+4)
	for _, l := range model.AllLevels() {
		out = append(out, strings.ToLower(l.String()))
	}
	return append(out, "info", "warn", "trace", "critical")
}

// durationLiteral returns v as a number (with optional unit); a string such
// as "250ms" (from a dashboard variable) is accepted too.
func durationLiteral(v Value) (Value, bool) {
	switch v.Kind {
	case ValueNumber:
		return v, true
	case ValueString:
		if n, unit, ok := parseNumber(strings.TrimSpace(v.Text)); ok {
			v.Kind, v.Num, v.Unit = ValueNumber, n, unit
			return v, true
		}
	}
	return v, false
}
