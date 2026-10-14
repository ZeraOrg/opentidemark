// SPDX-License-Identifier: Apache-2.0

// Package api implements the HTTP API under /api/v1 (SPEC §11). Handlers are thin: decode,
// call a service, encode. Every route declares the access level it requires; the Router
// enforces authentication (session cookie or API token), CSRF, roles and the API rate limit.
//
// New API areas (query, dashboards, alerts, streams …) plug in by implementing Registrar and
// being registered in internal/app — existing modules are not edited (SPEC §6.2 OCP).
package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// Prefix is the mount point of the API.
const Prefix = "/api/v1"

// MaxRequestBody limits API request bodies (ingestion endpoints have their own limits).
const MaxRequestBody = 1 << 20

// API rate limit per session or token (SPEC §15.4).
const (
	APIRateBurst  = 300
	APIRateWindow = time.Minute
)

// Access is the minimum privilege a route requires.
type Access int

// Access levels.
const (
	Public        Access = iota // no authentication
	Authenticated               // any authenticated principal ("any" in SPEC §11.2)
	Viewer
	Editor
	Admin
)

// String names the access level (used in tests and docs).
func (a Access) String() string {
	return [...]string{"public", "any", "viewer", "editor", "admin"}[a]
}

func (a Access) role() auth.Role {
	switch a {
	case Viewer:
		return auth.RoleViewer
	case Editor:
		return auth.RoleEditor
	case Admin:
		return auth.RoleAdmin
	default:
		return ""
	}
}

// Handler handles a request; a returned error is mapped to the standard error body.
type Handler func(w http.ResponseWriter, r *http.Request) error

// Route is one API endpoint. Path is relative to Prefix and uses net/http patterns.
type Route struct {
	Method string
	Path   string
	Access Access
	Handle Handler
}

// Registrar is implemented by every API module.
type Registrar interface {
	Routes() []Route
}

// BodyLimiter is implemented by registrars whose routes accept bodies larger than
// MaxRequestBody (file uploads); keys are "METHOD /path" as in Route.
type BodyLimiter interface {
	BodyLimits() map[string]int64
}

// SessionValidator resolves session cookies.
type SessionValidator interface {
	Validate(ctx context.Context, token string) (auth.Principal, error)
}

// TokenValidator resolves bearer API tokens.
type TokenValidator interface {
	Authenticate(ctx context.Context, raw string) (auth.Principal, error)
}

// RouterDeps are the collaborators of the Router.
type RouterDeps struct {
	Sessions  SessionValidator
	Tokens    TokenValidator
	Limiter   *auth.KeyedLimiter
	PublicURL *url.URL
	ClientIP  func(*http.Request) netip.Addr
	Logger    *slog.Logger
}

// Router dispatches API routes with authentication and authorization.
type Router struct {
	d      RouterDeps
	mux    *http.ServeMux
	routes []Route
	limits map[string]int64 // "METHOD /path" ⇒ body limit above MaxRequestBody
}

// NewRouter returns an empty router.
func NewRouter(d RouterDeps) *Router {
	rt := &Router{d: d, mux: http.NewServeMux(), limits: map[string]int64{}}
	rt.mux.HandleFunc(Prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, d.Logger, errNotFound)
	})
	return rt
}

// Register mounts the routes of each registrar.
func (rt *Router) Register(regs ...Registrar) {
	for _, reg := range regs {
		if bl, ok := reg.(BodyLimiter); ok {
			for k, v := range bl.BodyLimits() {
				rt.limits[k] = v
			}
		}
		for _, route := range reg.Routes() {
			rt.routes = append(rt.routes, route)
			rt.mux.Handle(route.Method+" "+Prefix+route.Path, rt.wrap(route))
		}
	}
}

// Routes returns every registered route (used by the route × role test).
func (rt *Router) Routes() []Route { return append([]Route(nil), rt.routes...) }

// ServeHTTP implements http.Handler; request bodies are limited to MaxRequestBody unless the
// route declares a larger limit (BodyLimiter).
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	rt.mux.ServeHTTP(w, r)
}

func (rt *Router) wrap(route Route) http.Handler {
	limit, ok := rt.limits[route.Method+" "+route.Path]
	if !ok {
		limit = MaxRequestBody
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		if route.Access != Public {
			p, err := rt.authorize(r, route.Access)
			if err != nil {
				writeError(w, r, rt.d.Logger, err)
				return
			}
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		if err := route.Handle(w, r); err != nil {
			writeError(w, r, rt.d.Logger, err)
		}
	})
}

// authorize authenticates the request and checks CSRF, role and rate limit.
func (rt *Router) authorize(r *http.Request, access Access) (auth.Principal, error) {
	p, err := rt.authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if p.Kind == auth.KindSession && !auth.SafeMethod(r.Method) && !auth.CSRFValid(r, rt.d.PublicURL) {
		return auth.Principal{}, errCSRF
	}
	if access != Authenticated && !p.Role.Allows(access.role()) {
		return auth.Principal{}, auth.ErrForbidden
	}
	key := p.SessionID
	if p.Kind == auth.KindToken {
		key = "t:" + p.TokenID
	}
	if rt.d.Limiter != nil {
		if ok, wait := rt.d.Limiter.AllowWait(key); !ok {
			return auth.Principal{}, &rateLimited{retryAfter: wait}
		}
	}
	return p, nil
}

func (rt *Router) authenticate(r *http.Request) (auth.Principal, error) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil && c.Value != "" {
		return rt.d.Sessions.Validate(r.Context(), c.Value)
	}
	if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "bearer") {
		return rt.d.Tokens.Authenticate(r.Context(), strings.TrimSpace(token))
	}
	return auth.Principal{}, auth.ErrUnauthorized
}

// rateLimited carries Retry-After for 429 responses.
type rateLimited struct{ retryAfter time.Duration }

func (e *rateLimited) Error() string { return "rate limit exceeded" }

func (e *rateLimited) seconds() string { return strconv.Itoa(int(e.retryAfter.Seconds()) + 1) }

// actor builds the audit actor of the request.
func actor(r *http.Request, clientIP func(*http.Request) netip.Addr) service.Actor {
	a := service.Actor{ID: "anonymous"}
	if p, ok := auth.PrincipalFrom(r.Context()); ok {
		a.ID = p.Actor()
	}
	if ip := clientIP(r); ip.IsValid() {
		a.IP = ip.String()
	}
	return a
}

// principal returns the authenticated principal (routes with Access > Public always have one).
func principal(r *http.Request) auth.Principal {
	p, _ := auth.PrincipalFrom(r.Context())
	return p
}
