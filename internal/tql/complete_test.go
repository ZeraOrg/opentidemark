// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func labels(c Completion) []string {
	out := make([]string, len(c.Suggestions))
	for i, s := range c.Suggestions {
		out[i] = string(s.Kind) + ":" + s.Label
	}
	return out
}

func hasLabel(c Completion, kind SuggestionKind, label string) bool {
	for _, s := range c.Suggestions {
		if s.Kind == kind && s.Label == label {
			return true
		}
	}
	return false
}

func TestComplete(t *testing.T) {
	catalog := []FieldInfo{{Name: "elapsed", Numeric: true, Hits: 10}, {Name: "weird key", Hits: 50}, {Name: "Service", Hits: 99}}
	tests := []struct {
		q         string
		pos       int // -1 = end
		src       model.Source
		field     string
		from, to  int
		present   []string // kind:label
		absent    []string
		exactSize int
	}{
		{q: "", pos: -1, present: []string{"keyword:source", "field:level", "field:elapsed", "function:contains", "keyword:not"}},
		{q: "lev", pos: -1, from: 0, to: 3, present: []string{"field:level"}, absent: []string{"field:message"}},
		{q: "le xyz", pos: 2, from: 0, to: 2, present: []string{"field:level"}},
		{q: "source ", pos: -1, present: []string{"keyword:spans", "keyword:metrics", "keyword:logs"}},
		{q: "source spans | dur", pos: -1, present: []string{"field:duration"}},
		{q: "dur", pos: -1, src: model.SourceSpans, present: []string{"field:duration"}},
		{q: "level:", pos: -1, field: "level", present: []string{"value:error", "value:*"}},
		{q: "level:er", pos: -1, field: "level", from: 6, present: []string{"value:error"}, absent: []string{"value:warning"}},
		{q: "service:", pos: -1, field: "service", present: []string{"value:*"}, absent: []string{"value:error"}},
		{q: "elapsed >= ", pos: -1, field: "elapsed"},
		{q: "x like ", pos: -1, field: "x"},
		{q: "level ", pos: -1, present: []string{"operator::", "operator:>=", "keyword:and", "operator:|"}},
		{q: `"phrase" `, pos: -1, present: []string{"keyword:or", "operator:|"}, absent: []string{"operator::"}},
		{q: "a and ", pos: -1, present: []string{"field:level", "function:has"}},
		{q: "(", pos: -1, present: []string{"field:level"}},
		{q: "x | ", pos: -1, present: []string{"stage:stats", "stage:where", "stage:top"}},
		{q: "x | st", pos: -1, present: []string{"stage:stats"}, absent: []string{"stage:sort"}},
		{q: "x | stats ", pos: -1, present: []string{"function:count", "function:p95"}},
		{q: "x | stats count(), ", pos: -1, present: []string{"function:avg"}},
		{q: "x | stats avg(", pos: -1, present: []string{"field:elapsed"}},
		{q: "x | stats count() ", pos: -1, present: []string{"keyword:by", "keyword:span", "keyword:as"}},
		{q: "x | stats count() by ", pos: -1, present: []string{"field:level"}},
		{q: "x | stats count() by a, ", pos: -1, present: []string{"field:level"}},
		{q: "x | stats count() by a ", pos: -1, present: []string{"keyword:span"}, absent: []string{"keyword:by"}},
		{q: "x | stats count() span ", pos: -1, present: []string{"keyword:auto", "keyword:5m"}},
		{q: "x | stats count() as n, avg(e) by svc span 1m | sort ", pos: -1, present: []string{"field:n", "field:avg_e", "field:svc", "field:@bucket"}},
		{q: "x | stats count() | sort count ", pos: -1, present: []string{"keyword:desc", "keyword:asc"}},
		{q: "x:( | sort ", pos: -1, exactSize: 0},
		{q: "x | where ", pos: -1, present: []string{"field:level"}},
		{q: "x | where level:", pos: -1, field: "level"},
		{q: "x | fields ", pos: -1, present: []string{"field:message"}},
		{q: "x | fields a, ", pos: -1, present: []string{"field:message"}},
		{q: "x | top 5 ", pos: -1, present: []string{"field:message"}},
		{q: "x | top 5 a ", pos: -1, present: []string{"keyword:by"}},
		{q: "x | top 5 a by ", pos: -1, present: []string{"field:level"}},
		{q: "x | limit ", pos: -1, exactSize: 0},
		{q: "wei", pos: -1, present: []string{"field:weird key"}},
		{q: "level", pos: 99, from: 0, to: 5, present: []string{"field:level"}},
		{q: "é", pos: 1, present: nil},
	}
	for _, tt := range tests {
		pos := tt.pos
		if pos == -1 {
			pos = len(tt.q)
		}
		src := tt.src
		if src == "" {
			src = model.SourceLogs
		}
		c := Complete(tt.q, pos, src, catalog)
		if c.Field != tt.field {
			t.Errorf("Complete(%q).Field = %q, want %q", tt.q, c.Field, tt.field)
		}
		if (tt.from != 0 || tt.to != 0) && (c.From != tt.from || (tt.to != 0 && c.To != tt.to)) {
			t.Errorf("Complete(%q) range = %d..%d, want %d..%d", tt.q, c.From, c.To, tt.from, tt.to)
		}
		got := labels(c)
		for _, p := range tt.present {
			if !strings.Contains(strings.Join(got, "\n")+"\n", p+"\n") {
				t.Errorf("Complete(%q) missing %s; got %v", tt.q, p, got)
			}
		}
		for _, a := range tt.absent {
			if strings.Contains(strings.Join(got, "\n")+"\n", a+"\n") {
				t.Errorf("Complete(%q) unexpectedly has %s", tt.q, a)
			}
		}
		if tt.exactSize == 0 && tt.present == nil && tt.field == "" && len(got) != 0 && tt.q != "é" {
			t.Errorf("Complete(%q) = %v, want none", tt.q, got)
		}
	}
}

func TestCompleteFieldDetails(t *testing.T) {
	c := Complete("", 0, "bogus", []FieldInfo{{Name: "weird key", Numeric: true}, {Name: "level"}, {Name: ""}})
	if !hasLabel(c, SuggestField, "level") || !hasLabel(c, SuggestField, "weird key") {
		t.Fatalf("suggestions %v", labels(c))
	}
	for _, s := range c.Suggestions {
		if s.Label == "weird key" && (s.InsertText != "`weird key`" || s.Detail != "number") {
			t.Errorf("weird key suggestion %+v", s)
		}
	}
	long := strings.Repeat("a", MaxQueryBytes+10)
	if c := Complete(long, len(long), model.SourceLogs, nil); c.To > MaxQueryBytes {
		t.Errorf("long query range %d", c.To)
	}
	many := make([]FieldInfo, 200)
	for i := range many {
		many[i] = FieldInfo{Name: "f" + strconv.Itoa(i)}
	}
	if c := Complete("f", 1, model.SourceLogs, many); len(c.Suggestions) != MaxSuggestions {
		t.Errorf("cap: %d", len(c.Suggestions))
	}
}
