// SPDX-License-Identifier: Apache-2.0

// Package cluster makes Tidemark nodes interchangeable (SPEC §17.5): lease-based leader election
// for singleton work, cache invalidation across nodes (PostgreSQL LISTEN/NOTIFY) and the
// graceful drain state used by readiness probes.
package cluster

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Lease timing (SPEC §17.5): a 30 s lease renewed every 10 s. A node considers itself leader
// only until renewal time + TTL - RenewEvery, so it stops a full renewal interval before another
// node may take the lease over.
const (
	DefaultLeaseTTL   = 30 * time.Second
	DefaultRenewEvery = 10 * time.Second
	leaseCallTimeout  = 5 * time.Second
)

// Well-known lease names for singleton work.
const (
	LeaseAlerting     = "alerting"
	LeaseRetention    = "retention"
	LeaseStats        = "stats"
	LeaseCHMigrations = "clickhouse-migrations"
	LeasePurge        = "purge"
	LeaseImportPrefix = "import:"
)

// Leader reports whether this node currently holds a lease (consumed by singleton workers).
type Leader interface {
	IsLeader(name string) bool
}

// Clock is the time source (fake in tests).
type Clock interface{ Now() time.Time }

// LeaseStore persists leases (implemented by meta.Leases).
type LeaseStore interface {
	TryAcquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error)
	Release(ctx context.Context, name, holder string, now time.Time) error
}

// ElectorConfig configures an Elector.
type ElectorConfig struct {
	NodeID     string
	TTL        time.Duration
	RenewEvery time.Duration
}

type leaseState struct {
	validUntil time.Time
	held       bool
}

// Elector acquires and renews the leases registered on it and answers IsLeader locally.
type Elector struct {
	store  LeaseStore
	clock  Clock
	cfg    ElectorConfig
	logger *slog.Logger

	mu      sync.Mutex
	leases  map[string]*leaseState
	changed chan struct{} // closed and replaced after every renewal round
}

// NewElector returns an elector; call Run to keep the leases.
func NewElector(store LeaseStore, clock Clock, cfg ElectorConfig, logger *slog.Logger) *Elector {
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultLeaseTTL
	}
	if cfg.RenewEvery <= 0 || cfg.RenewEvery >= cfg.TTL {
		cfg.RenewEvery = DefaultRenewEvery
	}
	return &Elector{store: store, clock: clock, cfg: cfg, logger: logger, leases: map[string]*leaseState{},
		changed: make(chan struct{})}
}

// Register adds lease names to compete for (IsLeader also registers lazily).
func (e *Elector) Register(names ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range names {
		if _, ok := e.leases[n]; !ok {
			e.leases[n] = &leaseState{}
		}
	}
}

// IsLeader reports whether this node holds name now. An unknown name is registered and becomes
// eligible at the next renewal round.
func (e *Elector) IsLeader(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, ok := e.leases[name]
	if !ok {
		e.leases[name] = &leaseState{}
		return false
	}
	return st.held && e.clock.Now().Before(st.validUntil)
}

// Changed returns a channel closed after the next renewal round.
func (e *Elector) Changed() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.changed
}

// Tick runs one renewal round for every registered lease.
func (e *Elector) Tick(ctx context.Context) {
	e.mu.Lock()
	names := make([]string, 0, len(e.leases))
	for n := range e.leases {
		names = append(names, n)
	}
	e.mu.Unlock()
	for _, n := range names {
		e.renew(ctx, n)
	}
	e.mu.Lock()
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
}

func (e *Elector) renew(ctx context.Context, name string) {
	now := e.clock.Now()
	cctx, cancel := context.WithTimeout(ctx, leaseCallTimeout)
	ok, err := e.store.TryAcquire(cctx, name, e.cfg.NodeID, now, e.cfg.TTL)
	cancel()
	e.mu.Lock()
	defer e.mu.Unlock()
	st, registered := e.leases[name]
	switch {
	case !registered:
		return // released while renewing; an acquired row simply expires
	case err != nil:
		e.logger.Warn("lease renewal failed", "lease", name, "err", err) // keep the local validity window
	case ok:
		if !st.held {
			e.logger.Info("lease acquired", "lease", name, "node", e.cfg.NodeID)
		}
		st.held, st.validUntil = true, now.Add(e.cfg.TTL-e.cfg.RenewEvery)
	default:
		if st.held {
			e.logger.Warn("lease lost", "lease", name, "node", e.cfg.NodeID)
		}
		st.held = false
	}
}

// Run renews the leases until ctx ends, then releases the held ones.
func (e *Elector) Run(ctx context.Context) {
	e.Tick(ctx)
	t := time.NewTicker(e.cfg.RenewEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.releaseAll(context.WithoutCancel(ctx))
			return
		case <-t.C:
			e.Tick(ctx)
		}
	}
}

func (e *Elector) releaseAll(ctx context.Context) {
	e.mu.Lock()
	var held []string
	for n, st := range e.leases {
		if st.held {
			held = append(held, n)
			st.held = false
		}
	}
	e.mu.Unlock()
	for _, n := range held {
		cctx, cancel := context.WithTimeout(ctx, leaseCallTimeout)
		if err := e.store.Release(cctx, n, e.cfg.NodeID, e.clock.Now()); err != nil {
			e.logger.Warn("lease release failed", "lease", n, "err", err)
		}
		cancel()
	}
}

// Release stops competing for name (e.g. a finished import:<id> job) and releases the lease if
// this node holds it, so short-lived leases do not stay registered forever.
func (e *Elector) Release(ctx context.Context, name string) {
	e.mu.Lock()
	st, ok := e.leases[name]
	delete(e.leases, name)
	e.mu.Unlock()
	if !ok || !st.held {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, leaseCallTimeout)
	defer cancel()
	if err := e.store.Release(cctx, name, e.cfg.NodeID, e.clock.Now()); err != nil {
		e.logger.Warn("lease release failed", "lease", name, "err", err)
	}
}

// Lead runs fn while this node holds name: fn starts once the lease is acquired and its context
// is cancelled as soon as the lease is lost. Lead returns when fn returns or ctx ends.
func (e *Elector) Lead(ctx context.Context, name string, fn func(ctx context.Context)) {
	e.Register(name)
	for ctx.Err() == nil {
		changed := e.Changed()
		if !e.IsLeader(name) {
			select {
			case <-changed:
			case <-ctx.Done():
			}
			continue
		}
		if e.runWhileLeader(ctx, name, fn) {
			return
		}
	}
}

// runWhileLeader returns true when fn finished, false when leadership was lost.
func (e *Elector) runWhileLeader(ctx context.Context, name string, fn func(ctx context.Context)) bool {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(lctx)
	}()
	for {
		changed := e.Changed()
		select {
		case <-done:
			return true
		case <-ctx.Done():
			cancel()
			<-done
			return true
		case <-changed:
			if !e.IsLeader(name) {
				cancel()
				<-done
				return false
			}
		}
	}
}
