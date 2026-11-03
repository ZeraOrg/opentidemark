// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// TestIntegrationDeviationAlert evaluates a grouped deviation rule on real ClickHouse (SPEC
// §14.1): the baseline buckets of `span <window>` lie on the evaluator's grid, a service that
// stops sending reads 0 and fires below its baseline, and a steady service stays ok. It also
// checks `stddev` (SPEC §10.5) against the sample standard deviation.
func TestIntegrationDeviationAlert(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	const window = 5 * time.Minute
	now := time.Now().UTC().Truncate(window).Add(2*time.Minute + 30*time.Second)
	doc := json.RawMessage(`{"query":"* | stats count() by service","groupBy":["service"],
		"condition":{"type":"deviation","direction":"below","sigma":3,"baseline":"1h","minDelta":2},
		"window":"5m","every":"1m","channels":["c1"]}`)
	parsed, err := alerting.ParseRule(doc)
	if err != nil {
		t.Fatal(err)
	}
	from, to := parsed.BaselineRange(now)
	var events []model.Event
	event := func(svc string, at time.Time, elapsed float64) model.Event {
		return model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: at, IngestedAt: now, Stream: "api",
			Level: model.Information, Service: svc, Message: "ok", Protocol: "native",
			Properties: map[string]any{"elapsed": elapsed}}
	}
	var elapsed []float64
	for i := range 12 {
		start := from.Add(time.Duration(i) * window)
		for k := range 9 + i%3 { // payments: 9–11 events in every baseline window
			v := float64(10 * (k + 1))
			elapsed = append(elapsed, v)
			events = append(events, event("payments", start.Add(time.Duration(k)*time.Second), v))
		}
		for k := range 4 { // search: 4 events per window, also now
			events = append(events, event("search", start.Add(time.Duration(k)*time.Second), 1))
		}
	}
	for k := range 4 {
		events = append(events, event("search", now.Add(-time.Duration(k+1)*time.Second), 1))
	}
	w, err := NewWriter(e.conn, e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	db, err := meta.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rules := meta.NewAlertRules(db)
	rule := &meta.AlertRule{Name: "volume", Enabled: true, Doc: doc}
	if err := rules.Create(ctx, rule); err != nil {
		t.Fatal(err)
	}
	notes := &captured{}
	reader := newAlertReader(t, e)
	ev := alerting.NewEvaluator(alerting.EvaluatorDeps{Rules: rules, Querier: reader, Notify: notes,
		Clock: fixedClock{at: now}, Leader: leaderAlways{}, Database: e.db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	out := ev.Evaluate(ctx, *rule, now)
	if out.Err != nil || len(out.Samples) != 2 {
		t.Fatalf("evaluate: %v, samples %+v (baseline [%s, %s))", out.Err, out.Samples, from, to)
	}
	byService := map[string]alerting.Sample{}
	for _, s := range out.Samples {
		byService[s.Labels["service"]] = s
	}
	pay, search := byService["payments"], byService["search"]
	if pay.Value == nil || *pay.Value != 0 || pay.Baseline == nil || pay.Baseline.Samples != 12 || pay.Baseline.Mean != 10 {
		t.Errorf("payments %+v baseline %+v", pay, pay.Baseline)
	}
	if search.Value == nil || *search.Value != 4 || search.Baseline == nil || search.Baseline.StdDev != 0 {
		t.Errorf("search %+v baseline %+v", search, search.Baseline)
	}
	if ran := ev.Tick(ctx); ran != 1 {
		t.Fatalf("ran %d", ran)
	}
	if len(notes.list) != 1 || notes.list[0].Group["service"] != "payments" || notes.list[0].Op != "<" {
		t.Fatalf("notifications %+v", notes.list)
	}

	// The rolling band of the window that starts at `to` (the current window) matches the
	// evaluator's baseline: payments mean 10, the bound minDelta or 3σ below it.
	bands, err := ev.Bands(ctx, parsed, alerting.RuleScope(nil), to, to.Add(window))
	if err != nil || len(bands) != 2 {
		t.Fatalf("bands %+v %v", bands, err)
	}
	for _, b := range bands {
		if len(b.Points) != 1 || !b.Points[0].T.Equal(to) || b.Points[0].Upper != nil || b.Points[0].Lower == nil {
			t.Fatalf("%s band %+v", b.Labels["service"], b.Points)
		}
		want := map[string]float64{"payments": 10, "search": 4}[b.Labels["service"]]
		if b.Points[0].Mean != want {
			t.Errorf("%s band mean %v want %v", b.Labels["service"], b.Points[0].Mean, want)
		}
	}

	// stddev(elapsed) over the payments events equals the sample standard deviation.
	q, err := tql.Parse("service:payments | stats stddev(elapsed)")
	if err != nil {
		t.Fatal(err)
	}
	c, err := tql.Compile(q, tql.Options{Database: e.db, From: from, To: to, Now: now, Streams: model.AllStreams()})
	if err != nil {
		t.Fatal(err)
	}
	res, err := reader.Run(ctx, c)
	if err != nil || len(res.Rows) != 1 {
		t.Fatalf("stddev query: %v %+v", err, res.Rows)
	}
	var mean, sq float64
	for _, v := range elapsed {
		mean += v
	}
	mean /= float64(len(elapsed))
	for _, v := range elapsed {
		sq += (v - mean) * (v - mean)
	}
	want := math.Sqrt(sq / float64(len(elapsed)-1))
	got, ok := res.Rows[0][0].(float64)
	if !ok || math.Abs(got-want) > 1e-9*want {
		t.Errorf("stddev %v want %v", res.Rows[0][0], want)
	}
	// One value has no standard deviation: NULL.
	q, _ = tql.Parse("service:payments | stats stddev(elapsed)")
	c, err = tql.Compile(q, tql.Options{Database: e.db, From: from, To: from.Add(time.Second), Now: now, Streams: model.AllStreams()})
	if err != nil {
		t.Fatal(err)
	}
	if res, err := reader.Run(ctx, c); err != nil || len(res.Rows) != 1 || !isNull(res.Rows[0][0]) {
		t.Errorf("stddev of one value: %v %+v", err, res.Rows)
	}
}

func isNull(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case *float64:
		return x == nil
	}
	return false
}
