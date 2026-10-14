// SPDX-License-Identifier: Apache-2.0

package native_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/ingest/native"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

func setup(t *testing.T) (*http.ServeMux, *testutil.Sink) {
	t.Helper()
	sink := &testutil.Sink{}
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, sink, testutil.NewObserver(), nil)
	mux := http.NewServeMux()
	native.New(d).Register(mux)
	return mux, sink
}

func post(mux *http.ServeMux, body []byte, key bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", native.Path, bytes.NewReader(body))
	if key {
		r.Header.Set("Authorization", "Bearer "+testutil.TestKey)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) native.Response {
	t.Helper()
	var r native.Response
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("%s: %v", w.Body.String(), err)
	}
	return r
}

func TestArrayFixture(t *testing.T) {
	mux, sink := setup(t)
	body, _ := os.ReadFile("testdata/array.json")
	w := post(mux, body, true)
	resp := decode(t, w)
	if w.Code != http.StatusAccepted || resp.Accepted != 3 || resp.Rejected != 1 || resp.Errors[0].Index != 2 {
		t.Fatalf("%d %+v", w.Code, resp)
	}
	ev := sink.Events[0]
	if ev.Level != model.Error || ev.Service != "billing" || ev.Environment != "prod" || ev.Stream != "billing" ||
		ev.Message != "payment failed" || ev.Protocol != "native" {
		t.Errorf("event 0 %+v", ev)
	}
	if _, ok := ev.Properties["_stream"]; ok {
		t.Error("_stream kept")
	}
	if ev := sink.Events[1]; ev.Message != "User 7 signed in" || ev.Stream != "app" {
		t.Errorf("event 1 %+v", ev)
	}
	if ev := sink.Events[2]; !ev.Timestamp.Equal(time.Unix(1791892682, 0).UTC()) || ev.Message != "bare" {
		t.Errorf("event 2 %+v", ev)
	}
}

func TestNDJSONFixture(t *testing.T) {
	mux, sink := setup(t)
	body, _ := os.ReadFile("testdata/lines.ndjson")
	resp := decode(t, post(mux, body, true))
	if resp.Accepted != 2 || resp.Rejected != 1 || resp.Errors[0].Index != 1 {
		t.Fatalf("%+v", resp)
	}
	if ev := sink.Events[0]; ev.Level != model.Warning || ev.Message != "slow query" || ev.Host != "db-01" {
		t.Errorf("event 0 %+v", ev)
	}
	if ev := sink.Events[1]; ev.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || ev.SpanID != "00f067aa0ba902b7" {
		t.Errorf("event 1 %+v", ev)
	}
}

func TestSingleObjectAndErrors(t *testing.T) {
	mux, sink := setup(t)
	if resp := decode(t, post(mux, []byte(`{"message":"one"}`), true)); resp.Accepted != 1 || len(resp.Errors) != 0 {
		t.Errorf("%+v", resp)
	}
	if len(sink.Events) != 1 {
		t.Fatal("not enqueued")
	}
	if w := post(mux, []byte(`{"message":"one"}`), false); w.Code != 401 || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
		t.Errorf("no key %d %s", w.Code, w.Body.String())
	}
	tests := []struct {
		body string
		code int
	}{
		{"plain text", 400}, {"[1, 2", 400}, {"", 202},
		{strings.Repeat("[", 70) + strings.Repeat("]", 70), 400},
		{"{" + strings.Repeat(`"a":{`, 70) + "1" + strings.Repeat("}", 71), 400},
	}
	for _, tt := range tests {
		if w := post(mux, []byte(tt.body), true); w.Code != tt.code {
			t.Errorf("%.20q: %d %s", tt.body, w.Code, w.Body.String())
		}
	}
	sink.Err = ingest.ErrBackpressure
	if w := post(mux, []byte(`{"message":"x"}`), true); w.Code != 503 || !strings.Contains(w.Body.String(), "backpressure") {
		t.Errorf("backpressure %d %s", w.Code, w.Body.String())
	}
}

func TestErrorCap(t *testing.T) {
	mux, _ := setup(t)
	items := make([]string, 30)
	for i := range items {
		items[i] = "1"
	}
	resp := decode(t, post(mux, []byte("["+strings.Join(items, ",")+"]"), true))
	if resp.Rejected != 30 || len(resp.Errors) != native.MaxReportedErrors {
		t.Errorf("%d %d", resp.Rejected, len(resp.Errors))
	}
}

func TestRateLimited(t *testing.T) {
	conn := testutil.AllProtocolsConn()
	conn.RateLimitEPS = 1
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, testutil.NewObserver(), deny{})
	mux := http.NewServeMux()
	native.New(d).Register(mux)
	if w := post(mux, []byte(`{"message":"x"}`), true); w.Code != 429 || !strings.Contains(w.Body.String(), "rate_limited") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("POST", native.Path, bytes.NewReader(bytes.Repeat([]byte("x"), 2<<20)))
	r.Header.Set("X-Tidemark-Key", testutil.TestKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 413 || !strings.Contains(w.Body.String(), "payload_too_large") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

type deny struct{}

func (deny) AllowRate(string, int, int) (bool, time.Duration) { return false, time.Second }
