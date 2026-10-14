// SPDX-License-Identifier: Apache-2.0

// Package testutil holds fakes shared by tests of several packages. It is imported only
// from _test.go files.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// TestKey is the key accepted by FakeAuth.
const TestKey = "tmk_testkeytestkeytestkeytestkeytestkey1234"

// Now is the fixed time used by NewDeps.
var Now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// FakeAuth accepts TestKey for the connection Conn.
type FakeAuth struct {
	Conn  auth.Connection
	Calls int
	mu    sync.Mutex
}

// AuthenticateKey implements ingest.KeyAuthenticator.
func (f *FakeAuth) AuthenticateKey(_ context.Context, raw string, p auth.Protocol, _ netip.Addr) (auth.Connection, error) {
	f.mu.Lock()
	f.Calls++
	f.mu.Unlock()
	if raw != TestKey {
		return auth.Connection{}, auth.ErrUnauthorized
	}
	if !f.Conn.Allows(p) {
		return auth.Connection{}, auth.ErrForbidden
	}
	return f.Conn, nil
}

// Sink records everything enqueued; Err makes every enqueue fail.
type Sink struct {
	mu      sync.Mutex
	Events  []model.Event
	Spans   []model.Span
	Metrics []model.MetricPoint
	Err     error
}

// EnqueueEvents implements ingest.EventSink.
func (s *Sink) EnqueueEvents(_ context.Context, ev []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Events = append(s.Events, ev...)
	return nil
}

// EnqueueSpans implements ingest.SpanSink.
func (s *Sink) EnqueueSpans(_ context.Context, sp []model.Span) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Spans = append(s.Spans, sp...)
	return nil
}

// EnqueueMetrics implements ingest.MetricSink.
func (s *Sink) EnqueueMetrics(_ context.Context, mp []model.MetricPoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Err != nil {
		return s.Err
	}
	s.Metrics = append(s.Metrics, mp...)
	return nil
}

// Observer counts self-metric callbacks.
type Observer struct {
	mu       sync.Mutex
	Accepts  map[string]int
	Drops    map[string]int
	Rejects  map[string]int
	Reroutes map[string]int
	usageIDs []string
}

// NewObserver returns an empty observer.
func NewObserver() *Observer {
	return &Observer{Accepts: map[string]int{}, Drops: map[string]int{}, Rejects: map[string]int{}, Reroutes: map[string]int{}}
}

// Accepted implements ingest.Observer.
func (o *Observer) Accepted(protocol string, source model.Source, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Accepts[protocol+"/"+string(source)] += n
}

// Dropped implements ingest.Observer.
func (o *Observer) Dropped(protocol, reason string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Drops[protocol+"/"+reason] += n
}

// Rerouted implements ingest.Observer.
func (o *Observer) Rerouted(protocol, reason string, n int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Reroutes[protocol+"/"+reason] += n
}

// Rejected implements ingest.Observer.
func (o *Observer) Rejected(protocol, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.Rejects[protocol+"/"+reason]++
}

// Record implements ingest.UsageRecorder.
func (o *Observer) Record(id, _ string, _ int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.usageIDs = append(o.usageIDs, id)
}

// UsageIDs returns the recorded connection ids.
func (o *Observer) UsageIDs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.usageIDs...)
}

// AllProtocolsConn returns a connection allowing every protocol with stream "app".
func AllProtocolsConn() auth.Connection {
	return auth.Connection{ID: "conn-1", Name: "test", Protocols: auth.AllProtocols(), DefaultStream: "app"}
}

// NewDeps wires fakes into ingest.Deps with small limits suited to tests.
func NewDeps(a ingest.KeyAuthenticator, sink *Sink, obs *Observer, limiter ingest.RateLimiter) *ingest.Deps {
	return &ingest.Deps{
		Auth: a, Events: sink, Spans: sink, Metrics: sink, Limiter: limiter, Usage: obs, Observer: obs,
		Limits:   ingest.Limits{MaxBodyBytes: 1 << 20, MaxDecompressedBytes: 4 << 20},
		Now:      func() time.Time { return Now },
		ClientIP: func(*http.Request) netip.Addr { return netip.MustParseAddr("127.0.0.1") },
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:  "test",
	}
}
