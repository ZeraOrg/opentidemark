// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	db := openDB(t, c)
	users := meta.NewUsers(db)
	u := &meta.User{Username: "ann", PasswordHash: "x", Role: meta.RoleEditor}
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	m := NewSessionManager(meta.NewSessions(db), users, c, time.Hour, 3*time.Hour)
	s, err := m.Create(ctx, u.ID, "1.2.3.4", strings.Repeat("u", 600))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Token) < 43 || s.CSRF == "" || s.ID != HashKey(s.Token) {
		t.Fatalf("session %+v", s)
	}
	p, err := m.Validate(ctx, s.Token)
	if err != nil || p.UserID != u.ID || p.Role != RoleEditor || p.Kind != KindSession || p.Actor() != "user:"+u.ID {
		t.Fatalf("validate: %+v %v", p, err)
	}
	if _, err := m.Validate(ctx, "bogus"); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("bogus: %v", err)
	}
	if _, err := m.Validate(ctx, ""); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("empty: %v", err)
	}
	// Activity keeps the session alive past the idle timeout…
	for range 4 {
		c.Advance(50 * time.Minute)
		if _, err := m.Validate(ctx, s.Token); err != nil && c.Now().Before(s.ExpiresAt) {
			t.Fatalf("active session rejected: %v", err)
		}
	}
	// …but not past the absolute timeout.
	if _, err := m.Validate(ctx, s.Token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("absolute timeout not enforced: %v", err)
	}
	s2, _ := m.Create(ctx, u.ID, "", "")
	c.Advance(61 * time.Minute)
	if _, err := m.Validate(ctx, s2.Token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("idle timeout not enforced: %v", err)
	}
	s3, _ := m.Create(ctx, u.ID, "", "")
	s4, _ := m.Create(ctx, u.ID, "", "")
	if err := m.RevokeUser(ctx, u.ID, s4.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Validate(ctx, s3.Token); err == nil {
		t.Error("revoked session still valid")
	}
	if _, err := m.Validate(ctx, s4.Token); err != nil {
		t.Error("kept session revoked")
	}
	if err := m.Revoke(ctx, s4.ID); err != nil {
		t.Fatal(err)
	}
	s5, _ := m.Create(ctx, u.ID, "", "")
	u.Disabled = true
	_ = users.Update(ctx, u)
	if _, err := m.Validate(ctx, s5.Token); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("disabled user: %v", err)
	}
}

func TestCSRF(t *testing.T) {
	pub, _ := url.Parse("https://tm.example.com")
	tests := []struct {
		name   string
		cookie string
		header string
		origin string
		want   bool
	}{
		{"match", "tok", "tok", "", true},
		{"match with origin", "tok", "tok", "https://tm.example.com", true},
		{"mismatch", "tok", "other", "", false},
		{"no header", "tok", "", "", false},
		{"no cookie", "", "tok", "", false},
		{"bad origin", "tok", "tok", "https://evil.example.com", false},
		{"bad scheme", "tok", "tok", "http://tm.example.com", false},
		{"garbage origin", "tok", "tok", "%zz", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/api/v1/x", nil)
		if tt.cookie != "" {
			r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: tt.cookie})
		}
		if tt.header != "" {
			r.Header.Set(CSRFHeader, tt.header)
		}
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if got := CSRFValid(r, pub); got != tt.want {
			t.Errorf("%s: got %v", tt.name, got)
		}
	}
	if !SafeMethod("GET") || !SafeMethod("HEAD") || SafeMethod("POST") || SafeMethod("DELETE") {
		t.Error("SafeMethod")
	}
}
