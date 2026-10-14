// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

func TestLoginLogoutMe(t *testing.T) {
	e := newEnv(t)
	if w := e.do(nil, "POST", "/auth/login", map[string]string{"username": "admin1", "password": "wrong"}); w.Code != 401 ||
		!strings.Contains(w.Body.String(), "invalid username or password") {
		t.Errorf("bad password: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(nil, "POST", "/auth/login", map[string]string{"username": "ghost", "password": "wrong"}); w.Code != 401 ||
		!strings.Contains(w.Body.String(), "invalid username or password") {
		t.Errorf("unknown user: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(nil, "POST", "/auth/login", map[string]string{"username": "", "password": ""}); w.Code != 400 {
		t.Errorf("empty: %d", w.Code)
	}
	w := e.do(nil, "POST", "/auth/login", map[string]string{"username": "viewer1", "password": testPassword})
	var session, csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case auth.SessionCookie:
			session = c
		case auth.CSRFCookie:
			csrf = c
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode || session.Path != "/" ||
		csrf == nil || csrf.HttpOnly {
		t.Fatalf("cookies %+v %+v", session, csrf)
	}
	c := &client{cookies: []*http.Cookie{session, csrf}, csrf: csrf.Value}
	me := decodeBody[map[string]any](t, e.do(c, "GET", "/auth/me", nil))
	if me["username"] != "viewer1" || me["role"] != "viewer" || me["defaultCredentials"] != false {
		t.Errorf("me %v", me)
	}
	if w := e.do(c, "POST", "/auth/logout", nil); w.Code != http.StatusNoContent {
		t.Errorf("logout %d", w.Code)
	}
	if w := e.do(c, "GET", "/auth/me", nil); w.Code != 401 {
		t.Errorf("after logout %d", w.Code)
	}
	audit := decodeBody[[]auditView](t, e.do(e.sessions["admin"], "GET", "/audit?action=auth.login_failed&from=now-1h&to=now", nil))
	if len(audit) != 2 || audit[0].Action != "auth.login_failed" {
		t.Errorf("audit %+v", audit)
	}
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	e := newEnv(t)
	first := e.sessions["editor"]
	second := e.login("editor1", testPassword)
	if w := e.do(first, "POST", "/auth/password", map[string]string{"currentPassword": "nope", "newPassword": "Another-Long-Pass-42"}); w.Code != 400 {
		t.Errorf("wrong current: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(first, "POST", "/auth/password", map[string]string{"currentPassword": testPassword, "newPassword": "short"}); w.Code != 400 {
		t.Errorf("weak: %d", w.Code)
	}
	if w := e.do(first, "POST", "/auth/password", map[string]string{"currentPassword": testPassword, "newPassword": testPassword}); w.Code != 400 {
		t.Errorf("same: %d", w.Code)
	}
	if w := e.do(first, "POST", "/auth/password", map[string]string{"currentPassword": testPassword, "newPassword": "Another-Long-Pass-42"}); w.Code != 204 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(first, "GET", "/auth/me", nil); w.Code != 200 {
		t.Errorf("own session revoked: %d", w.Code)
	}
	if w := e.do(second, "GET", "/auth/me", nil); w.Code != 401 {
		t.Errorf("other session kept: %d", w.Code)
	}
	e.login("editor1", "Another-Long-Pass-42")
}

func TestDefaultCredentialsFlag(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, _ := meta.NewUsers(e.db).GetByUsername(ctx, "admin1")
	_ = meta.NewSettings(e.db).Set(ctx, "security.default_admin_user_id", json.RawMessage(`"`+u.ID+`"`))
	admin := e.sessions["admin"]
	if me := decodeBody[map[string]any](t, e.do(admin, "GET", "/auth/me", nil)); me["defaultCredentials"] != true {
		t.Errorf("flag not set: %v", me)
	}
	e.do(admin, "POST", "/auth/password", map[string]string{"currentPassword": testPassword, "newPassword": "Brand-New-Secret-77"})
	if me := decodeBody[map[string]any](t, e.do(admin, "GET", "/auth/me", nil)); me["defaultCredentials"] != false {
		t.Errorf("flag not cleared: %v", me)
	}
}

func TestUsersCRUDAndGuards(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	w := e.do(admin, "POST", "/users", map[string]string{"username": "dana", "password": testPassword, "role": "viewer", "displayName": "Dana"})
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	dana := decodeBody[userView](t, w)
	tests := []struct {
		body map[string]any
		code int
	}{
		{map[string]any{"username": "dana", "password": testPassword, "role": "viewer"}, 400},
		{map[string]any{"username": "bad name", "password": testPassword, "role": "viewer"}, 400},
		{map[string]any{"username": "eve", "password": "Scandinavian", "role": "viewer"}, 400},
		{map[string]any{"username": "eve", "password": testPassword, "role": "root"}, 400},
		{map[string]any{"username": "eve", "password": testPassword, "role": "viewer", "extra": 1}, 400},
	}
	for _, tt := range tests {
		if w := e.do(admin, "POST", "/users", tt.body); w.Code != tt.code {
			t.Errorf("%v: %d %s", tt.body, w.Code, w.Body.String())
		}
	}
	list := decodeBody[[]userView](t, e.do(admin, "GET", "/users", nil))
	if len(list) != 4 {
		t.Errorf("list %d", len(list))
	}
	if got := decodeBody[userView](t, e.do(admin, "GET", "/users/"+dana.ID, nil)); got.DisplayName != "Dana" {
		t.Errorf("get %+v", got)
	}
	w = e.do(admin, "PATCH", "/users/"+dana.ID, map[string]any{"role": "editor", "password": "Reset-By-Admin-123"})
	if got := decodeBody[userView](t, w); got.Role != "editor" || !got.MustChangePassword {
		t.Errorf("patch %d %+v", w.Code, got)
	}
	e.login("dana", "Reset-By-Admin-123")
	adminUser := decodeBody[map[string]any](t, e.do(admin, "GET", "/auth/me", nil))["id"].(string)
	if w := e.do(admin, "PATCH", "/users/"+adminUser, map[string]any{"role": "viewer"}); w.Code != 409 {
		t.Errorf("demote last admin: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "DELETE", "/users/"+adminUser, nil); w.Code != 400 {
		t.Errorf("delete self: %d", w.Code)
	}
	if w := e.do(admin, "PATCH", "/users/"+dana.ID, map[string]any{"disabled": true}); w.Code != 200 {
		t.Errorf("disable: %d", w.Code)
	}
	if w := e.do(nil, "POST", "/auth/login", map[string]string{"username": "dana", "password": "Reset-By-Admin-123"}); w.Code != 401 {
		t.Errorf("disabled login: %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/users/"+dana.ID, nil); w.Code != 204 {
		t.Errorf("delete: %d", w.Code)
	}
	if w := e.do(admin, "GET", "/users/"+dana.ID, nil); w.Code != 404 {
		t.Errorf("deleted get: %d", w.Code)
	}
}

// TestUsersUnlimited guards the removal of the editions (SPEC §19): any number of accounts can be
// created, re-enabled and used to log in, and the former /license routes no longer exist.
func TestUsersUnlimited(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	const extra = 12
	for i := range extra {
		name := fmt.Sprintf("user%02d", i)
		if w := e.do(admin, "POST", "/users", map[string]string{"username": name, "password": testPassword, "role": "viewer"}); w.Code != 201 {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
	}
	all := decodeBody[[]userView](t, e.do(admin, "GET", "/users", nil))
	if len(all) != 3+extra {
		t.Fatalf("users: %d", len(all))
	}
	viewerID := ""
	for _, u := range all {
		if u.Username == "viewer1" {
			viewerID = u.ID
		}
	}
	e.do(admin, "PATCH", "/users/"+viewerID, map[string]any{"disabled": true})
	if w := e.do(admin, "PATCH", "/users/"+viewerID, map[string]any{"disabled": false}); w.Code != 200 {
		t.Errorf("re-enable: %d %s", w.Code, w.Body.String())
	}
	for _, name := range []string{"viewer1", "editor1", "user00", fmt.Sprintf("user%02d", extra-1)} {
		e.login(name, testPassword)
	}
	for _, method := range []string{"GET", "PUT"} {
		if w := e.do(admin, method, "/license", map[string]string{"key": "x"}); w.Code != http.StatusNotFound {
			t.Errorf("%s /license: %d", method, w.Code)
		}
	}
}

func TestTokens(t *testing.T) {
	e := newEnv(t)
	viewer, admin := e.sessions["viewer"], e.sessions["admin"]
	w := e.do(viewer, "POST", "/tokens", map[string]any{"name": "grafana", "scope": "admin", "expiresAt": time.Now().Add(time.Hour)})
	tok := decodeBody[tokenView](t, w)
	if w.Code != 201 || !strings.HasPrefix(tok.Token, "tmt_") || tok.KeyPrefix != tok.Token[:8] {
		t.Fatalf("create %d %+v", w.Code, tok)
	}
	bearer := &client{bearer: tok.Token}
	if me := decodeBody[map[string]any](t, e.do(bearer, "GET", "/auth/me", nil)); me["role"] != "viewer" {
		t.Errorf("token role must be capped by the user's role: %v", me)
	}
	if w := e.do(bearer, "GET", "/users", nil); w.Code != 403 {
		t.Errorf("viewer token on admin route: %d", w.Code)
	}
	for _, bad := range []map[string]any{{"name": "", "scope": "read"}, {"name": "x", "scope": "root"},
		{"name": "x", "scope": "read", "expiresAt": time.Now().Add(-time.Hour)}} {
		if w := e.do(viewer, "POST", "/tokens", bad); w.Code != 400 {
			t.Errorf("%v: %d", bad, w.Code)
		}
	}
	list := decodeBody[[]tokenView](t, e.do(viewer, "GET", "/tokens", nil))
	if len(list) != 1 || list[0].Token != "" {
		t.Errorf("list must not reveal tokens: %+v", list)
	}
	if w := e.do(e.sessions["editor"], "DELETE", "/tokens/"+tok.ID, nil); w.Code != 404 {
		t.Errorf("other user's token: %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/tokens/"+tok.ID, nil); w.Code != 204 {
		t.Errorf("admin delete: %d", w.Code)
	}
	if w := e.do(bearer, "GET", "/auth/me", nil); w.Code != 401 {
		t.Errorf("deleted token: %d", w.Code)
	}
}

func TestConnections(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	w := e.do(admin, "POST", "/connections", map[string]any{
		"name": "checkout", "protocols": []string{"seq", "otlp", "seq"}, "defaultStream": "checkout",
		"allowedCidrs": []string{"10.0.0.0/8", "192.168.1.5"}, "minimumLevel": "warn",
		"appliedProperties": map[string]any{"team": "payments"}, "rateLimitEps": 500,
	})
	conn := decodeBody[connectionView](t, w)
	if w.Code != 201 || !strings.HasPrefix(conn.Key, "tmk_") || len(conn.Key) != 44 || conn.KeyPrefix != conn.Key[:8] ||
		len(conn.Protocols) != 2 || conn.AllowedCIDRs[1] != "192.168.1.5/32" || *conn.MinimumLevel != "Warning" {
		t.Fatalf("create %d %+v", w.Code, conn)
	}
	if strings.Contains(e.do(admin, "GET", "/connections/"+conn.ID, nil).Body.String(), conn.Key) {
		t.Error("key returned after creation")
	}
	if conn.AllowedStreams == nil || len(conn.AllowedStreams) != 0 {
		t.Errorf("allowed streams default %#v", conn.AllowedStreams)
	}
	w = e.do(admin, "PATCH", "/connections/"+conn.ID, map[string]any{"allowedStreams": []string{"web", "checkout", "web"}})
	if got := decodeBody[connectionView](t, w); w.Code != 200 || strings.Join(got.AllowedStreams, ",") != "checkout,web" {
		t.Errorf("allowed streams %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "PATCH", "/connections/"+conn.ID, map[string]any{"defaultStream": "db"}); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "allowedStreams") {
		t.Errorf("default outside the allowed streams %d %s", w.Code, w.Body.String())
	}
	entries := decodeBody[[]auditView](t, e.do(admin, "GET", "/audit?action=connection.update", nil))
	if len(entries) != 1 || !strings.Contains(string(entries[0].Details), `"allowedStreams":["checkout","web"]`) {
		t.Errorf("audit %+v", entries)
	}
	bad := []map[string]any{
		{}, {"name": "checkout"}, {"name": "x", "protocols": []string{"syslog"}}, {"name": "x", "protocols": []string{}},
		{"name": "x", "defaultStream": "Bad Stream"}, {"name": "x", "allowedCidrs": []string{"nope"}},
		{"name": "x", "minimumLevel": "loud"}, {"name": "x", "rateLimitEps": -1},
		{"name": "x", "expiresAt": time.Now().Add(-time.Hour)}, {"name": "x", "appliedProperties": map[string]any{"@stream": "Bad!"}},
		{"name": strings.Repeat("n", 200)}, {"name": "x", "description": strings.Repeat("d", 2000)},
		{"name": "x", "allowedStreams": []string{"Bad!"}}, {"name": "x", "allowedStreams": []string{"web"}},
		{"name": "x", "defaultStream": "web", "allowedStreams": []string{"web"}, "appliedProperties": map[string]any{"@stream": "db"}},
	}
	for _, b := range bad {
		if w := e.do(admin, "POST", "/connections", b); w.Code != 400 {
			t.Errorf("%v: %d %s", b, w.Code, w.Body.String())
		}
	}
	w = e.do(admin, "PATCH", "/connections/"+conn.ID, map[string]any{"description": "d", "minimumLevel": "", "expiresAt": time.Now().Add(time.Hour)})
	if got := decodeBody[connectionView](t, w); got.Description != "d" || got.MinimumLevel != nil || got.ExpiresAt == nil {
		t.Errorf("patch %+v", got)
	}
	if got := decodeBody[connectionView](t, e.do(admin, "PATCH", "/connections/"+conn.ID, `{"expiresAt":null}`)); got.ExpiresAt != nil {
		t.Errorf("clear expiry %+v", got)
	}
	w = e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", map[string]int{"graceMinutes": 30})
	rot := decodeBody[connectionView](t, w)
	if w.Code != 200 || rot.Key == conn.Key || rot.RotationGraceUntil == nil {
		t.Errorf("rotate %d %+v", w.Code, rot)
	}
	if w := e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", map[string]int{"graceMinutes": 5000}); w.Code != 400 {
		t.Errorf("grace too long: %d", w.Code)
	}
	if w := e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", nil); w.Code != 200 {
		t.Errorf("rotate without body: %d", w.Code)
	}
	sw := e.do(admin, "GET", "/connections/"+conn.ID+"/snippets", nil)
	snips := decodeBody[[]map[string]string](t, sw)
	if len(snips) != 7 || !strings.Contains(snips[0]["content"], "http://tidemark.test") || strings.Contains(sw.Body.String(), "tmk_") {
		t.Errorf("snippets %v", snips)
	}
	if got := decodeBody[connectionView](t, e.do(admin, "POST", "/connections/"+conn.ID+"/revoke", nil)); got.RevokedAt == nil {
		t.Error("revoke")
	}
	if w := e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", nil); w.Code != 400 {
		t.Errorf("rotate revoked: %d", w.Code)
	}
	if list := decodeBody[[]connectionView](t, e.do(admin, "GET", "/connections", nil)); len(list) != 1 {
		t.Errorf("list %d", len(list))
	}
}

// TestConnectionSuppliedKey covers admin-supplied migration keys (SPEC §9.9, §15.3).
func TestConnectionSuppliedKey(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	const seqKey = "Xk3mQ9pLr2Tz8vNw4Hs7"
	w := e.do(admin, "POST", "/connections", map[string]any{"name": "from-seq", "key": seqKey})
	conn := decodeBody[connectionView](t, w)
	if w.Code != 201 || conn.Key != seqKey || conn.KeyPrefix != "Xk3m" {
		t.Fatalf("create with supplied key: %d %+v", w.Code, conn)
	}
	if w := e.do(admin, "POST", "/connections", map[string]any{"name": "dup", "key": seqKey}); w.Code != 409 {
		t.Errorf("duplicate supplied key: %d %s", w.Code, w.Body.String())
	}
	for _, weak := range []string{"short-key", strings.Repeat("a", 30), "Password1234567890ab", "has space in the key 12345"} {
		w := e.do(admin, "POST", "/connections", map[string]any{"name": "weak", "key": weak})
		if w.Code != 400 || !strings.Contains(w.Body.String(), "key") {
			t.Errorf("weak key %q: %d %s", weak, w.Code, w.Body.String())
		}
	}
	if w := e.do(admin, "PATCH", "/connections/"+conn.ID, map[string]any{"key": "ui2lp2axTNmsyakw9tvNnw"}); w.Code != 400 {
		t.Errorf("patch key: %d", w.Code)
	}
	w = e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", map[string]any{"key": "ui2lp2axTNmsyakw9tvNnw"})
	if rot := decodeBody[connectionView](t, w); w.Code != 200 || rot.Key != "ui2lp2axTNmsyakw9tvNnw" || rot.KeyPrefix != "ui2l" {
		t.Errorf("rotate with supplied key: %d %+v", w.Code, rot)
	}
	if w := e.do(admin, "POST", "/connections/"+conn.ID+"/rotate", map[string]any{"key": "weak"}); w.Code != 400 {
		t.Errorf("rotate with weak key: %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/connections/"+conn.ID, nil); w.Code != 204 {
		t.Errorf("delete %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/connections/"+conn.ID, nil); w.Code != 404 {
		t.Errorf("delete again %d", w.Code)
	}
}

func TestSettingsAuditSystem(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	s := decodeBody[map[string]json.RawMessage](t, e.do(admin, "GET", "/settings", nil))
	if string(s["retention"]) != `{"logsDays":30,"spansDays":7,"metricsDays":30}` || string(s["ui"]) != "{}" ||
		string(s["access"]) != `{"denyByDefault":false}` {
		t.Errorf("settings %s %s %s", s["retention"], s["ui"], s["access"])
	}
	w := e.do(admin, "PATCH", "/settings", `{"retention":{"logsDays":14,"spansDays":3,"metricsDays":60},"ui":{"theme":"dark"},`+
		`"access":{"denyByDefault":true}}`)
	s = decodeBody[map[string]json.RawMessage](t, w)
	if w.Code != 200 || string(s["retention"]) != `{"logsDays":14,"spansDays":3,"metricsDays":60}` || e.applier.calls != 1 ||
		string(s["access"]) != `{"denyByDefault":true}` {
		t.Errorf("patch %d %s calls=%d", w.Code, w.Body.String(), e.applier.calls)
	}
	for _, bad := range []string{`{"retention":{"logsDays":0,"spansDays":3,"metricsDays":60}}`, `{"retention":{"x":1}}`,
		`{"unknown":1}`, `{"ui":[1]}`, `{"ui":"x"}`, `{"access":{}}`, `{"access":{"denyByDefault":"yes"}}`,
		`{"access":{"denyByDefault":true,"x":1}}`, `{"access":true}`} {
		if w := e.do(admin, "PATCH", "/settings", bad); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	entries := decodeBody[[]auditView](t, e.do(admin, "GET", "/audit?action=settings.update", nil))
	if len(entries) != 1 || entries[0].IP != "192.0.2.10" || !strings.HasPrefix(entries[0].Actor, "user:") ||
		!strings.Contains(string(entries[0].Details), `"access.denyByDefault":true`) {
		t.Errorf("audit %+v", entries)
	}
	if w := e.do(admin, "GET", "/audit?from=yesterday", nil); w.Code != 400 {
		t.Errorf("bad from: %d", w.Code)
	}
	if w := e.do(admin, "GET", "/audit?limit=0", nil); w.Code != 400 {
		t.Errorf("bad limit: %d", w.Code)
	}
	if got := decodeBody[[]auditView](t, e.do(admin, "GET", "/audit?limit=2&actor=nobody", nil)); len(got) != 0 {
		t.Errorf("actor filter %+v", got)
	}
	sys := decodeBody[map[string]any](t, e.do(admin, "GET", "/system", nil))
	if sys["version"] != "test" || sys["clickhouse"] == nil {
		t.Errorf("system %v", sys)
	}
}

func TestErrorsAndLimits(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	w := e.do(admin, "GET", "/nope", nil)
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"not_found"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("unknown route %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "POST", "/tokens", "{bad"); w.Code != 400 || !strings.Contains(w.Body.String(), "bad_request") {
		t.Errorf("bad json %d", w.Code)
	}
	if w := e.do(admin, "POST", "/tokens", `{"name":"a","scope":"read"} {}`); w.Code != 400 {
		t.Errorf("trailing data %d", w.Code)
	}
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	if w := e.do(admin, "PATCH", "/settings", deep); w.Code != 400 {
		t.Errorf("deep json %d", w.Code)
	}
	big := `{"name":"` + strings.Repeat("x", MaxRequestBody) + `","scope":"read"}`
	if w := e.do(admin, "POST", "/tokens", big); w.Code != 413 || !strings.Contains(w.Body.String(), "payload_too_large") {
		t.Errorf("big body %d %s", w.Code, w.Body.String())
	}
	if w := e.do(&client{bearer: "tmt_unknown"}, "GET", "/auth/me", nil); w.Code != 401 {
		t.Errorf("unknown bearer %d", w.Code)
	}
	if w := e.do(&client{cookies: []*http.Cookie{{Name: auth.SessionCookie, Value: "forged"}}}, "GET", "/auth/me", nil); w.Code != 401 {
		t.Errorf("forged session %d", w.Code)
	}
}

func TestAPIRateLimit(t *testing.T) {
	e := newEnv(t)
	viewer := e.sessions["viewer"]
	// The bucket refills continuously (APIRateBurst per APIRateWindow), so on a slow machine a
	// few requests beyond the burst may pass; the first 429 must come after the burst.
	var last *httptest.ResponseRecorder
	sent := 0
	for sent < APIRateBurst+50 {
		last = e.do(viewer, "GET", "/dashboards", nil)
		sent++
		if last.Code == http.StatusTooManyRequests {
			break
		}
	}
	if last.Code != http.StatusTooManyRequests || last.Header().Get("Retry-After") == "" || sent <= APIRateBurst {
		t.Errorf("rate limit: %d after %d requests", last.Code, sent)
	}
	if w := e.do(e.sessions["editor"], "GET", "/dashboards", nil); w.Code != 200 {
		t.Errorf("other session limited: %d", w.Code)
	}
}

func TestParseTimeParam(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Time{
		"now": now, "now-15m": now.Add(-15 * time.Minute), "now-7d": now.Add(-7 * 24 * time.Hour),
		"now-2w": now.Add(-14 * 24 * time.Hour), "now-30s": now.Add(-30 * time.Second), "now-1h": now.Add(-time.Hour),
		"2026-10-12T00:00:00+02:00": time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC),
	}
	for in, want := range tests {
		got, err := ParseTimeParam(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "now-", "now-5y", "now-xm", "now--5m", "yesterday"} {
		if _, err := ParseTimeParam(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestHealth(t *testing.T) {
	ok := pingerFunc(func(context.Context) error { return nil })
	fail := pingerFunc(func(context.Context) error { return context.DeadlineExceeded })
	for _, tt := range []struct {
		checks []Pinger
		code   int
	}{{[]Pinger{ok}, 200}, {[]Pinger{ok, fail}, 503}} {
		mux := http.NewServeMux()
		(&Health{Checks: tt.checks}).Register(mux)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
		if w.Code != tt.code {
			t.Errorf("readyz %d", w.Code)
		}
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != 200 {
			t.Errorf("healthz %d", w.Code)
		}
	}
}

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }
