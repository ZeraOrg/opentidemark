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
