// SPDX-License-Identifier: Apache-2.0

//go:build integration

package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// apiClient is a logged-in API session (cookie + CSRF header).
type apiClient struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func loginAdmin(t *testing.T, base string) *apiClient {
	t.Helper()
	return login(t, base, "admin", "admin")
}

// login opens an API session for username.
func login(t *testing.T, base, username, password string) *apiClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	a := &apiClient{t: t, base: base, c: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
	if code := a.do(http.MethodPost, "/auth/login", map[string]string{"username": username, "password": password}, nil); code != 200 {
		t.Fatalf("login %s: %d", username, code)
	}
	u, _ := url.Parse(base)
	for _, ck := range jar.Cookies(u) {
		if ck.Name == "tm_csrf" {
			a.csrf = ck.Value
		}
	}
	return a
}

// do sends a JSON request to /api/v1 and decodes the response into out (when not nil).
func (a *apiClient) do(method, path string, body, out any) int {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, a.base+"/api/v1"+path, r)
	req.Header.Set("Content-Type", "application/json")
	if a.csrf != "" {
		req.Header.Set("X-Tidemark-CSRF", a.csrf)
	}
	resp, err := a.c.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
		}
	}
	if resp.StatusCode >= 300 && out != nil {
		a.t.Logf("%s %s: %d %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode
}

func (a *apiClient) createKey(name string, protocols []string) string {
	a.t.Helper()
	var out struct {
		Key string `json:"key"`
	}
	if code := a.do(http.MethodPost, "/connections", map[string]any{"name": name, "protocols": protocols,
		"defaultStream": "itest"}, &out); code != http.StatusCreated || out.Key == "" {
		a.t.Fatalf("create connection %s: %d", name, code)
	}
	return out.Key
}

type queryResult struct {
	Kind       string           `json:"kind"`
	Events     []map[string]any `json:"events"`
	NextCursor *string          `json:"nextCursor"`
	Columns    []map[string]any `json:"columns"`
	Rows       [][]any          `json:"rows"`
	Series     []struct {
		Key    map[string]any `json:"key"`
		Points [][]float64    `json:"points"`
	} `json:"series"`
}

// itRange covers the fixtures (fixed October 2026 times and ingest-time defaults).
type itRange struct{ from, to string }

func newITRange() itRange {
	now := time.Now().UTC()
	return itRange{from: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), to: now.Add(30 * 24 * time.Hour).Format(time.RFC3339)}
}

func (r itRange) query(a *apiClient, q string, extra map[string]any) queryResult {
	a.t.Helper()
	body := map[string]any{"q": q, "from": r.from, "to": r.to}
	for k, v := range extra {
		body[k] = v
	}
	var out queryResult
	if code := a.do(http.MethodPost, "/query", body, &out); code != 200 {
		a.t.Fatalf("query %q: %d", q, code)
	}
	return out
}

func (r itRange) params(extra string) string {
	return "from=" + url.QueryEscape(r.from) + "&to=" + url.QueryEscape(r.to) + extra
}

func TestIntegrationSearchEndToEnd(t *testing.T) {
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
	key := admin.createKey("it-all", []string{"seq", "elastic", "otlp", "native"})
	seqOnly := admin.createKey("it-seq-only", []string{"seq"})

	checkRejections(t, base, addrs, seqOnly)
	ingestFixtures(t, base, addrs, key)
	rng := newITRange()
	waitForCounts(t, admin, rng)

	t.Run("events and cursor", func(t *testing.T) { checkEvents(t, admin, rng) })
	t.Run("stats", func(t *testing.T) { checkStats(t, admin, rng) })
	t.Run("explorer", func(t *testing.T) { checkExplorer(t, admin, rng) })
	t.Run("traces", func(t *testing.T) { checkTraces(t, admin, rng) })
	t.Run("export", func(t *testing.T) { checkExport(t, admin, rng) })
	t.Run("tail", func(t *testing.T) { checkTail(t, admin, base, key) })
}

func checkRejections(t *testing.T, base string, addrs map[string]string, seqOnly string) {
	t.Helper()
	native := base + "/api/v1/ingest/logs"
	cases := []struct {
		name, url, ct string
		headers       map[string]string
		want          int
	}{
		{"native without key", native, "application/json", nil, http.StatusUnauthorized},
		{"native with seq-only key", native, "application/json", map[string]string{"X-Tidemark-Key": seqOnly}, http.StatusForbidden},
		{"otlp with seq-only key", "http://" + addrs[ListenerOTLPHTTP] + "/v1/logs", "application/json",
			map[string]string{"Authorization": "Bearer " + seqOnly}, http.StatusForbidden},
		{"bulk without key", "http://" + addrs[ListenerES] + "/_bulk", "application/x-ndjson", nil, http.StatusUnauthorized},
		{"seq with wrong key", base + "/api/events/raw?clef", "application/vnd.serilog.clef",
			map[string]string{"X-Seq-ApiKey": "tmk_" + strings.Repeat("x", 40)}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		if got := post(t, c.url, c.ct, []byte(`{"message":"rejected"}`), c.headers); got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
	}
	if got := post(t, base+"/api/events/raw?clef", "application/vnd.serilog.clef", []byte(`{"@m":"seq ok","@l":"Debug"}`),
		map[string]string{"X-Seq-ApiKey": seqOnly}); got != http.StatusCreated {
		t.Errorf("seq-only key on seq: %d", got)
	}
}

func ingestFixtures(t *testing.T, base string, addrs map[string]string, key string) {
	t.Helper()
	seqKey := map[string]string{"X-Seq-ApiKey": key}
	bearer := map[string]string{"Authorization": "Bearer " + key}
	apiKey := map[string]string{"Authorization": "ApiKey " + key}
	otlp := "http://" + addrs[ListenerOTLPHTTP]
	steps := []struct {
		url, ct, file string
		headers       map[string]string
		want          int
	}{
		{"http://" + addrs[ListenerSeq] + "/api/events/raw", "application/vnd.serilog.clef", "seq/testdata/serilog_clef.ndjson", seqKey, 201},
		{base + "/api/events/raw", "application/json", "seq/testdata/nlog_legacy.json", seqKey, 201},
		{"http://" + addrs[ListenerES] + "/_bulk", "application/x-ndjson", "elastic/testdata/filebeat_bulk.ndjson", apiKey, 200},
		{"http://" + addrs[ListenerES] + "/_bulk", "application/x-ndjson", "elastic/testdata/fluentbit_bulk.ndjson", apiKey, 200},
		{base + "/es/_bulk", "application/x-ndjson", "elastic/testdata/vector_bulk.ndjson", apiKey, 200},
		{base + "/es/_bulk", "application/x-ndjson", "elastic/testdata/logstash_bulk.ndjson", apiKey, 200},
		{base + "/api/v1/ingest/logs", "application/json", "native/testdata/array.json", bearer, 202},
		{base + "/api/v1/ingest/logs", "application/x-ndjson", "native/testdata/lines.ndjson", bearer, 202},
		{base + "/otlp/v1/logs", "application/json", "otlp/testdata/logs.json", bearer, 200},
		{otlp + "/v1/logs", "application/x-protobuf", "otlp/testdata/logs.pb", bearer, 200},
		{otlp + "/v1/traces", "application/json", "otlp/testdata/traces.json", bearer, 200},
		{otlp + "/v1/traces", "application/x-protobuf", "otlp/testdata/traces.pb", bearer, 200},
		{base + "/otlp/v1/metrics", "application/json", "otlp/testdata/metrics.json", bearer, 200},
		{otlp + "/v1/metrics", "application/x-protobuf", "otlp/testdata/metrics.pb", bearer, 200},
	}
	for _, s := range steps {
		if got := post(t, s.url, s.ct, fixture(t, s.file), s.headers); got != s.want {
			t.Errorf("%s: status %d, want %d", s.file, got, s.want)
		}
	}
}

// Expected rows: 7 Seq + 8 Elasticsearch + 5 native + 5 OTLP log events + 1 event from the
// seq-only key; 4 spans; 7 metric points.
var wantByProtocol = map[string]float64{"seq": 8, "elastic": 8, "native": 5, "otlp": 5}

const (
	wantEvents  = 26
	wantSpans   = 4
	wantMetrics = 7
)

func waitForCounts(t *testing.T, a *apiClient, rng itRange) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		ev := totalCount(rng.query(a, "* | stats count()", nil))
		sp := totalCount(rng.query(a, "source spans | * | stats count()", nil))
		mp := totalCount(rng.query(a, "source metrics | * | stats count()", nil))
		if ev == wantEvents && sp == wantSpans && mp == wantMetrics {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("counts events=%v spans=%v metrics=%v", ev, sp, mp)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func totalCount(r queryResult) float64 {
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 {
		return -1
	}
	n, _ := r.Rows[0][0].(float64)
	return n
}

func checkEvents(t *testing.T, a *apiClient, rng itRange) {
	seen := map[string]bool{}
	var cursor any
	for page := 0; page < 10; page++ {
		extra := map[string]any{"limit": 3}
		if cursor != nil {
			extra["cursor"] = cursor
		}
		r := rng.query(a, "protocol:seq", extra)
		for _, ev := range r.Events {
			id := ev["id"].(string)
			if seen[id] {
				t.Errorf("duplicate event %s across pages", id)
			}
			seen[id] = true
		}
		if r.NextCursor == nil {
			break
		}
		cursor = *r.NextCursor
	}
	if len(seen) != int(wantByProtocol["seq"]) {
		t.Errorf("paged %d seq events", len(seen))
	}
	r := rng.query(a, `"shipped to Amsterdam"`, nil)
	if len(r.Events) != 1 || r.Events[0]["message"] != "Order 1042 shipped to Amsterdam" {
		t.Errorf("free text: %+v", r.Events)
	}
	r = rng.query(a, "RequestPath:/api/orders/42 and StatusCode = 200", nil)
	if len(r.Events) != 1 {
		t.Fatalf("property query: %+v", r.Events)
	}
	ev := r.Events[0]
	props := ev["properties"].(map[string]any)
	if props["StatusCode"] != float64(200) || ev["service"] != "checkout-api" || ev["level"] != "Information" {
		t.Errorf("event %+v", ev)
	}
	var byID map[string]any
	if code := a.do(http.MethodGet, "/events/"+ev["id"].(string), nil, &byID); code != 200 || byID["message"] != ev["message"] {
		t.Errorf("event by id: %d %+v", code, byID)
	}
	var ctx struct {
		Before []map[string]any `json:"before"`
		After  []map[string]any `json:"after"`
	}
	if code := a.do(http.MethodGet, "/events/"+ev["id"].(string)+"/context?before=5&after=5", nil, &ctx); code != 200 ||
		len(ctx.Before)+len(ctx.After) == 0 {
		t.Errorf("context: %d %+v", code, ctx)
	}
	r = rng.query(a, "source spans | status:error", nil)
	if len(r.Events) != 1 || r.Events[0]["duration_ms"] != float64(250) {
		t.Errorf("span records: %+v", r.Events)
	}
}

func checkStats(t *testing.T, a *apiClient, rng itRange) {
	r := rng.query(a, "* | stats count() by protocol | sort protocol", nil)
	if r.Kind != "table" || len(r.Rows) != len(wantByProtocol) {
		t.Fatalf("table %+v", r)
	}
	for _, row := range r.Rows {
		if wantByProtocol[row[0].(string)] != row[1].(float64) {
			t.Errorf("protocol %v: %v", row[0], row[1])
		}
	}
	r = rng.query(a, "* | stats count() by level span auto", map[string]any{"span": "1h"})
	var total float64
	for _, s := range r.Series {
		for _, p := range s.Points {
			total += p[1]
		}
	}
	if r.Kind != "timeseries" || len(r.Series) < 3 || total != wantEvents {
		t.Errorf("timeseries kind=%s series=%d total=%v", r.Kind, len(r.Series), total)
	}
	r = rng.query(a, "source metrics | * | stats count() by name", nil)
	if len(r.Rows) == 0 {
		t.Errorf("metrics table empty")
	}
}

func checkExplorer(t *testing.T, a *apiClient, rng itRange) {
	var h struct {
		Span    int64 `json:"span"`
		Buckets []struct {
			Counts map[string]float64 `json:"counts"`
		} `json:"buckets"`
	}
	if code := a.do(http.MethodGet, "/histogram?q=*&buckets=80&"+rng.params(""), nil, &h); code != 200 {
		t.Fatalf("histogram %d", code)
	}
	var total, errs float64
	for _, b := range h.Buckets {
		errs += b.Counts["Error"]
		for _, n := range b.Counts {
			total += n
		}
	}
	if total != wantEvents || errs == 0 || len(h.Buckets) > 80 || h.Span <= 0 {
		t.Errorf("histogram total=%v errors=%v buckets=%d span=%d", total, errs, len(h.Buckets), h.Span)
	}
	var facets []struct {
		Field  string `json:"field"`
		Values []struct {
			Value string  `json:"value"`
			Count float64 `json:"count"`
		} `json:"values"`
		OtherCount float64 `json:"otherCount"`
		Sampled    bool    `json:"sampled"`
	}
	if code := a.do(http.MethodGet, "/facets?q=*&fields=protocol,level,RequestPath&top=2&"+rng.params(""), nil, &facets); code != 200 || len(facets) != 3 {
		t.Fatalf("facets %d %+v", code, facets)
	}
	p := facets[0]
	if p.Field != "protocol" || len(p.Values) != 2 || p.Values[0].Count+p.Values[1].Count+p.OtherCount != wantEvents || p.Sampled {
		t.Errorf("protocol facet %+v", p)
	}
	if len(facets[2].Values) == 0 {
		t.Errorf("property facet %+v", facets[2])
	}
	var fields []map[string]any
	if code := a.do(http.MethodGet, "/fields?source=logs&"+rng.params(""), nil, &fields); code != 200 {
		t.Fatalf("fields %d", code)
	}
	names := map[string]bool{}
	for _, f := range fields {
		names[f["name"].(string)] = true
	}
	if !names["level"] || !names["RequestPath"] || !names["StatusCode"] {
		t.Errorf("fields %v", names)
	}
	var comp struct {
		Field       string `json:"field"`
		Suggestions []struct {
			Kind  string `json:"kind"`
			Label string `json:"label"`
		} `json:"suggestions"`
	}
	if code := a.do(http.MethodGet, "/tql/complete?q=protocol:s&pos=10", nil, &comp); code != 200 || comp.Field != "protocol" ||
		len(comp.Suggestions) == 0 || comp.Suggestions[0].Label != "seq" {
		t.Errorf("complete %d %+v", code, comp)
	}
}

func checkTraces(t *testing.T, a *apiClient, rng itRange) {
	const traceID = "5b8efff798038103d269b633813fc60c"
	var tr struct {
		Spans  []map[string]any `json:"spans"`
		Events []map[string]any `json:"events"`
	}
	if code := a.do(http.MethodGet, "/traces/"+strings.ToUpper(traceID), nil, &tr); code != 200 || len(tr.Spans) != wantSpans || len(tr.Events) == 0 {
		t.Errorf("trace %d spans=%d events=%d", code, len(tr.Spans), len(tr.Events))
	}
	var sums []map[string]any
	if code := a.do(http.MethodGet, "/traces?q=service:payments&"+rng.params(""), nil, &sums); code != 200 || len(sums) != 1 ||
		sums[0]["traceId"] != traceID || sums[0]["spanCount"] != float64(wantSpans) || sums[0]["rootService"] != "frontend" {
		t.Errorf("trace search %d %+v", code, sums)
	}
	var names []map[string]any
	if code := a.do(http.MethodGet, "/metrics/names?"+rng.params(""), nil, &names); code != 200 || len(names) == 0 {
		t.Errorf("metric names %d %+v", code, names)
	}
}

func checkExport(t *testing.T, a *apiClient, rng itRange) {
	req, _ := http.NewRequest(http.MethodGet, a.base+"/api/v1/export?q=protocol:native&format=ndjson&"+rng.params(""), nil)
	resp, err := a.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if resp.StatusCode != 200 || len(lines) != int(wantByProtocol["native"]) {
		t.Errorf("export %d: %d lines", resp.StatusCode, len(lines))
	}
}

func checkTail(t *testing.T, a *apiClient, base, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.base+"/api/v1/tail?q=service:tail-it", nil)
	resp, err := (&http.Client{Jar: a.c.Jar}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("tail: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	marker := fmt.Sprintf("tail marker %d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"@t":%q,"message":%q,"service":"tail-it"}`, time.Now().UTC().Format(time.RFC3339Nano), marker)
	if got := post(t, base+"/api/v1/ingest/logs", "application/json", []byte(body), map[string]string{"X-Tidemark-Key": key}); got != 202 {
		t.Fatalf("ingest for tail: %d", got)
	}
	sc := bufio.NewScanner(resp.Body)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			event = name
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok && event == "events" && strings.Contains(data, marker) {
			return
		}
	}
	t.Errorf("tail ended without the marker event: %v", sc.Err())
}
