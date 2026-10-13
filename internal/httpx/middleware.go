// SPDX-License-Identifier: Apache-2.0

// Package httpx holds HTTP plumbing shared by every listener (SPEC §15.4, §15.7): security
// headers, request ids, access logging with redaction, panic recovery, body limits, client IP
// resolution behind trusted proxies, server timeouts, TLS settings and the SSRF-safe client.
package httpx

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies mws so that the first one is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ContentSecurityPolicy is the CSP of every UI/API response (SPEC §15.7).
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// hstsValue is sent when the public URL is https.
const hstsValue = "max-age=31536000"

// SecurityHeaders sets the hardening headers; hsts adds Strict-Transport-Security.
func SecurityHeaders(hsts bool) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Security-Policy", ContentSecurityPolicy)
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			h.Set("X-Frame-Options", "DENY")
			if hsts {
				h.Set("Strict-Transport-Security", hstsValue)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequestIDHeader carries the request id.
const RequestIDHeader = "X-Request-Id"

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type requestIDKey struct{}

// RequestID accepts a well-formed incoming X-Request-Id or generates one, echoes it in the
// response and stores it in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// RequestIDFrom returns the request id stored by RequestID.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

const requestIDBytes = 12

func newRequestID() string {
	b := make([]byte, requestIDBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error on supported platforms
	return hex.EncodeToString(b)
}

// statusWriter records the status code and body size; it keeps Flush/Hijack/Unwrap working.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer (SSE deadlines, flushing).
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Flush implements http.Flusher.
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack implements http.Hijacker.
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

// sensitiveParams are redacted from logged query strings.
var sensitiveParams = map[string]bool{"apikey": true, "api_key": true, "token": true, "key": true, "password": true}

// RedactQuery returns the query string with sensitive parameter values replaced.
func RedactQuery(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	out := url.Values{}
	for k, vs := range q {
		if sensitiveParams[strings.ToLower(k)] {
			out[k] = []string{"REDACTED"}
			continue
		}
		out[k] = vs
	}
	return out.Encode()
}

const maxLoggedUserAgent = 256

// AccessLog logs one line per request (never bodies; sensitive query values redacted).
func AccessLog(logger *slog.Logger, clientIP func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			ua := r.UserAgent()
			if len(ua) > maxLoggedUserAgent {
				ua = ua[:maxLoggedUserAgent]
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "http request",
				slog.String("method", r.Method), slog.String("path", r.URL.Path),
				slog.String("query", RedactQuery(r.URL.Query())), slog.Int("status", sw.status),
				slog.Int64("bytes", sw.bytes), slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("ip", clientIP(r)), slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("user_agent", ua))
		})
	}
}

// Recover turns panics into a generic 500 and logs them with the request id.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() { //nolint:contextcheck // recovery handler; the request context is used for logging
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler { //nolint:errorlint // sentinel value comparison is intended
					panic(rec)
				}
				logger.ErrorContext(r.Context(), "panic in handler", "panic", fmt.Sprint(rec), "path", r.URL.Path,
					"request_id", RequestIDFrom(r.Context()))
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"internal error"}}`)) // best effort
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBody limits request bodies read by handlers to n bytes.
func MaxBody(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}
