// SPDX-License-Identifier: Apache-2.0

// Package ingest holds what every ingestion protocol shares (SPEC §9.1): the sink and key
// authentication ports, request authentication, body decoding limits, connection policy
// (stream, applied properties, minimum level, rate limit) and error classification.
// Protocol handlers live in sub-packages and only parse and map.
package ingest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// EventSink accepts normalized events; it returns ErrBackpressure when full.
type EventSink interface {
	EnqueueEvents(ctx context.Context, ev []model.Event) error
}

// SpanSink accepts spans.
type SpanSink interface {
	EnqueueSpans(ctx context.Context, sp []model.Span) error
}

// MetricSink accepts metric points.
type MetricSink interface {
	EnqueueMetrics(ctx context.Context, mp []model.MetricPoint) error
}

// KeyAuthenticator validates connection keys.
type KeyAuthenticator interface {
	AuthenticateKey(ctx context.Context, raw string, proto auth.Protocol, remote netip.Addr) (auth.Connection, error)
}

// RateLimiter enforces per-connection events/s limits.
type RateLimiter interface {
	AllowRate(key string, n, ratePerSec int) (bool, time.Duration)
}

// UsageRecorder records connection usage (last used, counters).
type UsageRecorder interface {
	Record(connectionID, ip string, n int)
}

// Observer receives ingestion self-metrics.
type Observer interface {
	Accepted(protocol string, source model.Source, n int)
	Dropped(protocol, reason string, n int)
	Rejected(protocol, reason string)
}

// ErrBackpressure is returned by sinks when their queue cannot take the whole request.
var ErrBackpressure = errors.New("ingest queue is full")

// Errors produced while handling requests.
var (
	ErrBodyTooLarge        = errors.New("request body too large")
	ErrUnsupportedEncoding = errors.New("unsupported content encoding")
	ErrBadRequest          = errors.New("bad request")
)

// RateLimitedError carries the suggested retry delay.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "connection rate limit exceeded" }

// Limits are body size limits (SPEC §7).
type Limits struct {
	MaxBodyBytes         int64
	MaxDecompressedBytes int64
}

// Deps bundles the collaborators shared by protocol handlers.
type Deps struct {
	Auth     KeyAuthenticator
	Events   EventSink
	Spans    SpanSink
	Metrics  MetricSink
	Limiter  RateLimiter
	Usage    UsageRecorder
	Observer Observer
	Limits   Limits
	Now      func() time.Time
	ClientIP func(*http.Request) netip.Addr
	Logger   *slog.Logger
	Version  string
}

// Status maps an error to an HTTP status code.
func Status(err error) int {
	var rl *RateLimitedError
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, auth.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrBodyTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrUnsupportedEncoding):
		return http.StatusUnsupportedMediaType
	case errors.Is(err, ErrBackpressure):
		return http.StatusServiceUnavailable
	case errors.As(err, &rl):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrBadRequest):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// SetRetryHeaders sets Retry-After for 429/503 responses.
func SetRetryHeaders(w http.ResponseWriter, err error) {
	var rl *RateLimitedError
	switch {
	case errors.As(err, &rl):
		secs := int(rl.RetryAfter.Seconds() + 1)
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	case errors.Is(err, ErrBackpressure):
		w.Header().Set("Retry-After", "1")
	}
}

// PublicMessage returns a client-safe error message (internal errors are not detailed).
func PublicMessage(err error) string {
	if Status(err) == http.StatusInternalServerError {
		return "internal error"
	}
	return err.Error()
}
