// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Apply states (Status.State, SPEC §17.7); StateIdle when nothing is being applied.
const (
	StateDownloading = "downloading"
	StateVerifying   = "verifying"
	StateStaged      = "staged"
	StateInstalling  = "installing"
	StateRestarting  = "restarting"
)

// Reasons an apply request is refused besides Apply.Reason (details.reason of a 409).
const (
	ReasonInProgress = "in-progress"
	ReasonDraining   = "draining"
	ReasonWALFailed  = "wal-failed"
)

// Limits of applying an update.
const (
	// spaceFactor is the free space the staging directory needs, as a multiple of the archive:
	// the archive, the helper's private copy and the extracted binary.
	spaceFactor = 3
	// pickupTimeout is how long the helper may take to record the request in state.json.
	pickupTimeout  = 5 * time.Minute
	pickupPoll     = time.Second
	stagingDirPerm = 0o750
)

// Errors of applying an update.
var (
	ErrApplyRefused      = errors.New("the update cannot be applied")
	ErrVersionMismatch   = errors.New("version is not the latest release")
	ErrInsufficientSpace = errors.New("not enough free disk space for the update")
	ErrHelperTimeout     = errors.New("the update helper did not pick up the request")
)

// ApplyRefusedError says why an update cannot be applied now (an Apply reason or
// ReasonInProgress, ReasonDraining, ReasonWALFailed).
type ApplyRefusedError struct{ Reason string }

func (e *ApplyRefusedError) Error() string { return ErrApplyRefused.Error() + ": " + e.Reason }

// Is makes errors.Is(err, ErrApplyRefused) true.
func (e *ApplyRefusedError) Is(target error) bool { return target == ErrApplyRefused }

// Progress is the download progress of the archive in bytes.
type Progress struct {
	Bytes int64 `json:"bytes"`
	Total int64 `json:"total"`
}

// Result is the outcome of the last update attempt (Status.LastResult).
type Result struct {
	Phase   Phase     `json:"phase"`
	From    string    `json:"from,omitempty"`
	To      string    `json:"to"`
	At      time.Time `json:"at"`
	Message string    `json:"message,omitempty"`
}

func resultOf(st InstallState) *Result {
	return &Result{Phase: st.Phase, From: st.From, To: st.To, At: st.At, Message: st.Message}
}

// releaseStager stages a release (implemented by *Stager).
type releaseStager interface {
	Stage(ctx context.Context, req Request, hooks StageHooks) error
}

// ApplierDeps are the collaborators of an Applier.
type ApplierDeps struct {
	Stager    releaseStager
	Paths     HelperPaths
	Current   string                           // the running version
	Trigger   func() error                     // starts the helper after staging (StartHelper)
	FreeSpace func(dir string) (uint64, error) // free bytes of the file system holding dir
	Reports   *Reporter
	Events    Events
	NewID     func() string
	Now       func() time.Time
	After     func(time.Duration) <-chan time.Time
	Logger    *slog.Logger
}

// Applier downloads, verifies and stages one update at a time in the background, starts the
// helper and follows its state.json until the server is restarted or the attempt failed.
type Applier struct {
	d    ApplierDeps
	jobs chan Request

	mu       sync.Mutex
	state    string
	progress *Progress
	last     *Result // the outcome seen by this process, newer than state.json's
}

// NewApplier returns an idle applier; Run processes what Begin accepts.
func NewApplier(d ApplierDeps) *Applier {
	return &Applier{d: d, jobs: make(chan Request, 1), state: StateIdle}
}

// Begin accepts an update to version; it fails with ApplyRefusedError (ReasonInProgress) while
// another one is being applied, also while the helper still waits for an installed update to
// confirm.
func (a *Applier) Begin(version string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state != StateIdle || a.helperWaiting() {
		return &ApplyRefusedError{Reason: ReasonInProgress}
	}
	a.state, a.progress = StateDownloading, &Progress{}
	a.jobs <- Request{ID: a.d.NewID(), Version: version} // never blocks: the state was idle
	return nil
}

// Run applies accepted updates until ctx ends.
func (a *Applier) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-a.jobs:
			a.apply(ctx, req)
		}
	}
}

func (a *Applier) apply(ctx context.Context, req Request) {
	a.d.Logger.Info("applying update", "from", a.d.Current, "to", req.Version, "id", req.ID)
	if err := a.stage(ctx, req); err != nil {
		stage := StageDownload
		if errors.Is(err, ErrSignature) || errors.Is(err, ErrChecksums) || errors.Is(err, ErrChecksumMismatch) {
			stage = StageVerify
		}
		a.fail(ctx, req, stage, err)
		return
	}
	a.setState(StateStaged)
	if err := a.d.Trigger(); err != nil {
		a.fail(ctx, req, StageInstall, fmt.Errorf("start the update helper: %w", err))
		return
	}
	a.setState(StateInstalling)
	st, err := a.waitHelper(ctx, req.ID)
	if err != nil {
		a.fail(ctx, req, StageInstall, err)
		return
	}
	if st.Phase == PhaseInstalled {
		a.d.Logger.Info("update installed; the server restarts", "to", st.To)
		a.setState(StateRestarting)
		return
	}
	a.d.Reports.Report(ctx, st)
	a.finish(resultOf(st))
}

// stage downloads and verifies the release into the staging directory, which needs spaceFactor
// times the archive size free.
func (a *Applier) stage(ctx context.Context, req Request) error {
	if err := os.MkdirAll(a.d.Paths.Staging, stagingDirPerm); err != nil {
		return fmt.Errorf("staging directory: %w", err)
	}
	hooks := StageHooks{
		Reserve: func(size int64) error {
			free, err := a.d.FreeSpace(a.d.Paths.Staging)
			if err != nil {
				return fmt.Errorf("free disk space: %w", err)
			}
			if need := uint64(size) * spaceFactor; free < need { //nolint:gosec // size is > 0 and capped
				return fmt.Errorf("%w: %d bytes free, %d needed", ErrInsufficientSpace, free, need)
			}
			return nil
		},
		Progress: a.setProgress,
	}
	return a.d.Stager.Stage(ctx, req, hooks)
}

// helperWaiting reports a state.json that records an installed update not confirmed yet, within
// the helper's confirmation timeout (a helper that died while waiting does not block forever).
func (a *Applier) helperWaiting() bool {
	st, ok, err := readState(a.d.Paths.StatePath())
	if err != nil || !ok || st.Phase != PhaseInstalled || confirmedIn(a.d.Paths.Staging, st.ID) {
		return false
	}
	return a.d.Now().Sub(st.At) < ConfirmTimeout
}

// waitHelper polls state.json until it records request id.
func (a *Applier) waitHelper(ctx context.Context, id string) (InstallState, error) {
	deadline := a.d.Now().Add(pickupTimeout)
	for {
		if st, ok, err := readState(a.d.Paths.StatePath()); err == nil && ok && st.ID == id {
			return st, nil
		}
		if a.d.Now().After(deadline) {
			return InstallState{}, ErrHelperTimeout
		}
		select {
		case <-ctx.Done():
			return InstallState{}, ctx.Err()
		case <-a.d.After(pickupPoll):
		}
	}
}

func (a *Applier) fail(ctx context.Context, req Request, stage string, err error) {
	a.d.Logger.Warn("update failed", "to", req.Version, "stage", stage, "err", err)
	a.d.Events.Failed(ctx, req.Version, stage, err.Error())
	a.finish(&Result{Phase: PhaseFailed, From: a.d.Current, To: req.Version, At: a.d.Now().UTC(), Message: err.Error()})
}

func (a *Applier) finish(r *Result) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state, a.progress, a.last = StateIdle, nil, r
}

func (a *Applier) setState(state string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = state
}

// setProgress records the download; the complete archive is being verified.
func (a *Applier) setProgress(done, total int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progress = &Progress{Bytes: done, Total: total}
	if done >= total {
		a.state = StateVerifying
	}
}

// decorate adds the apply state, progress and last result to s.
func (a *Applier) decorate(s *Status) {
	a.mu.Lock()
	state, last := a.state, a.last
	var progress *Progress
	if a.progress != nil {
		p := *a.progress
		progress = &p
	}
	a.mu.Unlock()
	s.State, s.Progress = state, progress
	if last != nil {
		r := *last
		s.LastResult = &r
	} else if st, ok, err := readState(a.d.Paths.StatePath()); err == nil && ok {
		s.LastResult = resultOf(st)
	}
}
