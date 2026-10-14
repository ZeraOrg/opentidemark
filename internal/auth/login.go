// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// Lockout policy (SPEC §15.2).
const (
	LockoutThreshold = 5
	LockoutBase      = 15 * time.Minute
	LockoutMax       = 24 * time.Hour
	LoginIPBurst     = 20
	LoginIPWindow    = 5 * time.Minute
)

// LoginUserStore is the user persistence needed for logins.
type LoginUserStore interface {
	GetByUsername(ctx context.Context, username string) (meta.User, error)
	RecordLoginFailure(ctx context.Context, id string, failures int, lockedUntil *time.Time) error
	RecordLoginSuccess(ctx context.Context, id string, at time.Time) error
	UpdatePasswordHash(ctx context.Context, id, oldHash, newHash string) error
}

// LoginResult describes a successful login.
type LoginResult struct {
	User    meta.User
	Session NewSession
}

// LoginFailure carries the internal reason of a failed login for auditing; callers must
// show only the generic error to clients.
type LoginFailure struct {
	Reason string
	Err    error
}

func (f *LoginFailure) Error() string { return f.Err.Error() }
func (f *LoginFailure) Unwrap() error { return f.Err }

// Login verifies credentials with throttling, lockout and enumeration-safe timing.
type Login struct {
	users     LoginUserStore
	hasher    *PasswordHasher
	sessions  *SessionManager
	clock     Clock
	ipLimiter *KeyedLimiter
	dummyHash string
}

// NewLogin builds the login service. The dummy hash is computed once at construction.
func NewLogin(users LoginUserStore, hasher *PasswordHasher, sessions *SessionManager, clock Clock) (*Login, error) {
	dummy, err := hasher.Hash("tidemark-dummy-password-for-timing")
	if err != nil {
		return nil, err
	}
	return &Login{users: users, hasher: hasher, sessions: sessions, clock: clock,
		ipLimiter: NewKeyedLimiter(clock, LoginIPBurst, LoginIPWindow), dummyHash: dummy}, nil
}

// Login authenticates username/password from ip and creates a session.
func (l *Login) Login(ctx context.Context, username, password, ip, userAgent string) (LoginResult, error) {
	if !l.ipLimiter.Allow(ip) {
		return LoginResult{}, &LoginFailure{Reason: "ip_rate_limited", Err: ErrRateLimited}
	}
	u, err := l.users.GetByUsername(ctx, username)
	if errors.Is(err, meta.ErrNotFound) {
		l.burnHash(password)
		return LoginResult{}, &LoginFailure{Reason: "unknown_user", Err: ErrInvalidCredentials}
	}
	if err != nil {
		return LoginResult{}, fmt.Errorf("load user: %w", err)
	}
	now := l.clock.Now()
	if u.Disabled || (u.LockedUntil != nil && now.Before(*u.LockedUntil)) {
		l.burnHash(password)
		reason := "disabled"
		if !u.Disabled {
			reason = "locked"
		}
		return LoginResult{}, &LoginFailure{Reason: reason, Err: ErrInvalidCredentials}
	}
	ok, err := l.hasher.Verify(password, u.PasswordHash)
	if err != nil || !ok {
		return LoginResult{}, l.fail(ctx, u, now)
	}
	if err := l.users.RecordLoginSuccess(ctx, u.ID, now); err != nil {
		return LoginResult{}, fmt.Errorf("record login: %w", err)
	}
	l.rehash(ctx, u, password)
	s, err := l.sessions.Create(ctx, u.ID, ip, userAgent)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{User: u, Session: s}, nil
}

func (l *Login) fail(ctx context.Context, u meta.User, now time.Time) error {
	failures := u.FailedLogins + 1
	var lockedUntil *time.Time
	if d := LockoutDuration(failures); d > 0 {
		t := now.Add(d)
		lockedUntil = &t
	}
	if err := l.users.RecordLoginFailure(ctx, u.ID, failures, lockedUntil); err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	return &LoginFailure{Reason: "bad_password", Err: ErrInvalidCredentials}
}

// rehash upgrades a hash created with outdated parameters (SPEC §8.5 item 5). Failure is not
// fatal: the stored hash keeps working and the next login retries.
func (l *Login) rehash(ctx context.Context, u meta.User, password string) {
	if !l.hasher.NeedsRehash(u.PasswordHash) {
		return
	}
	h, err := l.hasher.Hash(password)
	if err != nil {
		return
	}
	_ = l.users.UpdatePasswordHash(ctx, u.ID, u.PasswordHash, h) // best effort; retried at the next login
}

// burnHash verifies against a dummy hash so unknown users cost the same time as known ones.
func (l *Login) burnHash(password string) {
	_, _ = l.hasher.Verify(password, l.dummyHash) // result intentionally unused: timing only
}

// LockoutDuration returns the lock to apply after `failures` consecutive failures:
// 15 min at 5, doubling for every further 5 failures, capped at 24 h; 0 when no lock applies.
func LockoutDuration(failures int) time.Duration {
	if failures < LockoutThreshold || failures%LockoutThreshold != 0 {
		return 0
	}
	d := LockoutBase
	for i := failures / LockoutThreshold; i > 1 && d < LockoutMax; i-- {
		d *= 2
	}
	if d > LockoutMax {
		d = LockoutMax
	}
	return d
}
