// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

func setupLogin(t *testing.T) (*Login, *meta.Users, *fakeClock, *meta.User) {
	t.Helper()
	ctx := context.Background()
	c := newFakeClock()
	db := openDB(t, c)
	users := meta.NewUsers(db)
	h := NewPasswordHasher(fastParams())
	hash, _ := h.Hash("right-password-123")
	u := &meta.User{Username: "bob", PasswordHash: hash, Role: meta.RoleAdmin}
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	sm := NewSessionManager(meta.NewSessions(db), users, c, time.Hour, 24*time.Hour)
	l, err := NewLogin(users, h, sm, c)
	if err != nil {
		t.Fatal(err)
	}
	return l, users, c, u
}

func TestLoginSuccessAndFailures(t *testing.T) {
	ctx := context.Background()
	l, users, c, u := setupLogin(t)
	res, err := l.Login(ctx, "BOB", "right-password-123", "10.0.0.1", "ua")
	if err != nil || res.User.ID != u.ID || res.Session.Token == "" {
		t.Fatalf("login: %+v %v", res, err)
	}
	var lf *LoginFailure
	_, err = l.Login(ctx, "nobody", "x", "10.0.0.1", "")
	if !errors.As(err, &lf) || lf.Reason != "unknown_user" || !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown: %v", err)
	}
	for i := 1; i <= 4; i++ {
		_, err = l.Login(ctx, "bob", "wrong", "10.0.0.2", "")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	got, _ := users.Get(ctx, u.ID)
	if got.FailedLogins != 4 || got.LockedUntil != nil {
		t.Fatalf("after 4: %+v", got)
	}
	_, _ = l.Login(ctx, "bob", "wrong", "10.0.0.2", "")
	got, _ = users.Get(ctx, u.ID)
	if got.LockedUntil == nil || !got.LockedUntil.Equal(c.Now().Add(15*time.Minute)) {
		t.Fatalf("lock: %+v", got.LockedUntil)
	}
	_, err = l.Login(ctx, "bob", "right-password-123", "10.0.0.3", "")
	if !errors.As(err, &lf) || lf.Reason != "locked" || !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("locked login: %v", err)
	}
	c.Advance(16 * time.Minute)
	if _, err := l.Login(ctx, "bob", "right-password-123", "10.0.0.3", ""); err != nil {
		t.Errorf("after lock expiry: %v", err)
	}
	got, _ = users.Get(ctx, u.ID)
	if got.FailedLogins != 0 || got.LastLoginAt == nil {
		t.Errorf("reset: %+v", got)
	}
	got.Disabled = true
	_ = users.Update(ctx, &got)
	if _, err := l.Login(ctx, "bob", "right-password-123", "10.0.0.3", ""); !errors.As(err, &lf) || lf.Reason != "disabled" {
		t.Errorf("disabled: %v", err)
	}
	if lf.Error() != ErrInvalidCredentials.Error() {
		t.Error("message leaks reason")
	}
}

func TestLoginIPRateLimit(t *testing.T) {
	l, _, _, _ := setupLogin(t)
	ctx := context.Background()
	for range LoginIPBurst {
		_, _ = l.Login(ctx, "nobody", "x", "9.9.9.9", "")
	}
	if _, err := l.Login(ctx, "bob", "right-password-123", "9.9.9.9", ""); !errors.Is(err, ErrRateLimited) {
		t.Errorf("rate limit: %v", err)
	}
	if _, err := l.Login(ctx, "bob", "right-password-123", "9.9.9.8", ""); err != nil {
		t.Errorf("other ip: %v", err)
	}
}

func TestLockoutDuration(t *testing.T) {
	tests := map[int]time.Duration{
		0: 0, 4: 0, 5: 15 * time.Minute, 6: 0, 10: 30 * time.Minute, 15: time.Hour, 20: 2 * time.Hour,
		30: 8 * time.Hour, 35: 16 * time.Hour, 40: 24 * time.Hour, 500: 24 * time.Hour,
	}
	for n, want := range tests {
		if got := LockoutDuration(n); got != want {
			t.Errorf("LockoutDuration(%d) = %v want %v", n, got, want)
		}
	}
}

func TestLoginRehashesOutdatedHash(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	db := openDB(t, c)
	users := meta.NewUsers(db)
	old := Argon2Params{MemoryKiB: 32, Time: 1, Threads: 1, SaltLen: 8, KeyLen: 16}
	oldHash, _ := NewPasswordHasher(old).Hash("right-password-123")
	u := &meta.User{Username: "carol", PasswordHash: oldHash, Role: meta.RoleViewer}
	if err := users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	h := NewPasswordHasher(fastParams())
	if !h.NeedsRehash(oldHash) || h.NeedsRehash("garbage") {
		t.Fatal("NeedsRehash")
	}
	l, err := NewLogin(users, h, NewSessionManager(meta.NewSessions(db), users, c, time.Hour, 24*time.Hour), c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Login(ctx, "carol", "right-password-123", "10.0.0.9", ""); err != nil {
		t.Fatalf("login with old parameters: %v", err)
	}
	got, _ := users.Get(ctx, u.ID)
	if got.PasswordHash == oldHash || h.NeedsRehash(got.PasswordHash) {
		t.Fatalf("hash not upgraded: %s", got.PasswordHash)
	}
	if _, err := l.Login(ctx, "carol", "right-password-123", "10.0.0.9", ""); err != nil {
		t.Fatalf("login with upgraded hash: %v", err)
	}
	if err := users.UpdatePasswordHash(ctx, u.ID, oldHash, "x"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("stale re-hash must not overwrite: %v", err)
	}
}
