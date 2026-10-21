// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Helper protocol (SPEC §17.7).
const (
	// ConfirmFile is written by the new version into the staging directory once it has been
	// ready for ConfirmReadyAfter.
	ConfirmFile = "confirmed.json"
	// StateFile records the last install attempt in the helper's work directory.
	StateFile = "state.json"
	// PreviousSuffix names the kept previous binary, newSuffix the binary being installed.
	PreviousSuffix = ".previous"
	newSuffix      = ".new"
	lockFile       = "install.lock"
	// ConfirmReadyAfter is how long the new version must be ready before it confirms.
	ConfirmReadyAfter = 30 * time.Second
	// ConfirmTimeout is how long the helper waits for the confirmation before rolling back.
	ConfirmTimeout = 10 * time.Minute
	// lockStaleAfter frees the lock of a helper that died; longer than any install and wait.
	lockStaleAfter = 3 * ConfirmTimeout
	// defaultPollInterval is how often WaitConfirmed looks for the confirmation.
	defaultPollInterval = time.Second
)

// Errors of the helper side.
var (
	ErrLocked         = errors.New("another update is being installed")
	ErrNotNewer       = errors.New("staged version is not newer than the installed one")
	ErrNothingStaged  = errors.New("no update request staged")
	ErrNoInstall      = errors.New("no installed update to roll back")
	ErrNotConfirmed   = errors.New("new version did not confirm in time")
	ErrInstallerSetup = errors.New("invalid installer configuration")
)

// Phase is the outcome of an install attempt.
type Phase string

// Phases of state.json.
const (
	PhaseInstalled  Phase = "installed"
	PhaseRolledBack Phase = "rolled_back"
	PhaseFailed     Phase = "failed"
)

// InstallState is state.json: the last request the helper handled and what became of it.
type InstallState struct {
	ID      string    `json:"id"`
	Phase   Phase     `json:"phase"`
	From    string    `json:"from"`
	To      string    `json:"to"`
	At      time.Time `json:"at"`
	Message string    `json:"message,omitempty"`
}

// Confirmation is confirmed.json, written by the new version.
type Confirmation struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// InstallerConfig configures the privileged helper side.
type InstallerConfig struct {
	StagingDir   string   // written by the service: request, checksums, signature, archive
	WorkDir      string   // private to the helper: lock, state.json, temporary copies
	BinaryPath   string   // the installed tidemark binary
	Current      string   // version of the installed binary (the helper's own version)
	Platform     Platform // GOOS/GOARCH of the installation
	Keys         []string // trusted minisign public keys (TrustedSigningKeys)
	Now          func() time.Time
	PollInterval time.Duration // WaitConfirmed polling; 0 means one second
}

// Installer installs a staged release, keeps the previous binary and rolls back on request.
type Installer struct {
	cfg     InstallerConfig
	current Version
}

// NewInstaller checks cfg and returns an installer.
func NewInstaller(cfg InstallerConfig) (*Installer, error) {
	current, err := ParseVersion(cfg.Current)
	if err != nil {
		return nil, fmt.Errorf("%w: current version: %w", ErrInstallerSetup, err)
	}
	if cfg.StagingDir == "" || cfg.WorkDir == "" || cfg.BinaryPath == "" || len(cfg.Keys) == 0 || cfg.Now == nil {
		return nil, fmt.Errorf("%w: directories, binary, keys and clock are required", ErrInstallerSetup)
	}
	if _, err := AssetName(cfg.Current, cfg.Platform); err != nil {
		return nil, err
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &Installer{cfg: cfg, current: current}, nil
}

// Install handles the staged request under the lock. A request whose id state.json already
// records is not installed again: its recorded state is returned. Any failure after the request
// was read is recorded as phase failed; the installed binary is then unchanged.
func (in *Installer) Install(ctx context.Context) (InstallState, error) {
	unlock, err := in.lock()
	if err != nil {
		return InstallState{}, err
	}
	defer unlock()
	req, to, err := in.readRequest()
	if err != nil {
		return InstallState{}, err
	}
	if prev, ok, err := in.State(); err != nil {
		return InstallState{}, err
	} else if ok && prev.ID == req.ID {
		return prev, nil
	}
	if err := in.install(ctx, req, to); err != nil {
		st := in.newState(req, PhaseFailed, err.Error())
		return st, errors.Join(err, in.writeState(st))
	}
	st := in.newState(req, PhaseInstalled, "")
	in.current = to // the binary now in place; a further request in this run is compared with it
	return st, in.writeState(st)
}

// install verifies private copies of the staged files and swaps the binary.
func (in *Installer) install(ctx context.Context, req Request, to Version) error {
	if to.Compare(in.current) <= 0 {
		return fmt.Errorf("%w: %s, installed %s", ErrNotNewer, to, in.current)
	}
	tmp, err := os.MkdirTemp(in.cfg.WorkDir, "install-")
	if err != nil {
		return fmt.Errorf("private copy: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() // temporary copies only; a leftover is harmless
	archive, err := in.copyVerified(tmp, req.Version)
	if err != nil {
		return err
	}
	if err := in.extract(archive, req.Version); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(in.cfg.BinaryPath + newSuffix) // cancelled before the swap; nothing changed
		return err
	}
	return replaceBinary(in.cfg.BinaryPath)
}

// copyVerified copies checksums.txt, its signature and the archive from the staging directory
// into dir (regular files only, size-capped), verifies the signature with the embedded keys and
// the archive's SHA-256, and returns the private archive path.
func (in *Installer) copyVerified(dir, version string) (string, error) {
	asset, err := AssetName(version, in.cfg.Platform)
	if err != nil {
		return "", err
	}
	staged := func(name string) string { return filepath.Join(in.cfg.StagingDir, name) }
	sums, err := readRegular(staged(ChecksumsName), MaxChecksumsBytes)
	if err != nil {
		return "", err
	}
	sig, err := readRegular(staged(SignatureName), MaxSignatureBytes)
	if err != nil {
		return "", err
	}
	want, err := verifiedChecksum(sums, sig, version, in.cfg.Keys, asset)
	if err != nil {
		return "", err
	}
	archive := filepath.Join(dir, asset)
	got, err := copyRegular(staged(asset), archive, MaxAssetBytes)
	if err != nil {
		return "", err
	}
	if got != want {
		return "", fmt.Errorf("%w: %s", ErrChecksumMismatch, asset)
	}
	return archive, nil
}

// extract writes the binary of archive to <bin>.new.
func (in *Installer) extract(archive, version string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	next := in.cfg.BinaryPath + newSuffix
	if err := removeIfExists(next); err != nil {
		return err
	}
	out, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755) //nolint:gosec // an executable for the service
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(next), err)
	}
	err = ExtractBinary(f, fi.Size(), version, in.cfg.Platform, out)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(next) // a partial binary is never installed
		return err
	}
	return nil
}

// Rollback restores the previous binary after an installed update and records phase
// rolled_back with reason.
func (in *Installer) Rollback(reason string) (InstallState, error) {
	unlock, err := in.lock()
	if err != nil {
		return InstallState{}, err
	}
	defer unlock()
	st, ok, err := in.State()
	if err != nil {
		return InstallState{}, err
	}
	if !ok || st.Phase != PhaseInstalled {
		return st, ErrNoInstall
	}
	if err := restorePrevious(in.cfg.BinaryPath); err != nil {
		return st, err
	}
	if from, err := ParseVersion(st.From); err == nil {
		in.current = from
	}
	st.Phase, st.At, st.Message = PhaseRolledBack, in.cfg.Now().UTC(), reason
	return st, in.writeState(st)
}

// WaitConfirmed waits up to timeout (ConfirmTimeout in production) for confirmed.json naming
// the installed request's id and version.
func (in *Installer) WaitConfirmed(ctx context.Context, timeout time.Duration) error {
	st, ok, err := in.State()
	if err != nil {
		return err
	}
	if !ok || st.Phase != PhaseInstalled {
		return ErrNoInstall
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(in.cfg.PollInterval)
	defer tick.Stop()
	for {
		if in.confirmed(st) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrNotConfirmed, ctx.Err())
		case <-tick.C:
		}
	}
}

// confirmed reports whether confirmed.json names st; an unreadable file is not a confirmation.
func (in *Installer) confirmed(st InstallState) bool {
	data, err := readRegular(filepath.Join(in.cfg.StagingDir, ConfirmFile), maxProtocolBytes)
	if err != nil {
		return false
	}
	var c Confirmation
	return json.Unmarshal(data, &c) == nil && c.ID == st.ID && c.Version == st.To
}

// Pending reports whether request.json holds a valid request that state.json does not record
// yet (staged while this helper was busy).
func (in *Installer) Pending() bool {
	req, _, err := in.readRequest()
	if err != nil {
		return false
	}
	st, ok, err := in.State()
	return err == nil && (!ok || st.ID != req.ID)
}

// State returns state.json; ok is false when the helper has handled no request yet.
func (in *Installer) State() (InstallState, bool, error) {
	return readState(filepath.Join(in.cfg.WorkDir, StateFile))
}

// readState reads the state.json at path; ok is false when it does not exist.
func readState(path string) (InstallState, bool, error) {
	data, err := readRegular(path, maxProtocolBytes)
	if errors.Is(err, os.ErrNotExist) {
		return InstallState{}, false, nil
	}
	if err != nil {
		return InstallState{}, false, err
	}
	var st InstallState
	if err := json.Unmarshal(data, &st); err != nil {
		return InstallState{}, false, fmt.Errorf("read %s: %w", StateFile, err)
	}
	return st, true, nil
}

func (in *Installer) readRequest() (Request, Version, error) {
	data, err := readRegular(filepath.Join(in.cfg.StagingDir, RequestFile), maxProtocolBytes)
	if errors.Is(err, os.ErrNotExist) {
		return Request{}, Version{}, ErrNothingStaged
	}
	if err != nil {
		return Request{}, Version{}, err
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return Request{}, Version{}, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	to, err := req.validate()
	return req, to, err
}

func (in *Installer) newState(req Request, phase Phase, msg string) InstallState {
	return InstallState{ID: req.ID, Phase: phase, From: in.current.String(), To: req.Version, At: in.cfg.Now().UTC(), Message: msg}
}

func (in *Installer) writeState(st InstallState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return writeFileAtomic(in.cfg.WorkDir, StateFile, data, publicFileMode)
}

// lock creates the lock file exclusively; a lock older than lockStaleAfter (a helper that died)
// is taken over once. The returned function releases it.
func (in *Installer) lock() (func(), error) {
	path := filepath.Join(in.cfg.WorkDir, lockFile)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
		if err == nil {
			if err := f.Close(); err != nil {
				return nil, fmt.Errorf("lock: %w", err)
			}
			return func() { _ = os.Remove(path) }, nil // a lock left behind expires as stale
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock: %w", err)
		}
		fi, serr := os.Stat(path)
		if serr != nil || in.cfg.Now().Sub(fi.ModTime()) < lockStaleAfter {
			return nil, ErrLocked
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("lock: %w", err)
		}
	}
	return nil, ErrLocked
}
