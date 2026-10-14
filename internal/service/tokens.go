// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// TokenStore is the API token persistence used by Tokens.
type TokenStore interface {
	Create(ctx context.Context, t *meta.APIToken) error
	Get(ctx context.Context, id string) (meta.APIToken, error)
	ListForUser(ctx context.Context, userID string) ([]meta.APIToken, error)
	Delete(ctx context.Context, id string) error
}

// Token limits.
const (
	maxTokenName     = 128
	maxTokensPerUser = 100
)

// Tokens manages the caller's own API tokens (SPEC §11.2).
type Tokens struct {
	store TokenStore
	audit *Auditor
	now   func() time.Time
}

// NewTokens returns the token service.
func NewTokens(store TokenStore, audit *Auditor, now func() time.Time) *Tokens {
	return &Tokens{store: store, audit: audit, now: now}
}

// NewToken is the input of Create.
type NewToken struct {
	Name      string
	Scope     string
	ExpiresAt *time.Time
}

// List returns the caller's tokens.
func (s *Tokens) List(ctx context.Context, p auth.Principal) ([]meta.APIToken, error) {
	return s.store.ListForUser(ctx, p.UserID)
}

// Create issues a token for the caller; the raw value is returned once.
func (s *Tokens) Create(ctx context.Context, actor Actor, p auth.Principal, in NewToken) (meta.APIToken, string, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > maxTokenName {
		return meta.APIToken{}, "", invalid("name", "must be 1-128 characters")
	}
	if auth.ScopeRole(in.Scope) == "" {
		return meta.APIToken{}, "", invalid("scope", "must be read, write or admin")
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.now()) {
		return meta.APIToken{}, "", invalid("expiresAt", "must be in the future")
	}
	existing, err := s.store.ListForUser(ctx, p.UserID)
	if err != nil {
		return meta.APIToken{}, "", err
	}
	if len(existing) >= maxTokensPerUser {
		return meta.APIToken{}, "", invalid("name", "token limit reached; delete unused tokens")
	}
	raw, err := auth.GenerateKey(auth.UserTokenPrefix)
	if err != nil {
		return meta.APIToken{}, "", err
	}
	t := meta.APIToken{Name: in.Name, UserID: p.UserID, KeyPrefix: auth.KeyPrefix(raw), KeyHash: auth.HashKey(raw),
		Scope: in.Scope, ExpiresAt: in.ExpiresAt}
	if err := s.store.Create(ctx, &t); err != nil {
		return meta.APIToken{}, "", err
	}
	s.audit.Record(ctx, actor, ActionTokenCreate, t.ID, map[string]any{"name": t.Name, "scope": t.Scope})
	return t, raw, nil
}

// Delete removes one of the caller's tokens (admins may delete any token).
func (s *Tokens) Delete(ctx context.Context, actor Actor, p auth.Principal, id string) error {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	if t.UserID != p.UserID && p.Role != auth.RoleAdmin {
		return meta.ErrNotFound // do not reveal other users' tokens
	}
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	s.audit.Record(ctx, actor, ActionTokenDelete, id, nil)
	return nil
}
