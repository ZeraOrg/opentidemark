// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/pipeline"
	"github.com/ZeraOrg/opentidemark/internal/selfmetrics"
)

type fakeJournal struct {
	failed chan struct{}
	err    error
}

func (f *fakeJournal) Failed() <-chan struct{} { return f.failed }
func (f *fakeJournal) Err() error              { return f.err }

func TestWatchJournal(t *testing.T) {
	errFsync := errors.New("write-ahead log failed: fsync wal: input/output error")
	tests := []struct {
		name      string
		exit      bool
		wantFatal bool
	}{
		{name: "exits by default", exit: true, wantFatal: true},
		{name: "stays up when disabled", exit: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{cfg: config.Config{WAL: config.WAL{ExitOnFailure: tc.exit}},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)), errs: make(chan error, 1)}
			j := &fakeJournal{failed: make(chan struct{}), err: errFsync}
			done := make(chan struct{})
			go func() {
				defer close(done)
				a.watchJournal(context.Background(), j)
			}()
			close(j.failed)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("watcher did not return after the failure")
			}
			select {
			case err := <-a.errs:
				if !tc.wantFatal || !errors.Is(err, errFsync) {
					t.Fatalf("fatal error = %v, want fatal %v", err, tc.wantFatal)
				}
			default:
				if tc.wantFatal {
					t.Fatal("no fatal error reported")
				}
			}
		})
	}
}

func TestWatchJournalStopsWithContext(t *testing.T) {
	a := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), errs: make(chan error, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.watchJournal(ctx, &fakeJournal{failed: make(chan struct{})})
	if len(a.errs) != 0 {
		t.Fatal("cancelled watcher reported an error")
	}
}

// Regression: the WAL logs stay watched after the pipeline takes them over; before, the
// hand-off emptied the only list the failure watcher and /healthz read.
func TestWALLogsStayWatchedAfterHandOff(t *testing.T) {
	cfg := config.Config{WAL: config.WAL{Enabled: true, Dir: t.TempDir(), MaxBytes: 1 << 30, Sync: "batch"}}
	a := &App{cfg: cfg, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: selfmetrics.New()}
	journals, err := a.openWAL()
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}
	t.Cleanup(func() {
		for _, l := range []pipeline.Journal{journals.Events, journals.Spans, journals.Metrics} {
			_ = l.Close() // test cleanup
		}
	})
	a.handOffJournals()
	if len(a.journals) != 0 {
		t.Fatalf("app still owns %d logs after the hand-off", len(a.journals))
	}
	if len(a.walLogs) != 3 {
		t.Fatalf("watching %d logs, want 3 (events, spans, metrics)", len(a.walLogs))
	}
	if err := a.walFailure(); err != nil {
		t.Fatalf("healthy logs reported as failed: %v", err)
	}
}
