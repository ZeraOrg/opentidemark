// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// helperInstaller is the Installer as the helper flow uses it.
type helperInstaller interface {
	State() (InstallState, bool, error)
	Pending() bool
	Install(ctx context.Context) (InstallState, error)
	WaitConfirmed(ctx context.Context, timeout time.Duration) error
	Rollback(reason string) (InstallState, error)
}

// HelperDeps are the collaborators of RunHelper.
type HelperDeps struct {
	Installer helperInstaller
	// Restart restarts the service so it runs the binary now installed (Windows). Nil on Linux,
	// where the service restarts itself when state.json changes (Watcher).
	Restart func(ctx context.Context) error
	Timeout time.Duration // ConfirmTimeout in production
	Logger  *slog.Logger
}

// maxHelperRounds bounds how many requests one helper run handles: a request staged while the
// helper was busy (its trigger was lost) is handled before it exits.
const maxHelperRounds = 3

// RunHelper is the privileged helper (SPEC §17.7): it installs the staged request, has the
// service restart, waits for the new version's confirmation and rolls back when it does not
// come. A request handled before is not installed again; an installed one whose confirmation is
// still missing (the helper was stopped while waiting) is waited for again without a restart.
// Before it returns it handles a newer request staged meanwhile, up to maxHelperRounds in all.
func RunHelper(ctx context.Context, d HelperDeps) error {
	var errs []error
	for round := 1; ; round++ {
		if err := helperRound(ctx, d); err != nil {
			errs = append(errs, err)
		}
		if ctx.Err() != nil || !d.Installer.Pending() {
			return errors.Join(errs...)
		}
		if round == maxHelperRounds {
			d.Logger.Warn("another update request is staged; leaving it after the maximum number of rounds", "rounds", round)
			return errors.Join(errs...)
		}
		d.Logger.Info("another update request was staged meanwhile; handling it", "round", round+1)
	}
}

// helperRound handles the staged request once.
func helperRound(ctx context.Context, d HelperDeps) error {
	before, had, err := d.Installer.State()
	if err != nil {
		return err
	}
	st, err := d.Installer.Install(ctx)
	switch {
	case errors.Is(err, ErrNothingStaged):
		d.Logger.Info("no update request staged")
		return nil
	case err != nil:
		d.Logger.Error("update failed; the installed binary is unchanged", "to", st.To, "err", err)
		return err
	case st.Phase != PhaseInstalled:
		d.Logger.Info("update request already handled", "id", st.ID, "phase", st.Phase)
		return nil
	}
	fresh := !had || before.ID != st.ID
	if fresh {
		d.Logger.Info("update installed; waiting for the new version to confirm", "from", st.From, "to", st.To)
	}
	if fresh && d.Restart != nil {
		if err := d.Restart(ctx); err != nil {
			return d.rollback(ctx, fmt.Errorf("restart: %w", err))
		}
	}
	if err := d.Installer.WaitConfirmed(ctx, d.Timeout); err != nil {
		return d.rollback(ctx, err)
	}
	d.Logger.Info("update confirmed", "version", st.To)
	return nil
}

// rollback restores the previous binary because of cause and restarts the service when the
// helper does that itself.
func (d HelperDeps) rollback(ctx context.Context, cause error) error {
	st, err := d.Installer.Rollback(cause.Error())
	if err != nil {
		d.Logger.Error("rollback failed", "cause", cause, "err", err)
		return errors.Join(cause, err)
	}
	d.Logger.Warn("update rolled back", "from", st.To, "to", st.From, "reason", cause)
	if d.Restart != nil {
		if err := d.Restart(context.WithoutCancel(ctx)); err != nil {
			return errors.Join(cause, fmt.Errorf("restart after rollback: %w", err))
		}
	}
	return fmt.Errorf("update rolled back: %w", cause)
}
