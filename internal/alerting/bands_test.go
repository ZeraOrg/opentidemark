// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCanonicalQuery(t *testing.T) {
	a, ok1 := CanonicalQuery("level>=error|stats count() by service span 5m")
	b, ok2 := CanonicalQuery("level >= error | stats   count() by service")
	if !ok1 || !ok2 || a != b {
		t.Errorf("%q != %q", a, b)
	}
	if c, _ := CanonicalQuery("level >= error | stats count() by host"); c == a {
		t.Error("different by-fields match")
	}
	if _, ok := CanonicalQuery("level >= ("); ok {
		t.Error("unparsable query has a canonical form")
	}
}

// TestEvaluatorBands checks the rolling band against hand-computed baselines: every window is
// compared with the 12 windows before it, missing count buckets read 0 from the group's first
// bucket, and bounds follow the direction.
func TestEvaluatorBands(t *testing.T) {
	e := newEvalEnv()
	raw := `{"query":"x | stats count() by service","groupBy":["service"],"condition":{"type":"deviation",` +
		`"direction":"below","sigma":2,"baseline":"1h","minDelta":1},"window":"5m","every":"1m"}`
	doc, err := ParseRule(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	w := 5 * time.Minute
	from := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	to := from.Add(30 * time.Minute)
	qFrom := from.Add(-time.Hour)
	var rows [][]any
	for i := range 18 { // 12 baseline windows before `from` + 6 band windows
		at := qFrom.Add(time.Duration(i) * w)
		rows = append(rows, []any{at, "steady", uint64(10)})
		if i >= 6 && i%2 == 0 { // "sparse": first data at window 6, then every other window
			rows = append(rows, []any{at, "sparse", uint64(4)})
		}
	}
	e.q.baseline = rows
	bands, err := e.ev.Bands(context.Background(), doc, RuleScope(nil), from.Add(time.Minute), to)
	if err != nil {
		t.Fatal(err)
	}
	if len(bands) != 2 || bands[0].Values[0] != "sparse" || bands[1].Labels["service"] != "steady" {
		t.Fatalf("bands %+v", bands)
	}
	steady := bands[1].Points
	if len(steady) != 6 || !steady[0].T.Equal(from) || steady[0].Mean != 10 || steady[0].Upper != nil ||
		steady[0].Lower == nil || *steady[0].Lower != 9 { // σ 0 ⇒ the bound is minDelta away
		t.Fatalf("steady %+v", steady)
	}
	// sparse at band window 0 (j=12): windows 6..11 = 4,0,4,0,4,0 ⇒ mean 2, σ = sqrt(24/5)
	sp := bands[0].Points
	if len(sp) != 6 || sp[0].Mean != 2 {
		t.Fatalf("sparse %+v", sp)
	}
	if !e.q.baselineFrom.Equal(qFrom) || !e.q.baselineTo.Equal(to) {
		t.Errorf("query range [%s, %s)", e.q.baselineFrom, e.q.baselineTo)
	}
	threshold, _ := ParseRule(json.RawMessage(`{"query":"x | stats count()","condition":{"op":">","threshold":1},"window":"5m","every":"1m"}`))
	if b, err := e.ev.Bands(context.Background(), threshold, RuleScope(nil), from, to); b != nil || err != nil {
		t.Errorf("threshold rule bands %v %v", b, err)
	}
	if _, err := e.ev.Bands(context.Background(), doc, RuleScope(nil), from, from.Add(40*24*time.Hour)); !errors.Is(err, ErrBandRange) {
		t.Errorf("long range: %v", err)
	}
}
