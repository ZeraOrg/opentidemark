// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// TokenStore is the API token persistence needed for authentication.
type TokenStore interface {
	GetByHash(ctx context.Context, hash string) (meta.APIToken, error)
	Touch(ctx context.Context, id string, at time.Time) error
}

// TokenAuthenticator authenticates "Authorization: Bearer tmt_…" API tokens.
type TokenAuthenticator struct {
	tokens TokenStore
	users  UserGetter
	clock  Clock
}

// NewTokenAuthenticator returns a token authenticator.
func NewTokenAuthenticator(tokens TokenStore, users UserGetter, clock Clock) *TokenAuthenticator {
	return &TokenAuthenticator{tokens: tokens, users: users, clock: clock}
}

// Authenticate resolves raw to a principal whose role is limited by the token scope.
func (a *TokenAuthenticator) Authenticate(ctx context.Context, raw string) (Principal, error) {
	if !strings.HasPrefix(raw, UserTokenPrefix) {
		return Principal{}, ErrUnauthorized
	}
	t, err := a.tokens.GetByHash(ctx, HashKey(raw))
	if errors.Is(err, meta.ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, fmt.Errorf("lookup token: %w", err)
	}
	now := a.clock.Now()
	if t.RevokedAt != nil || (t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)) {
		return Principal{}, ErrUnauthorized
	}
	u, err := a.users.Get(ctx, t.UserID)
	if err != nil || u.Disabled {
		return Principal{}, ErrUnauthorized
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchInterval {
		if err := a.tokens.Touch(ctx, t.ID, now); err != nil {
			return Principal{}, fmt.Errorf("touch token: %w", err)
		}
	}
	return Principal{Kind: KindToken, UserID: u.ID, Username: u.Username, TokenID: t.ID,
		Role: MinRole(Role(u.Role), ScopeRole(t.Scope)), MustChangePassword: u.MustChangePassword}, nil
}
