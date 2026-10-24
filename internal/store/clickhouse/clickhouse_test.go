// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

func TestFlattenRules(t *testing.T) {
	props, _ := jsonx.DecodeObject([]byte(`{
		"Request": {"Path": "/a", "Headers": {"Accept": "json"}},
		"already.dotted": "x",
		"n": 42, "f": 1.50, "big": 12345678901234567890, "neg": -7, "exp": 1e300,
		"ok": true, "no": false,
		"arr": [1, "two", {"three": 3}],
		"nil": null,
		"s": "plain"
	}`), jsonx.MaxDepth)
	f := Flatten(props)
	wantStr := map[string]string{
		"Request.Path": "/a", "Request.Headers.Accept": "json", "already.dotted": "x",
		"n": "42", "f": "1.5", "big": "12345678901234567890", "neg": "-7", "exp": "1e+300",
		"ok": "true", "no": "false", "arr": `[1,"two",{"three":3}]`, "s": "plain",
	}
	if len(f.Strings) != len(wantStr) {
		t.Errorf("keys %v", f.Strings)
	}
	for k, v := range wantStr {
		if f.Strings[k] != v {
			t.Errorf("props[%q] = %q want %q", k, f.Strings[k], v)
		}
	}
	wantNum := map[string]float64{"n": 42, "f": 1.5, "big": 12345678901234567890, "neg": -7, "exp": 1e300, "ok": 1, "no": 0}
	if len(f.Numbers) != len(wantNum) {
		t.Errorf("num keys %v", f.Numbers)
	}
	for k, v := range wantNum {
		if f.Numbers[k] != v {
			t.Errorf("props_num[%q] = %v want %v", k, f.Numbers[k], v)
		}
	}
	if _, ok := f.Strings["nil"]; ok || f.Truncated {
		t.Error("null kept or truncated")
	}
}

func TestFlattenGoTypes(t *testing.T) {
	f := Flatten(map[string]any{"i": int64(5), "d": 2.25, "u": uint64(9), "b": []byte{1}, "nan": math.NaN(),
		"inf": math.Inf(1), "m": map[string]any{"x": int64(1)}, "arr": []any{math.NaN()}, "f32": float32(0.5)})
	if f.Strings["i"] != "5" || f.Numbers["i"] != 5 || f.Strings["d"] != "2.25" || f.Strings["u"] != "9" ||
		f.Strings["b"] != "AQ==" || f.Strings["nan"] != "NaN" || f.Strings["m.x"] != "1" || f.Strings["arr"] != `["NaN"]` ||
		f.Numbers["f32"] != 0.5 {
		t.Errorf("%+v", f)
	}
	if _, ok := f.Numbers["nan"]; ok {
		t.Error("NaN in props_num")
	}
	if _, ok := f.Numbers["inf"]; ok {
		t.Error("Inf in props_num")
	}
}

func TestFlattenLimits(t *testing.T) {
	props := map[string]any{}
	for i := range 600 {
		props["k"+strconv.Itoa(1000+i)] = i
	}
	f := Flatten(props)
	if len(f.Strings) != MaxPropertyKeys+1 || !f.Truncated || f.Strings["_tidemark.truncated"] != "true" {
		t.Errorf("keys %d truncated %v", len(f.Strings), f.Truncated)
	}
	if _, ok := f.Strings["k1000"]; !ok {
		t.Error("deterministic order: first sorted key missing")
	}
	long := strings.Repeat("é", MaxValueBytes)
	f = Flatten(map[string]any{"long": long})
	if !f.Truncated || len(f.Strings["long"]) > MaxValueBytes || !strings.HasSuffix(f.Strings["long"], TruncatedSuffix) {
		t.Errorf("len %d", len(f.Strings["long"]))
	}
	deep := map[string]any{}
	cur := deep
	for range maxFlattenDepth + 5 {
		next := map[string]any{}
		cur["d"] = next
		cur = next
	}
	cur["leaf"] = 1
	if f := Flatten(deep); len(f.Strings) != 1 {
		t.Errorf("deep %d", len(f.Strings))
	}
}

func TestNumberText(t *testing.T) {
	tests := map[string]string{"1.50": "1.5", "100": "100", "18446744073709551615": "18446744073709551615", "1E3": "1000", "abc": "abc"}
	for in, want := range tests {
		got, _, _ := numberText(json.Number(in))
		if got != want {
			t.Errorf("numberText(%s) = %s want %s", in, got, want)
		}
	}
}

func TestEventRow(t *testing.T) {
	id := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")
	ts := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	ev := model.Event{ID: id, Timestamp: ts, IngestedAt: ts, Stream: "app", Level: model.Error, Service: "s",
		Message: "m", Properties: map[string]any{"a": map[string]any{"b": 1}, "big": strings.Repeat("x", MaxValueBytes+1)}}
	row := EventRow(ev)
	if len(row) != len(strings.Split(eventColumns, ",")) {
		t.Fatalf("columns %d", len(row))
	}
	if row[4] != "Error" || row[5] != uint8(4) || row[2] != id {
		t.Errorf("row %v", row[:6])
	}
	if !strings.Contains(row[19].(string), `"_tidemark.truncated":true`) || !strings.Contains(row[19].(string), `"a":{"b":1}`) {
		t.Errorf("props_json %.80s", row[19])
	}
	if ev.Properties["_tidemark.truncated"] != nil {
		t.Error("input mutated")
	}
	empty := EventRow(model.Event{})
	if empty[19] != "{}" {
		t.Errorf("empty props_json %v", empty[19])
	}
}

func TestSpanAndMetricRows(t *testing.T) {
	s := model.Span{Name: "n", Duration: 1500 * time.Microsecond, Attributes: map[string]any{"http.status_code": int64(500)},
		Events: []model.SpanEvent{{Name: "e"}}, Links: []model.SpanLink{{TraceID: "t"}}}
	row := SpanRow(s)
	if len(row) != len(strings.Split(spanColumns, ",")) || row[8] != uint64(1500000) ||
		row[13].(map[string]float64)["http.status_code"] != 500 || row[14] != "{}" ||
		!strings.Contains(row[15].(string), `"name":"e"`) || !strings.Contains(row[16].(string), `"traceId":"t"`) {
		t.Errorf("span row %v", row)
	}
	if neg := SpanRow(model.Span{Duration: -time.Second}); neg[8] != uint64(0) {
		t.Error("negative duration")
	}
	p := model.MetricPoint{Name: "m", Service: "svc", Attributes: map[string]any{"b": "2", "a": int64(1)}}
	mrow := MetricRow(p)
	if len(mrow) != len(strings.Split(metricColumns, ",")) || len(mrow[14].([]float64)) != 0 || len(mrow[15].([]uint64)) != 0 {
		t.Errorf("metric row %v", mrow)
	}
	if mrow[17] != SeriesID("m", "svc", map[string]string{"a": "1", "b": "2"}) {
		t.Error("series id mismatch")
	}
	if got := mrow[19].(time.Time); !got.Equal(time.Unix(0, 0)) {
		t.Errorf("unknown start time = %v, want the epoch", got)
	}
	start := time.Date(2026, 10, 1, 8, 0, 0, 123456789, time.UTC)
	p.StartTime = start
	if got := MetricRow(p)[19].(time.Time); !got.Equal(start) {
		t.Errorf("start time = %v, want %v", got, start)
	}
}

func TestSummarySeriesIgnoresQuantileValues(t *testing.T) {
	point := func(kind string, q99 float64) model.MetricPoint {
		return model.MetricPoint{Name: "gc.pause", Service: "api", Kind: kind,
			Attributes: map[string]any{"pool": "gen0", "quantile.0.5": q99 / 2, "quantile.0.99": q99}}
	}
	want := SeriesID("gc.pause", "api", map[string]string{"pool": "gen0"})
	tests := []struct {
		name     string
		a, b     model.MetricPoint
		same     bool
		identity uint64 // 0 = not checked
	}{
		{"summary points with different quantile values", point(model.MetricSummary, 12), point(model.MetricSummary, 30), true, want},
		{"quantile-like attributes of other kinds stay in the identity", point(model.MetricGauge, 12), point(model.MetricGauge, 30), false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, b := MetricRow(tc.a)[17].(uint64), MetricRow(tc.b)[17].(uint64)
			if (a == b) != tc.same {
				t.Errorf("series ids %d and %d: same = %v, want %v", a, b, a == b, tc.same)
			}
			if tc.identity != 0 && a != tc.identity {
				t.Errorf("series id %d, want %d (name, pool, service)", a, tc.identity)
			}
			if attrs := MetricRow(tc.a)[16].(map[string]string); attrs["quantile.0.99"] != "12" {
				t.Errorf("quantile values must stay stored: %v", attrs)
			}
		})
	}
}

func TestSeriesID(t *testing.T) {
	a := SeriesID("cpu", "api", map[string]string{"host": "a", "core": "1"})
	b := SeriesID("cpu", "api", map[string]string{"core": "1", "host": "a"})
	c := SeriesID("cpu", "api", map[string]string{"core": "1", "host": "b"})
	d := SeriesID("cpu", "web", map[string]string{"core": "1", "host": "a"})
	if a != b || a == c || a == d {
		t.Errorf("%d %d %d %d", a, b, c, d)
	}
}

func TestMigrations(t *testing.T) {
	if _, err := LoadMigrations("bad-name"); err != ErrInvalidDatabase {
		t.Errorf("bad db: %v", err)
	}
	migs, err := LoadMigrations("tm_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 7 || migs[0].Version != 1 || migs[3].Version != 4 || len(migs[3].Statements) != 3 {
		t.Fatalf("migrations %+v", migs)
	}
	for _, m := range migs {
		for _, s := range m.Statements {
			if strings.Contains(s, "{db}") || strings.HasSuffix(s, ";") || strings.HasPrefix(s, "--") {
				t.Errorf("%s: bad statement %.60q", m.Name, s)
			}
		}
	}
	if !strings.Contains(migs[0].Statements[0], "CREATE TABLE IF NOT EXISTS tm_test.events") {
		t.Errorf("substitution: %.80s", migs[0].Statements[0])
	}
	got := SplitStatements("-- c\nSELECT 1;\n\nSELECT 2;\n")
	if len(got) != 2 || got[0] != "SELECT 1" || got[1] != "SELECT 2" {
		t.Errorf("%q", got)
	}
}

func TestValidDatabase(t *testing.T) {
	for _, ok := range []string{"tidemark", "tm_1", "_x"} {
		if !ValidDatabase(ok) {
			t.Errorf("%q", ok)
		}
	}
	for _, bad := range []string{"", "1abc", "a-b", "a;drop", "a.b", strings.Repeat("a", 65)} {
		if ValidDatabase(bad) {
			t.Errorf("%q", bad)
		}
	}
	if _, err := NewWriter(nil, "x-y"); err != ErrInvalidDatabase {
		t.Error("writer accepted bad db")
	}
	if _, err := NewRetentionManager(nil, "x y"); err != ErrInvalidDatabase {
		t.Error("retention accepted bad db")
	}
}
