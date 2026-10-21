// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Schedule of the background check (SPEC §17.7).
const (
	firstCheckMin     = time.Minute
	firstCheckSpread  = 14 * time.Minute // first check 1–15 min after start
	dailyMin          = 23 * time.Hour
	dailySpread       = 2 * time.Hour // then every 24 h ± 1 h
	retryDelay        = time.Hour
	maxAttemptsPerDay = 6
	checkTimeout      = 30 * time.Second
	// MinCheckGap is the minimum time between two checks requested by admins.
	MinCheckGap = time.Minute
)

// ErrUnavailable is returned by CheckNow when this node never checks (UnavailableError).
var ErrUnavailable = errors.New("update check unavailable")

// UnavailableError says why the check is off: ReasonDisabled or ReasonDev.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string { return ErrUnavailable.Error() + ": " + e.Reason }

// Is makes errors.Is(err, ErrUnavailable) true.
func (e *UnavailableError) Is(target error) bool { return target == ErrUnavailable }

// ErrTooSoon is returned by CheckNow within MinCheckGap of the previous check (TooSoonError).
var ErrTooSoon = errors.New("update checked too recently")

// TooSoonError carries the time until the next check is allowed.
type TooSoonError struct{ RetryAfter time.Duration }

func (e *TooSoonError) Error() string {
	return fmt.Sprintf("%s: retry in %s", ErrTooSoon, e.RetryAfter.Round(time.Second))
}

// Is makes errors.Is(err, ErrTooSoon) true.
func (e *TooSoonError) Is(target error) bool { return target == ErrTooSoon }

// Source lists releases (implemented by *GitHubSource).
type Source interface {
	Releases(ctx context.Context, prereleases bool) ([]ReleaseInfo, error)
}

// CheckerDeps are the collaborators of a Checker.
type CheckerDeps struct {
	Source  Source
	Build   Build
	Install Install
	NodeID  string
	Now     func() time.Time
	After   func(time.Duration) <-chan time.Time // time.After in production
	Rand    func(n int64) int64                  // uniform in [0, n): schedule jitter
	Logger  *slog.Logger
}

// Checker runs the background release check and holds the status in memory.
type Checker struct {
	d       CheckerDeps
	current Version
	reason  string // non-empty: no check is ever made (ReasonDisabled, ReasonDev)

	checkMu sync.Mutex // one check at a time

	mu          sync.Mutex
	status      Status
	lastAttempt time.Time
}

// NewChecker returns a checker; it makes no request until Run or CheckNow.
func NewChecker(d CheckerDeps) *Checker {
	c := &Checker{d: d, status: Status{Enabled: d.Install.Enabled, Current: d.Build.Version, NodeID: d.NodeID,
		InstallKind: d.Install.Kind, Apply: d.Install.Apply(), State: StateIdle}}
	v, err := ParseVersion(d.Build.Version)
	switch {
	case !d.Install.Enabled:
		c.reason = ReasonDisabled
	case d.Install.Kind == KindDev || err != nil:
		c.reason = ReasonDev
	}
	c.current = v
	return c
}

// Status returns a copy of the current status.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	if s.Latest != nil {
		latest := *s.Latest
		s.Latest = &latest
	}
	if s.CheckedAt != nil {
		at := *s.CheckedAt
		s.CheckedAt = &at
	}
	return s
}

// Available reports whether a newer release is known (self-metric tidemark_update_available).
func (c *Checker) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status.Available
}

// CheckNow checks immediately for an admin. A failed lookup is reported in the status, not as an
// error; errors are UnavailableError and TooSoonError.
func (c *Checker) CheckNow(ctx context.Context) (Status, error) {
	if c.reason != "" {
		return c.Status(), &UnavailableError{Reason: c.reason}
	}
	c.mu.Lock()
	now := c.d.Now()
	if since := now.Sub(c.lastAttempt); !c.lastAttempt.IsZero() && since < MinCheckGap {
		c.mu.Unlock()
		return c.Status(), &TooSoonError{RetryAfter: MinCheckGap - since}
	}
	c.lastAttempt = now
	c.mu.Unlock()
	_ = c.check(ctx) // the outcome is in the status returned below
	return c.Status(), nil
}

// Run checks 1–15 minutes after start, then every 24 h ± 1 h; a failed check is retried after an
// hour (or when a rate limit ends), at most six attempts in a row before the daily rhythm resumes.
// It returns at once when the check is disabled or this is a development build.
func (c *Checker) Run(ctx context.Context) {
	if c.reason != "" {
		return
	}
	wait := firstCheckMin + time.Duration(c.d.Rand(int64(firstCheckSpread)))
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.d.After(wait):
		}
		c.mu.Lock()
		c.lastAttempt = c.d.Now()
		c.mu.Unlock()
		err := c.check(ctx)
		if err == nil || failures+1 >= maxAttemptsPerDay {
			failures = 0
			wait = c.daily()
			continue
		}
		failures++
		wait = c.retryWait(err)
	}
}

func (c *Checker) daily() time.Duration {
	return dailyMin + time.Duration(c.d.Rand(int64(dailySpread)))
}

// retryWait is an hour, or longer while the source's rate limit lasts (at most a day).
func (c *Checker) retryWait(err error) time.Duration {
	wait := retryDelay
	var rl *RateLimitError
	if errors.As(err, &rl) {
		if d := rl.Until.Sub(c.d.Now()); d > wait {
			wait = min(d, dailyMin+dailySpread)
		}
	}
	return wait
}

// check asks the source and records the outcome.
func (c *Checker) check(ctx context.Context) error {
	c.checkMu.Lock()
	defer c.checkMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	releases, err := c.d.Source.Releases(ctx, c.current.Prerelease())
	now := c.d.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.CheckedAt = &now
	if err != nil {
		c.status.Error = errorCode(err)
		c.d.Logger.Warn("update check failed", "error", c.status.Error, "err", err)
		return err
	}
	latest := Select(c.current, releases)
	if latest != nil && (c.status.Latest == nil || c.status.Latest.Version != latest.Version) {
		c.d.Logger.Info("a newer OpenTidemark release is available", "current", c.status.Current, "latest", latest.Version)
	}
	c.status.Error, c.status.Latest, c.status.Available = "", latest, latest != nil
	return nil
}

// errorCode maps a source error to its Status.Error code.
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrRateLimited):
		return CodeRateLimited
	case errors.Is(err, ErrNotFound):
		return CodeNotFound
	case errors.Is(err, ErrInvalidResponse):
		return CodeInvalidResponse
	default:
		return CodeUnreachable
	}
}
