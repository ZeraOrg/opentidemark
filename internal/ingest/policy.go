// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"
	"maps"
	"net/http"
	"net/netip"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/normalize"
)

// Applied-property keys that pin the stream instead of becoming properties.
const (
	pinStreamKey    = "@stream"
	pinStreamKeyAlt = "_stream"
)

// Drop reasons reported to the Observer.
const (
	DropBelowMinimumLevel = "below_minimum_level"
)

// Request is an authenticated ingestion request.
type Request struct {
	Conn     auth.Connection
	Protocol auth.Protocol
	Remote   netip.Addr
	policy   connPolicy
}

// connPolicy is the per-request view of the connection's ingest rules.
type connPolicy struct {
	pinnedStream string
	applied      map[string]any
	promoted     normalize.Fields
}

// Authenticate extracts and validates the key of r for protocol p.
func (d *Deps) Authenticate(r *http.Request, p auth.Protocol, allowQueryKey bool) (*Request, error) {
	key, src := KeyFromRequest(r, allowQueryKey)
	remote := d.ClientIP(r)
	if src == KeyQueryAPIKey && d.Logger != nil {
		d.Logger.Warn("ingest key passed in query string; prefer the X-Seq-ApiKey header",
			"protocol", string(p), "remote", remote.String(), "key", auth.Redact(key))
	}
	return d.AuthenticateRaw(r.Context(), key, p, remote)
}

// AuthenticateRaw validates a key obtained elsewhere (e.g. gRPC metadata).
func (d *Deps) AuthenticateRaw(ctx context.Context, key string, p auth.Protocol, remote netip.Addr) (*Request, error) {
	conn, err := d.Auth.AuthenticateKey(ctx, key, p, remote)
	if err != nil {
		if d.Observer != nil {
			d.Observer.Rejected(string(p), "auth")
		}
		return nil, err
	}
	return &Request{Conn: conn, Protocol: p, Remote: remote, policy: newConnPolicy(conn)}, nil
}

func newConnPolicy(c auth.Connection) connPolicy {
	p := connPolicy{applied: map[string]any{}}
	for k, v := range c.AppliedProperties {
		if k == pinStreamKey || k == pinStreamKeyAlt {
			if s, ok := v.(string); ok && s != "" {
				p.pinnedStream = model.NormalizeStream(s)
			}
			continue
		}
		p.applied[k] = v
	}
	if len(p.applied) > 0 {
		p.promoted = normalize.Promote(p.applied, normalize.Options{})
	}
	return p
}

// Stream picks the stream: pinned by the connection, else the explicit value, else the
// connection default (SPEC §9.1).
func (r *Request) Stream(explicit string) string {
	switch {
	case r.policy.pinnedStream != "":
		return r.policy.pinnedStream
	case explicit != "":
		return model.NormalizeStream(explicit)
	default:
		return model.NormalizeStream(r.Conn.DefaultStream)
	}
}

// CheckRate enforces the connection's events/s limit for n items.
func (d *Deps) CheckRate(r *Request, n int) error {
	if r.Conn.RateLimitEPS <= 0 || d.Limiter == nil || n == 0 {
		return nil
	}
	if ok, wait := d.Limiter.AllowRate(r.Conn.ID, n, r.Conn.RateLimitEPS); !ok {
		if d.Observer != nil {
			d.Observer.Rejected(string(r.Protocol), "rate_limited")
		}
		return &RateLimitedError{RetryAfter: wait}
	}
	return nil
}

// PrepareEvents applies the connection policy and finalizes every event in place (so callers
// can report generated ids), then returns a new slice with the events to enqueue and the
// number dropped for being below the connection's minimum level.
func (d *Deps) PrepareEvents(r *Request, events []model.Event) ([]model.Event, int) {
	now := d.Now()
	kept := make([]model.Event, 0, len(events))
	dropped := 0
	for i := range events {
		ev := &events[i]
		r.applyToEvent(ev)
		normalize.Finalize(ev, now)
		if r.Conn.MinimumLevel != nil && ev.Level < *r.Conn.MinimumLevel {
			dropped++
			continue
		}
		kept = append(kept, *ev)
	}
	if dropped > 0 && d.Observer != nil {
		d.Observer.Dropped(string(r.Protocol), DropBelowMinimumLevel, dropped)
	}
	return kept, dropped
}

func (r *Request) applyToEvent(ev *model.Event) {
	ev.Stream = r.Stream(ev.Stream)
	ev.ConnectionID = r.Conn.ID
	ev.Protocol = string(r.Protocol)
	if len(r.policy.applied) > 0 {
		if ev.Properties == nil {
			ev.Properties = make(map[string]any, len(r.policy.applied))
		}
		maps.Copy(ev.Properties, r.policy.applied)
		pr := r.policy.promoted
		ev.Service = firstNonEmpty(pr.Service, ev.Service)
		ev.Environment = firstNonEmpty(pr.Environment, ev.Environment)
		ev.Host = firstNonEmpty(pr.Host, ev.Host)
	}
	if ev.Service == "" {
		ev.Service = ev.Stream
	}
}

// PrepareSpans applies stream selection, applied properties and the connection id.
func (r *Request) PrepareSpans(spans []model.Span) {
	for i := range spans {
		s := &spans[i]
		s.Stream = r.Stream(s.Stream)
		s.ConnectionID = r.Conn.ID
		s.Attributes = r.mergeApplied(s.Attributes)
		if s.Service == "" {
			s.Service = s.Stream
		}
	}
}

// PrepareMetrics applies stream selection, applied properties and the connection id.
func (r *Request) PrepareMetrics(points []model.MetricPoint) {
	for i := range points {
		p := &points[i]
		p.Stream = r.Stream(p.Stream)
		p.ConnectionID = r.Conn.ID
		p.Attributes = r.mergeApplied(p.Attributes)
		if p.Service == "" {
			p.Service = p.Stream
		}
	}
}

func (r *Request) mergeApplied(attrs map[string]any) map[string]any {
	if len(r.policy.applied) == 0 {
		return attrs
	}
	if attrs == nil {
		attrs = make(map[string]any, len(r.policy.applied))
	}
	maps.Copy(attrs, r.policy.applied)
	return attrs
}

// Accepted records usage and self-metrics for n accepted items of a source.
func (d *Deps) Accepted(r *Request, source model.Source, n int) {
	if n == 0 {
		return
	}
	if d.Usage != nil {
		d.Usage.Record(r.Conn.ID, r.Remote.String(), n)
	}
	if d.Observer != nil {
		d.Observer.Accepted(string(r.Protocol), source, n)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
