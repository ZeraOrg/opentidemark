// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

func TestParseCanonical(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{``, `*`},
		{`*`, `*`},
		{`   `, `*`},
		{`timeout`, `"timeout"`},
		{`"connection refused"`, `"connection refused"`},
		{`a b c`, `"a" and "b" and "c"`},
		{`a or b and c`, `"a" or "b" and "c"`},
		{`a || b && !c`, `"a" or "b" and not "c"`},
		{`not not a`, `not not "a"`},
		{`(a or b) c`, `("a" or "b") and "c"`},
		{`* and level:error`, `* and level:"error"`},
		{`level:error`, `level:"error"`},
		{`level: error`, `level:"error"`},
		{`LEVEL:Error`, `LEVEL:"Error"`},
		{`x:"a b"`, `x:"a b"`},
		{`x:'it\'s'`, `x:"it's"`},
		{`x:*`, `x:*`},
		{`x:ab*`, `x:ab*`},
		{`x:true and y:FALSE and z:null`, `x:true and y:false and z:null`},
		{`x:12.5e3`, `x:12.5e3`},
		{`x:-3`, `x:-3`},
		{`x:/a\/b/`, `x:/a\/b/`},
		{`x:/api/v1`, `x:"/api/v1"`},
		{`x:>=5`, `x:>= 5`},
		{`x: < 'b'`, `x:< "b"`},
		{`x:(1 or 2)`, `x:1 or x:2`},
		{`x:(a and b or c)`, `x:"a" and x:"b" or x:"c"`},
		{`x:(a OR "b c")`, `x:"a" or x:"b c"`},
		{`x = 'a' ci`, `x = "a" ci`},
		{`x == 1 and y <> 2 and z != 3`, `x == 1 and y <> 2 and z != 3`},
		{`x like '%a%' and y not like 'b'`, `x like "%a%" and y not like "b"`},
		{`x in [1, 'a', b] and y not in [true]`, `x in [1, "a", "b"] and y not in [true]`},
		{`contains(message, 'x')`, `contains(message, "x")`},
		{`HAS(x)`, `has(x)`},
		{`match(x, '^a\d+')`, `match(x, "^a\\d+")`},
		{"`weird key`:1", "`weird key`:1"},
		{"`a``b`:1", "`a``b`:1"},
		{`@t > now-15m`, `@t > "now-15m"`},
		{`source spans | duration > 250ms`, `source spans | duration > 250ms`},
		{`SOURCE Metrics`, `source metrics | *`},
		{`source logs | x`, `"x"`},
		{`source:x`, `source:"x"`},
		{`source spans x`, `"source" and "spans" and "x"`},
		{`x:$svc and y = $v and z in [$a, 1]`, `x:$svc and y = $v and z in [$a, 1]`},
		{`* | stats count()`, `* | stats count()`},
		{`* | STATS Count() AS n, p95(x) by a, b span 5m`, `* | stats count() as n, p95(x) by a, b span 5m`},
		{`* | stats count() span auto`, `* | stats count() span auto`},
		{`* | stats count() span 90m`, `* | stats count() span 90m`},
		{`* | stats count() span 1d`, `* | stats count() span 1d`},
		{`* | stats count() by a | sort count desc, a`, `* | stats count() by a | sort count desc, a asc`},
		{`* | limit 5`, `* | limit 5`},
		{`* | fields a, b`, `* | fields a, b`},
		{`* | where a:1 or b:2`, `* | where a:1 or b:2`},
		{`* | top 5 a by b`, `* | top 5 a by b`},
		{"* | stats count() as `my count`", "* | stats count() as `my count`"},
		{`* | stats p95( 0.50 ) as q`, `* | stats p95(0.50) as q`},
		{`* | stats p95(5e-1)`, `* | stats p95(5e-1)`},
		{`* | stats sum(a)/sum(b) as r`, `* | stats sum(a) / sum(b) as r`},
		{`* | stats sum(a)-1, 2*count()`, `* | stats sum(a) - 1, 2 * count()`},
		{`* | stats count()-1 as d`, `* | stats count() - 1 as d`},
		{`* | stats (sum(a) + sum(b)) / 2 as m`, `* | stats (sum(a) + sum(b)) / 2 as m`},
		{`* | stats sum(a) - (sum(b) - sum(c)) as m`, `* | stats sum(a) - (sum(b) - sum(c)) as m`},
		{`* | stats (sum(a) - sum(b)) - sum(c) as m`, `* | stats sum(a) - sum(b) - sum(c) as m`},
		{`* | stats sum(a) * (2 / count()) as m`, `* | stats sum(a) * (2 / count()) as m`},
		{`* | stats sum(a) * 2 + max(b) / 4 as m`, `* | stats sum(a) * 2 + max(b) / 4 as m`},
		{`* | stats (count()) as n`, `* | stats count() as n`},
		{`* | stats count() * -1 as n`, `* | stats count() * -1 as n`},
		{`* | stats 2.50*count() as n`, `* | stats 2.50 * count() as n`},
		{`* | stats sum(a)/sum(b)`, `* | stats sum(a) / sum(b)`},
		{`world!`, `"world!"`},
		{`a and (b and c)`, `"a" and ("b" and "c")`},
		{`a or (b or c)`, `"a" or ("b" or "c")`},
		{`not (a or b) and c`, `not ("a" or "b") and "c"`},
		{`(a or b) and (c or d)`, `("a" or "b") and ("c" or "d")`},
		{`a&b`, `"a&b"`},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got := q.String(); got != tt.want {
			t.Errorf("Parse(%q).String() = %s, want %s", tt.in, got, tt.want)
		}
		if q.InterpretedAsText {
			t.Errorf("Parse(%q) unexpectedly interpreted as text", tt.in)
		}
		again, err := Parse(q.String())
		if err != nil || again.String() != q.String() {
			t.Errorf("round trip of %q: %v %q", q.String(), err, again)
		}
	}
}

func TestParseAggNumberArg(t *testing.T) {
	tests := []struct {
		q     string
		num   float64
		alias string
		pos   int
	}{
		{`* | stats p95(0.95)`, 0.95, "p95_0_95", 14},
		{`* | stats p95(5e-1)`, 0.5, "p95_0_5", 14},
		{`* | stats p95(-2)`, -2, "p95_-2", 14},
	}
	for _, tc := range tests {
		q, err := Parse(tc.q)
		if err != nil {
			t.Fatalf("%s: %v", tc.q, err)
		}
		a := q.Stages[0].(*Stats).Aggs[0]
		if a.Field != nil || a.Arg == nil || a.Arg.Kind != ValueNumber || a.Arg.Num != tc.num || a.Alias != tc.alias || a.Arg.Pos != tc.pos {
			t.Errorf("%s: agg %+v arg %+v", tc.q, a, a.Arg)
		}
	}
}

// TestNumberAliasIsIdentifier checks that the default alias of every allowed quantile, also
// with many decimals or in exponent form, lexes as one plain identifier without '.'.
func TestNumberAliasIsIdentifier(t *testing.T) {
	for _, q := range []string{"0.95", "0.5", "0.999999999999", "0.123456789012345678", "1e-3", "9.5e-1", "0.000001", "5e-324"} {
		pq, err := Parse("source metrics | x | stats histogram_quantile(" + q + ")")
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		alias := pq.Stages[0].(*Stats).Aggs[0].Alias
		toks := lexer{src: alias}.tokens()
		if strings.Contains(alias, ".") || len(toks) != 2 || toks[0].Kind != tIdent || toks[0].Text != alias {
			t.Errorf("q %s: alias %q is not one plain identifier (%+v)", q, alias, toks)
		}
	}
}

func TestQuantileAliasAfterStats(t *testing.T) {
	q := "source metrics | name:x | stats histogram_quantile(0.95) by service | where histogram_quantile_0_95 > 200 | " +
		"sort histogram_quantile_0_95 desc"
	pq, err := Parse(q)
	if err != nil {
		t.Fatal(err)
	}
	if got := pq.String(); got != `source metrics | name:"x" | stats histogram_quantile(0.95) by service | where histogram_quantile_0_95 > 200 | sort histogram_quantile_0_95 desc` {
		t.Errorf("canonical %q", got)
	}
	again, err := Parse(pq.String())
	if err != nil || again.String() != pq.String() {
		t.Fatalf("round trip: %v %q", err, again)
	}
	c, err := Compile(pq, goldenOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, " WHERE (a0 > ?) ORDER BY a0 DESC") || c.Columns[1].Name != "histogram_quantile_0_95" {
		t.Errorf("sql %s columns %v", c.SQL, c.Columns)
	}
	if _, err := Parse("source metrics | x | stats histogram_quantile(0.95) | sort -histogram_quantile_0_95"); err == nil {
		t.Error("sort -name is not TQL syntax (descending is 'desc') but parsed")
	}
	explicit, err := Parse("source metrics | x | stats histogram_quantile(0.95) as p95 | where p95 > 200 | sort p95 desc")
	if err != nil {
		t.Fatal(err)
	}
	if c, err := Compile(explicit, goldenOptions()); err != nil || c.Columns[0].Name != "p95" {
		t.Errorf("as p95: %v %v", err, c)
	}
}

func TestParseSource(t *testing.T) {
	tests := map[string]model.Source{
		`x`: model.SourceLogs, `source spans | x`: model.SourceSpans, `source metrics`: model.SourceMetrics,
		`source LOGS | *`: model.SourceLogs,
	}
	for in, want := range tests {
		q, err := Parse(in)
		if err != nil || q.Source != want {
			t.Errorf("Parse(%q) source = %v, %v; want %v", in, q, err, want)
		}
	}
}

func TestParseValueKinds(t *testing.T) {
	tests := []struct {
		in      string
		kind    ValueKind
		text    string
		pattern string
		unit    string
	}{
		{`x:abc`, ValueString, "abc", "", ""},
		{`x:"a*b?"`, ValueWildcard, "a*b?", "a%b_", ""},
		{`x:a%_*`, ValueWildcard, "a%_*", `a\%\_%`, ""},
		{`x:a\*b`, ValueString, "a*b", "", ""},
		{`x:a\(b\)`, ValueString, "a(b)", "", ""},
		{`x:C:\temp`, ValueString, `C:\temp`, "", ""},
		{`x:*`, ValueAny, "*", "", ""},
		{`x:"*"`, ValueWildcard, "*", "%", ""},
		{`x:5m`, ValueNumber, "5m", "", "m"},
		{`x:/re/`, ValueRegex, "re", "", ""},
		{`x:$v`, ValueVar, "v", "", ""},
		{`x:$1`, ValueString, "$1", "", ""},
		{`x:NULL`, ValueNull, "NULL", "", ""},
		{`x:"tab\there"`, ValueString, "tab\there", "", ""},
		{`x:"a\qb"`, ValueString, `a\qb`, "", ""},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		m, ok := q.Filter.(*FieldMatch)
		if !ok {
			t.Errorf("Parse(%q): %T", tt.in, q.Filter)
			continue
		}
		v := m.Value
		if v.Kind != tt.kind || v.Text != tt.text || v.Pattern != tt.pattern || v.Unit != tt.unit {
			t.Errorf("Parse(%q) value = %+v", tt.in, v)
		}
	}
}

func TestParsePositions(t *testing.T) {
	q, err := Parse(`level:error and  service = 'x' | stats count() as n by host span 5m | sort n | limit 3`)
	if err != nil {
		t.Fatal(err)
	}
	and := q.Filter.(*And)
	fm := and.Left.(*FieldMatch)
	cmp := and.Right.(*Compare)
	checks := []struct {
		n        Node
		pos, end int
	}{
		{and, 0, 30}, {fm, 0, 11}, {fm.Field, 0, 5}, {fm.Value, 6, 11},
		{cmp, 17, 30}, {cmp.Field, 17, 24}, {cmp.Value, 27, 30},
		{q.Stages[0], 33, 67}, {q.Stages[1], 70, 76}, {q.Stages[2], 79, 86},
	}
	for i, c := range checks {
		if p, e := c.n.Bounds(); p != c.pos || e != c.end {
			t.Errorf("check %d (%T): bounds %d..%d want %d..%d", i, c.n, p, e, c.pos, c.end)
		}
	}
	st := q.Stages[0].(*Stats)
	if st.Aggs[0].AliasPos.Pos != 50 || st.Span.Dur.String() != "5m0s" {
		t.Errorf("stats = %+v", st)
	}
}

func TestParseErrorPositions(t *testing.T) {
	tests := []struct {
		in   string
		pos  int
		msg  string
		hint string
	}{
		{`a:(x or)`, 7, "expected a value", ""},
		{`(a or b`, 7, "expected ')'", ""},
		{`foo | statz count()`, 6, "unknown stage", "did you mean 'stats'?"},
		{`source span | x`, 7, "unknown source", "did you mean 'spans'?"},
		{`x | | y`, 4, "expected a stage", ""},
		{`"abc`, 0, "unterminated string", ""},
		{`a = b and c in (1)`, 15, "expected '['", ""},
		{`contains(x, 'a'`, 15, "expected ')'", ""},
		{`* | stats count() span x`, 23, "expected a duration", ""},
		{`* | stats count() span 200y`, 23, "expected a duration", ""},
		{`* | stats count() span 0s`, 23, "invalid span duration", ""},
		{`* | sort 1`, 9, "expected a sort key", ""},
		{`* | top 3 'x'`, 10, "expected a field name", ""},
		{`* | stats count( x`, 18, "expected ')'", ""},
		{`* | stats count() as 'x'`, 21, "expected an alias", ""},
		{`* | stats sum(a) /`, 18, "unknown aggregate", ""},
		{`* | stats sum(a) / -sum(b)`, 19, "unknown aggregate", ""},
		{`* | stats 1 + 2 as n`, 10, "arithmetic needs an aggregate", "e.g. sum(x) / 2"},
		{`* | stats (sum(a) / 2 as n`, 22, "expected ')'", ""},
		{`* | stats sum(a) as x / 2`, 22, "expected '|' or end of query", ""},
		{`* | top 3 a by 'x'`, 15, "expected a field name", ""},
		{`* | limit 99999999999`, 10, "positive integer", ""},
		{`a = )`, 4, "expected a value", ""},
		{`a:1 or or`, 7, "expected an expression", ""},
		{"x:1 \xff", 4, "not valid UTF-8", ""},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		var e *Error
		if !errors.As(err, &e) {
			t.Errorf("Parse(%q) = %v, want *Error", tt.in, err)
			continue
		}
		if e.Pos != tt.pos || !strings.Contains(e.Msg, tt.msg) || !strings.Contains(e.Hint, tt.hint) {
			t.Errorf("Parse(%q) = pos %d %q hint %q; want pos %d %q hint %q", tt.in, e.Pos, e.Msg, e.Hint, tt.pos, tt.msg, tt.hint)
		}
		if !strings.Contains(e.Error(), tt.msg) {
			t.Errorf("Error() = %q", e.Error())
		}
	}
}

func TestParseLimits(t *testing.T) {
	long := strings.Repeat("a ", MaxQueryBytes/2+1)
	if _, err := Parse(long); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("long query: %v", err)
	}
	// "''" is written as `"" and ` in canonical form, so this short query would not round-trip.
	expands := strings.Repeat("'' ", MaxQueryBytes/6)
	var e *Error
	if _, err := Parse(expands); !errors.As(err, &e) || !strings.Contains(e.Msg, "too long in canonical form") ||
		e.Pos != 0 || e.Len != len(expands) {
		t.Errorf("query too long in canonical form: %v", err)
	}
	deep := strings.Repeat("(", MaxDepth+1) + "a" + strings.Repeat(")", MaxDepth+1)
	if _, err := Parse(deep); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Errorf("deep: %v", err)
	}
	if _, err := Parse(strings.Repeat("(", MaxDepth) + "a" + strings.Repeat(")", MaxDepth)); err != nil {
		t.Errorf("max depth should parse: %v", err)
	}
	nots := strings.Repeat("not ", MaxDepth+1) + "a:1"
	if _, err := Parse(nots); err == nil {
		t.Error("deep not should fail")
	}
	vals := make([]string, MaxValues+1)
	for i := range vals {
		vals[i] = "1"
	}
	if _, err := Parse("x in [" + strings.Join(vals, ",") + "]"); err == nil || !strings.Contains(err.Error(), "too many values") {
		t.Errorf("in list: %v", err)
	}
	if _, err := Parse("x:(" + strings.Join(vals, " or ") + ")"); err == nil || !strings.Contains(err.Error(), "too many values") {
		t.Errorf("group: %v", err)
	}
	if _, err := Parse("x in [" + strings.Join(vals[:MaxValues], ",") + "]"); err != nil {
		t.Errorf("max list: %v", err)
	}
	stages := "*" + strings.Repeat(" | limit 1", MaxStages+1)
	if _, err := Parse(stages); err == nil || !strings.Contains(err.Error(), "too many stages") {
		t.Errorf("stages: %v", err)
	}
	by := make([]string, MaxByFields+1)
	for i := range by {
		by[i] = "f" + strconv.Itoa(i)
	}
	if _, err := Parse("* | stats count() by " + strings.Join(by, ", ")); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("by: %v", err)
	}
	aggs := strings.TrimSuffix(strings.Repeat("count(), ", MaxAggregates+1), ", ")
	if _, err := Parse("* | stats " + aggs); err == nil {
		t.Error("too many aggregates should fail")
	}
	inArith := "count() as a, " + strings.TrimSuffix(strings.Repeat("count() + ", MaxAggregates), " + ") + " as b"
	if _, err := Parse("* | stats " + inArith); err == nil || !strings.Contains(err.Error(), "too many aggregates") {
		t.Errorf("aggregates inside arithmetic count against the limit: %v", err)
	}
	terms := "count()" + strings.Repeat(" + 1", MaxAggTerms) + " as n"
	if _, err := Parse("* | stats " + terms); err == nil || !strings.Contains(err.Error(), "too many terms") {
		t.Errorf("terms: %v", err)
	}
	parens := strings.Repeat("(", MaxDepth+1) + "count()" + strings.Repeat(")", MaxDepth+1)
	if _, err := Parse("* | stats " + parens); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Errorf("nesting: %v", err)
	}
}

func TestParseAggArithmetic(t *testing.T) {
	q, err := Parse("* | stats sum(a) / (count() - 1), max(b) by c")
	if err != nil {
		t.Fatal(err)
	}
	st := q.Stages[0].(*Stats)
	a := st.Aggs[0]
	if a.Expr == nil || a.Fn != "" || a.Explicit || a.Alias != "sum(a) / (count() - 1)" {
		t.Fatalf("column = %+v", a)
	}
	if a.Pos != 10 || a.End != 32 || a.AliasPos != a.Position {
		t.Errorf("column position = %+v, alias %+v", a.Position, a.AliasPos)
	}
	var fns []string
	for _, c := range a.Calls() {
		fns = append(fns, c.Fn)
	}
	if strings.Join(fns, ",") != "sum,count" {
		t.Errorf("calls = %v", fns)
	}
	if r := a.Expr.Right; r.Op != "-" || r.Right.Num == nil || r.Right.Num.Num != 1 {
		t.Errorf("right operand = %+v", r)
	}
	if b := st.Aggs[1]; b.Expr != nil || b.Alias != "max_b" || len(b.Calls()) != 1 {
		t.Errorf("plain column = %+v", b)
	}
}

func TestFreeTextFallback(t *testing.T) {
	tests := []struct {
		in       string
		fallback bool
	}{
		{"not", true}, {"foo and", true}, {"or bar", true}, {"x not", true}, {"Hello, world!", true},
		{"  spaced out and  ", true},
		{"foo (bar", false}, {"a:", false}, {"x = ", false}, {"!", true}, {"!=", false},
		{"a && ", false}, {"a !b !", false},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if tt.fallback {
			if err != nil || !q.InterpretedAsText {
				t.Errorf("Parse(%q) = %v, %v; want fallback", tt.in, q, err)
				continue
			}
			ft := q.Filter.(*FreeText)
			if ft.Text != strings.TrimSpace(tt.in) || tt.in[ft.Pos:ft.End] != ft.Text {
				t.Errorf("fallback text %q at %d..%d", ft.Text, ft.Pos, ft.End)
			}
		} else if err == nil {
			t.Errorf("Parse(%q) should fail", tt.in)
		}
	}
}

func TestLexerTokens(t *testing.T) {
	toks := lexer{src: `a.b:1 ( ) [ ] , | = == != <> < <= > >= && || ! 'x' "y" ` + "`z`" + ` * 12ms w*rd`}.tokens()
	want := []tokenKind{tIdent, tColon, tNumber, tLParen, tRParen, tLBrack, tRBrack, tComma, tPipe, tOp, tOp, tOp, tOp,
		tOp, tOp, tOp, tOp, tAndAnd, tOrOr, tBang, tString, tString, tBacktick, tStar, tNumber, tWord, tEOF}
	if len(toks) != len(want) {
		t.Fatalf("got %d tokens: %+v", len(toks), toks)
	}
	for i, k := range want {
		if toks[i].Kind != k {
			t.Errorf("token %d = %+v, want kind %d", i, toks[i], k)
		}
	}
	if toks[24].Unit != "ms" || toks[24].Num != 12 {
		t.Errorf("number token %+v", toks[24])
	}
	for _, tk := range toks {
		if tk.Pos > tk.End {
			t.Errorf("bad bounds %+v", tk)
		}
	}
}
