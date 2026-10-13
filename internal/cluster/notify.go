// SPDX-License-Identifier: Apache-2.0

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Cache invalidation (SPEC §17.5).
const (
	// NotifyChannel is the PostgreSQL channel nodes LISTEN on.
	NotifyChannel = "tidemark_invalidate"
	// TopicConnections invalidates connection-key caches.
	TopicConnections = "connections"

	publishTimeout   = 5 * time.Second
	reconnectBase    = time.Second
	reconnectMax     = 30 * time.Second
	listenSetupLimit = 10 * time.Second
)

// Bus delivers invalidation topics to every node.
type Bus interface {
	Publish(ctx context.Context, topic string) error
	Subscribe(topic string, fn func())
	Run(ctx context.Context)
}

// LocalBus is the single-node bus (SQLite): publishing calls the local subscribers directly.
type LocalBus struct {
	mu   sync.Mutex
	subs map[string][]func()
}

// NewLocalBus returns a bus without cross-node delivery.
func NewLocalBus() *LocalBus { return &LocalBus{subs: map[string][]func(){}} }

// Publish implements Bus.
func (b *LocalBus) Publish(_ context.Context, topic string) error {
	for _, fn := range b.handlers(topic) {
		fn()
	}
	return nil
}

// Subscribe implements Bus.
func (b *LocalBus) Subscribe(topic string, fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[topic] = append(b.subs[topic], fn)
}

// Run implements Bus (nothing to do).
func (b *LocalBus) Run(context.Context) {}

func (b *LocalBus) handlers(topic string) []func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]func(){}, b.subs[topic]...)
}

// Notifier sends a PostgreSQL NOTIFY (implemented by meta.DB).
type Notifier interface {
	Notify(ctx context.Context, channel, payload string) error
}

// PGBus publishes with NOTIFY through the metadata pool and listens on a dedicated pgx
// connection; after a reconnect every subscriber is called once, because notifications sent
// while disconnected are lost.
type PGBus struct {
	LocalBus
	dsn    string
	notify Notifier
	logger *slog.Logger
}

// NewPGBus returns a bus for the PostgreSQL metadata database at dsn.
func NewPGBus(dsn string, n Notifier, logger *slog.Logger) *PGBus {
	return &PGBus{LocalBus: LocalBus{subs: map[string][]func(){}}, dsn: dsn, notify: n, logger: logger}
}

// Publish implements Bus: the local subscribers run immediately, other nodes on NOTIFY.
func (b *PGBus) Publish(ctx context.Context, topic string) error {
	for _, fn := range b.handlers(topic) {
		fn()
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if err := b.notify.Notify(ctx, NotifyChannel, topic); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

// Run listens until ctx ends, reconnecting with backoff.
func (b *PGBus) Run(ctx context.Context) {
	delay := reconnectBase
	for ctx.Err() == nil {
		err := b.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		b.logger.Warn("invalidation listener disconnected; reconnecting", "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, reconnectMax)
	}
}

func (b *PGBus) listen(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(ctx, listenSetupLimit)
	conn, err := pgx.Connect(sctx, b.dsn)
	if err == nil {
		_, err = conn.Exec(sctx, "LISTEN "+NotifyChannel)
	}
	cancel()
	if err != nil {
		if conn != nil {
			_ = conn.Close(context.WithoutCancel(ctx)) // the connect/listen error is reported
		}
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }() // closing an idle listener
	b.invalidateAll()
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		for _, fn := range b.handlers(n.Payload) {
			fn()
		}
	}
}

func (b *PGBus) invalidateAll() {
	b.mu.Lock()
	var all []func()
	for _, fns := range b.subs {
		all = append(all, fns...)
	}
	b.mu.Unlock()
	for _, fn := range all {
		fn()
	}
}

// Invalidator is a local cache.
type Invalidator interface{ Invalidate() }

// BroadcastCache invalidates a local cache and tells the other nodes to do the same.
type BroadcastCache struct {
	Bus    Bus
	Topic  string
	Logger *slog.Logger
}

// Invalidate implements service.KeyCache.
func (c BroadcastCache) Invalidate() {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := c.Bus.Publish(ctx, c.Topic); err != nil {
		c.Logger.Warn("cache invalidation broadcast failed; other nodes refresh within the cache TTL", "topic", c.Topic, "err", err)
	}
}
