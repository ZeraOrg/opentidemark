// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"errors"
	"sync"
	"time"
)

// DiskCheckInterval is how long a free-space reading is reused; appends in between are
// subtracted from it.
const DiskCheckInterval = time.Second

// ErrLowDisk: the file system holding the WAL has less free space than the configured floor
// (TIDEMARK_WAL_MIN_FREE_BYTES); ingestion answers with backpressure until space is freed.
var ErrLowDisk = errors.New("write-ahead log disk is below its free-space floor")

// DiskGuard refuses appends while the WAL's file system has less than a floor of free bytes,
// so backpressure starts before the disk is full (the WAL shares it with the metadata database,
// dead letters and backups). It is shared by the logs of all signals and safe for concurrent use.
type DiskGuard struct {
	minFree uint64
	free    func() (uint64, error)
	now     func() time.Time

	mu        sync.Mutex
	checkedAt time.Time
	avail     uint64 // free bytes at the last reading minus the bytes allowed since
	known     bool   // false when the last reading failed: the guard then never refuses
}

// NewDiskGuard returns a guard that keeps minFree bytes free on the file system that free
// measures. A failing free (unsupported platform) never refuses an append.
func NewDiskGuard(minFree uint64, free func() (uint64, error), now func() time.Time) *DiskGuard {
	return &DiskGuard{minFree: minFree, free: free, now: now}
}

// allow reports whether n more bytes keep the floor, and accounts them when they do.
func (g *DiskGuard) allow(n int64) bool {
	if g == nil || g.minFree == 0 || n <= 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if now := g.now(); g.checkedAt.IsZero() || now.Sub(g.checkedAt) >= DiskCheckInterval {
		g.checkedAt = now
		free, err := g.free()
		g.avail, g.known = free, err == nil
	}
	if !g.known {
		return true
	}
	need := uint64(n) //nolint:gosec // n > 0
	if g.avail < g.minFree || g.avail-g.minFree < need {
		return false
	}
	g.avail -= need
	return true
}
