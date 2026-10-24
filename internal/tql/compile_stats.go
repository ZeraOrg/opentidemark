// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"time"
)

// quantileLevel maps pNN aggregates to the quantile constant (allow-list).
func quantileLevel(fn string) (string, bool) {
	switch fn {
	case "p50":
		return "0.5", true
	case "p75":
		return "0.75", true
	case "p90":
		return "0.9", true
	case "p95":
		return "0.95", true
	case "p99":
		return "0.99", true
	}
	return "", false
}

const bucketAlias = "bucket"

// statsQuery renders table and timeseries queries.
func (c *compiler) statsQuery(b *sqlBuf, out *Compiled) error {
	sp := c.p.stats
	if sp.span != nil {
		d, err := c.chooseSpan(sp.span)
		if err != nil {
			return err
		}
		out.Span = d
	}
	limit := c.o.MaxRows
	if c.p.limit > 0 {
		limit = min(limit, c.p.limit)
	}
	if sp.topN > 0 && sp.topBy < 0 {
		limit = min(limit, sp.topN)
	}
	out.Limit = limit
	out.Columns = c.statsColumns(sp)
	filterKw := " HAVING "
	switch {
	case sp.histogram:
		c.histStats(b, sp, out.Span)
		filterKw = " WHERE " // the outer query does not aggregate (SPEC §10.6)
	case sp.metricRate:
		c.metricRate(b, sp, out.Span)
	default:
		c.plainStats(b, sp, out.Span)
	}
	c.statsTail(b, sp, out.Span, limit, filterKw)
	return nil
}

func (c *compiler) statsColumns(sp *statsPlan) []Column {
	var cols []Column
	if sp.span != nil {
		cols = append(cols, Column{Name: BucketColumn, Type: ColumnTime})
	}
	for _, f := range sp.by {
		r, _ := c.s.resolve(f)
		t := columnTypeOf(r.typ())
		if r.typ() == typeLevel {
			t = ColumnString
		}
		cols = append(cols, Column{Name: f.Name, Type: t})
	}
	for _, a := range sp.aggs {
		cols = append(cols, Column{Name: a.Alias, Type: c.aggType(a)})
	}
	return cols
}

func (c *compiler) aggType(a Agg) ColumnType {
	if a.Expr != nil || (a.Fn != "first" && a.Fn != "last") || a.Field == nil {
		return ColumnNumber
	}
	r, _ := c.s.resolve(*a.Field)
	if r.typ() == typeLevel {
		return ColumnString
	}
	return columnTypeOf(r.typ())
}

// rangeSeconds is the rate denominator: the bucket, or the whole range.
func (c *compiler) rangeSeconds(span time.Duration) float64 {
	if span > 0 {
		return span.Seconds()
	}
	return c.o.To.Sub(c.o.From).Seconds()
}

// keys renders the bucket and group-by select items.
func (c *compiler) keys(b *sqlBuf, sp *statsPlan, span time.Duration) {
	sep := ""
	if span > 0 {
		b.w("toStartOfInterval(", c.s.timeCol, ", toIntervalSecond(")
		b.arg(int64(span / time.Second))
		b.w(")) AS ", bucketAlias)
		sep = ", "
	}
	for i, f := range sp.by {
		b.w(sep)
		c.access(f).val(b)
		b.w(" AS ", groupAlias(i))
		sep = ", "
	}
}

func groupByList(sp *statsPlan, span time.Duration, extra ...string) string {
	var list []string
	if span > 0 {
		list = append(list, bucketAlias)
	}
	for i := range sp.by {
		list = append(list, groupAlias(i))
	}
	list = append(list, extra...)
	out := ""
	for i, k := range list {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}

func (c *compiler) plainStats(b *sqlBuf, sp *statsPlan, span time.Duration) {
	b.w("SELECT ")
	c.keys(b, sp, span)
	for i, a := range sp.aggs {
		if i > 0 || span > 0 || len(sp.by) > 0 {
			b.w(", ")
		}
		column(b, a, func(call Agg) { c.agg(b, call, span) })
		b.w(" AS ", aggAlias(i))
	}
	b.w(" FROM ", c.table(), " WHERE ")
	c.where(b)
	if g := groupByList(sp, span); g != "" {
		b.w(" GROUP BY ", g)
	}
}

// metricRate renders rate(value) on metrics: the per-point increases of each series (SPEC §10.5)
// of the points in the request range, summed per bucket and group and divided by the bucket (or
// range) seconds. The points are read from one lookback before the range, so the first point in
// the range has a previous point.
func (c *compiler) metricRate(b *sqlBuf, sp *statsPlan, span time.Duration) {
	outer := groupByList(sp, span)
	b.w("SELECT ")
	if outer != "" {
		b.w(outer, ", ")
	}
	column(b, sp.aggs[0], func(Agg) { // a quotient: binds tighter than the arithmetic around it
		b.w("sum(r_d) / ")
		b.arg(c.rangeSeconds(span))
	})
	b.w(" AS ", aggAlias(0), " FROM (")
	c.ratePoints(b, sp, span)
	b.w(") WHERE ", c.s.timeCol, " >= ")
	b.arg(c.o.From.UTC())
	if outer != "" {
		b.w(" GROUP BY ", outer)
	}
}

// ratePoints renders the increase of each point against the previous point of its series. A
// point counts whole when its temporality is delta, when the series restarted (both start times
// known and different, or the value decreased), or when it is the first point of its series and
// its start time lies in the read range. Any other first point is only the baseline of the next.
func (c *compiler) ratePoints(b *sqlBuf, sp *statsPlan, span time.Duration) {
	readFrom := c.o.From.Add(-c.deltaLookback(span))
	b.w("SELECT ")
	c.keys(b, sp, span)
	if span > 0 || len(sp.by) > 0 {
		b.w(", ")
	}
	b.w(c.s.timeCol, ", row_number() OVER w = 1 AS r_first, ",
		"toUnixTimestamp64Nano(start_time) AS r_st, lagInFrame(toUnixTimestamp64Nano(start_time)) OVER w AS r_pst, ",
		"lagInFrame(value) OVER w AS r_pv, ",
		"multiIf(temporality = 'delta' OR (NOT r_first AND ((r_st != 0 AND r_pst != 0 AND r_st != r_pst) OR value < r_pv)) ",
		"OR (r_first AND r_st != 0 AND start_time >= ")
	b.arg(readFrom.UTC())
	b.w("), value, r_first, 0, value - r_pv) AS r_d FROM ", c.table(), " WHERE ")
	c.whereFrom(b, readFrom)
	b.w(" WINDOW w AS (PARTITION BY stream, series_id ORDER BY ", c.s.timeCol,
		" ROWS BETWEEN 1 PRECEDING AND CURRENT ROW)")
}

// column renders stats column a: its aggregate call, or its arithmetic with each call rendered
// by leaf in written order. Numbers are parameters; division by zero yields NULL (SPEC §10.5).
func column(b *sqlBuf, a Agg, leaf func(Agg)) {
	if a.Expr == nil {
		leaf(a)
		return
	}
	arith(b, a.Expr, leaf)
}

func arith(b *sqlBuf, e *AggExpr, leaf func(Agg)) {
	switch {
	case e.Call != nil:
		leaf(*e.Call)
	case e.Num != nil:
		b.arg(e.Num.Num)
	case e.Op == "/":
		b.w("(")
		arith(b, e.Left, leaf)
		b.w(" / nullIf(")
		arith(b, e.Right, leaf)
		b.w(", 0))")
	default:
		b.w("(")
		arith(b, e.Left, leaf)
		b.w(" ", e.Op, " ") // + - * (allow-listed by the parser)
		arith(b, e.Right, leaf)
		b.w(")")
	}
}

// agg renders one aggregate expression.
func (c *compiler) agg(b *sqlBuf, a Agg, span time.Duration) {
	var acc *accessor
	if a.Field != nil {
		acc = c.access(*a.Field)
	}
	switch a.Fn {
	case "count":
		if acc == nil {
			b.w("count()")
			return
		}
		b.w("countIf(")
		acc.exists(b)
		b.w(")")
	case "count_distinct":
		b.w("uniq(")
		acc.val(b)
		b.w(")")
	case "rate":
		b.w("count() / ")
		b.arg(c.rangeSeconds(span))
	case "first", "last":
		fn := "argMin("
		if a.Fn == "last" {
			fn = "argMax("
		}
		b.w(fn)
		acc.val(b)
		b.w(", ", c.s.timeCol, ")")
	default:
		if q, ok := quantileLevel(a.Fn); ok {
			b.w("quantileTDigest(", q, ")(")
		} else {
			b.w(a.Fn, "(") // sum, avg, min, max (allow-listed by the parser)
		}
		acc.aggNum(b)
		b.w(")")
	}
}

// statsTail renders the conditions of `where` after stats (filterKw: HAVING, or WHERE when the
// outer query does not aggregate), ORDER BY and LIMIT.
func (c *compiler) statsTail(b *sqlBuf, sp *statsPlan, span time.Duration, limit int, filterKw string) {
	if len(c.p.having) > 0 {
		c.scope = newHavingScope(c.s, sp)
		b.w(filterKw)
		for i, h := range c.p.having {
			if i > 0 {
				b.w(" AND ")
			}
			c.exprParen(b, h)
		}
		c.scope = nil
	}
	b.w(" ORDER BY ", c.orderBy(sp, span))
	if sp.topN > 0 && sp.topBy >= 0 {
		b.w(" LIMIT ")
		b.arg(sp.topN)
		b.w(" BY ", groupAlias(sp.topBy))
	}
	b.w(" LIMIT ")
	b.arg(limit)
}

func (c *compiler) orderBy(sp *statsPlan, span time.Duration) string {
	var keys []string
	used := map[string]bool{}
	add := func(k, dir string) {
		if !used[k] {
			used[k] = true
			keys = append(keys, k+" "+dir)
		}
	}
	if span > 0 {
		add(bucketAlias, "ASC")
	}
	scope := newHavingScope(c.s, sp)
	for _, k := range c.p.sort {
		ref, _ := scope.lookup(k.Name)
		dir := "ASC"
		if k.Desc {
			dir = "DESC"
		}
		switch {
		case ref.bucket:
			add(bucketAlias, dir)
		case ref.agg >= 0:
			add(aggAlias(ref.agg), dir)
		default:
			add(groupAlias(ref.by), dir)
		}
	}
	if c.p.sort == nil && span == 0 {
		add(aggAlias(0), "DESC")
	}
	for i := range sp.by {
		add(groupAlias(i), "ASC")
	}
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}
