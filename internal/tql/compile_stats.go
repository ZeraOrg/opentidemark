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
	if sp.metricRate {
		c.metricRate(b, sp, out.Span)
	} else {
		c.plainStats(b, sp, out.Span)
	}
	c.statsTail(b, sp, out.Span, limit)
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
	if (a.Fn != "first" && a.Fn != "last") || a.Field == nil {
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
		c.agg(b, a, span)
		b.w(" AS ", aggAlias(i))
	}
	b.w(" FROM ", c.table(), " WHERE ")
	c.where(b)
	if g := groupByList(sp, span); g != "" {
		b.w(" GROUP BY ", g)
	}
}

// metricRate renders rate(value) on metrics: per-series increase (deltaSum of
// cumulative sums, or sum of delta points) summed across series and divided
// by the bucket (or range) seconds (SPEC §10.5).
func (c *compiler) metricRate(b *sqlBuf, sp *statsPlan, span time.Duration) {
	outer := groupByList(sp, span)
	b.w("SELECT ")
	if outer != "" {
		b.w(outer, ", ")
	}
	b.w("sum(r) / ")
	b.arg(c.rangeSeconds(span))
	b.w(" AS ", aggAlias(0), " FROM (SELECT ")
	c.keys(b, sp, span)
	if span > 0 || len(sp.by) > 0 {
		b.w(", ")
	}
	b.w("series_id, if(any(temporality) = 'delta', sum(value), ",
		"deltaSumTimestamp(value, toUnixTimestamp64Milli(timestamp))) AS r FROM ", c.table(), " WHERE ")
	c.where(b)
	b.w(" GROUP BY ", groupByList(sp, span, "series_id"), ")")
	if outer != "" {
		b.w(" GROUP BY ", outer)
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

// statsTail renders HAVING, ORDER BY and LIMIT.
func (c *compiler) statsTail(b *sqlBuf, sp *statsPlan, span time.Duration, limit int) {
	if len(c.p.having) > 0 {
		c.scope = newHavingScope(c.s, sp)
		b.w(" HAVING ")
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
