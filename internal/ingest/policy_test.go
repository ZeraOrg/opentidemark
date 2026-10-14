// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

type fixedLimiter struct{ ok bool }

func (f fixedLimiter) AllowRate(string, int, int) (bool, time.Duration) {
	return f.ok, 1500 * time.Millisecond
}

func authed(t *testing.T, d *ingest.Deps, p auth.Protocol) *ingest.Request {
	t.Helper()
	req, err := d.AuthenticateRaw(t.Context(), testutil.TestKey, p, netip.MustParseAddr("127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestPrepareEvents(t *testing.T) {
	warn := model.Warning
	conn := testutil.AllProtocolsConn()
	conn.MinimumLevel = &warn
	conn.AppliedProperties = map[string]any{"team": "payments", "Environment": "prod", "@stream": "Pinned Stream"}
	obs := testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, obs, nil)
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("X-Tidemark-Key", testutil.TestKey)
	req, err := d.Authenticate(r, auth.ProtocolNative, false)
	if err != nil {
		t.Fatal(err)
	}
	events := []model.Event{
		{Level: model.Debug, Message: "dropped"},
		{Level: model.Error, Message: "kept", Stream: "explicit", Properties: map[string]any{"team": "x"}},
	}
	kept, dropped := d.PrepareEvents(req, events)
	if dropped != 1 || len(kept) != 1 || obs.Drops["native/"+ingest.DropBelowMinimumLevel] != 1 {
		t.Fatalf("kept %d dropped %d", len(kept), dropped)
	}
	ev := kept[0]
	if ev.Stream != "pinned-stream" || ev.Properties["team"] != "payments" || ev.Environment != "prod" ||
		ev.Service != "pinned-stream" || ev.ConnectionID != "conn-1" || ev.Protocol != "native" {
		t.Errorf("event %+v", ev)
	}
	if _, ok := ev.Properties["@stream"]; ok {
		t.Error("@stream leaked into properties")
	}
	if events[0].ID == events[1].ID || events[0].ID.Version() != 7 {
		t.Error("ids not assigned in place")
	}
}

func TestStreamSelection(t *testing.T) {
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, &testutil.Sink{}, testutil.NewObserver(), nil)
	req := authed(t, d, auth.ProtocolOTLP)
	tests := map[string]string{"": "app", "Orders": "orders", "a b": "a-b"}
	for in, want := range tests {
		if got := req.Stream(in); got != want {
			t.Errorf("Stream(%q) = %q want %q", in, got, want)
		}
	}
	spans := []model.Span{{Name: "s"}, {Name: "t", Stream: "x", Service: "svc"}}
	req.PrepareSpans(spans)
	if spans[0].Stream != "app" || spans[0].Service != "app" || spans[1].Stream != "x" || spans[1].ConnectionID != "conn-1" {
		t.Errorf("spans %+v", spans)
	}
	points := []model.MetricPoint{{Name: "m"}}
	req.PrepareMetrics(points)
	if points[0].Stream != "app" || points[0].Service != "app" {
		t.Errorf("points %+v", points)
	}
}

func TestAppliedPropertiesOnSpansAndMetrics(t *testing.T) {
	conn := testutil.AllProtocolsConn()
	conn.AppliedProperties = map[string]any{"region": "eu", "_stream": "pinned"}
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, testutil.NewObserver(), nil)
	req := authed(t, d, auth.ProtocolOTLP)
	spans := []model.Span{{Name: "s", Stream: "other"}}
	req.PrepareSpans(spans)
	points := []model.MetricPoint{{Name: "m", Attributes: map[string]any{"region": "us"}}}
	req.PrepareMetrics(points)
	if spans[0].Attributes["region"] != "eu" || spans[0].Stream != "pinned" || points[0].Attributes["region"] != "eu" {
		t.Errorf("spans %+v points %+v", spans, points)
	}
}

func TestCheckRateAndAccepted(t *testing.T) {
	conn := testutil.AllProtocolsConn()
	conn.RateLimitEPS = 10
	obs := testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, obs, fixedLimiter{ok: false})
	req := authed(t, d, auth.ProtocolSeq)
	err := d.CheckRate(req, 5)
	var rl *ingest.RateLimitedError
	if !errors.As(err, &rl) || ingest.Status(err) != http.StatusTooManyRequests {
		t.Fatalf("rate: %v", err)
	}
	w := httptest.NewRecorder()
	ingest.SetRetryHeaders(w, err)
	if w.Header().Get("Retry-After") != "2" {
		t.Errorf("retry-after %q", w.Header().Get("Retry-After"))
	}
	if d.CheckRate(req, 0) != nil {
		t.Error("zero items limited")
	}
	d.Limiter = fixedLimiter{ok: true}
	if d.CheckRate(req, 5) != nil {
		t.Error("allowed rate limited")
	}
	d.Accepted(req, model.SourceLogs, 3)
	d.Accepted(req, model.SourceLogs, 0)
	if obs.Accepts["seq/logs"] != 3 || len(obs.UsageIDs()) != 1 {
		t.Errorf("accepted %v usage %v", obs.Accepts, obs.UsageIDs())
	}
}

func TestAuthenticateFailures(t *testing.T) {
	conn := testutil.AllProtocolsConn()
	conn.Protocols = []auth.Protocol{auth.ProtocolSeq}
	obs := testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, obs, nil)
	r := httptest.NewRequest("POST", "/api/events/raw?apiKey="+testutil.TestKey, nil)
	if _, err := d.Authenticate(r, auth.ProtocolSeq, true); err != nil {
		t.Errorf("query key: %v", err)
	}
	if _, err := d.Authenticate(r, auth.ProtocolSeq, false); ingest.Status(err) != http.StatusUnauthorized {
		t.Errorf("query not allowed: %v", err)
	}
	r.Header.Set("X-Tidemark-Key", testutil.TestKey)
	if _, err := d.Authenticate(r, auth.ProtocolElastic, false); ingest.Status(err) != http.StatusForbidden {
		t.Errorf("protocol: %v", err)
	}
	if obs.Rejects["seq/auth"] != 1 || obs.Rejects["elastic/auth"] != 1 {
		t.Errorf("rejects %v", obs.Rejects)
	}
}

func TestStatusMapping(t *testing.T) {
	tests := []struct {
		err  error
		code int
		msg  string
	}{
		{auth.ErrUnauthorized, 401, "unauthorized"},
		{auth.ErrForbidden, 403, "forbidden"},
		{ingest.ErrBodyTooLarge, 413, ingest.ErrBodyTooLarge.Error()},
		{ingest.ErrUnsupportedEncoding, 415, ingest.ErrUnsupportedEncoding.Error()},
		{ingest.ErrBackpressure, 503, ingest.ErrBackpressure.Error()},
		{fmt.Errorf("x: %w", ingest.ErrBadRequest), 400, "x: bad request"},
		{errors.New("db exploded with secrets"), 500, "internal error"},
	}
	for _, tt := range tests {
		if got := ingest.Status(tt.err); got != tt.code {
			t.Errorf("Status(%v) = %d want %d", tt.err, got, tt.code)
		}
		if got := ingest.PublicMessage(tt.err); got != tt.msg {
			t.Errorf("PublicMessage(%v) = %q", tt.err, got)
		}
	}
	w := httptest.NewRecorder()
	ingest.SetRetryHeaders(w, ingest.ErrBackpressure)
	if w.Header().Get("Retry-After") != "1" {
		t.Error("backpressure retry-after")
	}
}

func TestWriteJSONAndSplitLines(t *testing.T) {
	w := httptest.NewRecorder()
	ingest.WriteJSON(w, 201, map[string]string{"a": "<b>"})
	if w.Code != 201 || w.Body.String() != "{\"a\":\"<b>\"}\n" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("%d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	ingest.WriteJSON(w, 200, func() {})
	if w.Code != 500 {
		t.Error("unencodable")
	}
	lines := ingest.SplitLines([]byte("a\r\n\n  \nb"))
	if len(lines) != 2 || lines[0].Number != 1 || lines[1].Number != 4 || string(lines[0].Data) != "a" {
		t.Errorf("%+v", lines)
	}
}

// TestAllowedStreams checks that an explicit stream outside the connection's allowed streams
// goes to the default stream and is counted (SPEC §9.1).
func TestAllowedStreams(t *testing.T) {
	conn := testutil.AllProtocolsConn()
	conn.AllowedStreams = []string{"app", "orders"}
	obs := testutil.NewObserver()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, &testutil.Sink{}, obs, nil)
	req := authed(t, d, auth.ProtocolOTLP)
	for in, want := range map[string]string{"": "app", "Orders": "orders", "payments": "app"} {
		if got := req.Stream(in); got != want {
			t.Errorf("Stream(%q) = %q want %q", in, got, want)
		}
	}
	spans := []model.Span{{Stream: "secret"}, {Stream: "orders"}}
	req.PrepareSpans(spans)
	points := []model.MetricPoint{{Stream: "secret"}}
	req.PrepareMetrics(points)
	events := []model.Event{{Stream: "secret", Message: "m"}}
	d.PrepareEvents(req, events)
	if spans[0].Stream != "app" || spans[1].Stream != "orders" || points[0].Stream != "app" || events[0].Stream != "app" {
		t.Errorf("rerouted %q %q %q %q", spans[0].Stream, spans[1].Stream, points[0].Stream, events[0].Stream)
	}
	if n := obs.Reroutes["otlp/"+ingest.RerouteStreamNotAllowed]; n != 4 {
		t.Errorf("rerouted count %d (the Stream calls above count once)", n)
	}
}
