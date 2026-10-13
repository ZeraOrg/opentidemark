// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"sync"
	"time"
)

// UsageStore persists aggregated connection usage.
type UsageStore interface {
	AddUsage(ctx context.Context, id string, at time.Time, ip string, n int64) error
}

type usage struct {
	at     time.Time
	ip     string
	events int64
}

// UsageTracker aggregates connection usage in memory and flushes it periodically, so
// ingestion requests never wait on a metadata write.
type UsageTracker struct {
	mu      sync.Mutex
	store   UsageStore
	clock   Clock
	pending map[string]usage
}

// NewUsageTracker returns a tracker writing to store.
func NewUsageTracker(store UsageStore, clock Clock) *UsageTracker {
	return &UsageTracker{store: store, clock: clock, pending: map[string]usage{}}
}

// Record notes that connection id received n events from ip.
func (t *UsageTracker) Record(id, ip string, n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.pending[id]
	u.at, u.ip, u.events = t.clock.Now(), ip, u.events+int64(n)
	t.pending[id] = u
}

// Flush writes the pending usage; entries that fail are kept for the next flush.
func (t *UsageTracker) Flush(ctx context.Context) error {
	t.mu.Lock()
	batch := t.pending
	t.pending = map[string]usage{}
	t.mu.Unlock()
	var firstErr error
	for id, u := range batch {
		if err := t.store.AddUsage(ctx, id, u.at, u.ip, u.events); err != nil {
			t.requeue(id, u)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (t *UsageTracker) requeue(id string, u usage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cur, ok := t.pending[id]
	if ok {
		u.events += cur.events
		u.at, u.ip = cur.at, cur.ip
	}
	t.pending[id] = u
}

// Run flushes every interval until ctx is done, then flushes once more.
func (t *UsageTracker) Run(ctx context.Context, interval time.Duration, onErr func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interval)
			if err := t.Flush(flushCtx); err != nil {
				onErr(err)
			}
			cancel()
			return
		case <-ticker.C:
			if err := t.Flush(ctx); err != nil {
				onErr(err)
			}
		}
	}
}
