// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// countingStore wraps the repository and counts lookups (cache verification).
type countingStore struct {
	inner *meta.Connections
	calls int
}

func (s *countingStore) GetByKeyHash(ctx context.Context, h string, now time.Time) (meta.Connection, error) {
	s.calls++
	return s.inner.GetByKeyHash(ctx, h, now)
}

func TestConnectionAuthenticator(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	repo := meta.NewConnections(openDB(t, c))
	key, _ := GenerateKey(ConnectionKeyPrefix)
	exp := c.Now().Add(48 * time.Hour)
	conn := &meta.Connection{Name: "app", KeyPrefix: KeyPrefix(key), KeyHash: HashKey(key),
		Protocols: []string{"seq", "otlp"}, DefaultStream: "app", AllowedCIDRs: []string{"10.0.0.0/8", "2001:db8::/32"},
		MinimumLevel: "Warning", ExpiresAt: &exp}
	if err := repo.Create(ctx, conn); err != nil {
		t.Fatal(err)
	}
	store := &countingStore{inner: repo}
	a := NewConnectionAuthenticator(store, c)
	inNet := netip.MustParseAddr("10.1.2.3")
	tests := []struct {
		name  string
		key   string
		proto Protocol
		addr  netip.Addr
		want  error
	}{
		{"ok", key, ProtocolSeq, inNet, nil},
		{"ok v4-mapped", key, ProtocolOTLP, netip.MustParseAddr("::ffff:10.9.9.9"), nil},
		{"ok v6", key, ProtocolOTLP, netip.MustParseAddr("2001:db8::1"), nil},
		{"missing", "", ProtocolSeq, inNet, ErrUnauthorized},
		{"unknown", "tmk_nope", ProtocolSeq, inNet, ErrUnauthorized},
		{"protocol", key, ProtocolElastic, inNet, ErrForbidden},
		{"cidr", key, ProtocolSeq, netip.MustParseAddr("192.168.1.1"), ErrForbidden},
	}
	for _, tt := range tests {
		got, err := a.AuthenticateKey(ctx, tt.key, tt.proto, tt.addr)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: err %v want %v", tt.name, err, tt.want)
		}
		if tt.want == nil && (got.ID != conn.ID || got.MinimumLevel == nil || *got.MinimumLevel != model.Warning) {
			t.Errorf("%s: conn %+v", tt.name, got)
		}
	}
	calls := store.calls
	_, _ = a.AuthenticateKey(ctx, key, ProtocolSeq, inNet)
	if store.calls != calls {
		t.Error("cache not used")
	}
	c.Advance(KeyCacheTTL + time.Second)
	_, _ = a.AuthenticateKey(ctx, key, ProtocolSeq, inNet)
	if store.calls != calls+1 {
		t.Error("cache entry did not expire")
	}

	// Rotation with grace: the old key keeps working until the grace ends, even when cached.
	newKey, _ := GenerateKey(ConnectionKeyPrefix)
	grace := c.Now().Add(10 * time.Second)
	if err := repo.RotateKey(ctx, conn.ID, KeyPrefix(newKey), HashKey(newKey), &grace); err != nil {
		t.Fatal(err)
	}
	a.Invalidate()
	if _, err := a.AuthenticateKey(ctx, key, ProtocolSeq, inNet); err != nil {
		t.Errorf("old key during grace: %v", err)
	}
	if _, err := a.AuthenticateKey(ctx, newKey, ProtocolSeq, inNet); err != nil {
		t.Errorf("new key: %v", err)
	}
	c.Advance(11 * time.Second)
	if _, err := a.AuthenticateKey(ctx, key, ProtocolSeq, inNet); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("old key after grace: %v", err)
	}

	// Expiry and revocation.
	c.Advance(48 * time.Hour)
	a.Invalidate()
	if _, err := a.AuthenticateKey(ctx, newKey, ProtocolSeq, inNet); !errors.Is(err, ErrForbidden) {
		t.Errorf("expired: %v", err)
	}
	_ = repo.Revoke(ctx, conn.ID, c.Now())
	a.Invalidate()
	if _, err := a.AuthenticateKey(ctx, newKey, ProtocolSeq, netip.MustParseAddr("10.0.0.7")); !errors.Is(err, ErrForbidden) {
		t.Errorf("revoked: %v", err)
	}
}

func TestIngestFailureBlocking(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	repo := meta.NewConnections(openDB(t, c))
	key, _ := GenerateKey(ConnectionKeyPrefix)
	_ = repo.Create(ctx, &meta.Connection{Name: "a", KeyPrefix: KeyPrefix(key), KeyHash: HashKey(key), Protocols: []string{"native"}})
	a := NewConnectionAuthenticator(repo, c)
	bad := netip.MustParseAddr("203.0.113.9")
	for range IngestFailureLimit + 1 {
		_, _ = a.AuthenticateKey(ctx, "tmk_wrong", ProtocolNative, bad)
	}
	if _, err := a.AuthenticateKey(ctx, key, ProtocolNative, bad); !errors.Is(err, ErrForbidden) {
		t.Errorf("blocked ip with valid key: %v", err)
	}
	if _, err := a.AuthenticateKey(ctx, key, ProtocolNative, netip.MustParseAddr("203.0.113.10")); err != nil {
		t.Errorf("other ip: %v", err)
	}
	c.Advance(IngestBlockDuration)
	if _, err := a.AuthenticateKey(ctx, key, ProtocolNative, bad); err != nil {
		t.Errorf("after block: %v", err)
	}
}

func TestConnectionFromMeta(t *testing.T) {
	if _, err := ConnectionFromMeta(meta.Connection{AllowedCIDRs: []string{"nope"}}); err == nil {
		t.Error("bad cidr accepted")
	}
	if _, err := ConnectionFromMeta(meta.Connection{MinimumLevel: "loud"}); err == nil {
		t.Error("bad level accepted")
	}
	c, err := ConnectionFromMeta(meta.Connection{AllowedCIDRs: []string{"10.1.2.3/8"}})
	if err != nil || c.AllowedCIDRs[0].String() != "10.0.0.0/8" {
		t.Errorf("%v %v", c.AllowedCIDRs, err)
	}
	if !c.AllowsAddr(netip.MustParseAddr("10.200.0.1")) || (Connection{}).Allows(ProtocolSeq) {
		t.Error("allows")
	}
	if !ValidProtocol("otlp") || ValidProtocol("syslog") || len(AllProtocols()) != 4 {
		t.Error("protocols")
	}
	c, err = ConnectionFromMeta(meta.Connection{AllowedStreams: []string{"web", "api"}})
	if err != nil || !c.AllowsStream("api") || !c.AllowsStream("web") || c.AllowsStream("db") {
		t.Errorf("allowed streams %v %v", c.AllowedStreams, err)
	}
	if !(Connection{}).AllowsStream("any") {
		t.Error("an empty list allows every stream")
	}
}

func TestKeyCacheTTLInHAMode(t *testing.T) {
	ctx := context.Background()
	c := newFakeClock()
	repo := meta.NewConnections(openDB(t, c))
	key, _ := GenerateKey(ConnectionKeyPrefix)
	if err := repo.Create(ctx, &meta.Connection{Name: "ha", KeyPrefix: KeyPrefix(key), KeyHash: HashKey(key),
		Protocols: []string{"seq"}, DefaultStream: "app"}); err != nil {
		t.Fatal(err)
	}
	store := &countingStore{inner: repo}
	a := NewConnectionAuthenticator(store, c, WithKeyCacheTTL(HAKeyCacheTTL))
	addr := netip.MustParseAddr("10.0.0.1")
	_, _ = a.AuthenticateKey(ctx, key, ProtocolSeq, addr)
	c.Advance(HAKeyCacheTTL - time.Second)
	_, _ = a.AuthenticateKey(ctx, key, ProtocolSeq, addr)
	if store.calls != 1 {
		t.Fatalf("lookups within the HA TTL: %d", store.calls)
	}
	c.Advance(2 * time.Second)
	_, _ = a.AuthenticateKey(ctx, key, ProtocolSeq, addr)
	if store.calls != 2 {
		t.Fatalf("entry older than %v was served from cache", HAKeyCacheTTL)
	}
}
