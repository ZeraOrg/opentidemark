// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"sync"
)

// reportedFile in the staging directory records the last helper outcome already audited.
const reportedFile = "reported.json"

// Failure stages of the update.failed audit event (SPEC §15.5).
const (
	StageDownload = "download"
	StageVerify   = "verify"
	StageInstall  = "install"
)

// Events receives the outcome of an update for the audit log (system actor, SPEC §15.5).
type Events interface {
	Installed(ctx context.Context, from, to string)
	RolledBack(ctx context.Context, from, to, reason string)
	Failed(ctx context.Context, to, stage, reason string)
}

// Reporter turns the helper's state.json into audit events, each outcome once even across
// restarts (the last one reported is kept in reported.json).
type Reporter struct {
	dir    string
	events Events
	logger *slog.Logger
	mu     sync.Mutex
}

// NewReporter returns a reporter that keeps its record in the staging directory.
func NewReporter(stagingDir string, events Events, logger *slog.Logger) *Reporter {
	return &Reporter{dir: stagingDir, events: events, logger: logger}
}

type reported struct {
	ID    string `json:"id"`
	Phase Phase  `json:"phase"`
}

// Report records st unless that request id and phase were reported before.
func (r *Reporter) Report(ctx context.Context, st InstallState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	mark := reported{ID: st.ID, Phase: st.Phase}
	if data, err := readRegular(filepath.Join(r.dir, reportedFile), maxProtocolBytes); err == nil {
		var prev reported
		if json.Unmarshal(data, &prev) == nil && prev == mark {
			return
		}
	}
	switch st.Phase {
	case PhaseInstalled:
		r.events.Installed(ctx, st.From, st.To)
	case PhaseRolledBack:
		r.events.RolledBack(ctx, st.From, st.To, st.Message)
	case PhaseFailed:
		r.events.Failed(ctx, st.To, StageInstall, st.Message)
	default:
		return
	}
	data, err := json.Marshal(mark)
	if err == nil {
		err = writeFileAtomic(r.dir, reportedFile, data, sharedFileMode)
	}
	if err != nil {
		r.logger.Warn("recording the reported update outcome failed", "err", err)
	}
}
