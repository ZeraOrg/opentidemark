// SPDX-License-Identifier: Apache-2.0

//go:build integration

package pipeline

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/tidemark/internal/testutil"
	"github.com/ZeraOrg/tidemark/internal/wal"
)

// crashingWriter stores the batch in ClickHouse and then hangs until the queue is aborted, like
// a node that dies after the insert reached ClickHouse but before the checkpoint advanced.
type crashingWriter struct {
	inner    *ch.Writer
	inserted atomic.Int32
}

func (w *crashingWriter) WriteEvents(ctx context.Context, ev []model.Event) error {
	if err := w.inner.WriteEvents(ctx, ev); err != nil {
		return err
	}
	w.inserted.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

func walQueueFor(t *testing.T, dir string, write func(context.Context, []model.Event) error) (*wal.Log, *WALQueue[model.Event]) {
	t.Helper()
	l, _, err := wal.Open(dir, wal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	q := NewWALQueue(SignalEvents, Config{MaxRows: 1000, MaxWait: 10 * time.Millisecond, WriteTimeout: time.Minute},
		WALQueueDeps[model.Event]{
			QueueDeps: QueueDeps[model.Event]{Write: write, Stream: func(e *model.Event) *string { return &e.Stream },
				Dead: &walDead{}, Metrics: newStats(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
			Journal: l, Codec: EventCodec(), IsPermanent: ch.IsPermanent,
		})
	return l, q
}

// TestIntegrationWALReplayDoesNotDuplicate proves SPEC §8.5 item 7 / §9.8 end to end with a real
// ClickHouse: rows acknowledged through the WAL, inserted, then "crashed" before the checkpoint
// advanced are replayed on restart with the same token and stored exactly once.
func TestIntegrationWALReplayDoesNotDuplicate(t *testing.T) {
	env, ok := testutil.ClickHouseFromEnv()
	if !ok {
		t.Skip("TIDEMARK_TEST_CLICKHOUSE_ADDR not set")
	}
	ctx := context.Background()
	conn, err := ch.Open(ctx, ch.Options{Addrs: env.Addrs, Username: env.WriterUser, Password: env.WriterPassword})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+env.Database+" SYNC"); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.NewMigrator(conn, env.Database).Up(ctx); err != nil {
		t.Fatal(err)
	}
	writer, err := ch.NewWriter(conn, env.Database)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	crash := &crashingWriter{inner: writer}
	_, q := walQueueFor(t, dir, crash.WriteEvents)
	q.Start()
	if err := q.Enqueue(ctx, events(250, "wal-replay")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return crash.inserted.Load() == 1 })
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_ = q.Close(cctx) // the "crash": the checkpoint still names the batch as pending
	cancel()

	count := func() uint64 {
		var n uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM "+env.Database+".events WHERE message = 'wal-replay'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 250 {
		t.Fatalf("rows before replay: %d", n)
	}
	l2, q2 := walQueueFor(t, dir, writer.WriteEvents)
	if cp, _ := l2.Checkpoint(); cp.Pending == nil {
		t.Fatal("the interrupted batch was not recorded as pending")
	}
	q2.Start()
	waitFor(t, func() bool { cp, _ := l2.Checkpoint(); return cp.Pending == nil })
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	if err := q2.Close(dctx); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 250 {
		t.Fatalf("replay duplicated rows: %d, want 250", n)
	}
}
