// SPDX-License-Identifier: Apache-2.0

package seq_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/ingest/seq"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/testutil"
)

type env struct {
	mux  *http.ServeMux
	sink *testutil.Sink
	obs  *testutil.Observer
	d    *ingest.Deps
}

func newEnv(t *testing.T) *env {
	t.Helper()
	sink, obs := &testutil.Sink{}, testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, sink, obs, nil)
	mux := http.NewServeMux()
	seq.New(d).Register(mux)
	return &env{mux: mux, sink: sink, obs: obs, d: d}
}

func (e *env) post(path, contentType string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var keyHeader = map[string]string{"X-Seq-ApiKey": testutil.TestKey}

func TestSerilogCLEF(t *testing.T) {
	e := newEnv(t)
	w := e.post("/api/events/raw", "application/vnd.serilog.clef", fixture(t, "serilog_clef.ndjson"), keyHeader)
	if w.Code != http.StatusCreated || strings.TrimSpace(w.Body.String()) != `{"MinimumLevelAccepted":null}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(e.sink.Events) != 5 {
		t.Fatalf("events %d", len(e.sink.Events))
	}
	ev := e.sink.Events[0]
	wantTS := time.Date(2026, 10, 13, 11, 58, 1, 123456700, time.UTC)
	if !ev.Timestamp.Equal(wantTS) || ev.Level != model.Information || ev.Service != "checkout-api" ||
		ev.Environment != "Production" || ev.Host != "WEB-01" || ev.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		ev.SpanID != "00f067aa0ba902b7" || ev.Stream != "app" || ev.Protocol != "seq" {
		t.Errorf("event 0: %+v", ev)
	}
	if ev.Message != "HTTP GET /api/orders/42 responded 200 in 12.3456 ms" {
		t.Errorf("rendered %q", ev.Message)
	}
	if ev.EventType != model.ComputeEventType(ev.MessageTemplate, "") || ev.Properties["@r"] == nil {
		t.Errorf("event type / @r: %+v", ev)
	}
	if _, ok := ev.Properties["@mt"]; ok {
		t.Error("@mt kept as property")
	}
	ev = e.sink.Events[1]
	if ev.Level != model.Error || !strings.HasPrefix(ev.Exception, "System.TimeoutException") ||
		ev.Message != `Payment pay_7Hx2 failed for {"$type":"Order","Currency":"EUR","Id":42,"Total":99.95}` {
		t.Errorf("event 1: %q %q", ev.Message, ev.Exception)
	}
	if !e.sink.Events[2].Timestamp.Equal(time.Date(2026, 10, 13, 9, 58, 3, 0, time.UTC)) || e.sink.Events[2].Level != model.Debug {
		t.Errorf("event 2 ts/level: %v %v", e.sink.Events[2].Timestamp, e.sink.Events[2].Level)
	}
	ev = e.sink.Events[3]
	if ev.EventType != "a1b2c3d4" || ev.Level != model.Verbose || ev.Stream != "health" || ev.Properties["@t"] != "user property named @t" {
		t.Errorf("event 3: %+v", ev)
	}
	if e.obs.Accepts["seq/logs"] != 5 {
		t.Errorf("observer %v", e.obs.Accepts)
	}
}

func TestCLEFRoutesAndDetection(t *testing.T) {
	line := []byte(`{"@t":"2026-10-13T11:58:01Z","@mt":"x {A}","A":1}`)
	tests := []struct {
		path, ct string
		want     int
	}{
		{"/ingest/clef", "", http.StatusCreated},
		{"/api/events/raw?clef", "application/json", http.StatusCreated},
		{"/api/events/raw", "application/vnd.serilog.clef; charset=utf-8", http.StatusCreated},
		{"/api/events/raw", "application/json", http.StatusBadRequest}, // legacy parser rejects CLEF
	}
	for _, tt := range tests {
		e := newEnv(t)
		w := e.post(tt.path, tt.ct, line, keyHeader)
		if w.Code != tt.want {
			t.Errorf("%s %s: %d %s", tt.path, tt.ct, w.Code, w.Body.String())
		}
	}
}

func TestNLogLegacy(t *testing.T) {
	e := newEnv(t)
	w := e.post("/api/events/raw", "application/json", fixture(t, "nlog_legacy.json"), keyHeader)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(e.sink.Events) != 2 {
		t.Fatalf("events %d", len(e.sink.Events))
	}
	ev := e.sink.Events[0]
	if ev.Message != "Order 1042 shipped to Amsterdam" || ev.Service != "shop" || ev.Host != "APP-02" ||
		!ev.Timestamp.Equal(time.Date(2026, 10, 13, 11, 58, 1, 123000000, time.UTC)) {
		t.Errorf("event 0 %+v", ev)
	}
	ev = e.sink.Events[1]
	if ev.Level != model.Error || !strings.Contains(ev.Exception, "SqlException") || ev.Message != "Database unavailable" {
		t.Errorf("event 1 %+v", ev)
	}
}

func TestInvalidInput(t *testing.T) {
	tests := []struct {
		name, path, ct, body, wantErr string
		code                          int
	}{
		{"all clef lines invalid", "/ingest/clef", "", "not json\n{broken", "line 1: ", 400},
		{"some clef lines invalid", "/ingest/clef", "", "bad\n{\"@m\":\"ok\"}", "", 201},
		{"legacy not object", "/api/events/raw", "application/json", "[1,2]", "line 1: ", 400},
		{"legacy no events", "/api/events/raw", "application/json", `{"x":1}`, "line 1: expected", 400},
		{"legacy all invalid", "/api/events/raw", "application/json", `{"Events":[1]}`, "line 1: event is not an object", 400},
		{"empty body", "/ingest/clef", "", "", "", 201},
	}
	for _, tt := range tests {
		e := newEnv(t)
		w := e.post(tt.path, tt.ct, []byte(tt.body), keyHeader)
		if w.Code != tt.code {
			t.Errorf("%s: code %d %s", tt.name, w.Code, w.Body.String())
			continue
		}
		var body struct{ Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if tt.wantErr != "" && !strings.HasPrefix(body.Error, tt.wantErr) {
			t.Errorf("%s: error %q", tt.name, body.Error)
		}
	}
}

func TestAuthAndBackpressure(t *testing.T) {
	e := newEnv(t)
	line := []byte(`{"@m":"x"}`)
	if w := e.post("/ingest/clef", "", line, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no key: %d", w.Code)
	}
	if w := e.post("/ingest/clef?apiKey="+testutil.TestKey, "", line, nil); w.Code != http.StatusCreated {
		t.Errorf("query key: %d", w.Code)
	}
	e.sink.Err = ingest.ErrBackpressure
	w := e.post("/ingest/clef", "", line, keyHeader)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
		t.Errorf("backpressure: %d %v", w.Code, w.Header())
	}
	e.sink.Err = errors.New("boom")
	if w := e.post("/ingest/clef", "", line, keyHeader); w.Code != 500 || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("internal: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", "/ingest/clef", bytes.NewReader(line))
	r.Header.Set("Content-Encoding", "br")
	r.Header.Set("X-Seq-ApiKey", testutil.TestKey)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("encoding: %d", rec.Code)
	}
}

func TestMinimumLevelAndRateLimit(t *testing.T) {
	warn := model.Warning
	conn := testutil.AllProtocolsConn()
	conn.MinimumLevel = &warn
	conn.RateLimitEPS = 1
	sink, obs := &testutil.Sink{}, testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, sink, obs, denyLimiter{})
	mux := http.NewServeMux()
	seq.New(d).Register(mux)
	r := httptest.NewRequest("POST", "/ingest/clef", strings.NewReader(`{"@m":"x"}`))
	r.Header.Set("X-Seq-ApiKey", testutil.TestKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" {
		t.Errorf("rate limit: %d", w.Code)
	}
	d.Limiter = nil
	r = httptest.NewRequest("POST", "/ingest/clef", strings.NewReader("{\"@m\":\"x\"}\n{\"@m\":\"y\",\"@l\":\"Error\"}"))
	r.Header.Set("X-Seq-ApiKey", testutil.TestKey)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 201 || strings.TrimSpace(w.Body.String()) != `{"MinimumLevelAccepted":"Warning"}` || len(sink.Events) != 1 {
		t.Errorf("min level: %d %s %d", w.Code, w.Body.String(), len(sink.Events))
	}
}

type denyLimiter struct{}

func (denyLimiter) AllowRate(string, int, int) (bool, time.Duration) { return false, time.Second }

func TestProbe(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/api", "/api/"} {
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"Product":"Tidemark","Version":"test"}` {
			t.Errorf("%s: %d %s", p, w.Code, w.Body.String())
		}
	}
}

func TestLineError(t *testing.T) {
	err := &seq.LineError{Line: 3, Reason: "bad"}
	if err.Error() != "line 3: bad" {
		t.Error(err.Error())
	}
}
