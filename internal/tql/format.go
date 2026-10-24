// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"strconv"
	"strings"
	"time"
)

// String renders the query as canonical TQL. Parsing the result yields an
// equivalent query; it is used to show substituted dashboard queries and in
// tests. Parentheses are minimal (not > and > or, left-associative).
func (q *Query) String() string {
	var b strings.Builder
	if q.Source != "" && q.Source != "logs" {
		b.WriteString("source ")
		b.WriteString(string(q.Source))
		b.WriteString(" | ")
	}
	if q.Filter == nil {
		b.WriteString("*")
	} else {
		writeExpr(&b, q.Filter)
	}
	for _, st := range q.Stages {
		b.WriteString(" | ")
		writeStage(&b, st)
	}
	return b.String()
}

func writeField(b *strings.Builder, f Field) {
	if !f.Quoted && reIdent.MatchString(f.Name) && !isReservedWord(f.Name) {
		b.WriteString(f.Name)
		return
	}
	b.WriteByte('`')
	b.WriteString(strings.ReplaceAll(f.Name, "`", "``"))
	b.WriteByte('`')
}

func isReservedWord(s string) bool {
	switch strings.ToLower(s) {
	case kwAnd, kwOr, kwNot:
		return true
	}
	return false
}

// quote renders s as a double-quoted string; star escaping keeps a literal *
// from becoming a wildcard in value position.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"', '*':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func formatNumber(v Value) string {
	if v.Raw != "" {
		return v.Raw
	}
	return strconv.FormatFloat(v.Num, 'g', -1, 64) + v.Unit
}

// writeValue renders a value in syntax valid both right of : and after an operator.
func writeValue(b *strings.Builder, v Value) {
	switch v.Kind {
	case ValueNumber:
		b.WriteString(formatNumber(v))
	case ValueBool:
		b.WriteString(v.Text)
	case ValueNull:
		b.WriteString(kwNull)
	case ValueAny:
		b.WriteString("*")
	case ValueVar:
		b.WriteString("$" + v.Text)
	case ValueRegex:
		b.WriteString("/" + strings.ReplaceAll(v.Text, "/", `\/`) + "/")
	case ValueWildcard:
		b.WriteString(v.Raw)
	default:
		b.WriteString(quote(v.Text))
	}
}

// Precedence levels for printing (not > and > or).
const (
	precOr = iota + 1
	precAnd
	precUnary
)

func precedence(e Expr) int {
	switch e.(type) {
	case *Or:
		return precOr
	case *And:
		return precAnd
	}
	return precUnary
}

// writeExprIn writes e, parenthesised when its precedence is below min.
func writeExprIn(b *strings.Builder, e Expr, minPrec int) {
	if precedence(e) < minPrec {
		b.WriteByte('(')
		writeExpr(b, e)
		b.WriteByte(')')
		return
	}
	writeExpr(b, e)
}

// writeExpr prints with minimal parentheses; operators are left-associative,
// so a right operand of equal precedence is parenthesised.
func writeExpr(b *strings.Builder, e Expr) {
	switch n := e.(type) {
	case *And:
		writeBinary(b, n.Left, n.Right, kwAnd, precAnd)
	case *Or:
		writeBinary(b, n.Left, n.Right, kwOr, precOr)
	case *Not:
		b.WriteString("not ")
		writeExprIn(b, n.X, precUnary)
	case *MatchAll:
		b.WriteString("*")
	case *FieldMatch:
		writeField(b, n.Field)
		b.WriteByte(':')
		if n.Op != "" {
			b.WriteString(n.Op + " ")
		}
		writeValue(b, n.Value)
	case *Compare:
		writeField(b, n.Field)
		b.WriteString(" " + n.Op + " ")
		writeValue(b, n.Value)
		if n.CI {
			b.WriteString(" ci")
		}
	case *In:
		writeIn(b, n)
	case *Func:
		b.WriteString(n.Name + "(")
		writeField(b, n.Field)
		if n.Arg != nil {
			b.WriteString(", ")
			writeValue(b, *n.Arg)
		}
		b.WriteByte(')')
	case *FreeText:
		b.WriteString(quote(n.Text))
	}
}

func writeBinary(b *strings.Builder, l, r Expr, op string, prec int) {
	writeExprIn(b, l, prec)
	b.WriteString(" " + op + " ")
	writeExprIn(b, r, prec+1)
}

func writeIn(b *strings.Builder, n *In) {
	writeField(b, n.Field)
	if n.Not {
		b.WriteString(" not")
	}
	b.WriteString(" in [")
	for i, v := range n.Values {
		if i > 0 {
			b.WriteString(", ")
		}
		writeValue(b, v)
	}
	b.WriteByte(']')
}

func formatDuration(d time.Duration) string {
	units := []struct {
		u string
		d time.Duration
	}{{"d", 24 * time.Hour}, {"h", time.Hour}, {"m", time.Minute}, {"s", time.Second}, {"ms", time.Millisecond}, {"us", time.Microsecond}}
	for _, x := range units {
		if d%x.d == 0 {
			return strconv.FormatInt(int64(d/x.d), 10) + x.u
		}
	}
	return strconv.FormatInt(int64(d), 10) + "ns"
}

func writeStage(b *strings.Builder, st Stage) {
	switch s := st.(type) {
	case *Stats:
		writeStats(b, s)
	case *Sort:
		b.WriteString(stageSort + " ")
		for i, k := range s.Keys {
			if i > 0 {
				b.WriteString(", ")
			}
			writeField(b, Field{Name: k.Name})
			if k.Desc {
				b.WriteString(" desc")
			} else {
				b.WriteString(" asc")
			}
		}
	case *Limit:
		b.WriteString(stageLimit + " " + strconv.Itoa(s.N))
	case *Fields:
		b.WriteString(stageFields + " ")
		writeFieldList(b, s.Fields)
	case *Where:
		b.WriteString(stageWhere + " ")
		writeExpr(b, s.Expr)
	case *Top:
		b.WriteString(stageTop + " " + strconv.Itoa(s.N) + " ")
		writeField(b, s.Field)
		if s.By != nil {
			b.WriteString(" by ")
			writeField(b, *s.By)
		}
	}
}

func writeFieldList(b *strings.Builder, fs []Field) {
	for i, f := range fs {
		if i > 0 {
			b.WriteString(", ")
		}
		writeField(b, f)
	}
}

func writeStats(b *strings.Builder, s *Stats) {
	b.WriteString(stageStats + " ")
	for i, a := range s.Aggs {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Expr != nil {
			writeAggExpr(b, a.Expr, 0)
		} else {
			writeAggCall(b, a)
		}
		if a.Explicit {
			b.WriteString(" as ")
			writeField(b, Field{Name: a.Alias})
		}
	}
	if len(s.By) > 0 {
		b.WriteString(" by ")
		writeFieldList(b, s.By)
	}
	if s.Span != nil {
		if s.Span.Auto {
			b.WriteString(" span auto")
		} else {
			b.WriteString(" span " + formatDuration(s.Span.Dur))
		}
	}
}

func writeAggCall(b *strings.Builder, a Agg) {
	b.WriteString(a.Fn + "(")
	if a.Field != nil {
		writeField(b, *a.Field)
	}
	if a.Arg != nil {
		b.WriteString(formatNumber(*a.Arg))
	}
	b.WriteByte(')')
}

// aggPrecedence orders arithmetic: + - bind loosest, then * /, then calls and numbers.
func aggPrecedence(e *AggExpr) int {
	switch e.Op {
	case "+", "-":
		return 1
	case "*", "/":
		return 2
	}
	return 3
}

// writeAggExpr renders arithmetic with minimal parentheses: a child binding looser than
// minPrec is parenthesized; right operands need a strictly tighter binding (left-associative).
func writeAggExpr(b *strings.Builder, e *AggExpr, minPrec int) {
	prec := aggPrecedence(e)
	if prec < minPrec {
		b.WriteByte('(')
		defer b.WriteByte(')')
	}
	switch {
	case e.Call != nil:
		writeAggCall(b, *e.Call)
	case e.Num != nil:
		b.WriteString(formatNumber(*e.Num))
	default:
		writeAggExpr(b, e.Left, prec)
		b.WriteString(" " + e.Op + " ")
		writeAggExpr(b, e.Right, prec+1)
	}
}

// aggExprText is the canonical text of arithmetic, its default column name.
func aggExprText(e *AggExpr) string {
	var b strings.Builder
	writeAggExpr(&b, e, 0)
	return b.String()
}
