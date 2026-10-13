// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// Cookie names (SPEC §11.1, §15.2).
const (
	SessionCookie = "tm_session"
	CSRFCookie    = "tm_csrf"
	CSRFHeader    = "X-Tidemark-CSRF"
	tokenBytes    = 32
	// touchInterval limits last_seen_at writes to one per minute per session.
	touchInterval = time.Minute
)

// SessionStore is the persistence needed by SessionManager.
type SessionStore interface {
	Create(ctx context.Context, s meta.Session) error
	Get(ctx context.Context, id string) (meta.Session, error)
	Touch(ctx context.Context, id string, at time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteForUser(ctx context.Context, userID, keepID string) (int64, error)
}

// UserGetter loads users by id.
type UserGetter interface {
	Get(ctx context.Context, id string) (meta.User, error)
}

// SessionManager creates and validates login sessions.
type SessionManager struct {
	store    SessionStore
	users    UserGetter
	clock    Clock
	idle     time.Duration
	absolute time.Duration
}

// NewSessionManager returns a session manager with the given timeouts.
func NewSessionManager(store SessionStore, users UserGetter, clock Clock, idle, absolute time.Duration) *SessionManager {
	return &SessionManager{store: store, users: users, clock: clock, idle: idle, absolute: absolute}
}

// NewSession is the result of creating a session; Token and CSRF go into cookies.
type NewSession struct {
	Token     string
	CSRF      string
	ExpiresAt time.Time
	ID        string
}

// RandomToken returns 256 random bits, base64url encoded.
func RandomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create starts a session for userID.
func (m *SessionManager) Create(ctx context.Context, userID, ip, userAgent string) (NewSession, error) {
	token, err := RandomToken()
	if err != nil {
		return NewSession{}, err
	}
	csrf, err := RandomToken()
	if err != nil {
		return NewSession{}, err
	}
	now := m.clock.Now().UTC()
	s := meta.Session{ID: HashKey(token), UserID: userID, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(m.absolute), IP: ip, UserAgent: truncate(userAgent, maxUserAgentLen)}
	if err := m.store.Create(ctx, s); err != nil {
		return NewSession{}, fmt.Errorf("create session: %w", err)
	}
	return NewSession{Token: token, CSRF: csrf, ExpiresAt: s.ExpiresAt, ID: s.ID}, nil
}

const maxUserAgentLen = 512

// Validate resolves a session cookie token to a principal, enforcing idle/absolute timeouts.
func (m *SessionManager) Validate(ctx context.Context, token string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	id := HashKey(token)
	s, err := m.store.Get(ctx, id)
	if errors.Is(err, meta.ErrNotFound) {
		return Principal{}, ErrUnauthorized
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load session: %w", err)
	}
	now := m.clock.Now()
	if !now.Before(s.ExpiresAt) || !now.Before(s.LastSeenAt.Add(m.idle)) {
		_ = m.store.Delete(ctx, id) // best effort cleanup; the session is rejected either way
		return Principal{}, ErrUnauthorized
	}
	u, err := m.users.Get(ctx, s.UserID)
	if err != nil || u.Disabled {
		return Principal{}, ErrUnauthorized
	}
	if now.Sub(s.LastSeenAt) >= touchInterval {
		if err := m.store.Touch(ctx, id, now); err != nil {
			return Principal{}, fmt.Errorf("touch session: %w", err)
		}
	}
	return Principal{Kind: KindSession, UserID: u.ID, Username: u.Username, Role: Role(u.Role), SessionID: id,
		MustChangePassword: u.MustChangePassword}, nil
}

// Revoke deletes a session by its hashed id.
func (m *SessionManager) Revoke(ctx context.Context, sessionID string) error {
	return m.store.Delete(ctx, sessionID)
}

// RevokeUser deletes all sessions of a user except keepSessionID.
func (m *SessionManager) RevokeUser(ctx context.Context, userID, keepSessionID string) error {
	_, err := m.store.DeleteForUser(ctx, userID, keepSessionID)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
