// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"math"
	"sync"
	"time"
)

// bucket is a token bucket.
type bucket struct {
	tokens float64
	last   time.Time
}

// KeyedLimiter keeps one token bucket per key (IP, session, connection …). Memory is
// bounded: idle full buckets are swept and the map never exceeds maxKeys entries.
type KeyedLimiter struct {
	mu      sync.Mutex
	clock   Clock
	burst   float64
	perSec  float64
	maxKeys int
	buckets map[string]*bucket
}

// DefaultMaxLimiterKeys bounds the number of tracked keys per limiter.
const DefaultMaxLimiterKeys = 100000

// NewKeyedLimiter allows `burst` events immediately and refills `burst` tokens per `per`.
func NewKeyedLimiter(clock Clock, burst int, per time.Duration) *KeyedLimiter {
	return &KeyedLimiter{
		clock: clock, burst: float64(burst), perSec: float64(burst) / per.Seconds(),
		maxKeys: DefaultMaxLimiterKeys, buckets: map[string]*bucket{},
	}
}

// Allow consumes one token for key.
func (l *KeyedLimiter) Allow(key string) bool { return l.AllowN(key, 1) }

// AllowN consumes n tokens for key if available.
func (l *KeyedLimiter) AllowN(key string, n int) bool {
	ok, _ := l.reserve(key, float64(n), l.burst, l.perSec)
	return ok
}

// AllowWait consumes one token for key and, when denied, returns the wait until one is available.
func (l *KeyedLimiter) AllowWait(key string) (bool, time.Duration) {
	return l.reserve(key, 1, l.burst, l.perSec)
}

// AllowRate consumes n tokens from a bucket with a per-call rate (events/s, burst = rate),
// returning the wait until enough tokens would be available when denied.
func (l *KeyedLimiter) AllowRate(key string, n, ratePerSec int) (bool, time.Duration) {
	r := float64(ratePerSec)
	return l.reserve(key, math.Min(float64(n), r), r, r)
}

func (l *KeyedLimiter) reserve(key string, n, burst, perSec float64) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	b, ok := l.buckets[key]
	if !ok {
		l.makeRoom(now)
		b = &bucket{tokens: burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(burst, b.tokens+now.Sub(b.last).Seconds()*perSec)
	b.last = now
	if b.tokens >= n {
		b.tokens -= n
		return true, 0
	}
	wait := time.Duration((n - b.tokens) / perSec * float64(time.Second))
	return false, wait
}

// makeRoom removes refilled buckets, then arbitrary ones, until a new key fits.
func (l *KeyedLimiter) makeRoom(now time.Time) {
	if len(l.buckets) < l.maxKeys {
		return
	}
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.perSec >= l.burst {
			delete(l.buckets, k)
		}
	}
	for k := range l.buckets {
		if len(l.buckets) < l.maxKeys {
			break
		}
		delete(l.buckets, k)
	}
}

// Len returns the number of tracked keys.
func (l *KeyedLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// FailureBlocker blocks a key for a period after too many failures within a window
// (ingestion auth failures: 30/min ⇒ 403 for 5 min, SPEC §15.4).
type FailureBlocker struct {
	mu      sync.Mutex
	clock   Clock
	limiter *KeyedLimiter
	block   time.Duration
	blocked map[string]time.Time
}

// NewFailureBlocker allows `limit` failures per `window` and then blocks for `block`.
func NewFailureBlocker(clock Clock, limit int, window, block time.Duration) *FailureBlocker {
	return &FailureBlocker{
		clock: clock, limiter: NewKeyedLimiter(clock, limit, window), block: block, blocked: map[string]time.Time{},
	}
}

// RecordFailure counts a failure for key and starts a block when the limit is exceeded.
func (f *FailureBlocker) RecordFailure(key string) {
	if f.limiter.Allow(key) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.blocked) >= DefaultMaxLimiterKeys {
		f.sweep()
	}
	f.blocked[key] = f.clock.Now().Add(f.block)
}

// Blocked reports whether key is currently blocked.
func (f *FailureBlocker) Blocked(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	until, ok := f.blocked[key]
	if !ok {
		return false
	}
	if f.clock.Now().Before(until) {
		return true
	}
	delete(f.blocked, key)
	return false
}

func (f *FailureBlocker) sweep() {
	now := f.clock.Now()
	for k, until := range f.blocked {
		if !now.Before(until) {
			delete(f.blocked, k)
		}
	}
}
