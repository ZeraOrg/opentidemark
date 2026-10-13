// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

func TestTokenAuthenticator(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	db := openDB(t, c)
	users, tokens := meta.NewUsers(db), meta.NewAPITokens(db)
	u := &meta.User{Username: "ed", PasswordHash: "x", Role: meta.RoleEditor}
	_ = users.Create(ctx, u)
	raw, _ := GenerateKey(UserTokenPrefix)
	exp := c.Now().Add(time.Hour)
	tok := &meta.APIToken{Name: "t", UserID: u.ID, KeyPrefix: KeyPrefix(raw), KeyHash: HashKey(raw), Scope: meta.ScopeAdmin, ExpiresAt: &exp}
	_ = tokens.Create(ctx, tok)
	a := NewTokenAuthenticator(tokens, users, c)
	p, err := a.Authenticate(ctx, raw)
	if err != nil || p.Role != RoleEditor || p.Kind != KindToken || p.TokenID != tok.ID || p.Actor() != "token:"+tok.ID {
		t.Fatalf("%+v %v", p, err)
	}
	if got, _ := tokens.Get(ctx, tok.ID); got.LastUsedAt == nil {
		t.Error("not touched")
	}
	for _, bad := range []string{"", "tmk_xxx", "tmt_unknown"} {
		if _, err := a.Authenticate(ctx, bad); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	c.Advance(2 * time.Hour)
	if _, err := a.Authenticate(ctx, raw); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("expired: %v", err)
	}
	raw2, _ := GenerateKey(UserTokenPrefix)
	tok2 := &meta.APIToken{Name: "r", UserID: u.ID, KeyPrefix: KeyPrefix(raw2), KeyHash: HashKey(raw2), Scope: meta.ScopeRead}
	_ = tokens.Create(ctx, tok2)
	if p, err := a.Authenticate(ctx, raw2); err != nil || p.Role != RoleViewer {
		t.Errorf("read scope: %+v %v", p, err)
	}
	_ = tokens.Revoke(ctx, tok2.ID, c.Now())
	if _, err := a.Authenticate(ctx, raw2); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("revoked: %v", err)
	}
}

type flakyUsage struct {
	fail  bool
	total map[string]int64
}

func (f *flakyUsage) AddUsage(_ context.Context, id string, _ time.Time, _ string, n int64) error {
	if f.fail {
		return errors.New("db down")
	}
	f.total[id] += n
	return nil
}

func TestUsageTracker(t *testing.T) {
	c := newFakeClock()
	store := &flakyUsage{total: map[string]int64{}, fail: true}
	u := NewUsageTracker(store, c)
	u.Record("c1", "1.1.1.1", 5)
	u.Record("c1", "1.1.1.2", 3)
	if err := u.Flush(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	u.Record("c1", "1.1.1.3", 2)
	store.fail = false
	if err := u.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.total["c1"] != 10 {
		t.Errorf("total %d", store.total["c1"])
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.Record("c2", "x", 1)
	done := make(chan struct{})
	go func() {
		u.Run(ctx, 10*time.Millisecond, func(error) {})
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	if store.total["c2"] != 1 {
		t.Errorf("run did not flush: %v", store.total)
	}
}

func TestPrincipalContext(t *testing.T) {
	ctx := WithPrincipal(context.Background(), Principal{UserID: "u"})
	if p, ok := PrincipalFrom(ctx); !ok || p.UserID != "u" {
		t.Error("principal roundtrip")
	}
	if _, ok := PrincipalFrom(context.Background()); ok {
		t.Error("empty ctx")
	}
	if (SystemClock{}).Now().IsZero() {
		t.Error("clock")
	}
}
