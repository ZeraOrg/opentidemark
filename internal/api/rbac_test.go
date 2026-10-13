// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// expectedAccess is the route × role table (SPEC §11.2, §11.4, §11.5). Every registered route
// must appear here, and every entry must be registered.
var expectedAccess = map[string]Access{
	"POST /auth/login":                             Public,
	"POST /auth/logout":                            Authenticated,
	"GET /auth/me":                                 Authenticated,
	"POST /auth/password":                          Authenticated,
	"GET /users":                                   Admin,
	"POST /users":                                  Admin,
	"GET /users/{id}":                              Admin,
	"PATCH /users/{id}":                            Admin,
	"DELETE /users/{id}":                           Admin,
	"GET /tokens":                                  Authenticated,
	"POST /tokens":                                 Authenticated,
	"DELETE /tokens/{id}":                          Authenticated,
	"GET /connections":                             Admin,
	"POST /connections":                            Admin,
	"GET /connections/{id}":                        Admin,
	"PATCH /connections/{id}":                      Admin,
	"DELETE /connections/{id}":                     Admin,
	"POST /connections/{id}/rotate":                Admin,
	"POST /connections/{id}/revoke":                Admin,
	"GET /connections/{id}/snippets":               Admin,
	"GET /settings":                                Admin,
	"PATCH /settings":                              Admin,
	"GET /audit":                                   Admin,
	"GET /system":                                  Admin,
	"GET /streams":                                 Viewer,
	"POST /streams":                                Admin,
	"PATCH /streams/{name}":                        Admin,
	"DELETE /streams/{name}":                       Admin,
	"GET /streams/{name}/fields":                   Viewer,
	"GET /indexed-fields":                          Viewer,
	"POST /indexed-fields":                         Admin,
	"DELETE /indexed-fields/{id}":                  Admin,
	"POST /purge":                                  Admin,
	"GET /purge/{id}":                              Admin,
	"GET /dashboards":                              Viewer,
	"POST /dashboards":                             Editor,
	"GET /dashboards/templates":                    Viewer,
	"POST /dashboards/import":                      Editor,
	"GET /dashboards/{id}":                         Viewer,
	"PUT /dashboards/{id}":                         Editor,
	"DELETE /dashboards/{id}":                      Editor,
	"GET /dashboards/{id}/export":                  Viewer,
	"POST /dashboards/{id}/duplicate":              Editor,
	"POST /dashboards/{id}/panels/{panelId}/query": Viewer,
	"GET /alerts":                                  Viewer,
	"POST /alerts":                                 Editor,
	"GET /alerts/{id}":                             Viewer,
	"PUT /alerts/{id}":                             Editor,
	"DELETE /alerts/{id}":                          Editor,
	"POST /alerts/{id}/test":                       Editor,
	"POST /alerts/{id}/mute":                       Editor,
	"GET /alerts/{id}/history":                     Viewer,
	"GET /channels":                                Viewer,
	"POST /channels":                               Admin,
	"GET /channels/{id}":                           Viewer,
	"PUT /channels/{id}":                           Admin,
	"DELETE /channels/{id}":                        Admin,
	"POST /channels/{id}/test":                     Admin,
	"GET /imports":                                 Admin,
	"POST /imports":                                Admin,
	"GET /imports/{id}":                            Admin,
	"POST /imports/{id}/cancel":                    Admin,
	// search & analytics (SPEC §11.3) and signals (SPEC §11.4)
	"POST /query":              Viewer,
	"POST /query/validate":     Viewer,
	"GET /histogram":           Viewer,
	"GET /facets":              Viewer,
	"GET /fields":              Viewer,
	"GET /events/{id}":         Viewer,
	"GET /events/{id}/context": Viewer,
	"GET /tail":                Viewer,
	"GET /traces":              Viewer,
	"GET /traces/{traceId}":    Viewer,
	"GET /metrics/names":       Viewer,
	"GET /export":              Editor,
	"GET /tql/complete":        Viewer,
	"GET /signals":             Viewer,
	"POST /signals":            Editor,
	"GET /signals/{id}":        Viewer,
	"PUT /signals/{id}":        Editor,
	"DELETE /signals/{id}":     Editor,
}

func TestRouteTableIsComplete(t *testing.T) {
	e := newEnv(t)
	seen := map[string]bool{}
	for _, r := range e.router.Routes() {
		key := r.Method + " " + r.Path
		want, ok := expectedAccess[key]
		if !ok {
			t.Errorf("route %s is missing from the access table", key)
			continue
		}
		if r.Access != want {
			t.Errorf("route %s requires %s, table says %s", key, r.Access, want)
		}
		seen[key] = true
	}
	for key := range expectedAccess {
		if !seen[key] {
			t.Errorf("table entry %s is not registered", key)
		}
	}
}

// pathParam matches a path wildcard such as {id} or {traceId}.
var pathParam = regexp.MustCompile(`\{[A-Za-z]+\}`)

// TestRouteRoleMatrix sends every route as anonymous, viewer, editor and admin and checks that
// authentication and authorization are enforced before the handler runs.
func TestRouteRoleMatrix(t *testing.T) {
	e := newEnv(t)
	roles := []struct {
		name   string
		client *client
		level  Access
	}{
		{"anonymous", nil, Public},
		{"viewer", e.sessions["viewer"], Viewer},
		{"editor", e.sessions["editor"], Editor},
		{"admin", e.sessions["admin"], Admin},
	}
	for _, r := range e.router.Routes() {
		if r.Path == "/auth/logout" {
			continue // logging out would invalidate the shared sessions; covered in auth tests
		}
		path := strings.NewReplacer("{name}", "no-such-stream", "{panelId}", "p1").Replace(r.Path)
		path = pathParam.ReplaceAllString(path, "01890000-0000-7000-8000-000000000000")
		for _, role := range roles {
			var body any
			if r.Method != http.MethodGet {
				body = "{}"
			}
			w := e.do(role.client, r.Method, path, body)
			switch {
			case r.Access == Public:
				if w.Code == http.StatusUnauthorized && r.Path != "/auth/login" {
					t.Errorf("%s %s as %s: public route returned 401", r.Method, r.Path, role.name)
				}
			case role.client == nil:
				if w.Code != http.StatusUnauthorized {
					t.Errorf("%s %s as anonymous: %d, want 401", r.Method, r.Path, w.Code)
				}
			case r.Access != Authenticated && role.level < r.Access:
				if w.Code != http.StatusForbidden {
					t.Errorf("%s %s as %s: %d, want 403", r.Method, r.Path, role.name, w.Code)
				}
			default:
				if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
					t.Errorf("%s %s as %s: %d %s, want access", r.Method, r.Path, role.name, w.Code, w.Body.String())
				}
			}
		}
	}
}

func TestCSRFRequiredForCookieMutations(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	noCSRF := &client{cookies: admin.cookies}
	if w := e.do(noCSRF, "POST", "/tokens", map[string]string{"name": "x", "scope": "read"}); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), "CSRF") {
		t.Errorf("missing CSRF: %d %s", w.Code, w.Body.String())
	}
	wrong := &client{cookies: admin.cookies, csrf: "nope"}
	if w := e.do(wrong, "PATCH", "/settings", "{}"); w.Code != http.StatusForbidden {
		t.Errorf("wrong CSRF: %d", w.Code)
	}
	if w := e.do(noCSRF, "GET", "/auth/me", nil); w.Code != http.StatusOK {
		t.Errorf("GET without CSRF: %d", w.Code)
	}
	// Bearer tokens are not cookie-authenticated, so no CSRF header is needed.
	w := e.do(admin, "POST", "/tokens", map[string]string{"name": "ci", "scope": "admin"})
	tok := decodeBody[tokenView](t, w).Token
	if w := e.do(&client{bearer: tok}, "POST", "/tokens", map[string]string{"name": "ci2", "scope": "read"}); w.Code != http.StatusCreated {
		t.Errorf("bearer mutation: %d %s", w.Code, w.Body.String())
	}
}
