// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// fakeEvents records audit events as "action to [from] [stage]".
type fakeEvents struct{ got []string }

func (f *fakeEvents) Installed(_ context.Context, from, to string) {
	f.got = append(f.got, fmt.Sprintf("installed %s from %s", to, from))
}

func (f *fakeEvents) RolledBack(_ context.Context, from, to, reason string) {
	f.got = append(f.got, fmt.Sprintf("rolled_back %s from %s: %s", to, from, reason))
}

func (f *fakeEvents) Failed(_ context.Context, to, stage, reason string) {
	f.got = append(f.got, fmt.Sprintf("failed %s at %s: %s", to, stage, reason))
}

// fakeStager reports a download of size bytes, then fails with err.
type fakeStager struct {
	size int64
	err  error
	reqs []Request
}

func (f *fakeStager) Stage(_ context.Context, req Request, hooks StageHooks) error {
	f.reqs = append(f.reqs, req)
	if err := hooks.Reserve(f.size); err != nil {
		return err
	}
	hooks.Progress(f.size/2, f.size)
	hooks.Progress(f.size, f.size)
	return f.err
}

type applyHarness struct {
	t       *testing.T
	paths   HelperPaths
	stager  *fakeStager
	events  *fakeEvents
	free    uint64
	trigger func(h *applyHarness) error
	now     time.Time
	applier *Applier
}

func newApplyHarness(t *testing.T) *applyHarness {
	h := &applyHarness{t: t, paths: HelperPaths{Staging: t.TempDir(), Work: t.TempDir()}, stager: &fakeStager{size: 1000},
		events: &fakeEvents{}, free: 1 << 30, now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h.applier = NewApplier(ApplierDeps{Stager: h.stager, Paths: h.paths, Current: "1.0.0",
		Trigger:   func() error { return h.trigger(h) },
		FreeSpace: func(string) (uint64, error) { return h.free, nil },
		Reports:   NewReporter(h.paths.Staging, h.events, logger), Events: h.events,
		NewID: func() string { return "req-1" }, Now: func() time.Time { return h.now },
		After: func(d time.Duration) <-chan time.Time {
			h.now = h.now.Add(d)
			c := make(chan time.Time, 1)
			c <- h.now
			return c
		}, Logger: logger})
	h.trigger = func(*applyHarness) error { return nil }
	return h
}

func (h *applyHarness) helperWrites(phase Phase, msg string) {
	h.trigger = func(h *applyHarness) error {
		data, _ := json.Marshal(InstallState{ID: "req-1", Phase: phase, From: "1.0.0", To: "2.0.0", At: h.now, Message: msg})
		return writeFileAtomic(h.paths.Work, StateFile, data, publicFileMode)
	}
}

// run begins an update to 2.0.0 and processes it synchronously.
func (h *applyHarness) run() Status {
	h.t.Helper()
	if err := h.applier.Begin("2.0.0"); err != nil {
		h.t.Fatal(err)
	}
	h.applier.apply(context.Background(), <-h.applier.jobs)
	var s Status
	h.applier.decorate(&s)
	return s
}

func TestApplierInstalled(t *testing.T) {
	h := newApplyHarness(t)
	h.helperWrites(PhaseInstalled, "")
	s := h.run()
	if s.State != StateRestarting || s.Progress == nil || s.Progress.Bytes != 1000 {
		t.Errorf("status %+v", s)
	}
	if s.LastResult == nil || s.LastResult.Phase != PhaseInstalled {
		t.Errorf("last result %+v", s.LastResult)
	}
	if len(h.events.got) != 0 {
		t.Errorf("events %v (installed is reported by the new version)", h.events.got)
	}
	if err := h.applier.Begin("2.0.0"); !errors.Is(err, ErrApplyRefused) {
		t.Errorf("second apply while restarting: %v", err)
	}
}

func TestApplierFailures(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(h *applyHarness)
		wantEvent string
	}{
		{"download", func(h *applyHarness) { h.stager.err = fmt.Errorf("%w: status 404", ErrDownload) },
			"failed 2.0.0 at download: release download failed: status 404"},
		{"checksum", func(h *applyHarness) { h.stager.err = ErrChecksumMismatch }, "failed 2.0.0 at verify: checksum mismatch"},
		{"signature", func(h *applyHarness) { h.stager.err = fmt.Errorf("%w: key", ErrSignature) },
			"failed 2.0.0 at verify: release signature rejected: key"},
		{"disk space", func(h *applyHarness) { h.free = 2999 }, "failed 2.0.0 at download: not enough free disk space"},
		{"trigger", func(h *applyHarness) { h.trigger = func(*applyHarness) error { return errors.New("access denied") } },
			"failed 2.0.0 at install: start the update helper: access denied"},
		{"helper timeout", func(*applyHarness) {}, "failed 2.0.0 at install: the update helper did not pick up the request"},
		{"helper failed", func(h *applyHarness) { h.helperWrites(PhaseFailed, "staged version is not newer") },
			"failed 2.0.0 at install: staged version is not newer"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newApplyHarness(t)
			tc.setup(h)
			s := h.run()
			if s.State != StateIdle || s.Progress != nil {
				t.Errorf("status %+v", s)
			}
			if s.LastResult == nil || s.LastResult.Phase != PhaseFailed || s.LastResult.To != "2.0.0" {
				t.Errorf("last result %+v", s.LastResult)
			}
			if len(h.events.got) != 1 || !strings.HasPrefix(h.events.got[0], tc.wantEvent) {
				t.Errorf("events %v, want %q", h.events.got, tc.wantEvent)
			}
			// Idle again: the next attempt is accepted.
			if err := h.applier.Begin("2.0.0"); err != nil {
				t.Errorf("next apply: %v", err)
			}
		})
	}
}

func TestApplierStatesWhileBusy(t *testing.T) {
	h := newApplyHarness(t)
	var states []string
	h.trigger = func(h *applyHarness) error {
		var s Status
		h.applier.decorate(&s)
		states = append(states, s.State)
		return errors.New("stop here")
	}
	if err := h.applier.Begin("2.0.0"); err != nil {
		t.Fatal(err)
	}
	var s Status
	h.applier.decorate(&s)
	if s.State != StateDownloading || s.Progress == nil || s.Progress.Bytes != 0 {
		t.Errorf("after begin %+v", s)
	}
	if err := h.applier.Begin("2.0.0"); !errors.Is(err, ErrApplyRefused) || err.(*ApplyRefusedError).Reason != ReasonInProgress {
		t.Errorf("second begin: %v", err)
	}
	h.applier.apply(context.Background(), <-h.applier.jobs)
	if len(states) != 1 || states[0] != StateStaged {
		t.Errorf("states at trigger %v", states)
	}
}

func TestManagerApply(t *testing.T) {
	latest := &Release{Version: "2.0.0"}
	supported := Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64", Helper: true}
	tests := []struct {
		name    string
		install Install
		latest  *Release
		busy    string
		version string
		want    error
		reason  string
	}{
		{"accepted", supported, latest, "", "2.0.0", nil, ""},
		{"helper missing", Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64"}, latest, "", "2.0.0",
			ErrApplyRefused, ReasonHelperMissing},
		{"ha", Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64", Helper: true, HA: true}, latest, "", "2.0.0",
			ErrApplyRefused, ReasonHA},
		{"no latest", supported, nil, "", "2.0.0", ErrVersionMismatch, ""},
		{"other version", supported, latest, "", "1.9.0", ErrVersionMismatch, ""},
		{"draining", supported, latest, ReasonDraining, "2.0.0", ErrApplyRefused, ReasonDraining},
		{"wal failed", supported, latest, ReasonWALFailed, "2.0.0", ErrApplyRefused, ReasonWALFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newApplyHarness(t)
			checker := NewChecker(CheckerDeps{Build: Build{Version: "1.0.0", Distribution: DistributionArchive}, Install: tc.install,
				Now: func() time.Time { return h.now }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			checker.status.Latest, checker.status.Available = tc.latest, tc.latest != nil
			var applier *Applier
			if tc.install.Apply().Supported {
				applier = h.applier
			}
			m := NewManager(checker, applier, func() string { return tc.busy })
			s, err := m.Apply(tc.version)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var refused *ApplyRefusedError
			if errors.As(err, &refused) && refused.Reason != tc.reason {
				t.Errorf("reason %q, want %q", refused.Reason, tc.reason)
			}
			if tc.want == nil && (s.State != StateDownloading || len(h.applier.jobs) != 1) {
				t.Errorf("status %+v, %d queued", s, len(h.applier.jobs))
			}
		})
	}
}

func TestManagerStatusLastResultFromState(t *testing.T) {
	h := newApplyHarness(t)
	data, _ := json.Marshal(InstallState{ID: "req-0", Phase: PhaseRolledBack, From: "0.9.0", To: "1.0.0", At: h.now, Message: "not confirmed"})
	if err := writeFileAtomic(h.paths.Work, StateFile, data, publicFileMode); err != nil {
		t.Fatal(err)
	}
	checker := NewChecker(CheckerDeps{Build: Build{Version: "0.9.0", Distribution: DistributionArchive},
		Install: Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64", Helper: true}, Logger: slog.Default()})
	s := NewManager(checker, h.applier, func() string { return "" }).Status()
	want := Result{Phase: PhaseRolledBack, From: "0.9.0", To: "1.0.0", At: h.now, Message: "not confirmed"}
	if s.State != StateIdle || s.LastResult == nil || *s.LastResult != want || !s.Apply.Supported {
		t.Errorf("status %+v, last %+v", s, s.LastResult)
	}
	// Without an applier (apply not supported) the status is the checker's.
	if s := NewManager(checker, nil, nil).Status(); s.LastResult != nil {
		t.Errorf("last result without applier: %+v", s.LastResult)
	}
}

func TestReporterReportsOnce(t *testing.T) {
	events := &fakeEvents{}
	r := NewReporter(t.TempDir(), events, slog.New(slog.NewTextHandler(io.Discard, nil)))
	st := InstallState{ID: "req-1", Phase: PhaseRolledBack, From: "1.0.0", To: "2.0.0", Message: "not confirmed"}
	r.Report(context.Background(), st)
	r.Report(context.Background(), st)
	st2 := st
	st2.ID, st2.Phase = "req-2", PhaseInstalled
	r.Report(context.Background(), st2)
	want := []string{"rolled_back 2.0.0 from 1.0.0: not confirmed", "installed 2.0.0 from 1.0.0"}
	if strings.Join(events.got, "|") != strings.Join(want, "|") {
		t.Errorf("events %v, want %v", events.got, want)
	}
}

// While the helper waits for an installed update to confirm, a second apply is refused.
func TestApplierRefusesWhileHelperWaits(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		confirmed bool
		refused   bool
	}{
		{"waiting", time.Minute, false, true},
		{"confirmed", time.Minute, true, false},
		{"helper gone", ConfirmTimeout + time.Minute, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newApplyHarness(t)
			data, _ := json.Marshal(InstallState{ID: "req-0", Phase: PhaseInstalled, From: "0.9.0", To: "1.0.0", At: h.now.Add(-tc.age)})
			if err := writeFileAtomic(h.paths.Work, StateFile, data, publicFileMode); err != nil {
				t.Fatal(err)
			}
			if tc.confirmed {
				c, _ := json.Marshal(Confirmation{ID: "req-0", Version: "1.0.0"})
				if err := writeFileAtomic(h.paths.Staging, ConfirmFile, c, sharedFileMode); err != nil {
					t.Fatal(err)
				}
			}
			err := h.applier.Begin("2.0.0")
			var refused *ApplyRefusedError
			if got := errors.As(err, &refused) && refused.Reason == ReasonInProgress; got != tc.refused {
				t.Errorf("err = %v, want refused %v", err, tc.refused)
			}
		})
	}
}
