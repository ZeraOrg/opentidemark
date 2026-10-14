// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// ConnectionStore is the connection persistence used by Connections.
type ConnectionStore interface {
	Create(ctx context.Context, c *meta.Connection) error
	Get(ctx context.Context, id string) (meta.Connection, error)
	List(ctx context.Context) ([]meta.Connection, error)
	Update(ctx context.Context, c *meta.Connection) error
	RotateKey(ctx context.Context, id, prefix, hash string, graceUntil *time.Time) error
	Revoke(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
	GetByKeyHash(ctx context.Context, hash string, now time.Time) (meta.Connection, error)
}

// ErrKeyInUse is returned when an admin-supplied key already belongs to a connection.
var ErrKeyInUse = fmt.Errorf("%w: the key is already used by another connection", meta.ErrConflict)

// KeyCache is invalidated after every connection change.
type KeyCache interface{ Invalidate() }

// Connection limits (SPEC §11.5).
const (
	maxConnectionName  = 128
	maxDescription     = 1024
	maxCIDRs           = 64
	maxAppliedProps    = 64
	maxAllowedStreams  = 100
	maxRateLimitEPS    = 1_000_000
	MaxRotateGraceMins = 1440
)

// Connections manages ingestion connections and their keys.
type Connections struct {
	store ConnectionStore
	cache KeyCache
	audit *Auditor
	now   func() time.Time
}

// NewConnections returns the connection service.
func NewConnections(store ConnectionStore, cache KeyCache, audit *Auditor, now func() time.Time) *Connections {
	return &Connections{store: store, cache: cache, audit: audit, now: now}
}

// ConnectionInput holds the editable fields (nil = unchanged on update).
type ConnectionInput struct {
	Name              *string
	Description       *string
	Protocols         *[]string
	DefaultStream     *string
	AllowedCIDRs      *[]string
	AllowedStreams    *[]string
	MinimumLevel      *string
	AppliedProperties *map[string]any
	RateLimitEPS      *int
	ExpiresAt         **time.Time
	// Key is an admin-supplied key (create and rotate only, SPEC §9.9); nil = generate one.
	Key *string
}

// List returns all connections.
func (s *Connections) List(ctx context.Context) ([]meta.Connection, error) { return s.store.List(ctx) }

// Get returns one connection.
func (s *Connections) Get(ctx context.Context, id string) (meta.Connection, error) {
	return s.store.Get(ctx, id)
}

// Create adds a connection and returns its key (shown once).
func (s *Connections) Create(ctx context.Context, actor Actor, in ConnectionInput) (meta.Connection, string, error) {
	c := meta.Connection{Protocols: protocolNames(auth.AllProtocols()), DefaultStream: model.DefaultStream}
	if in.Name == nil {
		return meta.Connection{}, "", invalid("name", "is required")
	}
	if err := s.apply(&c, in); err != nil {
		return meta.Connection{}, "", err
	}
	raw, prefix, err := s.newKey(ctx, in.Key)
	if err != nil {
		return meta.Connection{}, "", err
	}
	c.KeyPrefix, c.KeyHash = prefix, auth.HashKey(raw)
	if err := s.store.Create(ctx, &c); err != nil {
		return meta.Connection{}, "", nameConflict(err)
	}
	s.cache.Invalidate()
	s.audit.Record(ctx, actor, ActionConnectionCreate, c.ID, map[string]any{"name": c.Name, "protocols": c.Protocols,
		"suppliedKey": in.Key != nil, "allowedStreams": nonNil(c.AllowedStreams)})
	return c, raw, nil
}

// newKey returns the key to store and its display prefix: a generated tmk_ key, or the
// admin-supplied one after the length/entropy rules and a uniqueness check (SPEC §9.9).
func (s *Connections) newKey(ctx context.Context, supplied *string) (raw, prefix string, err error) {
	if supplied == nil {
		raw, err = auth.GenerateKey(auth.ConnectionKeyPrefix)
		return raw, auth.KeyPrefix(raw), err
	}
	raw = *supplied
	if err := auth.ValidateSuppliedKey(raw); err != nil {
		return "", "", invalid("key", strings.TrimPrefix(err.Error(), auth.ErrWeakKey.Error()+": "))
	}
	switch _, err := s.store.GetByKeyHash(ctx, auth.HashKey(raw), s.now()); {
	case err == nil:
		return "", "", ErrKeyInUse
	case !errors.Is(err, meta.ErrNotFound):
		return "", "", err
	}
	return raw, auth.SuppliedKeyPrefix(raw), nil
}

// Update edits a connection.
func (s *Connections) Update(ctx context.Context, actor Actor, id string, in ConnectionInput) (meta.Connection, error) {
	if in.Key != nil {
		return meta.Connection{}, invalid("key", "can only be set on create or rotate")
	}
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return meta.Connection{}, err
	}
	if err := s.apply(&c, in); err != nil {
		return meta.Connection{}, err
	}
	if err := s.store.Update(ctx, &c); err != nil {
		return meta.Connection{}, nameConflict(err)
	}
	s.cache.Invalidate()
	s.audit.Record(ctx, actor, ActionConnectionUpdate, c.ID, map[string]any{"name": c.Name, "allowedStreams": nonNil(c.AllowedStreams)})
	return s.store.Get(ctx, id)
}

// Delete removes a connection.
func (s *Connections) Delete(ctx context.Context, actor Actor, id string) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.cache.Invalidate()
	s.audit.Record(ctx, actor, ActionConnectionDelete, id, nil)
	return nil
}

// Rotate issues a new key (generated, or supplied when key is non-nil); the old key stays valid
// for graceMinutes (0–1440).
func (s *Connections) Rotate(ctx context.Context, actor Actor, id string, graceMinutes int, key *string) (meta.Connection, string, error) {
	if graceMinutes < 0 || graceMinutes > MaxRotateGraceMins {
		return meta.Connection{}, "", invalid("graceMinutes", "must be between 0 and 1440")
	}
	c, err := s.store.Get(ctx, id)
	if err != nil {
		return meta.Connection{}, "", err
	}
	if c.RevokedAt != nil {
		return meta.Connection{}, "", invalid("id", "a revoked connection cannot be rotated")
	}
	raw, prefix, err := s.newKey(ctx, key)
	if err != nil {
		return meta.Connection{}, "", err
	}
	var grace *time.Time
	if graceMinutes > 0 {
		t := s.now().Add(time.Duration(graceMinutes) * time.Minute)
		grace = &t
	}
	if err := s.store.RotateKey(ctx, id, prefix, auth.HashKey(raw), grace); err != nil {
		return meta.Connection{}, "", err
	}
	s.cache.Invalidate()
	s.audit.Record(ctx, actor, ActionConnectionRotate, id, map[string]any{"graceMinutes": graceMinutes, "suppliedKey": key != nil})
	c, err = s.store.Get(ctx, id)
	return c, raw, err
}

// Revoke disables a connection's keys permanently.
func (s *Connections) Revoke(ctx context.Context, actor Actor, id string) (meta.Connection, error) {
	if err := s.store.Revoke(ctx, id, s.now()); err != nil {
		return meta.Connection{}, err
	}
	s.cache.Invalidate()
	s.audit.Record(ctx, actor, ActionConnectionRevoke, id, nil)
	return s.store.Get(ctx, id)
}

func nameConflict(err error) error {
	if errors.Is(err, meta.ErrConflict) {
		return invalid("name", "already exists")
	}
	return err
}

func protocolNames(ps []auth.Protocol) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = string(p)
	}
	return out
}

func (s *Connections) apply(c *meta.Connection, in ConnectionInput) error {
	steps := []func(*meta.Connection, ConnectionInput) error{
		applyName, applyDescription, applyProtocols, applyStream, applyCIDRs, applyAllowedStreams, applyLevel, applyProps,
		applyRate, s.applyExpiry, checkStreamsAllowed,
	}
	for _, step := range steps {
		if err := step(c, in); err != nil {
			return err
		}
	}
	return nil
}

// applyAllowedStreams sets the streams the connection may write to (sorted, unique; empty =
// any stream).
func applyAllowedStreams(c *meta.Connection, in ConnectionInput) error {
	if in.AllowedStreams == nil {
		return nil
	}
	if len(*in.AllowedStreams) > maxAllowedStreams {
		return invalid("allowedStreams", fmt.Sprintf("at most %d streams", maxAllowedStreams))
	}
	for _, s := range *in.AllowedStreams {
		if !model.ValidStreamName(s) {
			return invalid("allowedStreams", fmt.Sprintf("%q is not a valid stream name", s))
		}
	}
	c.AllowedStreams = slices.Compact(slices.Sorted(slices.Values(*in.AllowedStreams)))
	return nil
}

// checkStreamsAllowed requires the default and the pinned stream to be allowed, so a
// connection with allowed streams never writes outside them.
func checkStreamsAllowed(c *meta.Connection, _ ConnectionInput) error {
	if len(c.AllowedStreams) == 0 {
		return nil
	}
	if !slices.Contains(c.AllowedStreams, c.DefaultStream) {
		return invalid("allowedStreams", "must include the default stream "+c.DefaultStream)
	}
	for _, k := range []string{"@stream", "_stream"} {
		if pinned, ok := c.AppliedProperties[k].(string); ok && !slices.Contains(c.AllowedStreams, pinned) {
			return invalid("allowedStreams", "must include the pinned stream "+pinned)
		}
	}
	return nil
}

func applyName(c *meta.Connection, in ConnectionInput) error {
	if in.Name == nil {
		return nil
	}
	name := strings.TrimSpace(*in.Name)
	if name == "" || len(name) > maxConnectionName {
		return invalid("name", "must be 1-128 characters")
	}
	c.Name = name
	return nil
}

func applyDescription(c *meta.Connection, in ConnectionInput) error {
	if in.Description == nil {
		return nil
	}
	if len(*in.Description) > maxDescription {
		return invalid("description", "too long")
	}
	c.Description = *in.Description
	return nil
}

func applyProtocols(c *meta.Connection, in ConnectionInput) error {
	if in.Protocols == nil {
		return nil
	}
	var ps []string
	for _, p := range *in.Protocols {
		if !auth.ValidProtocol(p) {
			return invalid("protocols", "unknown protocol "+p)
		}
		if !slices.Contains(ps, p) {
			ps = append(ps, p)
		}
	}
	if len(ps) == 0 {
		return invalid("protocols", "at least one protocol is required")
	}
	c.Protocols = ps
	return nil
}

func applyStream(c *meta.Connection, in ConnectionInput) error {
	if in.DefaultStream == nil {
		return nil
	}
	if !model.ValidStreamName(*in.DefaultStream) {
		return invalid("defaultStream", "must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	c.DefaultStream = *in.DefaultStream
	return nil
}

func applyCIDRs(c *meta.Connection, in ConnectionInput) error {
	if in.AllowedCIDRs == nil {
		return nil
	}
	if len(*in.AllowedCIDRs) > maxCIDRs {
		return invalid("allowedCidrs", "too many entries")
	}
	out := make([]string, 0, len(*in.AllowedCIDRs))
	for _, s := range *in.AllowedCIDRs {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			a, aerr := netip.ParseAddr(strings.TrimSpace(s))
			if aerr != nil {
				return invalid("allowedCidrs", "invalid CIDR "+s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked().String())
	}
	c.AllowedCIDRs = out
	return nil
}

func applyLevel(c *meta.Connection, in ConnectionInput) error {
	if in.MinimumLevel == nil {
		return nil
	}
	if *in.MinimumLevel == "" {
		c.MinimumLevel = ""
		return nil
	}
	l, ok := model.ParseLevel(*in.MinimumLevel)
	if !ok {
		return invalid("minimumLevel", "unknown level")
	}
	c.MinimumLevel = l.String()
	return nil
}

func applyProps(c *meta.Connection, in ConnectionInput) error {
	if in.AppliedProperties == nil {
		return nil
	}
	props := *in.AppliedProperties
	if len(props) > maxAppliedProps {
		return invalid("appliedProperties", "too many properties")
	}
	for k, v := range props {
		if (k == "@stream" || k == "_stream") && !isValidPinnedStream(v) {
			return invalid("appliedProperties", "pinned stream must be a valid stream name")
		}
	}
	c.AppliedProperties = props
	return nil
}

func isValidPinnedStream(v any) bool {
	s, ok := v.(string)
	return ok && model.ValidStreamName(s)
}

func applyRate(c *meta.Connection, in ConnectionInput) error {
	if in.RateLimitEPS == nil {
		return nil
	}
	if *in.RateLimitEPS < 0 || *in.RateLimitEPS > maxRateLimitEPS {
		return invalid("rateLimitEps", "must be between 0 and 1000000")
	}
	c.RateLimitEPS = *in.RateLimitEPS
	return nil
}

func (s *Connections) applyExpiry(c *meta.Connection, in ConnectionInput) error {
	if in.ExpiresAt == nil {
		return nil
	}
	if *in.ExpiresAt != nil && !(*in.ExpiresAt).After(s.now()) {
		return invalid("expiresAt", "must be in the future")
	}
	c.ExpiresAt = *in.ExpiresAt
	return nil
}
