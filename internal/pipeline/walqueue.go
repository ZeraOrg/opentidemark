// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// WAL reading limits per batch.
const (
	walReadRecords = 1024
	walReadBytes   = 64 << 20
)

// Drop reasons of the WAL queue.
const (
	DropWALCorrupt = "wal_corrupt"
	DropWALDecode  = "wal_decode"
)

// Journal is the write-ahead log of one signal (implemented by *wal.Log). Append returns ctx's
// error only after the record was written, when the caller stopped waiting for its fsync; the
// record still becomes durable and is read like any other.
type Journal interface {
	Append(ctx context.Context, payload []byte) (wal.Pos, error)
	ReadFrom(from wal.Pos, maxRecords int, maxBytes int64) ([]wal.Record, error)
	SkipCorrupt(pos wal.Pos) wal.Pos
	Release(upTo wal.Pos) error
	Checkpoint() (wal.Checkpoint, error)
	SaveCheckpoint(cp wal.Checkpoint) error
	Notify() <-chan struct{}
	Durable() wal.Pos
	Close() error
}

// WALQueueDeps are the collaborators of a WAL-backed queue.
type WALQueueDeps[T any] struct {
	QueueDeps[T]
	Journal     Journal
	Codec       Codec[T]
	IsPermanent func(error) bool // insert errors that retrying cannot fix ⇒ dead letter
}

// WALQueue acknowledges rows once they are durable in the WAL; a flusher reads the WAL in order,
// inserts batches with a deterministic deduplication token and advances a persisted checkpoint
// (SPEC §9.8). On restart, an interrupted batch is re-sent with its original token.
type WALQueue[T any] struct {
	signal  string
	cfg     Config
	d       WALQueueDeps[T]
	pending atomic.Int64
	closed  atomic.Bool

	closing   chan struct{}
	done      chan struct{}
	abortCtx  context.Context
	abort     context.CancelFunc
	startOnce sync.Once
	closeOnce sync.Once
}

// walBatch is a run of WAL records [start, end) with their decoded rows.
type walBatch[T any] struct {
	start, end wal.Pos
	ids        []uuid.UUID
	rows       []T
}

// NewWALQueue builds a WAL-backed queue; call Start, and Close to drain.
func NewWALQueue[T any](signal string, cfg Config, d WALQueueDeps[T]) *WALQueue[T] {
	abortCtx, abort := context.WithCancel(context.Background()) //nolint:gosec // stored in q.abort and called by Close
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = sleepCtx
	}
	return &WALQueue[T]{signal: signal, cfg: cfg.withDefaults(), d: d, closing: make(chan struct{}), done: make(chan struct{}),
		abortCtx: abortCtx, abort: abort}
}

// Enqueue appends the admitted rows to the WAL and returns once they are durable. A full WAL
// (size cap) or a failing disk yields ingest.ErrBackpressure; nothing is acknowledged then. When
// ctx ends while the rows wait for their fsync, they are already written and will be stored, so
// they count as enqueued and ctx's error is returned (SPEC §9.8).
func (q *WALQueue[T]) Enqueue(ctx context.Context, rows []T) error {
	rows = admitRows(ctx, q.signal, rows, q.d.Gate, q.d.Stream, q.d.Metrics)
	if len(rows) == 0 {
		return nil
	}
	if q.closed.Load() {
		return fmt.Errorf("%w: %w", ingest.ErrBackpressure, ErrClosed)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("batch id: %w", err)
	}
	payload, err := q.d.Codec.Encode(id, rows)
	if err != nil {
		return err
	}
	if _, err := q.d.Journal.Append(ctx, payload); err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			q.written(len(rows))
			return fmt.Errorf("wal append: %w", err)
		}
		q.d.Metrics.Dropped(q.signal, DropBackpressure, len(rows))
		if errors.Is(err, wal.ErrFull) {
			return ingest.ErrBackpressure
		}
		return fmt.Errorf("%w: wal append: %w", ingest.ErrBackpressure, err)
	}
	q.written(len(rows))
	return nil
}

// written counts n rows that are in the WAL and will be committed by the flusher.
func (q *WALQueue[T]) written(n int) {
	depth := q.pending.Add(int64(n))
	q.d.Metrics.Enqueued(q.signal, n)
	q.d.Metrics.QueueDepth(q.signal, int(depth))
}

// Depth returns rows acknowledged but not yet committed (backlog from a previous run is counted
// once the flusher has read it).
func (q *WALQueue[T]) Depth() int { return int(max(q.pending.Load(), 0)) }

// Start runs the flusher in a new goroutine (once).
func (q *WALQueue[T]) Start() { q.startOnce.Do(func() { go q.run() }) }

// Close stops accepting rows and flushes the WAL to storage until ctx ends; unflushed rows stay
// in the WAL for the next start. The journal is closed afterwards.
func (q *WALQueue[T]) Close(ctx context.Context) error {
	q.closeOnce.Do(func() {
		q.closed.Store(true)
		close(q.closing)
	})
	q.Start()
	var err error
	select {
	case <-q.done:
	case <-ctx.Done():
		q.abort()
		<-q.done
		err = fmt.Errorf("drain %s: %w (remaining rows stay in the WAL)", q.signal, ctx.Err())
	}
	q.abort()
	return errors.Join(err, q.d.Journal.Close())
}

func (q *WALQueue[T]) run() {
	defer close(q.done)
	cp, err := q.d.Journal.Checkpoint()
	if err != nil {
		q.d.Logger.Error("wal checkpoint unreadable; replaying from the start", "signal", q.signal, "err", err)
	}
	pos := cp.Committed
	if cp.Pending != nil {
		b, ok := q.collectRange(cp.Pending.Start, cp.Pending.End)
		if !ok || !q.commit(b, cp.Pending.Token) {
			return
		}
		pos = b.end
	}
	for {
		b, ok := q.nextBatch(pos)
		if !ok || !q.commit(b, batchToken(b.ids)) {
			return
		}
		pos = b.end
	}
}

// collectRange reads exactly the records of an interrupted batch.
func (q *WALQueue[T]) collectRange(start, end wal.Pos) (walBatch[T], bool) {
	b := walBatch[T]{start: start, end: start}
	for b.end.Less(end) {
		if q.abortCtx.Err() != nil {
			return b, false
		}
		n := q.read(&b, walReadRecords, &end)
		if n == 0 {
			break // the rest of the range was lost (torn tail with sync=none)
		}
	}
	return b, true
}

// nextBatch waits until MaxRows rows or MaxWait since the first record, or until the queue is
// closing and the WAL is drained (ok=false once nothing is left).
func (q *WALQueue[T]) nextBatch(pos wal.Pos) (walBatch[T], bool) {
	b := walBatch[T]{start: pos, end: pos}
	var due time.Time
	for {
		if q.abortCtx.Err() != nil {
			return b, false
		}
		got := q.read(&b, walReadRecords, nil)
		if len(b.ids) > 0 && due.IsZero() {
			due = q.d.Now().Add(q.cfg.MaxWait)
		}
		switch {
		case len(b.rows) >= q.cfg.MaxRows, len(b.ids) > 0 && !q.d.Now().Before(due):
			return b, true
		case got > 0:
			continue
		case q.isClosing() && !b.end.Less(q.d.Journal.Durable()):
			return b, len(b.ids) > 0 || b.start != b.end
		}
		q.wait(due)
	}
}

func (q *WALQueue[T]) isClosing() bool {
	select {
	case <-q.closing:
		return true
	default:
		return false
	}
}

func (q *WALQueue[T]) wait(due time.Time) {
	var timer <-chan time.Time
	if !due.IsZero() {
		t := time.NewTimer(max(due.Sub(q.d.Now()), 0))
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-q.d.Journal.Notify():
	case <-timer:
	case <-q.closing:
	case <-q.abortCtx.Done():
	}
}

// read appends up to limit records to b, returning how many records were consumed. With until
// set, records starting at or after it are left unread (an interrupted batch is re-sent with
// exactly its original records).
func (q *WALQueue[T]) read(b *walBatch[T], limit int, until *wal.Pos) int {
	recs, err := q.d.Journal.ReadFrom(b.end, min(limit, max(q.cfg.MaxRows-len(b.rows), 1)), walReadBytes)
	if until != nil {
		for i, r := range recs {
			if !r.Pos.Less(*until) {
				recs, err = recs[:i], nil
				break
			}
		}
	}
	for _, r := range recs {
		b.end = r.End
		id, rows, derr := q.d.Codec.Decode(r.Payload)
		if derr != nil {
			q.d.Logger.Error("undecodable wal record skipped", "signal", q.signal, "seg", r.Pos.Seg, "off", r.Pos.Off, "err", derr)
			q.d.Metrics.Dropped(q.signal, DropWALDecode, 1)
			continue
		}
		b.ids = append(b.ids, id)
		b.rows = append(b.rows, rows...)
	}
	if err != nil && errors.Is(err, wal.ErrCorrupt) {
		next := q.d.Journal.SkipCorrupt(b.end)
		q.d.Logger.Error("corrupt wal record; skipping to the next segment", "signal", q.signal, "seg", b.end.Seg, "err", err)
		q.d.Metrics.Dropped(q.signal, DropWALCorrupt, 1)
		b.end = next
		return len(recs) + 1
	} else if err != nil {
		q.d.Logger.Error("wal read failed", "signal", q.signal, "err", err)
	}
	return len(recs)
}

// commit inserts a batch (persisting it as pending first) and advances the checkpoint. It
// returns false when the queue was aborted before the batch was stored.
func (q *WALQueue[T]) commit(b walBatch[T], token string) bool {
	if len(b.rows) > 0 {
		if err := q.d.Journal.SaveCheckpoint(wal.Checkpoint{Committed: b.start,
			Pending: &wal.Pending{Start: b.start, End: b.end, Token: token}}); err != nil {
			q.d.Logger.Error("saving wal checkpoint failed", "signal", q.signal, "err", err)
		}
		if !q.insert(b.rows, token) {
			return false
		}
	}
	if err := q.d.Journal.SaveCheckpoint(wal.Checkpoint{Committed: b.end}); err != nil {
		q.d.Logger.Error("saving wal checkpoint failed", "signal", q.signal, "err", err)
	}
	if err := q.d.Journal.Release(b.end); err != nil {
		q.d.Logger.Warn("deleting committed wal segments failed", "signal", q.signal, "err", err)
	}
	depth := q.pending.Add(-int64(len(b.rows)))
	q.d.Metrics.QueueDepth(q.signal, int(max(depth, 0)))
	return true
}

// insert writes rows with the token, retrying transient failures with capped backoff until it
// succeeds or the queue is aborted; permanently rejected rows are dead-lettered.
func (q *WALQueue[T]) insert(rows []T, token string) bool {
	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(store.WithDedupToken(q.abortCtx, token), q.cfg.WriteTimeout)
		err := q.d.Write(ctx, rows)
		cancel()
		if err == nil {
			q.d.Metrics.Written(q.signal, len(rows))
			return true
		}
		if q.abortCtx.Err() != nil {
			return false
		}
		q.d.Metrics.WriteFailed(q.signal)
		if q.d.IsPermanent != nil && q.d.IsPermanent(err) {
			q.d.Logger.Error("batch permanently rejected; dead-lettering", "signal", q.signal, "rows", len(rows), "err", err)
			deadLetter(q.d.Dead, q.d.Metrics, q.d.Logger, q.signal, rows)
			return true
		}
		q.d.Logger.Warn("batch insert failed; retrying from the wal", "signal", q.signal, "rows", len(rows), "attempt", attempt, "err", err)
		if q.d.Sleep(q.abortCtx, Backoff(attempt, q.cfg.RetryBase, q.cfg.RetryMax)) != nil {
			return false
		}
	}
}

// batchToken derives the deduplication token of a batch from the ids of its records, so the
// same WAL range always maps to the same token.
func batchToken(ids []uuid.UUID) string {
	h := sha256.New()
	for _, id := range ids {
		_, _ = h.Write(id[:]) // hash.Hash.Write never fails
	}
	sum := h.Sum(nil)
	tok, err := uuid.FromBytes(sum[:16])
	if err != nil {
		return fmt.Sprintf("%x", sum[:16])
	}
	return tok.String()
}

// deadLetter writes rows to the dead-letter files, counting losses when that fails.
func deadLetter[T any](dl DeadLetter, m Metrics, logger *slog.Logger, signal string, batch []T) {
	rows := make([]any, len(batch))
	for i := range batch {
		rows[i] = batch[i]
	}
	if err := dl.Write(signal, rows); err != nil {
		logger.Error("dead-letter write failed; rows lost", "signal", signal, "rows", len(batch), "err", err)
		m.Dropped(signal, "deadletter_failed", len(batch))
		return
	}
	m.DeadLettered(signal, len(batch))
}
