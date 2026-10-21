// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"time"
)

// ErrRestartForUpdate ends the server so that its service manager starts the binary the helper
// installed or restored (Linux: exit status 3, SPEC §17.7).
var ErrRestartForUpdate = errors.New("restarting for an update")

// watchInterval is how often the watcher reads state.json and checks readiness.
const watchInterval = 5 * time.Second

// WatcherConfig configures the service side of the helper protocol.
type WatcherConfig struct {
	Paths   HelperPaths
	Version string                     // the running version
	Restart bool                       // ask for a restart when the helper changes the binary (Linux)
	Ready   func(context.Context) bool // metadata and ClickHouse reachable, WAL not failed
	Fatal   func(error)                // receives ErrRestartForUpdate
	Reports *Reporter                  // optional: audits the helper's outcomes
	Now     func() time.Time
	After   func(time.Duration) <-chan time.Time
	Logger  *slog.Logger
}

// Watcher confirms an update to the running version once it has been ready for
// ConfirmReadyAfter, and (Linux) asks for a restart when the helper installs another version or
// rolls back the running one.
type Watcher struct{ cfg WatcherConfig }

// NewWatcher returns a watcher; it reads nothing until Run.
func NewWatcher(cfg WatcherConfig) *Watcher { return &Watcher{cfg: cfg} }

// Run watches until ctx ends, the confirmation is written (when no restart is watched for) or a
// restart was requested. Only changes of state.json after the start lead to a restart, so an old
// state never restarts a server in a loop.
func (w *Watcher) Run(ctx context.Context) {
	initial, _ := w.state()
	pending := initial.Phase == PhaseInstalled && initial.To == w.cfg.Version && !w.confirmed(initial.ID)
	if !pending {
		w.report(ctx, initial)
	}
	if !pending && !w.cfg.Restart {
		return
	}
	var readySince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.cfg.After(watchInterval):
		}
		if pending && w.tryConfirm(ctx, initial, &readySince) {
			pending = false
			w.report(ctx, initial)
			if !w.cfg.Restart {
				return
			}
		}
		if w.cfg.Restart && w.restartNeeded(initial) {
			w.cfg.Fatal(ErrRestartForUpdate)
			return
		}
	}
}

// tryConfirm writes confirmed.json once the server has been ready for ConfirmReadyAfter without
// interruption; it reports whether the confirmation was written.
func (w *Watcher) tryConfirm(ctx context.Context, st InstallState, readySince *time.Time) bool {
	now := w.cfg.Now()
	if !w.cfg.Ready(ctx) {
		*readySince = time.Time{}
		return false
	}
	if readySince.IsZero() {
		*readySince = now
	}
	if now.Sub(*readySince) < ConfirmReadyAfter {
		return false
	}
	data, err := json.Marshal(Confirmation{ID: st.ID, Version: st.To})
	if err == nil {
		err = writeFileAtomic(w.cfg.Paths.Staging, ConfirmFile, data, sharedFileMode)
	}
	if err != nil {
		w.cfg.Logger.Warn("confirming the update failed; retrying", "err", err)
		return false
	}
	w.cfg.Logger.Info("update confirmed", "version", st.To, "from", st.From)
	return true
}

// report audits a finished outcome: a confirmed install of the running version, a rollback or a
// failure. An installed state that is not this version's is left to the version it names.
func (w *Watcher) report(ctx context.Context, st InstallState) {
	if w.cfg.Reports == nil || st.ID == "" || (st.Phase == PhaseInstalled && st.To != w.cfg.Version) {
		return
	}
	w.cfg.Reports.Report(ctx, st)
}

// restartNeeded reports a state.json that changed since the start and installed another version
// or rolled back the running one.
func (w *Watcher) restartNeeded(initial InstallState) bool {
	st, ok := w.state()
	if !ok || (st.ID == initial.ID && st.Phase == initial.Phase) {
		return false
	}
	switch {
	case st.Phase == PhaseInstalled && st.To != w.cfg.Version:
		w.cfg.Logger.Info("update installed; restarting", "from", w.cfg.Version, "to", st.To)
		return true
	case st.Phase == PhaseRolledBack && st.To == w.cfg.Version:
		w.cfg.Logger.Warn("update rolled back; restarting with the previous version", "to", st.From, "reason", st.Message)
		return true
	}
	return false
}

// state reads state.json; ok is false when it is missing or unreadable.
func (w *Watcher) state() (InstallState, bool) {
	st, ok, err := readState(w.cfg.Paths.StatePath())
	if err != nil {
		w.cfg.Logger.Warn("reading the update state failed", "err", err)
	}
	return st, ok && err == nil
}

// confirmed reports whether confirmed.json already names id.
func (w *Watcher) confirmed(id string) bool { return confirmedIn(w.cfg.Paths.Staging, id) }

// confirmedIn reports whether confirmed.json in the staging directory names id.
func confirmedIn(staging, id string) bool {
	data, err := readRegular(filepath.Join(staging, ConfirmFile), maxProtocolBytes)
	if err != nil {
		return false
	}
	var c Confirmation
	return json.Unmarshal(data, &c) == nil && c.ID == id
}
