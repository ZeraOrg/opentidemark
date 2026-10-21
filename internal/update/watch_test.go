// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// watchHarness drives a Watcher with a fake clock: every After returns at once and advances the
// clock; onTick runs at each tick, and the context ends after maxTicks.
type watchHarness struct {
	t        *testing.T
	paths    HelperPaths
	now      time.Time
	ticks    int
	maxTicks int
	cancel   context.CancelFunc
	onTick   func(tick int)
	ready    func(tick int) bool
	fatal    []error
}

func newWatchHarness(t *testing.T) *watchHarness {
	return &watchHarness{t: t, paths: HelperPaths{Staging: t.TempDir(), Work: t.TempDir()},
		now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), maxTicks: 50, ready: func(int) bool { return true }}
}

func (h *watchHarness) after(d time.Duration) <-chan time.Time {
	h.ticks++
	h.now = h.now.Add(d)
	if h.onTick != nil {
		h.onTick(h.ticks)
	}
	if h.ticks >= h.maxTicks {
		h.cancel()
	}
	c := make(chan time.Time, 1)
	c <- h.now
	return c
}

func (h *watchHarness) run(version string, restart bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.cancel = cancel
	NewWatcher(WatcherConfig{Paths: h.paths, Version: version, Restart: restart,
		Ready: func(context.Context) bool { return h.ready(h.ticks) }, Fatal: func(err error) { h.fatal = append(h.fatal, err) },
		Now: func() time.Time { return h.now }, After: h.after, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).Run(ctx)
}

func (h *watchHarness) writeState(st InstallState) {
	h.t.Helper()
	data, _ := json.Marshal(st)
	if err := writeFileAtomic(h.paths.Work, StateFile, data, publicFileMode); err != nil {
		h.t.Fatal(err)
	}
}

func (h *watchHarness) confirmation() (Confirmation, bool) {
	data, err := os.ReadFile(filepath.Join(h.paths.Staging, ConfirmFile))
	if err != nil {
		return Confirmation{}, false
	}
	var c Confirmation
	if err := json.Unmarshal(data, &c); err != nil {
		h.t.Fatal(err)
	}
	return c, true
}

var installedState = InstallState{ID: "req-1", Phase: PhaseInstalled, From: "1.0.0", To: "2.0.0"}

func TestWatcherConfirmsAfterReadyPeriod(t *testing.T) {
	h := newWatchHarness(t)
	h.writeState(installedState)
	// Not ready at the third tick: the ready period starts again.
	h.ready = func(tick int) bool { return tick != 3 }
	h.run("2.0.0", false)
	c, ok := h.confirmation()
	if !ok || c != (Confirmation{ID: "req-1", Version: "2.0.0"}) {
		t.Fatalf("confirmation %+v, %v", c, ok)
	}
	// Ready from tick 4 on; 30 s later at 5 s per tick is tick 10.
	if h.ticks != 4+int(ConfirmReadyAfter/watchInterval) {
		t.Errorf("confirmed at tick %d", h.ticks)
	}
	if len(h.fatal) != 0 {
		t.Errorf("fatal %v", h.fatal)
	}
}

func TestWatcherNoConfirmation(t *testing.T) {
	tests := []struct {
		name    string
		state   *InstallState
		version string
		ready   bool
	}{
		{"never ready", &installedState, "2.0.0", false},
		{"other version", &installedState, "1.0.0", true},
		{"rolled back", &InstallState{ID: "req-1", Phase: PhaseRolledBack, From: "1.0.0", To: "2.0.0"}, "2.0.0", true},
		{"no state", nil, "2.0.0", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newWatchHarness(t)
			if tc.state != nil {
				h.writeState(*tc.state)
			}
			h.ready = func(int) bool { return tc.ready }
			h.run(tc.version, true)
			if _, ok := h.confirmation(); ok {
				t.Error("confirmed")
			}
			if len(h.fatal) != 0 {
				t.Errorf("fatal %v", h.fatal)
			}
		})
	}
}

func TestWatcherAlreadyConfirmedReturns(t *testing.T) {
	h := newWatchHarness(t)
	h.writeState(installedState)
	data, _ := json.Marshal(Confirmation{ID: "req-1", Version: "2.0.0"})
	if err := os.WriteFile(filepath.Join(h.paths.Staging, ConfirmFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.run("2.0.0", false)
	if h.ticks != 0 {
		t.Errorf("watched %d ticks", h.ticks)
	}
}

func TestWatcherRestart(t *testing.T) {
	tests := []struct {
		name    string
		initial *InstallState
		later   InstallState
		version string
		restart bool
		want    bool
	}{
		{"installed other version", nil, installedState, "1.0.0", true, true},
		{"own version rolled back", &installedState,
			InstallState{ID: "req-1", Phase: PhaseRolledBack, From: "1.0.0", To: "2.0.0"}, "2.0.0", true, true},
		{"failed install", nil, InstallState{ID: "req-1", Phase: PhaseFailed, From: "1.0.0", To: "2.0.0"}, "1.0.0", true, false},
		{"another version rolled back", nil, InstallState{ID: "req-1", Phase: PhaseRolledBack, From: "1.0.0", To: "2.0.0"},
			"1.0.0", true, false},
		{"unchanged old state", &InstallState{ID: "req-0", Phase: PhaseInstalled, From: "0.9.0", To: "1.0.0"},
			InstallState{ID: "req-0", Phase: PhaseInstalled, From: "0.9.0", To: "1.0.0"}, "1.1.0", true, false},
		{"restart off", nil, installedState, "1.0.0", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newWatchHarness(t)
			if tc.initial != nil {
				h.writeState(*tc.initial)
			}
			h.ready = func(int) bool { return false }
			h.onTick = func(tick int) {
				if tick == 2 {
					h.writeState(tc.later)
				}
			}
			h.run(tc.version, tc.restart)
			got := len(h.fatal) == 1 && errors.Is(h.fatal[0], ErrRestartForUpdate)
			if got != tc.want || len(h.fatal) > 1 {
				t.Errorf("fatal %v, want restart %v", h.fatal, tc.want)
			}
		})
	}
}

func TestWatcherReports(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name    string
		state   InstallState
		version string
		want    []string
	}{
		{"confirmed install", installedState, "2.0.0", []string{"installed 2.0.0 from 1.0.0"}},
		{"rollback after restart", InstallState{ID: "req-1", Phase: PhaseRolledBack, From: "1.0.0", To: "2.0.0", Message: "timeout"},
			"1.0.0", []string{"rolled_back 2.0.0 from 1.0.0: timeout"}},
		{"failed install", InstallState{ID: "req-1", Phase: PhaseFailed, From: "1.0.0", To: "2.0.0", Message: "not newer"},
			"1.0.0", []string{"failed 2.0.0 at install: not newer"}},
		{"another version's install", installedState, "1.0.0", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newWatchHarness(t)
			h.writeState(tc.state)
			events := &fakeEvents{}
			reports := NewReporter(h.paths.Staging, events, logger)
			for range 2 { // a second start reports nothing new
				ctx, cancel := context.WithCancel(context.Background())
				h.cancel, h.ticks = cancel, 0
				NewWatcher(WatcherConfig{Paths: h.paths, Version: tc.version, Ready: func(context.Context) bool { return true },
					Fatal: func(error) {}, Reports: reports, Now: func() time.Time { return h.now }, After: h.after,
					Logger: logger}).Run(ctx)
				cancel()
			}
			if strings.Join(events.got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("events %v, want %v", events.got, tc.want)
			}
		})
	}
}
