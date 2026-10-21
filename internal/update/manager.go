// SPDX-License-Identifier: Apache-2.0

package update

import "context"

// Manager is the update API of a node: the release check, and on installations that support
// it the one-click apply (SPEC §11.5, §17.7).
type Manager struct {
	checker *Checker
	applier *Applier      // nil when this installation cannot apply updates
	busy    func() string // ReasonDraining or ReasonWALFailed while the node must not update
}

// NewManager combines checker and applier (nil when apply is not supported); busy reports why
// the node cannot update right now, or "".
func NewManager(checker *Checker, applier *Applier, busy func() string) *Manager {
	return &Manager{checker: checker, applier: applier, busy: busy}
}

// Status returns the status with the apply state, progress and last result.
func (m *Manager) Status() Status {
	s := m.checker.Status()
	if m.applier != nil {
		m.applier.decorate(&s)
	}
	return s
}

// CheckNow checks immediately (Checker.CheckNow) and returns the full status.
func (m *Manager) CheckNow(ctx context.Context) (Status, error) {
	if _, err := m.checker.CheckNow(ctx); err != nil {
		return m.Status(), err
	}
	return m.Status(), nil
}

// Apply starts installing version, which must be the latest known release. It fails with
// ApplyRefusedError when this installation cannot apply (Apply.Reason), the node is draining or
// its WAL failed, or an update is already being applied; with ErrVersionMismatch otherwise.
func (m *Manager) Apply(version string) (Status, error) {
	s := m.Status()
	if !s.Apply.Supported || m.applier == nil {
		return s, &ApplyRefusedError{Reason: s.Apply.Reason}
	}
	if s.Latest == nil || version != s.Latest.Version {
		return s, ErrVersionMismatch
	}
	if reason := m.busy(); reason != "" {
		return s, &ApplyRefusedError{Reason: reason}
	}
	if err := m.applier.Begin(version); err != nil {
		return s, err
	}
	return m.Status(), nil
}
