// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Protocol is an ingestion protocol a connection may use.
type Protocol string

// Ingestion protocols.
const (
	ProtocolSeq     Protocol = model.ProtocolSeq
	ProtocolElastic Protocol = model.ProtocolElastic
	ProtocolOTLP    Protocol = model.ProtocolOTLP
	ProtocolNative  Protocol = model.ProtocolNative
)

// AllProtocols lists every protocol.
func AllProtocols() []Protocol {
	return []Protocol{ProtocolSeq, ProtocolElastic, ProtocolOTLP, ProtocolNative}
}

// ValidProtocol reports whether p is known.
func ValidProtocol(p string) bool { return slices.Contains(AllProtocols(), Protocol(p)) }

// Connection is the authenticated view of a connection used by ingestion.
type Connection struct {
	ID                string
	Name              string
	Protocols         []Protocol
	DefaultStream     string
	AllowedCIDRs      []netip.Prefix
	AllowedStreams    []string // sorted; empty = any stream (SPEC §9.1)
	MinimumLevel      *model.Level
	AppliedProperties map[string]any
	RateLimitEPS      int
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
}

// Allows reports whether the connection may use protocol p.
func (c Connection) Allows(p Protocol) bool { return slices.Contains(c.Protocols, p) }

// AllowsStream reports whether the connection may write to stream (empty list = any).
func (c Connection) AllowsStream(stream string) bool {
	if len(c.AllowedStreams) == 0 {
		return true
	}
	_, ok := slices.BinarySearch(c.AllowedStreams, stream)
	return ok
}

// AllowsAddr reports whether remote is within the allowed CIDRs (empty list = any).
func (c Connection) AllowsAddr(remote netip.Addr) bool {
	if len(c.AllowedCIDRs) == 0 {
		return true
	}
	remote = remote.Unmap()
	for _, p := range c.AllowedCIDRs {
		if p.Contains(remote) {
			return true
		}
	}
	return false
}

// ConnectionFromMeta converts a stored connection; invalid CIDRs or levels are errors.
func ConnectionFromMeta(m meta.Connection) (Connection, error) {
	c := Connection{ID: m.ID, Name: m.Name, DefaultStream: m.DefaultStream, AppliedProperties: m.AppliedProperties,
		RateLimitEPS: m.RateLimitEPS, ExpiresAt: m.ExpiresAt, RevokedAt: m.RevokedAt,
		AllowedStreams: slices.Sorted(slices.Values(m.AllowedStreams))}
	for _, p := range m.Protocols {
		c.Protocols = append(c.Protocols, Protocol(p))
	}
	for _, s := range m.AllowedCIDRs {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return Connection{}, fmt.Errorf("connection %s: invalid CIDR %q", m.ID, s)
		}
		c.AllowedCIDRs = append(c.AllowedCIDRs, p.Masked())
	}
	if m.MinimumLevel != "" {
		l, ok := model.ParseLevel(m.MinimumLevel)
		if !ok {
			return Connection{}, fmt.Errorf("connection %s: invalid minimum level %q", m.ID, m.MinimumLevel)
		}
		c.MinimumLevel = &l
	}
	return c, nil
}

// ConnectionStore looks connections up by key hash.
type ConnectionStore interface {
	GetByKeyHash(ctx context.Context, hash string, now time.Time) (meta.Connection, error)
}

// Ingestion key policy (SPEC §15.3, §15.4, §17.5).
const (
	KeyCacheTTL         = 60 * time.Second
	HAKeyCacheTTL       = 10 * time.Second
	keyCacheSize        = 4096
	IngestFailureLimit  = 30
	IngestFailureWindow = time.Minute
	IngestBlockDuration = 5 * time.Minute
)

// cachedConn is a cache entry; validUntil bounds the use of a previous (rotated) key.
type cachedConn struct {
	conn       Connection
	validUntil time.Time
}

// ConnectionAuthenticator authenticates ingestion keys (implements ingest.KeyAuthenticator).
type ConnectionAuthenticator struct {
	store    ConnectionStore
	clock    Clock
	cache    *ttlLRU[cachedConn]
	cacheTTL time.Duration
	blocker  *FailureBlocker
}

// ConnAuthOption configures a ConnectionAuthenticator.
type ConnAuthOption func(*ConnectionAuthenticator)

// WithKeyCacheTTL overrides the key cache TTL (HA mode uses HAKeyCacheTTL, SPEC §17.5).
func WithKeyCacheTTL(d time.Duration) ConnAuthOption {
	return func(a *ConnectionAuthenticator) {
		if d > 0 {
			a.cacheTTL = d
		}
	}
}

// NewConnectionAuthenticator returns an authenticator with a 60 s LRU cache and per-IP blocking.
func NewConnectionAuthenticator(store ConnectionStore, clock Clock, opts ...ConnAuthOption) *ConnectionAuthenticator {
	a := &ConnectionAuthenticator{store: store, clock: clock, cache: newTTLLRU[cachedConn](clock, keyCacheSize),
		cacheTTL: KeyCacheTTL, blocker: NewFailureBlocker(clock, IngestFailureLimit, IngestFailureWindow, IngestBlockDuration)}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Invalidate drops all cached keys; call after any connection change.
func (a *ConnectionAuthenticator) Invalidate() { a.cache.purge() }

// AuthenticateKey validates raw for protocol p from remote. It returns ErrUnauthorized for a
// missing/unknown key and ErrForbidden for a known key that may not be used.
func (a *ConnectionAuthenticator) AuthenticateKey(ctx context.Context, raw string, p Protocol, remote netip.Addr) (Connection, error) {
	ipKey := remote.String()
	if a.blocker.Blocked(ipKey) {
		return Connection{}, fmt.Errorf("%w: too many failed attempts", ErrForbidden)
	}
	c, err := a.lookup(ctx, raw)
	if err == nil {
		err = a.check(c, p, remote)
	}
	if err != nil {
		if errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrForbidden) {
			a.blocker.RecordFailure(ipKey)
		}
		return Connection{}, err
	}
	return c, nil
}

func (a *ConnectionAuthenticator) lookup(ctx context.Context, raw string) (Connection, error) {
	if raw == "" {
		return Connection{}, fmt.Errorf("%w: missing key", ErrUnauthorized)
	}
	hash := HashKey(raw)
	now := a.clock.Now()
	if e, ok := a.cache.get(hash); ok && now.Before(e.validUntil) {
		return e.conn, nil
	}
	m, err := a.store.GetByKeyHash(ctx, hash, now)
	if errors.Is(err, meta.ErrNotFound) {
		return Connection{}, fmt.Errorf("%w: unknown key", ErrUnauthorized)
	}
	if err != nil {
		return Connection{}, fmt.Errorf("lookup key: %w", err)
	}
	validUntil := now.Add(a.cacheTTL)
	switch {
	case subtle.ConstantTimeCompare([]byte(hash), []byte(m.KeyHash)) == 1:
	case m.PrevKeyExpiresAt != nil && subtle.ConstantTimeCompare([]byte(hash), []byte(m.PrevKeyHash)) == 1:
		if m.PrevKeyExpiresAt.Before(validUntil) {
			validUntil = *m.PrevKeyExpiresAt
		}
	default:
		return Connection{}, fmt.Errorf("%w: unknown key", ErrUnauthorized)
	}
	c, err := ConnectionFromMeta(m)
	if err != nil {
		return Connection{}, err
	}
	a.cache.put(hash, cachedConn{conn: c, validUntil: validUntil}, validUntil)
	return c, nil
}

func (a *ConnectionAuthenticator) check(c Connection, p Protocol, remote netip.Addr) error {
	now := a.clock.Now()
	switch {
	case c.RevokedAt != nil:
		return fmt.Errorf("%w: key revoked", ErrForbidden)
	case c.ExpiresAt != nil && !now.Before(*c.ExpiresAt):
		return fmt.Errorf("%w: key expired", ErrForbidden)
	case !c.Allows(p):
		return fmt.Errorf("%w: protocol %s not allowed", ErrForbidden, p)
	case !c.AllowsAddr(remote):
		return fmt.Errorf("%w: source address not allowed", ErrForbidden)
	}
	return nil
}
