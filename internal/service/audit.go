// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// Audit actions (SPEC §15.5).
const (
	ActionLoginSuccess      = "auth.login"
	ActionLoginFailure      = "auth.login_failed"
	ActionLogout            = "auth.logout"
	ActionPasswordChange    = "auth.password_change"
	ActionPasswordReset     = "auth.password_reset"
	ActionUserCreate        = "user.create"
	ActionUserUpdate        = "user.update"
	ActionUserDelete        = "user.delete"
	ActionTokenCreate       = "token.create"
	ActionTokenDelete       = "token.delete"
	ActionConnectionCreate  = "connection.create"
	ActionConnectionUpdate  = "connection.update"
	ActionConnectionDelete  = "connection.delete"
	ActionConnectionRotate  = "connection.rotate"
	ActionConnectionRevoke  = "connection.revoke"
	ActionSettingsUpdate    = "settings.update"
	ActionIngestAuthFailure = "ingest.auth_failed"
)

// AuditStore appends and lists audit entries.
type AuditStore interface {
	Append(ctx context.Context, e *meta.AuditEntry) error
	List(ctx context.Context, f meta.AuditFilter) ([]meta.AuditEntry, error)
}

// Actor identifies who performed an action.
type Actor struct {
	ID string // "user:<id>", "token:<id>", "system" or "anonymous"
	IP string
}

// SystemActor is used for actions performed by OpenTidemark itself.
var SystemActor = Actor{ID: "system"}

// Auditor records audit entries. Failures are logged, never returned to callers, so an audit
// outage cannot block logins; they are visible in the server log.
type Auditor struct {
	store  AuditStore
	logger *slog.Logger
	now    func() time.Time
}

// NewAuditor returns an auditor.
func NewAuditor(store AuditStore, logger *slog.Logger, now func() time.Time) *Auditor {
	return &Auditor{store: store, logger: logger, now: now}
}

// Record appends an entry with optional details.
func (a *Auditor) Record(ctx context.Context, actor Actor, action, target string, details map[string]any) {
	raw := json.RawMessage("{}")
	if len(details) > 0 {
		if b, err := json.Marshal(details); err == nil {
			raw = b
		}
	}
	e := &meta.AuditEntry{At: a.now(), Actor: actor.ID, IP: actor.IP, Action: action, Target: target, Details: raw}
	if err := a.store.Append(ctx, e); err != nil {
		a.logger.Error("audit write failed", "action", action, "err", err)
	}
}

// List returns audit entries.
func (a *Auditor) List(ctx context.Context, f meta.AuditFilter) ([]meta.AuditEntry, error) {
	return a.store.List(ctx, f)
}
