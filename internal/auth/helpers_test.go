// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fastParams keeps argon2 cheap in tests.
func fastParams() Argon2Params {
	return Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}
}

func openDB(t *testing.T, clock Clock) *meta.DB {
	t.Helper()
	db, err := meta.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"), meta.WithClock(clock.Now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
