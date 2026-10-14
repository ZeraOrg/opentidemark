// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

func TestNewReaderValidates(t *testing.T) {
	if _, err := NewReader(nil, ReaderConfig{Database: "bad-name", QueryTimeout: time.Second}); err == nil {
		t.Error("invalid database accepted")
	}
	if _, err := NewReader(nil, ReaderConfig{Database: "tidemark"}); err == nil {
		t.Error("zero timeout accepted")
	}
	r, err := NewReader(nil, ReaderConfig{Database: "tidemark", QueryTimeout: time.Second})
	if err != nil || r.cfg.MaxMemoryBytes != DefaultReaderMemory {
		t.Errorf("defaults %+v %v", r, err)
	}
	if tbl, col := r.table(model.SourceMetrics); tbl != "tidemark.metric_points" || col != "timestamp" {
		t.Errorf("metrics table %s %s", tbl, col)
	}
	if tbl, col := r.table(model.SourceSpans); tbl != "tidemark.spans" || col != "start_time" {
		t.Errorf("spans table %s %s", tbl, col)
	}
}

func TestBindArgsKeepsSubSecondPrecision(t *testing.T) {
	ts := time.Date(2026, 10, 13, 11, 58, 1, 123456789, time.FixedZone("x", 3600))
	got := bindArgs(model.SourceLogs, []any{ts, "s", 3})
	if got[0] != "2026-10-13 10:58:01.123456789" || got[1] != "s" || got[2] != 3 {
		t.Errorf("logs args %v", got)
	}
	if got := bindArgs(model.SourceMetrics, []any{ts}); got[0] != "2026-10-13 10:58:01.123" {
		t.Errorf("metrics args %v", got)
	}
}

func TestJSONValue(t *testing.T) {
	id := uuid.MustParse("01890000-0000-7000-8000-000000000000")
	loc := time.FixedZone("x", 7200)
	tests := []struct {
		in   any
		typ  tql.ColumnType
		want any
	}{
		{nil, tql.ColumnString, nil},
		{id, tql.ColumnString, id.String()},
		{math.NaN(), tql.ColumnNumber, nil},
		{float32(1.5), tql.ColumnNumber, 1.5},
		{uint64(1 << 60), tql.ColumnNumber, "1152921504606846976"},
		{uint64(7), tql.ColumnNumber, uint64(7)},
		{int64(-1 << 60), tql.ColumnNumber, "-1152921504606846976"},
		{int64(-3), tql.ColumnNumber, int64(-3)},
		{`{"a":1}`, tql.ColumnJSON, json.RawMessage(`{"a":1}`)},
		{`{bad`, tql.ColumnJSON, `{bad`},
		{uint8(3), tql.ColumnNumber, uint8(3)},
	}
	for _, tt := range tests {
		got := jsonValue(tt.in, tt.typ)
		if gb, _ := json.Marshal(got); string(gb) != string(must(json.Marshal(tt.want))) {
			t.Errorf("jsonValue(%v) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
	if got := jsonValue(time.Date(2026, 1, 1, 2, 0, 0, 0, loc), tql.ColumnTime).(time.Time); got.Location() != time.UTC {
		t.Errorf("time not UTC: %v", got)
	}
	arr := jsonValue([]float64{1, math.Inf(1)}, tql.ColumnArray).([]any)
	if arr[0] != 1.0 || arr[1] != nil {
		t.Errorf("array %v", arr)
	}
	m := jsonValue(map[string]float64{"a": math.NaN(), "b": 2}, tql.ColumnMap).(map[string]any)
	if m["a"] != nil || m["b"] != 2.0 {
		t.Errorf("map %v", m)
	}
	if epochMillis(time.Unix(2, 0)) != int64(2000) || epochMillis("x") != "x" {
		t.Error("epochMillis")
	}
}

func must(b []byte, _ error) []byte { return b }

func TestDecodeHelpers(t *testing.T) {
	if m := decodeObject(`{"n":12345678901234567890,"s":"x"}`); m["n"].(json.Number).String() != "12345678901234567890" {
		t.Errorf("number precision lost: %v", m)
	}
	for _, bad := range []string{"", "[1]", "{", "null"} {
		if m := decodeObject(bad); m == nil || len(m) != 0 {
			t.Errorf("decodeObject(%q) = %v", bad, m)
		}
	}
	if l := decodeList[model.SpanLink](`[{"traceId":"a","spanId":"b"}]`); len(l) != 1 || l[0].SpanID != "b" {
		t.Errorf("links %v", l)
	}
	if l := decodeList[model.SpanEvent]("{}"); l == nil || len(l) != 0 {
		t.Errorf("bad list %v", l)
	}
	attrs := typedAttributes(map[string]string{"s": "x", "n": "42", "b": "true", "f": "1e400"},
		map[string]float64{"n": 42, "b": 1, "f": math.MaxFloat64})
	if attrs["s"] != "x" || attrs["n"] != json.Number("42") || attrs["b"] != true {
		t.Errorf("typed attributes %v", attrs)
	}
}

func TestResultShapeHelpers(t *testing.T) {
	if countGroupColumns([]string{"bucket", "g0", "g1", "a0", "g", "gx"}) != 2 {
		t.Error("countGroupColumns")
	}
	cols := make([]tql.Column, 0)
	for _, n := range strings.Split(logEventSelect, ", ") {
		cols = append(cols, tql.Column{Name: n})
	}
	if !isLogEventColumns(cols) || isLogEventColumns(cols[:3]) {
		t.Error("isLogEventColumns")
	}
	cols[1].Name = "other"
	if isLogEventColumns(cols) {
		t.Error("renamed column accepted")
	}
	spans := &tql.Compiled{Source: model.SourceSpans, Columns: []tql.Column{{Name: "start_time"}, {Name: "trace_id"}, {Name: "span_id"}}}
	if ti, ii := cursorColumns(spans); ti != 0 || ii != 2 {
		t.Errorf("span cursor columns %d %d", ti, ii)
	}
	proj := &tql.Compiled{Source: model.SourceLogs, Columns: []tql.Column{{Name: "timestamp"}, {Name: "id"}, {Name: "id"}}}
	if _, ii := cursorColumns(proj); ii != 1 {
		t.Errorf("projection id column %d", ii)
	}
	if clamp(-1, 0, 5) != 0 || clamp(9, 0, 5) != 5 || mod(-7, 5) != 3 {
		t.Error("clamp/mod")
	}
}

func TestEmptyHistogram(t *testing.T) {
	from := time.Date(2026, 10, 13, 11, 7, 30, 0, time.UTC)
	h := emptyHistogram(model.SourceLogs, store.TimeRange{From: from, To: from.Add(time.Hour)}, 10*time.Minute)
	if len(h.Buckets) != 7 || !h.Buckets[0].T.Equal(time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("buckets %d first %v", len(h.Buckets), h.Buckets[0].T)
	}
	if len(h.Buckets[0].Counts) != 6 || h.Buckets[0].Counts["Fatal"] != 0 {
		t.Errorf("counts %v", h.Buckets[0].Counts)
	}
	sub := emptyHistogram(model.SourceSpans, store.TimeRange{From: from, To: from.Add(500 * time.Millisecond)}, time.Second)
	if len(sub.Buckets) != 1 || len(sub.Buckets[0].Counts) != 3 {
		t.Errorf("sub-second range %+v", sub)
	}
	if keys := HistogramKeys(model.SourceMetrics); len(keys) != 1 || keys[0] != HistogramCountKey {
		t.Errorf("metrics keys %v", keys)
	}
	if histogramGroup(model.SourceMetrics) != "'count'" || !strings.Contains(histogramGroup(model.SourceSpans), "status_code") {
		t.Error("histogram group")
	}
}
