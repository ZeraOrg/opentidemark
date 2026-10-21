// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const helperTimeout = 20 * time.Millisecond

// confirmIn writes confirmed.json for id and version into the staging directory, as the new
// version's watcher does.
func (f *installFixture) confirm(t *testing.T, id, version string) {
	t.Helper()
	data, _ := json.Marshal(Confirmation{ID: id, Version: version})
	if err := os.WriteFile(filepath.Join(f.staging, ConfirmFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *installFixture) runHelper(t *testing.T, current string, restart func(context.Context) error) error {
	t.Helper()
	return RunHelper(context.Background(), HelperDeps{Installer: f.installer(t, current), Restart: restart,
		Timeout: helperTimeout, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestRunHelperConfirmed(t *testing.T) {
	f := newInstallFixture(t)
	f.stage(t, "req-1", "2.0.0", "new binary")
	restarts := 0
	restart := func(context.Context) error {
		restarts++
		f.confirm(t, "req-1", "2.0.0") // the restarted service confirms
		return nil
	}
	if err := f.runHelper(t, "1.0.0", restart); err != nil {
		t.Fatal(err)
	}
	assertFile(t, f.bin, "new binary")
	if restarts != 1 {
		t.Errorf("%d restarts", restarts)
	}
	// Triggered again for the same request: nothing is installed or restarted.
	if err := f.runHelper(t, "2.0.0", restart); err != nil || restarts != 1 {
		t.Errorf("repeat: %v, %d restarts", err, restarts)
	}
}

func TestRunHelperRollsBack(t *testing.T) {
	restartErr := errors.New("service did not start")
	tests := []struct {
		name         string
		restart      func(calls *int) func(context.Context) error
		wantRestarts int
		wantErr      error
	}{
		{"linux: no confirmation", nil, 0, ErrNotConfirmed},
		{"windows: no confirmation", func(calls *int) func(context.Context) error {
			return func(context.Context) error { *calls++; return nil }
		}, 2, ErrNotConfirmed},
		{"windows: restart fails", func(calls *int) func(context.Context) error {
			return func(context.Context) error {
				*calls++
				if *calls == 1 {
					return restartErr
				}
				return nil
			}
		}, 2, restartErr},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstallFixture(t)
			f.stage(t, "req-1", "2.0.0", "new binary")
			calls := 0
			var restart func(context.Context) error
			if tc.restart != nil {
				restart = tc.restart(&calls)
			}
			err := f.runHelper(t, "1.0.0", restart)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
			if calls != tc.wantRestarts {
				t.Errorf("%d restarts, want %d", calls, tc.wantRestarts)
			}
			assertFile(t, f.bin, oldBinary)
			st, ok, err := f.installer(t, "1.0.0").State()
			if err != nil || !ok || st.Phase != PhaseRolledBack || st.Message == "" {
				t.Errorf("state %+v, %v, %v", st, ok, err)
			}
		})
	}
}

func TestRunHelperResumesWaitWithoutRestart(t *testing.T) {
	f := newInstallFixture(t)
	f.stage(t, "req-1", "2.0.0", "new binary")
	if _, err := f.installer(t, "1.0.0").Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.confirm(t, "req-1", "2.0.0")
	restart := func(context.Context) error {
		t.Error("restarted while resuming")
		return nil
	}
	// The helper was stopped while waiting; started again it only waits.
	if err := f.runHelper(t, "2.0.0", restart); err != nil {
		t.Fatal(err)
	}
	assertFile(t, f.bin, "new binary")
}

func TestRunHelperNothingToDo(t *testing.T) {
	f := newInstallFixture(t)
	restart := func(context.Context) error {
		t.Error("restarted")
		return nil
	}
	if err := f.runHelper(t, "1.0.0", restart); err != nil {
		t.Errorf("nothing staged: %v", err)
	}
	f.stage(t, "req-1", "1.0.0", "same version")
	if err := f.runHelper(t, "1.0.0", restart); !errors.Is(err, ErrNotNewer) {
		t.Errorf("not newer: %v", err)
	}
	assertFile(t, f.bin, oldBinary)
	// The failed request is recorded and not tried again.
	if err := f.runHelper(t, "1.0.0", restart); err != nil {
		t.Errorf("failed request again: %v", err)
	}
}

// A request staged while the helper waits for a confirmation (its trigger lost) is handled
// before the helper exits, compared with the version just installed.
func TestRunHelperHandlesRequestStagedMeanwhile(t *testing.T) {
	f := newInstallFixture(t)
	f.stage(t, "req-1", "2.0.0", "binary 2")
	restarts := 0
	restart := func(context.Context) error {
		restarts++
		switch restarts {
		case 1:
			f.confirm(t, "req-1", "2.0.0")
			f.stage(t, "req-2", "2.1.0", "binary 2.1") // replaced during the wait
		case 2:
			f.confirm(t, "req-2", "2.1.0")
		}
		return nil
	}
	if err := f.runHelper(t, "1.0.0", restart); err != nil {
		t.Fatal(err)
	}
	if restarts != 2 {
		t.Errorf("%d restarts", restarts)
	}
	assertFile(t, f.bin, "binary 2.1")
	assertFile(t, f.bin+PreviousSuffix, "binary 2")
	st, _, _ := f.installer(t, "2.1.0").State()
	if st.ID != "req-2" || st.From != "2.0.0" || st.Phase != PhaseInstalled {
		t.Errorf("state %+v", st)
	}
}

func TestRunHelperRoundsBounded(t *testing.T) {
	f := newInstallFixture(t)
	versions := []string{"2.0.0", "2.1.0", "2.2.0", "2.3.0"}
	f.stage(t, "req-0", versions[0], "binary 0")
	restarts := 0
	restart := func(context.Context) error {
		f.confirm(t, fmt.Sprintf("req-%d", restarts), versions[restarts])
		restarts++
		f.stage(t, fmt.Sprintf("req-%d", restarts), versions[restarts], fmt.Sprintf("binary %d", restarts))
		return nil
	}
	if err := f.runHelper(t, "1.0.0", restart); err != nil {
		t.Fatal(err)
	}
	if restarts != maxHelperRounds {
		t.Errorf("%d rounds, want %d", restarts, maxHelperRounds)
	}
	if !f.installer(t, "2.2.0").Pending() {
		t.Error("the fourth request should be left pending")
	}
}
