// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/store"
)

// toSeries turns rows (bucket time, group values named by names, value) into the series of a
// timeseries result as the store returns them (points [epoch ms, value]).
func toSeries(rows [][]any, names []string) []store.Series {
	var out []store.Series
	index := map[string]int{}
	for _, row := range rows {
		key := map[string]any{}
		for i, n := range names {
			key[n] = row[i+1]
		}
		k := fmt.Sprint(row[1 : 1+len(names)])
		i, ok := index[k]
		if !ok {
			i = len(out)
			index[k] = i
			out = append(out, store.Series{Key: key})
		}
		ts := row[0]
		if t, ok := ts.(time.Time); ok {
			ts = t.UnixMilli()
		}
		out[i].Points = append(out[i].Points, []any{ts, row[len(row)-1]})
	}
	return out
}

func deviationDoc(query, direction string, sigma float64, extra string) string {
	return `{"query":"` + query + `","condition":{"type":"deviation","direction":"` + direction +
		`","sigma":` + FormatValue(sigma) + `,"baseline":"1h"` + extra + `},"window":"5m","every":"1m","channels":["c1"]}`
}

func TestDeviationRuleCanonical(t *testing.T) {
	d, err := ParseRule(json.RawMessage(deviationDoc("level:error | stats count()", "below", 3, `,"minDelta":2`)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(d.Condition)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"deviation","direction":"below","sigma":3,"baseline":"1h0m0s","minDelta":2}`; string(raw) != want {
		t.Errorf("deviation canonical %s want %s", raw, want)
	}
	for _, in := range []string{`{"op":">","threshold":0}`, `{"type":"threshold","op":">","threshold":0}`} {
		th, err := ParseRule(json.RawMessage(`{"query":"x | stats count()","condition":` + in + `,"window":"5m","every":"1m"}`))
		if err != nil {
			t.Fatal(in, err)
		}
		raw, _ := json.Marshal(th.Condition)
		// byte-identical to the encoding before deviation rules existed
		want, _ := json.Marshal(struct {
			Op        string  `json:"op"`
			Threshold float64 `json:"threshold"`
		}{">", 0})
		if string(raw) != string(want) || th.Condition.IsDeviation() {
			t.Errorf("%s: threshold canonical %s want %s", in, raw, want)
		}
	}
}

func TestDeviationRuleValidation(t *testing.T) {
	q := "level:error | stats count()"
	tests := []struct {
		name, doc, field string
	}{
		{"unknown type", `{"query":"` + q + `","condition":{"type":"zscore"},"window":"5m","every":"1m"}`, "condition.type"},
		{"op on deviation", deviationDoc(q, "both", 3, `,"op":">"`), "condition"},
		{"threshold on deviation", deviationDoc(q, "both", 3, `,"threshold":4`), "condition"},
		{"sigma on threshold", `{"query":"` + q + `","condition":{"op":">","threshold":1,"sigma":3},"window":"5m","every":"1m"}`, "condition"},
		{"direction", deviationDoc(q, "sideways", 3, ""), "condition.direction"},
		{"sigma zero", deviationDoc(q, "both", 0, ""), "condition.sigma"},
		{"sigma too big", deviationDoc(q, "both", 101, ""), "condition.sigma"},
		{"negative minDelta", deviationDoc(q, "both", 3, `,"minDelta":-1`), "condition.minDelta"},
		{"window not dividing a day", strings.Replace(deviationDoc(q, "both", 3, ""), `"window":"5m"`, `"window":"7m"`, 1), "window"},
		{"window not whole seconds", strings.Replace(deviationDoc(q, "both", 3, ""), `"window":"5m"`, `"window":"10500ms"`, 1), "window"},
		{"baseline not a multiple", strings.Replace(deviationDoc(q, "both", 3, ""), `"1h"`, `"62m"`, 1), "condition.baseline"},
		{"too few windows", strings.Replace(deviationDoc(q, "both", 3, ""), `"1h"`, `"20m"`, 1), "condition.baseline"},
		{"too many windows", strings.Replace(deviationDoc(q, "both", 3, ""), `"1h"`, `"168h"`, 1), "condition.baseline"},
		{"baseline over 30 days", strings.Replace(strings.Replace(deviationDoc(q, "both", 3, ""), `"1h"`, `"744h"`, 1), `"5m"`, `"24h"`, 1), "condition.baseline"},
		{"where after stats", deviationDoc(q+" | where count > 1", "both", 3, ""), "query"},
		{"limit", deviationDoc(q+" | limit 5", "both", 3, ""), "query"},
	}
	for _, tc := range tests {
		_, err := ParseRule(json.RawMessage(tc.doc))
		var re *RuleError
		if !errors.As(err, &re) || re.Field != tc.field {
			t.Errorf("%s: err %v, want field %s", tc.name, err, tc.field)
		}
	}
	for _, ok := range []string{
		deviationDoc(q, "above", 2.5, ""),
		strings.Replace(deviationDoc("level:error | stats count() by service | sort service", "both", 3, ""),
			`"window"`, `"groupBy":["service"],"window"`, 1),
		strings.Replace(strings.Replace(deviationDoc(q, "both", 3, ""), `"1h"`, `"720h"`, 1), `"5m"`, `"24h"`, 1),
	} {
		if _, err := ParseRule(json.RawMessage(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
}

func TestBaselineRange(t *testing.T) {
	d, err := ParseRule(json.RawMessage(deviationDoc("x | stats count()", "both", 3, "")))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 3, 20, 0, time.UTC)
	from, to := d.BaselineRange(now)
	// now − 5m = 11:58:20, rounded down to 11:55; one hour of 5m windows before it.
	if !to.Equal(time.Date(2026, 10, 13, 11, 55, 0, 0, time.UTC)) || !from.Equal(to.Add(-time.Hour)) {
		t.Errorf("range [%s, %s)", from, to)
	}
	c, err := d.CompileBaseline(now, "tidemark", nil, RuleScope(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Span != 5*time.Minute || c.Limit != MaxBaselineRows {
		t.Errorf("span %s limit %d", c.Span, c.Limit)
	}
}

func TestZeroFills(t *testing.T) {
	tests := map[string]bool{
		"x | stats count()":                      true,
		"x | stats count_distinct(user)":         true,
		"x | stats sum(bytes) by service":        true,
		"x | stats rate()":                       true,
		"x | stats avg(elapsed)":                 false,
		"x | stats stddev(elapsed)":              false,
		"x | stats count() * 2 as twice":         false,
		"source metrics | x | stats rate(value)": false,
	}
	for q, want := range tests {
		d := &RuleDoc{Query: q}
		if got := d.ZeroFills(); got != want {
			t.Errorf("%s: %v want %v", q, got, want)
		}
	}
}

func TestStatsOf(t *testing.T) {
	if statsOf([]float64{1, 2, 3, 4}) != nil {
		t.Error("4 values give a baseline")
	}
	b := statsOf([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	if b == nil || b.mean != 5 || math.Abs(b.stdDev-math.Sqrt(32.0/7)) > 1e-12 || b.samples != 8 {
		t.Fatalf("stats %+v", b)
	}
	got := b.For(1)
	if got.Z == nil || math.Abs(*got.Z-(-4/math.Sqrt(32.0/7))) > 1e-12 {
		t.Errorf("z %+v", got)
	}
	if flat := statsOf([]float64{3, 3, 3, 3, 3}).For(4); flat.Z != nil || flat.StdDev != 0 {
		t.Errorf("flat baseline %+v", flat)
	}
}

func TestBaselinesFromRows(t *testing.T) {
	from := time.Date(2026, 10, 13, 11, 0, 0, 0, time.UTC)
	w := 5 * time.Minute
	to := from.Add(12 * w)
	at := func(i int) time.Time { return from.Add(time.Duration(i) * w) }
	rows := [][]any{
		// group a: rows in buckets 2..11 except 5 (missing) and 7 (NULL)
		{at(2), "a", uint64(10)}, {at(3), "a", uint64(12)}, {at(4), "a", uint64(8)}, {at(6), "a", uint64(10)},
		{at(7), "a", nil}, {at(8), "a", uint64(11)}, {at(9), "a", uint64(9)}, {at(10), "a", uint64(10)},
		{at(11), "a", uint64(10)},
		// group b: too few values without zero-fill
		{at(0), "b", float64(1)}, {at(11), "b", float64(3)},
	}
	series := toSeries(rows, []string{"service"})
	filled, err := baselines(series, []string{"service"}, []string{"service"}, from, to, w, true)
	if err != nil {
		t.Fatal(err)
	}
	a := filled[GroupKey(map[string]string{"service": "a"})]
	// buckets 2..11 = 10 values, the missing 5 and the NULL 7 read as 0
	if a == nil || a.stats == nil || a.stats.samples != 10 || a.stats.mean != 8 {
		t.Fatalf("zero-filled a %+v", a)
	}
	if b := filled[GroupKey(map[string]string{"service": "b"})]; b.stats == nil || b.stats.samples != 12 {
		t.Errorf("zero-filled b %+v", b.stats)
	}
	skipped, err := baselines(series, []string{"service"}, []string{"service"}, from, to, w, false)
	if err != nil {
		t.Fatal(err)
	}
	if a := skipped[GroupKey(map[string]string{"service": "a"})]; a.stats == nil || a.stats.samples != 8 || a.stats.mean != 10 {
		t.Errorf("skipped a %+v", a.stats)
	}
	if b := skipped[GroupKey(map[string]string{"service": "b"})]; b.stats != nil {
		t.Errorf("b with 2 values has a baseline: %+v", b.stats)
	}
	for _, bad := range [][]any{{at(1).Add(time.Minute), "a", 1}, {at(12), "a", 1}, {from.Add(-w), "a", 1}, {"now", "a", 1}} {
		_, err := baselines(toSeries([][]any{bad}, []string{"service"}), []string{"service"}, []string{"service"}, from, to, w, true)
		if !errors.Is(err, ErrBaselineGrid) {
			t.Errorf("%v: err %v", bad[0], err)
		}
	}
	many := []store.Series{{Points: make([][]any, MaxBaselineRows)}}
	if _, err := baselines(many, nil, nil, from, to, w, true); !errors.Is(err, ErrBaselineRows) {
		t.Errorf("row cap: %v", err)
	}
}

func TestWithBaselines(t *testing.T) {
	ka := GroupKey(map[string]string{"service": "a"})
	kb := GroupKey(map[string]string{"service": "b"})
	base := map[string]*groupBaseline{
		ka: {labels: map[string]string{"service": "a"}, stats: &baselineStats{mean: 10, stdDev: 2, samples: 12}},
		kb: {labels: map[string]string{"service": "b"}, stats: &baselineStats{mean: 50, stdDev: 5, samples: 12}},
	}
	current := []Sample{{Key: ka, Labels: map[string]string{"service": "a"}, Value: fptr(4)}}
	got := withBaselines(current, base, true)
	if len(got) != 2 || got[0].Baseline == nil || *got[0].Baseline.Z != -3 || got[1].Key != kb || *got[1].Value != 0 ||
		*got[1].Baseline.Z != -10 {
		t.Fatalf("zero-filled samples %+v", got)
	}
	if got := withBaselines(current, base, false); len(got) != 1 {
		t.Errorf("without zero-fill a baseline-only group was added: %+v", got)
	}
	ungrouped := withBaselines([]Sample{{}}, map[string]*groupBaseline{"": {stats: &baselineStats{mean: 5, stdDev: 1, samples: 6}}}, true)
	if ungrouped[0].Value == nil || *ungrouped[0].Value != 0 || ungrouped[0].Baseline == nil {
		t.Errorf("ungrouped without a row %+v", ungrouped[0])
	}
}

func TestDeviationMet(t *testing.T) {
	b := &Baseline{Mean: 100, StdDev: 10}
	flat := &Baseline{Mean: 100}
	tests := []struct {
		dir           string
		sigma, minDel float64
		base          *Baseline
		v             float64
		want          bool
	}{
		{DirectionAbove, 3, 0, b, 131, true},
		{DirectionAbove, 3, 0, b, 130, false},
		{DirectionAbove, 3, 0, b, 60, false},
		{DirectionBelow, 3, 0, b, 69, true},
		{DirectionBelow, 3, 0, b, 131, false},
		{DirectionBoth, 3, 0, b, 69, true},
		{DirectionBoth, 3, 0, b, 131, true},
		{DirectionBoth, 3, 0, b, 100, false},
		{DirectionAbove, 3, 40, b, 131, false}, // within minDelta
		{DirectionAbove, 3, 0, flat, 100.5, true},
		{DirectionAbove, 3, 1, flat, 100.5, false},
		{DirectionBelow, 3, 0, flat, 100, false},
	}
	for _, tc := range tests {
		c := Condition{Type: ConditionDeviation, Direction: tc.dir, Sigma: tc.sigma, MinDelta: tc.minDel}
		if got := c.deviationMet(tc.v, tc.base); got != tc.want {
			t.Errorf("%s σ=%v min=%v base=%+v v=%v: %v", tc.dir, tc.sigma, tc.minDel, tc.base, tc.v, got)
		}
	}
	c := Condition{Type: ConditionDeviation, Direction: DirectionBoth, Sigma: 2}
	if op, th := c.Bound(fptr(50), b); op != "<" || th != 80 {
		t.Errorf("bound below %s %v", op, th)
	}
	if op, th := c.Bound(fptr(150), b); op != ">" || th != 120 {
		t.Errorf("bound above %s %v", op, th)
	}
	if op, _ := c.Bound(fptr(1), nil); op != "" {
		t.Errorf("bound without baseline %s", op)
	}
}

// TestEvaluatorDeviation drives a grouped deviation rule: a service whose error count drops
// below its baseline fires, the baseline is cached until its range moves, and the notification
// carries the bound and the baseline.
func TestEvaluatorDeviation(t *testing.T) {
	e := newEvalEnv()
	e.clock.now = time.Date(2026, 10, 13, 12, 3, 20, 0, time.UTC)
	e.addRule("d1", true, `{"query":"level:info | stats count() by service","groupBy":["service"],"condition":{"type":"deviation",`+
		`"direction":"below","sigma":3,"baseline":"1h","minDelta":5},"window":"5m","every":"1m","channels":["c1"]}`)
	doc, err := ParseRule(e.rules.get("d1").Doc)
	if err != nil {
		t.Fatal(err)
	}
	rowsFrom := func(from time.Time) [][]any {
		var rows [][]any
		for i := range 12 {
			at := from.Add(time.Duration(i) * 5 * time.Minute)
			rows = append(rows, []any{at, "checkout", uint64(100 + i%3)}, []any{at, "search", uint64(40)})
		}
		return rows
	}
	from, to := doc.BaselineRange(e.clock.Now())
	e.q.baseline = rowsFrom(from)
	e.q.set("*", [][]any{{"checkout", uint64(101)}, {"search", uint64(2)}}, nil)
	if ran := e.ev.Tick(context.Background()); ran != 1 {
		t.Fatalf("ran %d", ran)
	}
	r := e.rules.get("d1")
	if r.State != string(StateFiring) || e.q.baselineCalls != 1 || !e.q.baselineFrom.Equal(from) || !e.q.baselineTo.Equal(to) {
		t.Fatalf("state %s (err %q), baseline calls %d, range [%s, %s)", r.State, r.LastError, e.q.baselineCalls,
			e.q.baselineFrom, e.q.baselineTo)
	}
	sent := e.out.sent
	if len(sent) != 1 {
		t.Fatalf("sent %d notifications", len(sent))
	}
	n := sent[0].n
	if n.Group["service"] != "search" || n.Op != "<" || n.Threshold != 35 || n.Baseline == nil || n.Baseline.Mean != 40 ||
		n.Baseline.StdDev != 0 || n.Baseline.Samples != 12 || n.ConditionNote != "3σ below the 1h baseline, by at least 5" ||
		!strings.Contains(n.Message, "value 2 is below the 1h baseline (mean 40, σ 0, 12 samples)") {
		t.Errorf("notification %+v (baseline %+v)", n, n.Baseline)
	}
	st := DecodeRuleState(r.EvalState)
	if g := st.Groups[GroupKey(map[string]string{"service": "search"})]; g.Baseline == nil || g.Baseline.Mean != 40 {
		t.Errorf("persisted group %+v", g)
	}
	// One minute later the baseline range is unchanged: the cached baseline is used.
	e.clock.Advance(time.Minute)
	e.ev.Tick(context.Background())
	if e.q.baselineCalls != 1 {
		t.Errorf("baseline recomputed within the same range (%d calls)", e.q.baselineCalls)
	}
	// Once now − window crosses the next 5-minute boundary the baseline moves.
	e.clock.Advance(2 * time.Minute)
	moved, _ := doc.BaselineRange(e.clock.Now())
	if !moved.Equal(from.Add(5 * time.Minute)) {
		t.Fatalf("baseline range did not move: %s", moved)
	}
	e.q.baseline = rowsFrom(moved)
	e.ev.Tick(context.Background())
	if e.q.baselineCalls != 2 {
		t.Errorf("baseline not recomputed after the range moved (%d calls)", e.q.baselineCalls)
	}
	// A baseline-only group (no current row) reads 0 for count and is evaluated too.
	e.q.set("*", [][]any{{"checkout", uint64(101)}}, nil)
	e.clock.Advance(time.Minute)
	out := e.ev.Evaluate(context.Background(), e.rules.get("d1"), e.clock.Now())
	if out.Err != nil || len(out.Samples) != 2 || out.Samples[1].Labels["service"] != "search" || *out.Samples[1].Value != 0 {
		t.Errorf("samples %+v err %v", out.Samples, out.Err)
	}
	// A deleted rule's baseline is forgotten on the next tick.
	delete(e.rules.rules, "d1")
	e.ev.Tick(context.Background())
	e.ev.mu.Lock()
	defer e.ev.mu.Unlock()
	if len(e.ev.baselines) != 0 {
		t.Errorf("baseline cache kept %d entries", len(e.ev.baselines))
	}
}

func TestEvaluatorDeviationNotEnoughBaseline(t *testing.T) {
	e := newEvalEnv()
	e.addRule("d2", true, `{"query":"level:error | stats avg(elapsed)","condition":{"type":"deviation","direction":"above","sigma":3,`+
		`"baseline":"1h"},"window":"5m","every":"1m","noData":"alerting","channels":["c1"]}`)
	e.q.set("*", [][]any{{float64(900)}}, nil)
	e.ev.Tick(context.Background())
	r := e.rules.get("d2")
	if r.State != string(StateFiring) || len(e.rules.history) != 1 ||
		!strings.Contains(e.rules.history[0].Message, "not enough baseline data (noData=alerting)") {
		t.Errorf("state %s history %+v", r.State, e.rules.history)
	}
	e.q.baselineErr = errors.New("boom")
	e.clock.Advance(5 * time.Minute)
	if out := e.ev.Evaluate(context.Background(), r, e.clock.Now()); out.Err == nil ||
		!strings.Contains(out.Err.Error(), "baseline query") {
		t.Errorf("baseline error %v", out.Err)
	}
}

func TestDeviationFacts(t *testing.T) {
	z := -4.1
	n := Notification{Severity: SeverityWarning, State: "firing", PreviousState: "ok", Value: fptr(12), Op: "<",
		Threshold: 30.2, ConditionNote: "3σ below the 24h baseline",
		Baseline: &Baseline{Mean: 80, StdDev: 16.6, Samples: 288, Z: &z}, At: t0}
	got := map[string]string{}
	for _, f := range facts(n) {
		got[f[0]] = f[1]
	}
	if got["Condition"] != "< 30.2 (3σ below the 24h baseline)" || got["Baseline"] != "mean 80, σ 16.6, 288 samples, z -4.1" {
		t.Errorf("facts %v", got)
	}
	plain := facts(Notification{Op: ">", Threshold: 25, At: t0})
	for _, f := range plain {
		if f[0] == "Condition" && f[1] != "> 25" {
			t.Errorf("threshold fact %q", f[1])
		}
		if f[0] == "Baseline" {
			t.Error("threshold notification has a baseline fact")
		}
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		168 * time.Hour: "7d", 48 * time.Hour: "2d", 24 * time.Hour: "24h", 90 * time.Minute: "1h30m",
		time.Hour: "1h", 5 * time.Minute: "5m", 90 * time.Second: "1m30s", 30 * time.Second: "30s",
	} {
		if got := ShortDuration(d); got != want {
			t.Errorf("%s: %s want %s", d, got, want)
		}
	}
}
