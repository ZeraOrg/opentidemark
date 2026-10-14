// SPDX-License-Identifier: Apache-2.0

//go:build integration

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// Stream access fixture (SPEC §15.10): each of two streams gets three log events, one span of
// a trace shared by both streams and one metric point; team-b also gets a trace of its own.
const (
	streamA        = "team-a"
	streamB        = "team-b"
	sharedTraceID  = "4bf92f3577b34da6a3ce929d0e0e4736"
	onlyBTraceID   = "0bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	accessPassword = "Access-Integration-Pass-42"
	accessEvents   = 6
	accessSpans    = 3
	accessMetrics  = 2
	perStream      = 3
)

// suffix is the last letter of a fixture stream name ("a" or "b").
func suffix(stream string) string { return stream[len(stream)-1:] }

func TestIntegrationStreamAccess(t *testing.T) {
	a, _ := startApp(t)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()
	addrs := a.Addrs()
	base := "http://" + addrs[ListenerHTTP]
	admin := loginAdmin(t, base)
	keys := map[string]string{streamA: admin.createStreamKey("it-a", streamA), streamB: admin.createStreamKey("it-b", streamB)}
	for _, s := range []string{streamA, streamB} {
		ingestAccessFixture(t, base, addrs, keys[s], s)
	}
	rng := newITRange()
	waitForAccessData(t, admin, rng)
	aliceID := admin.createUser("alice", "editor")
	admin.createUser("bob", "viewer")
	teamA := admin.grantTeam("Team A", []string{aliceID}, []string{streamA})
	alice := login(t, base, "alice", accessPassword)

	t.Run("queries", func(t *testing.T) { checkScopedQueries(t, alice, rng) })
	t.Run("explorer", func(t *testing.T) { checkScopedExplorer(t, alice, rng) })
	t.Run("events", func(t *testing.T) { checkScopedEvents(t, alice, admin, rng) })
	t.Run("export", func(t *testing.T) { checkScopedExport(t, alice, rng) })
	t.Run("traces", func(t *testing.T) { checkScopedTraces(t, alice, rng) })
	t.Run("metrics", func(t *testing.T) { checkScopedMetrics(t, alice, rng) })
	t.Run("streams", func(t *testing.T) { checkScopedStreams(t, alice, admin) })
	t.Run("selection", func(t *testing.T) { checkSelection(t, alice, admin, rng) })
	t.Run("tail", func(t *testing.T) { checkScopedTail(t, alice, base, keys) })
	t.Run("alerts", func(t *testing.T) {
		checkScopedAlerts(t, alice, admin, func() {
			if code := admin.do(http.MethodPut, "/teams/"+teamA+"/members", map[string]any{"userIds": []string{}}, nil); code != 200 {
				t.Fatalf("remove member: %d", code)
			}
		})
	})
	t.Run("default access", func(t *testing.T) { checkDefaultAccess(t, admin, login(t, base, "bob", accessPassword), rng) })
	t.Run("connection streams", func(t *testing.T) { checkConnectionStreams(t, admin, base, rng) })
}

func (a *apiClient) createStreamKey(name, stream string) string {
	a.t.Helper()
	var out struct {
		Key string `json:"key"`
	}
	if code := a.do(http.MethodPost, "/connections", map[string]any{"name": name, "protocols": []string{"native", "otlp"},
		"defaultStream": stream}, &out); code != http.StatusCreated || out.Key == "" {
		a.t.Fatalf("create connection %s: %d", name, code)
	}
	return out.Key
}

func (a *apiClient) createUser(username, role string) string {
	a.t.Helper()
	var out struct {
		ID string `json:"id"`
	}
	if code := a.do(http.MethodPost, "/users", map[string]any{"username": username, "displayName": username,
		"password": accessPassword, "role": role}, &out); code != http.StatusCreated {
		a.t.Fatalf("create user %s: %d", username, code)
	}
	return out.ID
}

// grantTeam creates a team through the API with members and streams and returns its id.
func (a *apiClient) grantTeam(name string, members, streams []string) string {
	a.t.Helper()
	var team struct {
		ID string `json:"id"`
	}
	if code := a.do(http.MethodPost, "/teams", map[string]string{"name": name}, &team); code != http.StatusCreated {
		a.t.Fatalf("create team: %d", code)
	}
	if code := a.do(http.MethodPut, "/teams/"+team.ID+"/members", map[string]any{"userIds": members}, nil); code != 200 {
		a.t.Fatalf("team members: %d", code)
	}
	if code := a.do(http.MethodPut, "/teams/"+team.ID+"/streams", map[string]any{"streams": streams}, nil); code != 200 {
		a.t.Fatalf("team streams: %d", code)
	}
	return team.ID
}

// ingestAccessFixture sends the logs, spans and metric of one stream. Logs of both streams share
// service and host, so event context would mix them without the scope.
func ingestAccessFixture(t *testing.T, base string, addrs map[string]string, key, stream string) {
	t.Helper()
	bearer := map[string]string{"Authorization": "Bearer " + key}
	now := time.Now().UTC()
	var logs strings.Builder
	for i, env := range []string{"prod", "staging", "prod"} {
		fmt.Fprintf(&logs, `{"@t":%q,"message":"event %d of %s","service":"shared","host":"h1","environment":%q,`+
			`"owner":"owner-%s","only_%s":true,"trace_id":%q}`+"\n",
			now.Add(-time.Duration(i)*time.Second).Format(time.RFC3339Nano), i, stream, env, stream, suffix(stream), sharedTraceID)
	}
	if got := post(t, base+"/api/v1/ingest/logs", "application/x-ndjson", []byte(logs.String()), bearer); got != 202 {
		t.Fatalf("logs %s: %d", stream, got)
	}
	otlp := "http://" + addrs[ListenerOTLPHTTP]
	if got := post(t, otlp+"/v1/traces", "application/json", []byte(accessTraces(stream, now)), bearer); got != 200 {
		t.Fatalf("traces %s: %d", stream, got)
	}
	ts := fmt.Sprint(now.UnixNano())
	metrics := `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"shared"}}]},` +
		`"scopeMetrics":[{"metrics":[{"name":"it.access.` + suffix(stream) + `","gauge":{"dataPoints":[{"timeUnixNano":"` + ts +
		`","asInt":"1"}]}}]}]}]}`
	if got := post(t, otlp+"/v1/metrics", "application/json", []byte(metrics), bearer); got != 200 {
		t.Fatalf("metrics %s: %d", stream, got)
	}
}

// accessTraces is the OTLP/JSON traces body of a stream: team-a has the root span of the shared
// trace, team-b its child plus the root of a trace only team-b holds.
func accessTraces(stream string, now time.Time) string {
	start, end := fmt.Sprint(now.UnixNano()), fmt.Sprint(now.Add(10*time.Millisecond).UnixNano())
	span := func(trace, id, parent, name string) string {
		return fmt.Sprintf(`{"traceId":%q,"spanId":%q,"parentSpanId":%q,"name":%q,"kind":2,"startTimeUnixNano":%q,"endTimeUnixNano":%q}`,
			trace, id, parent, name, start, end)
	}
	spans := span(sharedTraceID, "aaaaaaaaaaaaaaa1", "", "GET /a")
	if stream == streamB {
		spans = span(sharedTraceID, "bbbbbbbbbbbbbbb1", "aaaaaaaaaaaaaaa1", "SELECT b") + "," +
			span(onlyBTraceID, "bbbbbbbbbbbbbbb2", "", "GET /b")
	}
	return `{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc-` + suffix(stream) +
		`"}}]},"scopeSpans":[{"spans":[` + spans + `]}]}]}`
}

func waitForAccessData(t *testing.T, a *apiClient, rng itRange) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ev := totalCount(rng.query(a, "* | stats count()", nil))
		sp := totalCount(rng.query(a, "source spans | * | stats count()", nil))
		mp := totalCount(rng.query(a, "source metrics | * | stats count()", nil))
		if ev == accessEvents && sp == accessSpans && mp == accessMetrics {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("counts events=%v spans=%v metrics=%v", ev, sp, mp)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// onlyStreamA checks that a "stats count() by stream" table has the single row team-a = want.
func onlyStreamA(t *testing.T, what string, r queryResult, want float64) {
	t.Helper()
	if len(r.Rows) != 1 || r.Rows[0][0] != streamA || r.Rows[0][1] != want {
		t.Errorf("%s: %+v", what, r.Rows)
	}
}

func checkScopedQueries(t *testing.T, alice *apiClient, rng itRange) {
	r := rng.query(alice, "*", nil)
	if len(r.Events) != perStream {
		t.Errorf("events %d", len(r.Events))
	}
	for _, ev := range r.Events {
		if ev["stream"] != streamA {
			t.Errorf("event of stream %v visible", ev["stream"])
		}
	}
	onlyStreamA(t, "logs by stream", rng.query(alice, "* | stats count() by stream", nil), perStream)
	onlyStreamA(t, "spans by stream", rng.query(alice, "source spans | * | stats count() by stream", nil), 1)
	onlyStreamA(t, "metrics by stream", rng.query(alice, "source metrics | * | stats count() by stream", nil), 1)
	ts := rng.query(alice, "* | stats count() by stream span auto", map[string]any{"span": "1h"})
	if ts.Kind != "timeseries" || len(ts.Series) != 1 || ts.Series[0].Key["stream"] != streamA {
		t.Errorf("timeseries %+v", ts.Series)
	}
	if r := rng.query(alice, "stream:"+streamB+" or only_b:true", nil); len(r.Events) != 0 {
		t.Errorf("explicit query for the hidden stream returned %d events", len(r.Events))
	}
}

func checkScopedExplorer(t *testing.T, alice *apiClient, rng itRange) {
	var h struct {
		Buckets []struct {
			Counts map[string]float64 `json:"counts"`
		} `json:"buckets"`
	}
	if code := alice.do(http.MethodGet, "/histogram?q=*&buckets=10&"+rng.params(""), nil, &h); code != 200 {
		t.Fatalf("histogram %d", code)
	}
	var total float64
	for _, b := range h.Buckets {
		for _, n := range b.Counts {
			total += n
		}
	}
	if total != perStream {
		t.Errorf("histogram total %v", total)
	}
	var facets []struct {
		Field  string `json:"field"`
		Values []struct {
			Value string `json:"value"`
		} `json:"values"`
		OtherCount float64 `json:"otherCount"`
	}
	if code := alice.do(http.MethodGet, "/facets?q=*&fields=stream,owner&top=10&"+rng.params(""), nil, &facets); code != 200 || len(facets) != 2 {
		t.Fatalf("facets %d %+v", code, facets)
	}
	for _, f := range facets {
		if len(f.Values) != 1 || !strings.HasSuffix(f.Values[0].Value, streamA) || f.OtherCount != 0 {
			t.Errorf("facet %s %+v", f.Field, f)
		}
	}
	var fields []map[string]any
	if code := alice.do(http.MethodGet, "/fields?source=logs&"+rng.params(""), nil, &fields); code != 200 {
		t.Fatalf("fields %d", code)
	}
	names := map[string]bool{}
	for _, f := range fields {
		names[f["name"].(string)] = true
	}
	if !names["only_a"] || names["only_b"] {
		t.Errorf("fields %v", names)
	}
	var comp struct {
		Suggestions []struct {
			Label string `json:"label"`
		} `json:"suggestions"`
	}
	if code := alice.do(http.MethodGet, "/tql/complete?q=owner:o&pos=7", nil, &comp); code != 200 || len(comp.Suggestions) == 0 {
		t.Fatalf("complete %d %+v", code, comp)
	}
	for _, s := range comp.Suggestions {
		if strings.Contains(s.Label, streamB) {
			t.Errorf("completion leaked %q", s.Label)
		}
	}
	var compFields struct {
		Suggestions []struct {
			Label string `json:"label"`
		} `json:"suggestions"`
	}
	if code := alice.do(http.MethodGet, "/tql/complete?q=only&pos=4", nil, &compFields); code != 200 {
		t.Fatalf("complete fields %d", code)
	}
	for _, s := range compFields.Suggestions {
		if s.Label == "only_b" {
			t.Error("field completion leaked only_b")
		}
	}
}

func checkScopedEvents(t *testing.T, alice, admin *apiClient, rng itRange) {
	hidden := rng.query(admin, "stream:"+streamB, map[string]any{"limit": 1})
	visible := rng.query(admin, "stream:"+streamA, map[string]any{"limit": 1})
	if len(hidden.Events) != 1 || len(visible.Events) != 1 {
		t.Fatalf("admin sees %d/%d events", len(hidden.Events), len(visible.Events))
	}
	hiddenID, visibleID := hidden.Events[0]["id"].(string), visible.Events[0]["id"].(string)
	if code := alice.do(http.MethodGet, "/events/"+hiddenID, nil, nil); code != http.StatusNotFound {
		t.Errorf("hidden event by id: %d", code)
	}
	if code := alice.do(http.MethodGet, "/events/"+hiddenID+"/context", nil, nil); code != http.StatusNotFound {
		t.Errorf("hidden event context: %d", code)
	}
	if code := alice.do(http.MethodGet, "/events/"+visibleID, nil, nil); code != 200 {
		t.Errorf("visible event by id: %d", code)
	}
	var ctx struct {
		Before []map[string]any `json:"before"`
		After  []map[string]any `json:"after"`
	}
	if code := alice.do(http.MethodGet, "/events/"+visibleID+"/context?before=10&after=10", nil, &ctx); code != 200 {
		t.Fatalf("context %d", code)
	}
	around := append(ctx.Before, ctx.After...)
	if len(around) != perStream-1 {
		t.Errorf("context has %d events", len(around))
	}
	for _, ev := range around {
		if ev["stream"] != streamA {
			t.Errorf("context event of stream %v", ev["stream"])
		}
	}
}

func (a *apiClient) raw(path string) (int, string) {
	a.t.Helper()
	resp, err := a.c.Get(a.base + "/api/v1" + path)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func checkScopedExport(t *testing.T, alice *apiClient, rng itRange) {
	for format, want := range map[string]int{"ndjson": perStream, "csv": perStream + 1} {
		code, body := alice.raw("/export?q=*&format=" + format + "&" + rng.params(""))
		lines := strings.Split(strings.TrimSpace(body), "\n")
		if code != 200 || len(lines) != want || strings.Contains(body, streamB) {
			t.Errorf("export %s: %d, %d lines", format, code, len(lines))
		}
	}
}

func checkScopedTraces(t *testing.T, alice *apiClient, rng itRange) {
	var sums []map[string]any
	if code := alice.do(http.MethodGet, "/traces?q=*&"+rng.params(""), nil, &sums); code != 200 || len(sums) != 1 ||
		sums[0]["traceId"] != sharedTraceID || sums[0]["spanCount"] != float64(1) {
		t.Errorf("trace search %d %+v", code, sums)
	}
	if code := alice.do(http.MethodGet, "/traces?q=service:svc-b&"+rng.params(""), nil, &sums); code != 200 || len(sums) != 0 {
		t.Errorf("trace search for hidden spans %d %+v", code, sums)
	}
	var tr struct {
		Spans  []map[string]any `json:"spans"`
		Events []map[string]any `json:"events"`
	}
	if code := alice.do(http.MethodGet, "/traces/"+sharedTraceID, nil, &tr); code != 200 || len(tr.Spans) != 1 ||
		tr.Spans[0]["stream"] != streamA || len(tr.Events) != perStream {
		t.Errorf("shared trace %d spans=%+v events=%d", code, tr.Spans, len(tr.Events))
	}
	for _, ev := range tr.Events {
		if ev["stream"] != streamA {
			t.Errorf("trace event of stream %v", ev["stream"])
		}
	}
	if code := alice.do(http.MethodGet, "/traces/"+onlyBTraceID, nil, nil); code != http.StatusNotFound {
		t.Errorf("hidden trace: %d", code)
	}
}

func metricNames(t *testing.T, a *apiClient, rng itRange) []string {
	t.Helper()
	var names []map[string]any
	if code := a.do(http.MethodGet, "/metrics/names?q=it.access&"+rng.params(""), nil, &names); code != 200 {
		t.Fatalf("metric names %d", code)
	}
	out := []string{}
	for _, n := range names {
		out = append(out, n["name"].(string))
	}
	slices.Sort(out)
	return out
}

func checkScopedMetrics(t *testing.T, alice *apiClient, rng itRange) {
	if got := metricNames(t, alice, rng); !slices.Equal(got, []string{"it.access.a"}) {
		t.Errorf("metric names %v", got)
	}
	r := rng.query(alice, `source metrics | name:"it.access.b" | stats count()`, nil)
	if n := totalCount(r); n != 0 {
		t.Errorf("hidden metric count %v", n)
	}
}

func checkScopedStreams(t *testing.T, alice, admin *apiClient) {
	var list struct {
		Streams []struct {
			Name string `json:"name"`
		} `json:"streams"`
	}
	if code := alice.do(http.MethodGet, "/streams", nil, &list); code != 200 || len(list.Streams) != 1 || list.Streams[0].Name != streamA {
		t.Errorf("streams %d %+v", code, list)
	}
	if code := admin.do(http.MethodGet, "/streams", nil, &list); code != 200 || len(list.Streams) < 2 {
		t.Errorf("admin streams %d %+v", code, list)
	}
	if code := alice.do(http.MethodGet, "/streams/"+streamB+"/fields", nil, nil); code != http.StatusNotFound {
		t.Errorf("hidden stream fields %d", code)
	}
	if code := alice.do(http.MethodGet, "/streams/"+streamA+"/fields", nil, nil); code != 200 {
		t.Errorf("stream fields %d", code)
	}
}

func checkSelection(t *testing.T, alice, admin *apiClient, rng itRange) {
	if r := rng.query(alice, "*", map[string]any{"streams": []string{streamB}}); len(r.Events) != 0 {
		t.Errorf("selection widened the scope: %d events", len(r.Events))
	}
	if r := rng.query(admin, "*", map[string]any{"streams": []string{streamB}}); len(r.Events) != perStream {
		t.Errorf("admin selection: %d events", len(r.Events))
	}
	if n := totalCount(rng.query(admin, "* | stats count()", map[string]any{"environments": []string{"prod"}})); n != 4 {
		t.Errorf("environment selection: %v", n)
	}
	if n := totalCount(rng.query(admin, "source spans | * | stats count()", map[string]any{"environments": []string{"prod"}})); n != accessSpans {
		t.Errorf("environments must not filter spans: %v", n)
	}
	var facets []struct {
		Values []struct {
			Value string `json:"value"`
		} `json:"values"`
	}
	path := "/facets?q=*&fields=stream&streams=" + streamB + "&env=staging&" + rng.params("")
	if code := admin.do(http.MethodGet, path, nil, &facets); code != 200 || len(facets) != 1 || len(facets[0].Values) != 1 ||
		facets[0].Values[0].Value != streamB {
		t.Errorf("GET selection %d %+v", code, facets)
	}
	if code := alice.do(http.MethodGet, "/histogram?streams="+url.QueryEscape("Bad'--"), nil, nil); code != http.StatusBadRequest {
		t.Errorf("invalid stream accepted: %d", code)
	}
}

func checkScopedTail(t *testing.T, alice *apiClient, base string, keys map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, alice.base+"/api/v1/tail?q=service:tail-access", nil)
	resp, err := (&http.Client{Jar: alice.c.Jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tail: %d", resp.StatusCode)
	}
	marker := fmt.Sprintf("access tail %d", time.Now().UnixNano())
	for _, s := range []string{streamB, streamA} {
		body := fmt.Sprintf(`{"@t":%q,"message":"%s %s","service":"tail-access"}`, time.Now().UTC().Format(time.RFC3339Nano), marker, s)
		if got := post(t, base+"/api/v1/ingest/logs", "application/json", []byte(body),
			map[string]string{"X-Tidemark-Key": keys[s]}); got != 202 {
			t.Fatalf("ingest for tail: %d", got)
		}
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if strings.Contains(data, marker+" "+streamB) {
			t.Fatal("tail delivered an event of a hidden stream")
		}
		if strings.Contains(data, marker+" "+streamA) {
			return
		}
	}
	t.Errorf("tail ended without the visible marker: %v", sc.Err())
}

// alertValue tests a rule and returns its single sample value (-1 when there is none).
func alertValue(t *testing.T, a *apiClient, id string) float64 {
	t.Helper()
	var out struct {
		Error   string `json:"error"`
		Samples []struct {
			Value *float64 `json:"value"`
		} `json:"samples"`
	}
	if code := a.do(http.MethodPost, "/alerts/"+id+"/test", nil, &out); code != 200 || out.Error != "" {
		t.Fatalf("test alert: %d %s", code, out.Error)
	}
	if len(out.Samples) != 1 || out.Samples[0].Value == nil {
		return -1
	}
	return *out.Samples[0].Value
}

// checkScopedAlerts covers rule streams (SPEC §14.1): a restricted editor must choose streams
// within the scope, sees only such rules, and a rule keeps reading its streams after its
// author loses access (removeAuthor).
func checkScopedAlerts(t *testing.T, alice, admin *apiClient, removeAuthor func()) {
	doc := json.RawMessage(`{"query":"service:shared | stats count()","condition":{"op":">","threshold":100},` +
		`"window":"1h","every":"1m"}`)
	for _, streams := range []any{nil, []string{streamB}, []string{streamA, streamB}} {
		if code := alice.do(http.MethodPost, "/alerts", map[string]any{"name": "bad", "doc": doc, "streams": streams}, nil); code != 400 {
			t.Errorf("rule over %v accepted: %d", streams, code)
		}
	}
	var mine, global struct {
		ID      string   `json:"id"`
		Streams []string `json:"streams"`
	}
	if code := alice.do(http.MethodPost, "/alerts", map[string]any{"name": "team a errors", "doc": doc,
		"streams": []string{streamA}}, &mine); code != http.StatusCreated || !slices.Equal(mine.Streams, []string{streamA}) {
		t.Fatalf("create scoped rule: %d %+v", code, mine)
	}
	if code := admin.do(http.MethodPost, "/alerts", map[string]any{"name": "everything", "doc": doc}, &global); code != http.StatusCreated ||
		global.Streams != nil {
		t.Fatalf("create global rule: %d %+v", code, global)
	}
	var list []map[string]any
	if code := alice.do(http.MethodGet, "/alerts", nil, &list); code != 200 || len(list) != 1 || list[0]["id"] != mine.ID {
		t.Errorf("visible rules %d %+v", code, list)
	}
	if code := alice.do(http.MethodGet, "/alerts/"+global.ID, nil, nil); code != http.StatusNotFound {
		t.Errorf("rule over every stream: %d", code)
	}
	if v := alertValue(t, alice, mine.ID); v != perStream {
		t.Errorf("scoped rule value %v", v)
	}
	if v := alertValue(t, admin, global.ID); v != accessEvents {
		t.Errorf("global rule value %v", v)
	}
	removeAuthor()
	if v := alertValue(t, admin, mine.ID); v != perStream {
		t.Errorf("rule after its author lost access: %v", v)
	}
}

// checkDefaultAccess covers a user in no team: every stream while deny-by-default is off (the
// behaviour of upgraded installations), nothing — and no errors — once it is on.
func checkDefaultAccess(t *testing.T, admin, bob *apiClient, rng itRange) {
	if n := totalCount(rng.query(bob, "* | stats count()", nil)); n < accessEvents {
		t.Errorf("team-less user before deny-by-default: %v events", n)
	}
	if code := admin.do(http.MethodPatch, "/settings", map[string]any{"access": map[string]bool{"denyByDefault": true}}, nil); code != 200 {
		t.Fatalf("settings %d", code)
	}
	if r := rng.query(bob, "*", nil); len(r.Events) != 0 {
		t.Errorf("deny-by-default query: %d events", len(r.Events))
	}
	if n := totalCount(rng.query(bob, "source spans | * | stats count()", nil)); n != 0 {
		t.Errorf("deny-by-default spans: %v", n)
	}
	for _, path := range []string{"/histogram?q=*&buckets=10&" + rng.params(""), "/facets?q=*&fields=stream&" + rng.params(""),
		"/fields?source=logs&" + rng.params(""), "/traces?q=*&" + rng.params(""), "/metrics/names?" + rng.params(""),
		"/tql/complete?q=owner:o&pos=7", "/streams"} {
		if code := bob.do(http.MethodGet, path, nil, nil); code != 200 {
			t.Errorf("deny-by-default %s: %d", path, code)
		}
	}
	if got := metricNames(t, bob, rng); len(got) != 0 {
		t.Errorf("deny-by-default metric names %v", got)
	}
	if code := bob.do(http.MethodGet, "/traces/"+sharedTraceID, nil, nil); code != http.StatusNotFound {
		t.Errorf("deny-by-default trace %d", code)
	}
}

// checkConnectionStreams sends an event naming a stream outside the connection's allowed
// streams; it must land in the connection's default stream (SPEC §9.1).
func checkConnectionStreams(t *testing.T, admin *apiClient, base string, rng itRange) {
	var conn struct {
		Key string `json:"key"`
	}
	if code := admin.do(http.MethodPost, "/connections", map[string]any{"name": "it-a-only", "protocols": []string{"native"},
		"defaultStream": streamA, "allowedStreams": []string{streamA}}, &conn); code != http.StatusCreated {
		t.Fatalf("create connection: %d", code)
	}
	marker := fmt.Sprintf("rerouted %d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"@t":%q,"message":%q,"@stream":%q}`, time.Now().UTC().Format(time.RFC3339Nano), marker, streamB)
	if got := post(t, base+"/api/v1/ingest/logs", "application/json", []byte(body), map[string]string{"X-Tidemark-Key": conn.Key}); got != 202 {
		t.Fatalf("ingest: %d", got)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		r := rng.query(admin, fmt.Sprintf("%q", marker), nil)
		if len(r.Events) == 1 {
			if r.Events[0]["stream"] != streamA {
				t.Errorf("event written to %v", r.Events[0]["stream"])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("event not found: %+v", r.Events)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
