// SPDX-License-Identifier: Apache-2.0

// Package pipeline batches enqueued telemetry and writes it to storage (SPEC §9.1): bounded
// queues with backpressure, batching by size and age, retries with exponential backoff, a
// dead-letter fallback and a graceful drain on shutdown.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/ingest"
)

// Defaults for retries (SPEC §9.1).
const (
	DefaultRetryBase    = 100 * time.Millisecond
	DefaultRetryMax     = 30 * time.Second
	DefaultMaxAttempts  = 10
	DefaultWriteTimeout = 30 * time.Second
)

// Drop reasons reported to Metrics.
const (
	DropStreamPaused = "stream_paused"
	DropBackpressure = "backpressure"
)

// Config sizes a queue.
type Config struct {
	MaxRows      int           // rows per write
	MaxWait      time.Duration // max age of the oldest buffered row before a flush
	QueueRows    int           // capacity (buffered + in flight); full ⇒ backpressure
	RetryBase    time.Duration
	RetryMax     time.Duration
	MaxAttempts  int
	WriteTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.RetryBase <= 0 {
		c.RetryBase = DefaultRetryBase
	}
	if c.RetryMax <= 0 {
		c.RetryMax = DefaultRetryMax
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	return c
}

// Metrics receives pipeline self-metrics.
type Metrics interface {
	Enqueued(signal string, n int)
	Written(signal string, n int)
	Dropped(signal, reason string, n int)
	DeadLettered(signal string, n int)
	WriteFailed(signal string)
	QueueDepth(signal string, n int)
}

// StreamGate resolves the stream a row is stored in; ok=false drops the row (paused stream).
type StreamGate interface {
	Resolve(ctx context.Context, stream string) (name string, ok bool)
}

// DeadLetter persists rows that could not be written.
type DeadLetter interface {
	Write(signal string, rows []any) error
}

// Writer writes one batch.
type Writer[T any] func(ctx context.Context, batch []T) error

// Queue is a bounded batching queue for one signal.
type Queue[T any] struct {
	signal  string
	cfg     Config
	write   Writer[T]
	stream  func(*T) *string
	gate    StreamGate
	dl      DeadLetter
	metrics Metrics
	logger  *slog.Logger
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error

	mu       sync.Mutex
	buf      []T
	inflight int
	firstAt  time.Time
	closed   bool

	notify    chan struct{}
	closing   chan struct{}
	done      chan struct{}
	abortCtx  context.Context
	abort     context.CancelFunc
	startOnce sync.Once
}

// QueueDeps are the collaborators of a queue.
type QueueDeps[T any] struct {
	Write   Writer[T]
	Stream  func(*T) *string // accessor for the row's stream (for the gate)
	Gate    StreamGate       // optional
	Dead    DeadLetter
	Metrics Metrics
	Logger  *slog.Logger
	Now     func() time.Time
	Sleep   func(ctx context.Context, d time.Duration) error // optional; tests inject a fast sleeper
}

// NewQueue builds a queue; call Run in a goroutine and Close to drain.
func NewQueue[T any](signal string, cfg Config, deps QueueDeps[T]) *Queue[T] {
	abortCtx, abort := context.WithCancel(context.Background()) //nolint:gosec // stored in q.abort and called by Close
	q := &Queue[T]{
		signal: signal, cfg: cfg.withDefaults(), write: deps.Write, stream: deps.Stream, gate: deps.Gate,
		dl: deps.Dead, metrics: deps.Metrics, logger: deps.Logger, now: deps.Now, sleep: deps.Sleep,
		notify: make(chan struct{}, 1), closing: make(chan struct{}), done: make(chan struct{}),
		abortCtx: abortCtx, abort: abort,
	}
	if q.now == nil {
		q.now = time.Now
	}
	if q.sleep == nil {
		q.sleep = sleepCtx
	}
	return q
}

// ErrClosed is returned by Enqueue after Close.
var ErrClosed = errors.New("pipeline is shutting down")

// Enqueue adds rows atomically: either all admitted rows are queued or, when the queue lacks
// room, none are and ingest.ErrBackpressure is returned.
func (q *Queue[T]) Enqueue(ctx context.Context, rows []T) error {
	rows = q.admit(ctx, rows)
	if len(rows) == 0 {
		return nil
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return fmt.Errorf("%w: %w", ingest.ErrBackpressure, ErrClosed)
	}
	if len(q.buf)+q.inflight+len(rows) > q.cfg.QueueRows {
		q.mu.Unlock()
		q.metrics.Dropped(q.signal, DropBackpressure, len(rows))
		return ingest.ErrBackpressure
	}
	if len(q.buf) == 0 {
		q.firstAt = q.now()
	}
	q.buf = append(q.buf, rows...)
	depth := len(q.buf) + q.inflight
	q.mu.Unlock()
	q.metrics.Enqueued(q.signal, len(rows))
	q.metrics.QueueDepth(q.signal, depth)
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

// admit applies the stream gate, returning the rows to keep (stream names may be rewritten).
func (q *Queue[T]) admit(ctx context.Context, rows []T) []T {
	return admitRows(ctx, q.signal, rows, q.gate, q.stream, q.metrics)
}

// admitRows resolves each row's stream through the gate; rows of paused streams are dropped
// and counted.
func admitRows[T any](ctx context.Context, signal string, rows []T, gate StreamGate, stream func(*T) *string, m Metrics) []T {
	if gate == nil || stream == nil {
		return rows
	}
	kept := make([]T, 0, len(rows))
	for i := range rows {
		s := stream(&rows[i])
		name, ok := gate.Resolve(ctx, *s)
		if !ok {
			continue
		}
		*s = name
		kept = append(kept, rows[i])
	}
	if dropped := len(rows) - len(kept); dropped > 0 {
		m.Dropped(signal, DropStreamPaused, dropped)
	}
	return kept
}

// Depth returns buffered plus in-flight rows.
func (q *Queue[T]) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buf) + q.inflight
}

// Run processes batches until the queue is closed and drained.
func (q *Queue[T]) Run() {
	defer close(q.done)
	for {
		batch, ok := q.next()
		if !ok {
			return
		}
		q.flush(batch)
	}
}

// next waits for the next batch; ok=false once closed and empty.
func (q *Queue[T]) next() ([]T, bool) {
	for {
		q.mu.Lock()
		n, closed := len(q.buf), q.closed
		due := q.firstAt.Add(q.cfg.MaxWait)
		if n > 0 && (n >= q.cfg.MaxRows || closed || !q.now().Before(due)) {
			batch := q.take()
			q.mu.Unlock()
			return batch, true
		}
		q.mu.Unlock()
		if n == 0 && closed {
			return nil, false
		}
		q.wait(n > 0, due)
	}
}

// wait blocks until new rows arrive, the queue closes or (when rows are buffered) the
// oldest row becomes due.
func (q *Queue[T]) wait(hasRows bool, due time.Time) {
	var timer <-chan time.Time
	if hasRows {
		t := time.NewTimer(due.Sub(q.now()))
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-q.notify:
	case <-timer:
	case <-q.closing:
	}
}

// take removes up to MaxRows rows from the buffer; the caller holds q.mu.
func (q *Queue[T]) take() []T {
	k := min(len(q.buf), q.cfg.MaxRows)
	batch := make([]T, k)
	copy(batch, q.buf[:k])
	rest := copy(q.buf, q.buf[k:])
	clear(q.buf[rest:])
	q.buf = q.buf[:rest]
	q.inflight += k
	if rest > 0 {
		q.firstAt = q.now()
	}
	return batch
}

// flush writes a batch with retries, dead-lettering it when all attempts fail.
func (q *Queue[T]) flush(batch []T) {
	defer func() {
		q.mu.Lock()
		q.inflight -= len(batch)
		depth := len(q.buf) + q.inflight
		q.mu.Unlock()
		q.metrics.QueueDepth(q.signal, depth)
	}()
	for attempt := 1; ; attempt++ {
		if q.abortCtx.Err() == nil {
			ctx, cancel := context.WithTimeout(q.abortCtx, q.cfg.WriteTimeout)
			err := q.write(ctx, batch)
			cancel()
			if err == nil {
				q.metrics.Written(q.signal, len(batch))
				return
			}
			q.metrics.WriteFailed(q.signal)
			q.logger.Warn("batch insert failed", "signal", q.signal, "rows", len(batch), "attempt", attempt, "err", err)
		}
		if attempt >= q.cfg.MaxAttempts || q.abortCtx.Err() != nil {
			q.deadLetter(batch)
			return
		}
		if err := q.sleep(q.abortCtx, Backoff(attempt, q.cfg.RetryBase, q.cfg.RetryMax)); err != nil {
			q.deadLetter(batch)
			return
		}
	}
}

func (q *Queue[T]) deadLetter(batch []T) {
	deadLetter(q.dl, q.metrics, q.logger, q.signal, batch)
}

// Close stops accepting rows and drains the queue. If ctx ends first, pending writes are
// aborted and the remaining rows are dead-lettered before Close returns.
func (q *Queue[T]) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.closing)
	}
	q.mu.Unlock()
	q.startOnce.Do(func() { go q.Run() }) //nolint:contextcheck // drain even if Run was never started; ctx only bounds the wait
	select {
	case <-q.done:
		q.abort()
		return nil
	case <-ctx.Done():
		q.abort()
		<-q.done
		return fmt.Errorf("drain %s: %w", q.signal, ctx.Err())
	}
}

// Start runs the queue in a new goroutine (once).
func (q *Queue[T]) Start() { q.startOnce.Do(func() { go q.Run() }) }

// Backoff returns base·2^(attempt-1) capped at maxDelay.
func Backoff(attempt int, base, maxDelay time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < maxDelay; i++ {
		d *= 2
	}
	return min(d, maxDelay)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
