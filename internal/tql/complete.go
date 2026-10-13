// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// SuggestionKind classifies an autocomplete suggestion (SPEC §10.7).
type SuggestionKind string

// Suggestion kinds.
const (
	SuggestField    SuggestionKind = "field"
	SuggestValue    SuggestionKind = "value"
	SuggestOperator SuggestionKind = "operator"
	SuggestKeyword  SuggestionKind = "keyword"
	SuggestStage    SuggestionKind = "stage"
	SuggestFunction SuggestionKind = "function"
)

// MaxSuggestions caps the number of suggestions returned.
const MaxSuggestions = 50

// FieldInfo is a known field from the field catalog (GET /fields).
type FieldInfo struct {
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	Numeric bool   `json:"numeric"`
	Hits    uint64 `json:"hits"`
}

// Suggestion is one completion item.
type Suggestion struct {
	Kind       SuggestionKind `json:"kind"`
	Label      string         `json:"label"`
	InsertText string         `json:"insertText"`
	Detail     string         `json:"detail,omitempty"`
}

// Completion is the result of Complete. Suggestions replace the byte range
// [From, To) of the query. When the cursor is in value position, Field names
// the field whose top values the caller should add (kind "value", e.g. from
// facets); level values are already included.
type Completion struct {
	From        int          `json:"from"`
	To          int          `json:"to"`
	Field       string       `json:"field,omitempty"`
	Suggestions []Suggestion `json:"suggestions"`
}

// Complete returns the syntactic suggestions for the cursor at byte offset pos
// in q (SPEC §10.7). src is the default source; a `source` clause in q wins.
// fields are catalog fields (properties); built-ins are added automatically.
func Complete(q string, pos int, src model.Source, fields []FieldInfo) Completion {
	if len(q) > MaxQueryBytes {
		q = q[:MaxQueryBytes]
	}
	pos = max(0, min(pos, len(q)))
	for pos > 0 && pos < len(q) && !utf8.RuneStart(q[pos]) {
		pos--
	}
	start, end := wordBounds(q, pos)
	cp := &completer{q: q, src: sourceOf(q, src), fields: fields}
	toks := tokensBefore(q, start)
	cp.suggest(toks)
	return Completion{From: start, To: end, Field: cp.field, Suggestions: filterPrefix(cp.out, q[start:pos])}
}

func isCompletionWordByte(c byte) bool {
	return c >= 0x80 || (c > ' ' && !strings.ContainsRune("()[],|:=<>!'\"`", rune(c)))
}

func wordBounds(q string, pos int) (int, int) {
	start, end := pos, pos
	for start > 0 && isCompletionWordByte(q[start-1]) {
		start--
	}
	for end < len(q) && isCompletionWordByte(q[end]) {
		end++
	}
	return start, end
}

func tokensBefore(q string, end int) []token {
	toks := lexer{src: q[:end]}.tokens()
	return toks[:len(toks)-1] // drop EOF
}

func sourceOf(q string, def model.Source) model.Source {
	p := newParser(q)
	tmp := &Query{}
	if err := p.parseSource(tmp); err == nil && tmp.Source != "" {
		return tmp.Source
	}
	if !def.Valid() {
		return model.SourceLogs
	}
	return def
}

type completer struct {
	q      string
	src    model.Source
	fields []FieldInfo
	field  string
	out    []Suggestion
}

func (cp *completer) add(kind SuggestionKind, label, insert, detail string) {
	cp.out = append(cp.out, Suggestion{Kind: kind, Label: label, InsertText: insert, Detail: detail})
}

func (cp *completer) suggest(toks []token) {
	if len(toks) == 0 {
		cp.add(SuggestKeyword, kwSource, "source ", "choose logs, spans or metrics")
		cp.termStart()
		return
	}
	last := toks[len(toks)-1]
	if isKw(last, kwSource) && len(toks) == 1 {
		for _, s := range sources() {
			cp.add(SuggestKeyword, string(s), string(s)+" | ", "source")
		}
		return
	}
	pipe := -1
	for i, t := range toks {
		if t.Kind == tPipe {
			pipe = i
		}
	}
	if pipe >= 0 && (pipe != 2 || !isKw(toks[0], kwSource)) {
		cp.stage(toks, pipe)
		return
	}
	cp.filter(toks)
}

// termStart suggests the start of a filter term.
func (cp *completer) termStart() {
	cp.fieldSuggestions()
	for _, f := range funcNames() {
		cp.add(SuggestFunction, f, f+"(", "function")
	}
	cp.add(SuggestKeyword, kwNot, "not ", "negation")
}

func (cp *completer) fieldSuggestions() {
	s := schemaFor(cp.src)
	seen := map[string]bool{}
	for _, b := range s.builtins {
		seen[strings.ToLower(b.name)] = true
		cp.add(SuggestField, b.name, b.name, "built-in")
	}
	fs := append([]FieldInfo(nil), cp.fields...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].Hits > fs[j].Hits })
	for _, f := range fs {
		if seen[strings.ToLower(f.Name)] || f.Name == "" {
			continue
		}
		seen[strings.ToLower(f.Name)] = true
		detail := "property"
		if f.Numeric {
			detail = "number"
		}
		var b strings.Builder
		writeField(&b, Field{Name: f.Name})
		cp.add(SuggestField, f.Name, b.String(), detail)
	}
}

func isFieldTok(t token) bool { return t.Kind == tIdent || t.Kind == tBacktick }

func (cp *completer) filter(toks []token) {
	last := toks[len(toks)-1]
	var prev token
	if len(toks) > 1 {
		prev = toks[len(toks)-2]
	}
	switch {
	case last.Kind == tColon && isFieldTok(prev):
		cp.valueContext(prev)
	case (last.Kind == tOp || isKw(last, kwLike)) && isFieldTok(prev):
		cp.valueContext(prev)
	case last.Kind == tLParen, last.Kind == tAndAnd, last.Kind == tOrOr, last.Kind == tBang,
		isKw(last, kwAnd), isKw(last, kwOr), isKw(last, kwNot), last.Kind == tPipe:
		cp.termStart()
	default:
		cp.afterTerm(last)
	}
}

// valueContext asks the caller for values of the field and adds level names.
func (cp *completer) valueContext(f token) {
	cp.field = f.Text
	r, err := schemaFor(cp.src).resolve(fieldOf(f))
	if err == nil && r.typ() == typeLevel {
		for _, l := range model.AllLevels() {
			name := strings.ToLower(l.String())
			cp.add(SuggestValue, name, name, "level")
		}
	}
	cp.add(SuggestValue, "*", "*", "field exists")
}

func (cp *completer) afterTerm(last token) {
	if isFieldTok(last) {
		for _, op := range []string{":", "=", "!=", ">", ">=", "<", "<=", "in", "like"} {
			cp.add(SuggestOperator, op, op, "operator")
		}
	}
	for _, kw := range []string{kwAnd, kwOr, kwNot} {
		cp.add(SuggestKeyword, kw, kw+" ", "boolean")
	}
	cp.add(SuggestOperator, "|", "| ", "add a stage")
	cp.fieldSuggestions()
}

// stage suggests inside a pipe stage starting after toks[pipe].
func (cp *completer) stage(toks []token, pipe int) {
	rest := toks[pipe+1:]
	if len(rest) == 0 {
		for _, s := range stageNames() {
			cp.add(SuggestStage, s, s+" ", "stage")
		}
		return
	}
	switch strings.ToLower(rest[0].Text) {
	case stageStats:
		cp.statsStage(rest)
	case stageWhere:
		if len(rest) == 1 {
			cp.termStart()
			return
		}
		cp.filter(rest[1:])
	case stageSort:
		cp.sortStage(toks, pipe, rest)
	case stageFields:
		if last := rest[len(rest)-1]; len(rest) == 1 || last.Kind == tComma {
			cp.fieldSuggestions()
		}
	case stageTop:
		if len(rest) == 2 || isKw(rest[len(rest)-1], kwBy) {
			cp.fieldSuggestions()
		} else if len(rest) == 3 {
			cp.add(SuggestKeyword, kwBy, "by ", "top per value of another field")
		}
	}
}

func (cp *completer) statsStage(rest []token) {
	last := rest[len(rest)-1]
	inBy, inSpan := false, false
	for _, t := range rest {
		inBy = inBy || isKw(t, kwBy)
		inSpan = inSpan || isKw(t, kwSpan)
	}
	switch {
	case inSpan:
		if isKw(last, kwSpan) {
			for _, d := range []string{"auto", "1m", "5m", "15m", "1h", "1d"} {
				cp.add(SuggestKeyword, d, d, "bucket size")
			}
		}
	case last.Kind == tLParen || ((isKw(last, kwBy) || last.Kind == tComma) && inBy):
		cp.fieldSuggestions()
	case len(rest) == 1 || last.Kind == tComma:
		for _, a := range aggNames() {
			cp.add(SuggestFunction, a, a+"(", "aggregate")
		}
	case last.Kind == tRParen || (inBy && isFieldTok(last)):
		for _, kw := range []string{kwBy, kwSpan, kwAs} {
			if !inBy || kw != kwBy {
				cp.add(SuggestKeyword, kw, kw+" ", "stats")
			}
		}
		cp.add(SuggestOperator, "|", "| ", "add a stage")
	}
}

// sortStage suggests sort keys from the preceding stats stage.
func (cp *completer) sortStage(toks []token, pipe int, rest []token) {
	last := rest[len(rest)-1]
	if len(rest) > 1 && last.Kind != tComma {
		cp.add(SuggestKeyword, kwAsc, kwAsc, "ascending")
		cp.add(SuggestKeyword, kwDesc, kwDesc, "descending")
		return
	}
	prior, err := Parse(cp.q[:toks[pipe].Pos])
	if err != nil {
		return
	}
	for _, st := range prior.Stages {
		s, ok := st.(*Stats)
		if !ok {
			continue
		}
		for _, a := range s.Aggs {
			cp.add(SuggestField, a.Alias, a.Alias, "aggregate")
		}
		for _, f := range s.By {
			cp.add(SuggestField, f.Name, f.Name, "group")
		}
		if s.Span != nil {
			cp.add(SuggestField, BucketColumn, BucketColumn, "time bucket")
		}
	}
}

func filterPrefix(in []Suggestion, prefix string) []Suggestion {
	out := []Suggestion{}
	p := strings.ToLower(prefix)
	for _, s := range in {
		if strings.HasPrefix(strings.ToLower(s.Label), p) {
			out = append(out, s)
			if len(out) == MaxSuggestions {
				break
			}
		}
	}
	return out
}
