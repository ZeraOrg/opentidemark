// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"sync/atomic"
	"time"
)

// Drainer is the graceful-drain state (SPEC §17.5): once started, /readyz answers 503 so the
// load balancer removes the node while it keeps serving for the drain period.
type Drainer struct {
	draining atomic.Bool
}

// Draining reports whether the drain has started.
func (d *Drainer) Draining() bool { return d.draining.Load() }

// Drain marks the node as draining and waits for period (or until ctx ends).
func (d *Drainer) Drain(ctx context.Context, period time.Duration) {
	d.draining.Store(true)
	if period <= 0 {
		return
	}
	t := time.NewTimer(period)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
