// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openLeases(t *testing.T) *meta.Leases {
	t.Helper()
	db, err := meta.Open(context.Background(), filepath.Join(t.TempDir(), meta.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return meta.NewLeases(db)
}

// flakyStore fails TryAcquire while down is set (database unreachable).
type flakyStore struct {
	LeaseStore
	down atomic.Bool
}

func (f *flakyStore) TryAcquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	if f.down.Load() {
		return false, errors.New("database unreachable")
	}
	return f.LeaseStore.TryAcquire(ctx, name, holder, now, ttl)
}

// TestTwoNodesElectOneLeader: exactly one node leads; when it stops renewing (database lost
// for it) it steps down before the other node can take over, and the other node takes over
// after the lease expires.
func TestTwoNodesElectOneLeader(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	store := openLeases(t)
	aStore := &flakyStore{LeaseStore: store}
	a := NewElector(aStore, clock, ElectorConfig{NodeID: "node-a"}, discard())
	b := NewElector(store, clock, ElectorConfig{NodeID: "node-b"}, discard())
	a.Register(LeaseAlerting)
	b.Register(LeaseAlerting)
	a.Tick(ctx)
	b.Tick(ctx)
	if !a.IsLeader(LeaseAlerting) || b.IsLeader(LeaseAlerting) {
		t.Fatalf("leaders: a=%v b=%v", a.IsLeader(LeaseAlerting), b.IsLeader(LeaseAlerting))
	}
	for range 5 { // steady state: a renews every 10 s and keeps the lease
		clock.Advance(DefaultRenewEvery)
		a.Tick(ctx)
		b.Tick(ctx)
		if !a.IsLeader(LeaseAlerting) || b.IsLeader(LeaseAlerting) {
			t.Fatal("leadership flapped while renewing")
		}
	}
	aStore.down.Store(true)
	clock.Advance(DefaultRenewEvery)
	a.Tick(ctx)
	b.Tick(ctx)
	if !a.IsLeader(LeaseAlerting) || b.IsLeader(LeaseAlerting) {
		t.Fatal("one failed renewal must not change leadership")
	}
	clock.Advance(DefaultRenewEvery)
	a.Tick(ctx)
	b.Tick(ctx)
	if a.IsLeader(LeaseAlerting) {
		t.Fatal("node-a must step down once its local validity (TTL - renew interval) ends")
	}
	if b.IsLeader(LeaseAlerting) {
		t.Fatal("node-b took over before the lease expired")
	}
	clock.Advance(DefaultRenewEvery)
	a.Tick(ctx)
	b.Tick(ctx)
	if !b.IsLeader(LeaseAlerting) || a.IsLeader(LeaseAlerting) {
		t.Fatalf("takeover after expiry: a=%v b=%v", a.IsLeader(LeaseAlerting), b.IsLeader(LeaseAlerting))
	}
	aStore.down.Store(false)
	clock.Advance(DefaultRenewEvery)
	a.Tick(ctx)
	b.Tick(ctx)
	if a.IsLeader(LeaseAlerting) || !b.IsLeader(LeaseAlerting) {
		t.Fatal("recovered node-a must not steal the lease")
	}
}

func TestLazyRegistrationAndRelease(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	store := openLeases(t)
	a := NewElector(store, clock, ElectorConfig{NodeID: "a", TTL: 30 * time.Second, RenewEvery: 10 * time.Second}, discard())
	b := NewElector(store, clock, ElectorConfig{NodeID: "b"}, discard())
	if a.IsLeader(LeaseRetention) {
		t.Fatal("unregistered lease reported as held")
	}
	a.Tick(ctx)
	if !a.IsLeader(LeaseRetention) {
		t.Fatal("lazily registered lease not acquired")
	}
	a.releaseAll(ctx)
	if a.IsLeader(LeaseRetention) {
		t.Fatal("released lease still held")
	}
	b.Register(LeaseRetention)
	b.Tick(ctx)
	if !b.IsLeader(LeaseRetention) {
		t.Fatal("released lease not available to another node")
	}
}

// TestReleaseOneLease: a per-job lease is handed back immediately and no longer renewed.
func TestReleaseOneLease(t *testing.T) {
	ctx := context.Background()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	store := openLeases(t)
	a := NewElector(store, clock, ElectorConfig{NodeID: "a"}, discard())
	b := NewElector(store, clock, ElectorConfig{NodeID: "b"}, discard())
	job := LeaseImportPrefix + "job-1"
	a.Register(job, LeaseStats)
	a.Tick(ctx)
	b.Register(job)
	b.Tick(ctx)
	if !a.IsLeader(job) || b.IsLeader(job) {
		t.Fatal("a should hold the job lease")
	}
	a.Release(ctx, job)
	a.Release(ctx, "never-registered")
	if a.IsLeader(job) {
		t.Fatal("released job lease still reported as held")
	}
	b.Tick(ctx)
	if !b.IsLeader(job) {
		t.Fatal("released job lease not available to b")
	}
	a.Tick(ctx) // a no longer competes for the job lease but keeps its other leases
	if !b.IsLeader(job) || !a.IsLeader(LeaseStats) {
		t.Fatal("release affected other leases")
	}
}

func TestLeadStopsWorkWhenLeaseIsLost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	store := &flakyStore{LeaseStore: openLeases(t)}
	e := NewElector(store, clock, ElectorConfig{NodeID: "a"}, discard())
	started, stopped := make(chan struct{}), make(chan struct{})
	go e.Lead(ctx, "import:job-1", func(lctx context.Context) {
		close(started)
		<-lctx.Done()
		close(stopped)
	})
	waitRegistered(t, e, "import:job-1")
	e.Tick(ctx)
	<-started
	store.down.Store(true)
	clock.Advance(DefaultLeaseTTL)
	e.Tick(ctx)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("work kept running after the lease was lost")
	}
}

func waitRegistered(t *testing.T, e *Elector, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.mu.Lock()
		_, ok := e.leases[name]
		e.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("lease never registered")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLocalBusAndBroadcastCache(t *testing.T) {
	bus := NewLocalBus()
	var hits atomic.Int32
	bus.Subscribe(TopicConnections, func() { hits.Add(1) })
	BroadcastCache{Bus: bus, Topic: TopicConnections, Logger: discard()}.Invalidate()
	if hits.Load() != 1 {
		t.Fatalf("hits %d", hits.Load())
	}
	bus.Run(context.Background())
}

func TestDrainer(t *testing.T) {
	var d Drainer
	if d.Draining() {
		t.Fatal("draining before start")
	}
	start := time.Now()
	d.Drain(context.Background(), 20*time.Millisecond)
	if !d.Draining() || time.Since(start) < 20*time.Millisecond {
		t.Fatal("drain did not wait")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d.Drain(ctx, time.Hour)
}

// TestPGBusDeliversAcrossNodes uses LISTEN/NOTIFY on a real PostgreSQL
// (TIDEMARK_TEST_POSTGRES_DSN).
func TestPGBusDeliversAcrossNodes(t *testing.T) {
	dsn := os.Getenv("TIDEMARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TIDEMARK_TEST_POSTGRES_DSN not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := meta.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sender, receiver := NewPGBus(dsn, db, discard()), NewPGBus(dsn, db, discard())
	got := make(chan struct{}, 8)
	receiver.Subscribe(TopicConnections, func() { got <- struct{}{} })
	go receiver.Run(ctx)
	select { // the initial "invalidate all" after connecting proves the listener is up
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("listener did not connect")
	}
	if err := sender.Publish(ctx, TopicConnections); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(10 * time.Second):
		t.Fatal("notification not delivered to the other node")
	}
}
