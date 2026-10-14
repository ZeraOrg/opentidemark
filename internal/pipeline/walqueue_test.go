// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/ingest/native"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/testutil"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// dedupStore imitates ClickHouse: inserts are idempotent per deduplication token, and the
// first failN calls store the rows but report an error (an insert whose outcome is unknown,
// e.g. the node crashed before the acknowledgement arrived).
type dedupStore struct {
	mu        sync.Mutex
	byToken   map[string]int
	rows      []model.Event
	failN     int
	calls     int
	tokens    []string
	permanent bool
}

var errUnknownOutcome = errors.New("connection reset after send")
var errPermanent = errors.New("type mismatch")

func (s *dedupStore) WriteEvents(ctx context.Context, ev []model.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	tok := store.DedupToken(ctx)
	s.tokens = append(s.tokens, tok)
	if s.permanent {
		return errPermanent
	}
	if _, dup := s.byToken[tok]; !dup || tok == "" {
		s.byToken[tok] = len(ev)
		s.rows = append(s.rows, ev...)
	}
	if s.calls <= s.failN {
		return errUnknownOutcome
	}
	return nil
}

func (s *dedupStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

type walDead struct {
	mu   sync.Mutex
	rows int
}

func (d *walDead) Write(_ string, rows []any) error {
	d.mu.Lock()
	d.rows += len(rows)
	d.mu.Unlock()
	return nil
}

type walEnv struct {
	dir   string
	log   *wal.Log
	q     *WALQueue[model.Event]
	store *dedupStore
	stats *stats
	dead  *walDead
}

func newWALEnv(t *testing.T, dir string, st *dedupStore, budget *wal.Budget) *walEnv {
	t.Helper()
	l, _, err := wal.Open(dir, wal.Options{SegmentBytes: 1024, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	e := &walEnv{dir: dir, log: l, store: st, stats: newStats(), dead: &walDead{}}
	e.q = NewWALQueue(SignalEvents, Config{MaxRows: 4, MaxWait: 5 * time.Millisecond, RetryBase: time.Millisecond, RetryMax: 2 * time.Millisecond},
		WALQueueDeps[model.Event]{
			QueueDeps: QueueDeps[model.Event]{Write: st.WriteEvents, Stream: func(e *model.Event) *string { return &e.Stream },
				Dead: e.dead, Metrics: e.stats, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
			Journal: l, Codec: EventCodec(), IsPermanent: func(err error) bool { return errors.Is(err, errPermanent) },
		})
	return e
}

func (e *walEnv) close(t *testing.T, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return e.q.Close(ctx)
}

func events(n int, msg string) []model.Event {
	out := make([]model.Event, n)
	for i := range out {
		out[i] = model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: time.Unix(1700000000, int64(i)).UTC(), Stream: "app",
			Message: msg, Properties: map[string]any{"i": int64(i), "nan": math.NaN()}}
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestWALQueueFlushesAndCheckpoints(t *testing.T) {
	st := &dedupStore{byToken: map[string]int{}}
	e := newWALEnv(t, t.TempDir(), st, nil)
	e.q.Start()
	for range 5 {
		if err := e.q.Enqueue(context.Background(), events(3, "hello")); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return st.count() == 15 })
	if err := e.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, tok := range st.tokens {
		if tok == "" {
			t.Fatal("insert without deduplication token")
		}
	}
	reopened, _, _ := wal.Open(e.dir, wal.Options{})
	defer reopened.Close()
	cp, _ := reopened.Checkpoint()
	if cp.Pending != nil || cp.Committed != reopened.Durable() {
		t.Fatalf("checkpoint %+v, durable %+v", cp, reopened.Durable())
	}
	if reopened.Segments() != 1 {
		t.Errorf("committed segments not deleted: %d", reopened.Segments())
	}
	if e.q.Enqueue(context.Background(), events(1, "late")) == nil {
		t.Error("enqueue after close accepted")
	}
}

// TestWALReplayIsIdempotent: the insert succeeded but its acknowledgement was lost and the
// process stopped; after a restart the same range is re-sent with the same token and stored once.
func TestWALReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	st := &dedupStore{byToken: map[string]int{}, failN: 1 << 30}
	e := newWALEnv(t, dir, st, nil)
	e.q.Start()
	if err := e.q.Enqueue(context.Background(), events(3, "once")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return st.count() == 3 })
	_ = e.close(t, 20*time.Millisecond) // the "crash": the batch never committed
	firstToken := st.tokens[0]

	st.failN = 0
	e2 := newWALEnv(t, dir, st, nil)
	cp, _ := e2.log.Checkpoint()
	if cp.Pending == nil || cp.Pending.Token != firstToken {
		t.Fatalf("pending batch not persisted: %+v", cp)
	}
	e2.q.Start()
	waitFor(t, func() bool { cp, _ := e2.log.Checkpoint(); return cp.Pending == nil })
	if err := e2.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if st.count() != 3 {
		t.Fatalf("replay duplicated rows: %d", st.count())
	}
	if last := st.tokens[len(st.tokens)-1]; last != firstToken {
		t.Errorf("replay used token %s, want %s", last, firstToken)
	}
}

// TestWALReplayStopsAtPendingEnd is a regression test: rows appended after an interrupted
// batch must not be re-sent under that batch's token (ClickHouse would drop them as duplicates).
func TestWALReplayStopsAtPendingEnd(t *testing.T) {
	dir := t.TempDir()
	st := &dedupStore{byToken: map[string]int{}, failN: 1 << 30}
	e := newWALEnv(t, dir, st, nil)
	e.q.Start()
	if err := e.q.Enqueue(context.Background(), events(3, "first")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return st.count() == 3 })
	_ = e.close(t, 20*time.Millisecond)

	st.mu.Lock()
	st.failN = 0
	st.mu.Unlock()
	e2 := newWALEnv(t, dir, st, nil)
	if err := e2.q.Enqueue(context.Background(), events(2, "after restart")); err != nil {
		t.Fatal(err)
	}
	e2.q.Start()
	waitFor(t, func() bool { return st.count() == 5 })
	if err := e2.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if st.count() != 5 {
		t.Fatalf("rows stored = %d, want 5", st.count())
	}
}

// TestWALCrashBeforeFlush: acknowledged rows survive a crash before any insert and a torn
// record at the end (kill mid-write) is dropped without losing earlier rows.
func TestWALCrashBeforeFlush(t *testing.T) {
	dir := t.TempDir()
	st := &dedupStore{byToken: map[string]int{}}
	e := newWALEnv(t, dir, st, nil) // never started: nothing is flushed
	for range 3 {
		if err := e.q.Enqueue(context.Background(), events(2, "acked")); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.log.Close()
	segs, _ := filepath.Glob(filepath.Join(dir, "*.wal"))
	f, err := os.OpenFile(segs[len(segs)-1], os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte{40, 0, 0, 0, 1, 2, 3, 4, 'p', 'a', 'r', 't'}) // torn record
	_ = f.Close()

	e2 := newWALEnv(t, dir, st, nil)
	e2.q.Start()
	waitFor(t, func() bool { return st.count() == 6 })
	if err := e2.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if st.count() != 6 {
		t.Fatalf("rows after recovery: %d", st.count())
	}
}

func TestWALPermanentFailureDeadLetters(t *testing.T) {
	st := &dedupStore{byToken: map[string]int{}, permanent: true}
	e := newWALEnv(t, t.TempDir(), st, nil)
	e.q.Start()
	if err := e.q.Enqueue(context.Background(), events(3, "bad")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { e.dead.mu.Lock(); defer e.dead.mu.Unlock(); return e.dead.rows == 3 })
	if err := e.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if e.q.Depth() != 0 {
		t.Errorf("depth %d after dead-letter", e.q.Depth())
	}
}

func TestWALTransientFailureRetriesWithoutLoss(t *testing.T) {
	st := &dedupStore{byToken: map[string]int{}, failN: 3}
	e := newWALEnv(t, t.TempDir(), st, nil)
	e.q.Start()
	if err := e.q.Enqueue(context.Background(), events(2, "retry")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { _, _, _, failed := e.stats.get(); return failed >= 3 && e.q.Depth() == 0 })
	if err := e.close(t, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if st.count() != 2 || e.dead.rows != 0 {
		t.Fatalf("rows %d dead %d", st.count(), e.dead.rows)
	}
}

// TestWALCapReturns503 drives the native HTTP endpoint against a WAL whose size cap is
// exhausted while ClickHouse is down: ingestion answers 503 with Retry-After.
func TestWALCapReturns503(t *testing.T) {
	st := &dedupStore{byToken: map[string]int{}, failN: 1 << 30}
	e := newWALEnv(t, t.TempDir(), st, wal.NewBudget(2048))
	e.q.Start()
	defer func() { _ = e.close(t, 50*time.Millisecond) }()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, &testutil.Sink{}, testutil.NewObserver(), nil)
	d.Events = &Pipeline{events: e.q}
	mux := http.NewServeMux()
	native.New(d).Register(mux)
	body := []byte(`{"message":"` + string(bytes.Repeat([]byte("x"), 300)) + `"}`)
	var last *httptest.ResponseRecorder
	accepted := 0
	for range 50 {
		r := httptest.NewRequest(http.MethodPost, native.Path, bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testutil.TestKey)
		last = httptest.NewRecorder()
		mux.ServeHTTP(last, r)
		if last.Code == http.StatusServiceUnavailable {
			break
		}
		accepted++
	}
	if last.Code != http.StatusServiceUnavailable || last.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 503 once the WAL is full, got %d after %d accepted", last.Code, accepted)
	}
	if accepted == 0 {
		t.Fatal("nothing accepted before the cap")
	}
	if !errors.Is(e.q.Enqueue(context.Background(), events(1, "x")), ingest.ErrBackpressure) {
		t.Error("Enqueue on a full WAL must return ErrBackpressure")
	}
}

// abandonedWait is a journal whose caller stops waiting for the fsync: the record is written
// (and made durable by the syncer) but Append reports the caller's context error.
type abandonedWait struct{ *wal.Log }

func (j abandonedWait) Append(ctx context.Context, payload []byte) (wal.Pos, error) {
	pos, err := j.Log.Append(context.WithoutCancel(ctx), payload)
	if err != nil {
		return pos, err
	}
	return pos, ctx.Err()
}

// TestWALCancelledWaitIsNotBackpressure: a client that gives up while its rows wait for the
// fsync loses nothing — the rows are stored — so they are counted as enqueued, never as dropped
// for backpressure, and the error is the context error (SPEC §9.8).
func TestWALCancelledWaitIsNotBackpressure(t *testing.T) {
	st := &dedupStore{byToken: map[string]int{}}
	e := newWALEnv(t, t.TempDir(), st, nil)
	e.q.d.Journal = abandonedWait{e.log}
	e.q.Start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := e.q.Enqueue(ctx, events(3, "gone"))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ingest.ErrBackpressure) {
		t.Fatalf("err = %v, want the context error without backpressure", err)
	}
	waitFor(t, func() bool { return st.count() == 3 })
	if err := e.close(t, time.Second); err != nil {
		t.Fatal(err)
	}
	enq, written, _, _ := e.stats.get()
	if enq != 3 || written != 3 || e.stats.dropped[DropBackpressure] != 0 || e.stats.lastDepth != 0 {
		t.Errorf("enqueued %d written %d dropped %v depth %d", enq, written, e.stats.dropped, e.stats.lastDepth)
	}
}

func TestCodecsRoundTrip(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	ev := events(2, "codec")
	p, err := EventCodec().Encode(id, ev)
	if err != nil {
		t.Fatal(err)
	}
	gotID, got, err := EventCodec().Decode(p)
	if err != nil || gotID != id || len(got) != 2 || got[1].Message != "codec" || got[1].ID != ev[1].ID ||
		got[0].Properties["nan"] != "NaN" || !got[0].Timestamp.Equal(ev[0].Timestamp) {
		t.Fatalf("event round trip: %+v %v", got, err)
	}
	mp := []model.MetricPoint{{Name: "m", Value: math.Inf(1), Sum: math.NaN(), BucketBounds: []float64{1, math.Inf(1)}, Count: 3}}
	p, err = MetricCodec().Encode(id, mp)
	if err != nil {
		t.Fatal(err)
	}
	_, gm, err := MetricCodec().Decode(p)
	if err != nil || !math.IsInf(gm[0].Value, 1) || !math.IsNaN(gm[0].Sum) || gm[0].Count != 3 || !math.IsInf(gm[0].BucketBounds[1], 1) {
		t.Fatalf("metric round trip: %+v %v", gm, err)
	}
	sp := []model.Span{{Name: "s", Duration: time.Second, Attributes: map[string]any{"x": math.NaN()},
		Events: []model.SpanEvent{{Name: "e", Attributes: map[string]any{"y": 1.5}}}}}
	p, _ = SpanCodec().Encode(id, sp)
	if _, gs, err := SpanCodec().Decode(p); err != nil || gs[0].Duration != time.Second || gs[0].Events[0].Name != "e" {
		t.Fatalf("span round trip: %+v %v", gs, err)
	}
	if _, _, err := EventCodec().Decode([]byte{9}); !errors.Is(err, ErrPayload) {
		t.Errorf("bad payload: %v", err)
	}
	first, again := batchToken([]uuid.UUID{id}), batchToken([]uuid.UUID{id})
	if first != again || first == batchToken(nil) {
		t.Error("batch tokens must be deterministic and content dependent")
	}
}
