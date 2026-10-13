// SPDX-License-Identifier: Apache-2.0

package wal

import "sync/atomic"

// Budget is the size cap shared by the logs of all signals (TIDEMARK_WAL_MAX_BYTES). Appends
// that would exceed it fail with ErrFull, which ingestion answers with backpressure (503).
type Budget struct {
	max  int64
	used atomic.Int64
}

// NewBudget returns a budget of max bytes (<= 0 = unlimited).
func NewBudget(maxBytes int64) *Budget { return &Budget{max: maxBytes} }

// reserve adds n when it fits.
func (b *Budget) reserve(n int64) bool {
	for {
		cur := b.used.Load()
		if b.max > 0 && cur+n > b.max {
			return false
		}
		if b.used.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// force adds n unconditionally (existing segments found at startup).
func (b *Budget) force(n int64) { b.used.Add(n) }

// Release returns n bytes (deleted segments, failed appends).
func (b *Budget) Release(n int64) { b.used.Add(-n) }

// Used returns the bytes currently accounted.
func (b *Budget) Used() int64 { return b.used.Load() }

// Max returns the cap (<= 0 = unlimited).
func (b *Budget) Max() int64 { return b.max }
