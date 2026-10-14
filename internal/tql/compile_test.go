// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func compileQ(t *testing.T, q string, o Options) *Compiled {
	t.Helper()
	p, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	c, err := Compile(p, o)
	if err != nil {
		t.Fatalf("Compile(%q): %v", q, err)
	}
	if n := strings.Count(c.SQL, "?"); n != len(c.Args) {
		t.Fatalf("Compile(%q): %d placeholders, %d args", q, n, len(c.Args))
	}
	return c
}

// docExamplesPath is the public TQL reference; its example block equals SPEC §10.1.
var docExamplesPath = filepath.Join("..", "..", "docs", "query-language.md")

// docExamples extracts the "Overview by example" block from docs/query-language.md.
func docExamples(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(docExamplesPath)
	if err != nil {
		t.Fatalf("%s: %v", docExamplesPath, err)
	}
	defer func() { _ = f.Close() }() // read-only file; close error is irrelevant
	var out []string
	state := 0 // 0 before the heading, 1 before fence, 2 inside fence
	comment := regexp.MustCompile(`\s{2,}#.*$`)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case state == 0 && line == "## Overview by example":
			state = 1
		case state == 1 && strings.HasPrefix(line, "```"):
			state = 2
		case state == 2 && strings.HasPrefix(line, "```"):
			return out
		case state == 2:
			out = append(out, strings.TrimSpace(comment.ReplaceAllString(line, "")))
		}
	}
	t.Fatalf("example block not found in %s", docExamplesPath)
	return nil
}

func TestDocExamplesParseAndCompile(t *testing.T) {
	examples := docExamples(t)
	if len(examples) < 18 {
		t.Fatalf("only %d examples found", len(examples))
	}
	for _, ex := range examples {
		c := compileQ(t, ex, goldenOptions())
		if c.InterpretedAsText {
			t.Errorf("%q was interpreted as text", ex)
		}
	}
}

func TestResultKinds(t *testing.T) {
	tests := map[string]ResultKind{
		`level:error`:                     KindEvents,
		`* | fields message`:              KindEvents,
		`* | stats count()`:               KindTable,
		`* | top 3 service`:               KindTable,
		`* | stats count() span 1m`:       KindTimeseries,
		`* | stats count() span auto`:     KindTimeseries,
		`source spans | * | stats rate()`: KindTable,
	}
	for q, want := range tests {
		if c := compileQ(t, q, goldenOptions()); c.Kind != want {
			t.Errorf("%q kind = %s, want %s", q, c.Kind, want)
		}
	}
}

func TestCompileOptionsErrors(t *testing.T) {
	q, _ := Parse("x")
	base, all := goldenOptions(), model.AllStreams()
	bad := []Options{
		{},
		{From: base.To, To: base.From, Streams: all},
		{Database: "bad-db; DROP", From: base.From, To: base.To, Streams: all},
		{From: base.From, To: base.To, Streams: all, IndexedFields: map[model.Source]map[string]string{model.SourceLogs: {"k": "props['x']"}}},
		{From: base.From, To: base.To, Streams: all, AutoSpan: 1500 * time.Millisecond},
		{From: base.From, To: base.To, Streams: all, AutoSpan: -time.Second},
		{Database: base.Database, From: base.From, To: base.To}, // no stream scope (SPEC §15.10)
	}
	for i, o := range bad {
		if _, err := Compile(q, o); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("case %d: err = %v, want ErrInvalidOptions", i, err)
		}
		if _, _, err := CompileWhere(q, o); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("case %d (where): err = %v", i, err)
		}
	}
	if _, err := Compile(nil, base); err == nil {
		t.Error("nil query should fail")
	}
	if err := Check(nil); err == nil {
		t.Error("Check(nil) should fail")
	}
	if err := Check(&Query{Source: "traces"}); err == nil {
		t.Error("unknown source should fail")
	}
	if err := Check(&Query{}); err != nil {
		t.Errorf("empty query: %v", err)
	}
}

func TestCompileLimits(t *testing.T) {
	o := goldenOptions()
	o.Limit, o.MaxRows = 500, 300
	if c := compileQ(t, "x", o); c.Limit != 300 {
		t.Errorf("limit capped by MaxRows: %d", c.Limit)
	}
	if c := compileQ(t, "x | limit 7", o); c.Limit != 7 {
		t.Errorf("stage limit: %d", c.Limit)
	}
	if c := compileQ(t, "* | stats count() by a | limit 9", o); c.Limit != 9 {
		t.Errorf("table limit: %d", c.Limit)
	}
	if c := compileQ(t, "* | top 4 a | limit 2", o); c.Limit != 2 {
		t.Errorf("top limit: %d", c.Limit)
	}
	if c := compileQ(t, "* | top 4 a", o); c.Limit != 4 {
		t.Errorf("top: %d", c.Limit)
	}
	if c := compileQ(t, "x", goldenOptions()); c.Limit != DefaultLimit {
		t.Errorf("default: %d", c.Limit)
	}
}

func TestSpanSelection(t *testing.T) {
	o := goldenOptions()
	if c := compileQ(t, "* | stats count() span auto", o); c.Span != time.Minute {
		t.Errorf("auto for 1h = %s, want 1m", c.Span)
	}
	o.AutoSpan = 10 * time.Second
	if c := compileQ(t, "* | stats count() span auto", o); c.Span != 10*time.Second {
		t.Errorf("explicit auto = %s", c.Span)
	}
	o = goldenOptions()
	o.From = o.To.Add(-365 * 24 * time.Hour)
	if c := compileQ(t, "* | stats count() span auto", o); c.Span != 5*24*time.Hour {
		t.Errorf("auto for 1y = %s", c.Span)
	}
	p, _ := Parse("* | stats count() span 1s")
	_, err := Compile(p, o)
	var e *Error
	if !errors.As(err, &e) || !strings.Contains(e.Msg, "buckets") || e.Pos != 18 {
		t.Errorf("too many buckets: %v", err)
	}
	if got := autoSpan(30*time.Second, DefaultAutoBuckets); got != time.Second {
		t.Errorf("autoSpan(30s) = %s", got)
	}
}

func TestCursorPerSource(t *testing.T) {
	o := goldenOptions()
	o.Cursor = &Cursor{Timestamp: goldenTime, ID: "42"}
	tests := map[string]string{
		"x":                "(timestamp, id) < (?, toUUIDOrZero(?))",
		"source spans | x": "(start_time, span_id) < (?, ?)",
		"source metrics":   "(timestamp, series_id) < (?, toUInt64OrZero(?))",
	}
	for q, want := range tests {
		if c := compileQ(t, q, o); !strings.Contains(c.SQL, want) {
			t.Errorf("%q: %s", q, c.SQL)
		}
	}
	if c := compileQ(t, "* | stats count()", o); strings.Contains(c.SQL, "toUUIDOrZero") {
		t.Errorf("cursor must not apply to stats: %s", c.SQL)
	}
}

func TestExtraFilters(t *testing.T) {
	o := goldenOptions()
	sig, _ := Parse("service:checkout or level >= error")
	o.ExtraFilters = []*Query{sig, nil}
	c := compileQ(t, "x | stats count()", o)
	if !strings.Contains(c.SQL, "(lower(service) = lower(?) OR level_num >= ?)") {
		t.Errorf("extra filter missing: %s", c.SQL)
	}
	where, args, err := CompileWhere(mustParse(t, "x"), o)
	if err != nil || !strings.Contains(where, "level_num >= ?") || strings.Count(where, "?") != len(args) {
		t.Errorf("CompileWhere: %s %v %v", where, args, err)
	}
	star, _ := Parse("*")
	o.ExtraFilters = []*Query{star}
	if c := compileQ(t, "x", o); strings.Count(c.SQL, "AND") != 2 {
		t.Errorf("match-all signal should add nothing: %s", c.SQL)
	}
	spans, _ := Parse("source spans | x")
	staged, _ := Parse("x | limit 1")
	broken := &Query{Filter: &FieldMatch{Field: Field{Name: "@nope"}, Value: Value{Kind: ValueString, Text: "a"}}}
	for _, bad := range []*Query{spans, staged, broken} {
		o.ExtraFilters = []*Query{bad}
		if _, err := Compile(mustParse(t, "x"), o); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("bad extra filter %v: %v", bad, err)
		}
	}
}

func TestIndexedFields(t *testing.T) {
	o := goldenOptions()
	o.IndexedFields = map[model.Source]map[string]string{
		model.SourceLogs:  {"Req.Path": "f_req_path"},
		model.SourceSpans: {"http.route": "f_http_route"},
	}
	c := compileQ(t, "Req.Path:/a and Req.Path:* and not Req.Path:null and Req.Path > 3", o)
	for _, want := range []string{"lower(f_req_path) = lower(?)", "f_req_path != ''", "f_req_path = ''", "props_num[?] > ?"} {
		if !strings.Contains(c.SQL, want) {
			t.Errorf("missing %q in %s", want, c.SQL)
		}
	}
	if c := compileQ(t, "source spans | http.route:/a | stats count() by http.route", o); strings.Count(c.SQL, "f_http_route") != 2 {
		t.Errorf("spans indexed: %s", c.SQL)
	}
	if c := compileQ(t, "other:/a", o); strings.Contains(c.SQL, "f_") {
		t.Errorf("unindexed key used column: %s", c.SQL)
	}
}

func TestFieldsProjection(t *testing.T) {
	c := compileQ(t, "source spans | * | fields duration, name, attrs.x, @timestamp", goldenOptions())
	want := "SELECT start_time, span_id, duration_ns / 1e6 AS f0, name AS f1, attrs[?] AS f2, start_time AS f3 FROM"
	if !strings.HasPrefix(c.SQL, want) {
		t.Errorf("projection: %s", c.SQL)
	}
	names := []string{"start_time", "span_id", "duration", "name", "attrs.x", "@timestamp"}
	types := []ColumnType{ColumnTime, ColumnString, ColumnDuration, ColumnString, ColumnString, ColumnTime}
	for i, col := range c.Columns {
		if col.Name != names[i] || col.Type != types[i] {
			t.Errorf("column %d = %+v", i, col)
		}
	}
	c = compileQ(t, "* | fields level, id", goldenOptions())
	if c.Columns[2].Type != ColumnString || !strings.Contains(c.SQL, "level AS f0, toString(id) AS f1") {
		t.Errorf("level projection: %s %+v", c.SQL, c.Columns)
	}
}

func TestStatsColumns(t *testing.T) {
	c := compileQ(t, "* | stats count() as n, first(level), last(elapsed), avg(elapsed) by level, elapsed span 5m", goldenOptions())
	want := []Column{{BucketColumn, ColumnTime}, {"level", ColumnString}, {"elapsed", ColumnString},
		{"n", ColumnNumber}, {"first_level", ColumnString}, {"last_elapsed", ColumnString}, {"avg_elapsed", ColumnNumber}}
	if len(c.Columns) != len(want) {
		t.Fatalf("columns %+v", c.Columns)
	}
	for i := range want {
		if c.Columns[i] != want[i] {
			t.Errorf("column %d = %+v, want %+v", i, c.Columns[i], want[i])
		}
	}
	c = compileQ(t, "source metrics | * | stats last(value), first(@timestamp) by count", goldenOptions())
	if c.Columns[1].Type != ColumnNumber || c.Columns[0].Type != ColumnNumber || c.Columns[2].Type != ColumnTime {
		t.Errorf("metric columns %+v", c.Columns)
	}
}

func TestHavingOnByFieldKinds(t *testing.T) {
	q := "source spans | * | stats count() by duration, service | where duration > 1s and duration in [5, 2s] and service:a* and count:3"
	c := compileQ(t, q, goldenOptions())
	for _, want := range []string{"g0 > ?", "g0 IN (?, ?)", "g1 ILIKE ?", "a0 = ?"} {
		if !strings.Contains(c.SQL, want) {
			t.Errorf("missing %q in %s", want, c.SQL)
		}
	}
	if c.Args[len(c.Args)-6] != 1000.0 {
		t.Errorf("duration in ms expected, args %v", c.Args)
	}
}

func TestSubstituteAndCompile(t *testing.T) {
	q, err := Parse("level >= error and service:$service | stats count() by service span auto")
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(q); err == nil {
		t.Error("unresolved variable must fail Check")
	}
	s, err := Substitute(q, map[string][]string{"service": {"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `level >= "error" and (service:"a" or service:"b") | stats count() by service span auto`; s.String() != want {
		t.Errorf("substituted = %s", s.String())
	}
	c := compileQ(t, s.String(), goldenOptions())
	if c.Kind != KindTimeseries {
		t.Errorf("kind %s", c.Kind)
	}
	if q.String() == s.String() {
		t.Error("Substitute modified nothing / mutated input")
	}
}

func mustParse(t *testing.T, q string) *Query {
	t.Helper()
	p, err := Parse(q)
	if err != nil {
		t.Fatalf("Parse(%q): %v", q, err)
	}
	return p
}
