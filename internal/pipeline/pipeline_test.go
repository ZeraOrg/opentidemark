// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/model"
)

type stats struct {
	mu                                    sync.Mutex
	enq, written, dead, failed, lastDepth int
	dropped                               map[string]int
}

func newStats() *stats { return &stats{dropped: map[string]int{}} }

func (s *stats) Enqueued(_ string, n int) { s.mu.Lock(); s.enq += n; s.mu.Unlock() }
func (s *stats) Written(_ string, n int)  { s.mu.Lock(); s.written += n; s.mu.Unlock() }
func (s *stats) Dropped(_, r string, n int) {
	s.mu.Lock()
	s.dropped[r] += n
	s.mu.Unlock()
}
func (s *stats) DeadLettered(_ string, n int) { s.mu.Lock(); s.dead += n; s.mu.Unlock() }
func (s *stats) WriteFailed(string)           { s.mu.Lock(); s.failed++; s.mu.Unlock() }
func (s *stats) QueueDepth(_ string, n int)   { s.mu.Lock(); s.lastDepth = n; s.mu.Unlock() }
func (s *stats) get() (int, int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enq, s.written, s.dead, s.failed
}

type recorder struct {
	mu      sync.Mutex
	batches [][]int
	fail    int // fail the first n calls
	block   chan struct{}
	calls   int
}

func (r *recorder) write(ctx context.Context, b []int) error {
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls <= r.fail {
		return errors.New("clickhouse down")
	}
	r.batches = append(r.batches, append([]int(nil), b...))
	return nil
}

func (r *recorder) total() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, b := range r.batches {
		n += len(b)
	}
	return n, len(r.batches)
}

type memDead struct {
	mu   sync.Mutex
	rows []any
	err  error
}

func (m *memDead) Write(_ string, rows []any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.rows = append(m.rows, rows...)
	return nil
}

func (m *memDead) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func newIntQueue(cfg Config, r *recorder, d DeadLetter, s *stats) *Queue[int] {
	return NewQueue("events", cfg, QueueDeps[int]{Write: r.write, Dead: d, Metrics: s, Logger: quietLogger(), Sleep: noSleep})
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestBatchByMaxRows(t *testing.T) {
	r, s := &recorder{}, newStats()
	q := newIntQueue(Config{MaxRows: 3, MaxWait: time.Hour, QueueRows: 100}, r, &memDead{}, s)
	q.Start()
	if err := q.Enqueue(t.Context(), []int{1, 2, 3, 4, 5, 6, 7}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { n, _ := r.total(); return n == 6 })
	if _, b := r.total(); b != 2 {
		t.Errorf("batches %d", b)
	}
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.total(); n != 7 {
		t.Errorf("drained %d", n)
	}
	if enq, written, _, _ := s.get(); enq != 7 || written != 7 {
		t.Errorf("stats %d %d", enq, written)
	}
}

func TestBatchByMaxWait(t *testing.T) {
	r := &recorder{}
	q := newIntQueue(Config{MaxRows: 1000, MaxWait: 20 * time.Millisecond, QueueRows: 2000}, r, &memDead{}, newStats())
	q.Start()
	defer q.Close(t.Context())
	start := time.Now()
	_ = q.Enqueue(t.Context(), []int{1})
	_ = q.Enqueue(t.Context(), []int{2})
	eventually(t, func() bool { n, _ := r.total(); return n == 2 })
	if time.Since(start) < 15*time.Millisecond {
		t.Error("flushed before max wait")
	}
	if _, b := r.total(); b != 1 {
		t.Errorf("batches %d", b)
	}
}

func TestBackpressureIsAtomic(t *testing.T) {
	r, s := &recorder{block: make(chan struct{})}, newStats()
	q := newIntQueue(Config{MaxRows: 2, MaxWait: time.Millisecond, QueueRows: 5}, r, &memDead{}, s)
	q.Start()
	if err := q.Enqueue(t.Context(), []int{1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(t.Context(), []int{5, 6}); !errors.Is(err, ingest.ErrBackpressure) {
		t.Fatalf("expected backpressure, got %v", err)
	}
	if q.Depth() != 4 || s.dropped[DropBackpressure] != 2 {
		t.Errorf("depth %d dropped %v", q.Depth(), s.dropped)
	}
	if err := q.Enqueue(t.Context(), []int{5}); err != nil {
		t.Errorf("room for one: %v", err)
	}
	close(r.block)
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.total(); n != 5 {
		t.Errorf("written %d", n)
	}
	if err := q.Enqueue(t.Context(), []int{9}); !errors.Is(err, ErrClosed) || !errors.Is(err, ingest.ErrBackpressure) {
		t.Errorf("after close: %v", err)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	r, s, dl := &recorder{fail: 3}, newStats(), &memDead{}
	q := newIntQueue(Config{MaxRows: 10, MaxWait: time.Millisecond, QueueRows: 10}, r, dl, s)
	var sleeps []time.Duration
	var mu sync.Mutex
	q.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		sleeps = append(sleeps, d)
		mu.Unlock()
		return nil
	}
	q.Start()
	_ = q.Enqueue(t.Context(), []int{1, 2})
	_ = q.Close(t.Context())
	if n, _ := r.total(); n != 2 || dl.count() != 0 {
		t.Errorf("written %d dead %d", n, dl.count())
	}
	if _, _, _, failed := s.get(); failed != 3 {
		t.Errorf("failed %d", failed)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleep %d = %v", i, sleeps[i])
		}
	}
}

func TestRetryExhaustionDeadLetters(t *testing.T) {
	r, s, dl := &recorder{fail: 1000}, newStats(), &memDead{}
	q := NewQueue("events", Config{MaxRows: 10, MaxWait: time.Millisecond, QueueRows: 10, MaxAttempts: 4},
		QueueDeps[int]{Write: r.write, Dead: dl, Metrics: s, Logger: quietLogger(), Sleep: func(context.Context, time.Duration) error { return nil }})
	q.Start()
	_ = q.Enqueue(t.Context(), []int{1, 2, 3})
	_ = q.Close(t.Context())
	if r.calls != 4 || dl.count() != 3 {
		t.Errorf("calls %d dead %d", r.calls, dl.count())
	}
	if _, _, dead, _ := s.get(); dead != 3 {
		t.Errorf("dead stat %d", dead)
	}
}

func TestDeadLetterFailureCounted(t *testing.T) {
	r, s := &recorder{fail: 1000}, newStats()
	q := NewQueue("events", Config{MaxRows: 10, MaxWait: time.Millisecond, QueueRows: 10, MaxAttempts: 1},
		QueueDeps[int]{Write: r.write, Dead: &memDead{err: errors.New("disk full")}, Metrics: s, Logger: quietLogger()})
	q.Start()
	_ = q.Enqueue(t.Context(), []int{1})
	_ = q.Close(t.Context())
	if s.dropped["deadletter_failed"] != 1 {
		t.Errorf("dropped %v", s.dropped)
	}
}

func TestShutdownDeadlineAborts(t *testing.T) {
	r, dl := &recorder{block: make(chan struct{})}, &memDead{}
	q := newIntQueue(Config{MaxRows: 2, MaxWait: time.Millisecond, QueueRows: 100}, r, dl, newStats())
	q.Start()
	_ = q.Enqueue(t.Context(), []int{1, 2, 3, 4, 5})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := q.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	if dl.count() != 5 {
		t.Errorf("dead-lettered %d of 5", dl.count())
	}
}

func TestCloseWithoutStart(t *testing.T) {
	r := &recorder{}
	q := newIntQueue(Config{MaxRows: 2, MaxWait: time.Hour, QueueRows: 10}, r, &memDead{}, newStats())
	_ = q.Enqueue(t.Context(), []int{1, 2, 3})
	if err := q.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.total(); n != 3 {
		t.Errorf("drained %d", n)
	}
	if err := q.Close(t.Context()); err != nil {
		t.Errorf("second close: %v", err)
	}
}

type gate struct{}

func (gate) Resolve(_ context.Context, s string) (string, bool) {
	switch s {
	case "paused":
		return "", false
	case "overflow":
		return "default", true
	default:
		return s, true
	}
}

type evWriter struct {
	mu sync.Mutex
	ev []model.Event
	sp []model.Span
	mp []model.MetricPoint
}

func (w *evWriter) WriteEvents(_ context.Context, ev []model.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ev = append(w.ev, ev...)
	return nil
}

func (w *evWriter) WriteSpans(_ context.Context, sp []model.Span) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sp = append(w.sp, sp...)
	return nil
}

func (w *evWriter) WriteMetrics(_ context.Context, mp []model.MetricPoint) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.mp = append(w.mp, mp...)
	return nil
}

func TestPipelineWithGate(t *testing.T) {
	w, s := &evWriter{}, newStats()
	p := New(Config{MaxRows: 100, MaxWait: time.Millisecond, QueueRows: 100},
		Deps{Events: w, Spans: w, Metrics: w, Gate: gate{}, Dead: &memDead{}, Stats: s, Logger: quietLogger(), Now: time.Now})
	p.Start()
	err := p.EnqueueEvents(t.Context(), []model.Event{{Stream: "a"}, {Stream: "paused"}, {Stream: "overflow"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = p.EnqueueEvents(t.Context(), []model.Event{{Stream: "paused"}})
	_ = p.EnqueueSpans(t.Context(), []model.Span{{Stream: "a"}})
	_ = p.EnqueueMetrics(t.Context(), []model.MetricPoint{{Stream: "paused"}, {Stream: "b"}})
	if d := p.Depths(); len(d) != 3 {
		t.Errorf("depths %v", d)
	}
	if err := p.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(w.ev) != 2 || w.ev[1].Stream != "default" || len(w.sp) != 1 || len(w.mp) != 1 {
		t.Errorf("written %+v %d %d", w.ev, len(w.sp), len(w.mp))
	}
	if s.dropped[DropStreamPaused] != 3 {
		t.Errorf("paused drops %v", s.dropped)
	}
}

func TestBackoff(t *testing.T) {
	tests := map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 5: 1600 * time.Millisecond,
		9: 25600 * time.Millisecond, 10: 30 * time.Second, 50: 30 * time.Second}
	for attempt, want := range tests {
		if got := Backoff(attempt, DefaultRetryBase, DefaultRetryMax); got != want {
			t.Errorf("Backoff(%d) = %v want %v", attempt, got, want)
		}
	}
	if err := sleepCtx(t.Context(), time.Millisecond); err != nil {
		t.Error(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Error("cancelled sleep returned nil")
	}
}

func TestFileDeadLetter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deadletter")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	d := NewFileDeadLetter(dir, func() time.Time { return now })
	if err := d.Write("events", []any{model.Event{Message: "a"}, model.Event{Message: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Write("events", []any{model.Event{Message: "c"}}); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "events-20261013T120000.000000000Z-*.ndjson"))
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	f, _ := os.Open(files[0])
	defer f.Close()
	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		var ev model.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		lines++
	}
	if lines != 2 {
		t.Errorf("lines %d", lines)
	}
	if err := d.Write("events", []any{func() {}}); err == nil {
		t.Error("unencodable row accepted")
	}
	bad := NewFileDeadLetter(filepath.Join(files[0], "sub"), time.Now)
	if err := bad.Write("events", nil); err == nil {
		t.Error("dir under a file accepted")
	}
}
