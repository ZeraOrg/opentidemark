// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AuditEntry is one append-only audit record (SPEC §15.5).
type AuditEntry struct {
	ID      string
	At      time.Time
	Actor   string
	IP      string
	Action  string
	Target  string
	Details json.RawMessage
}

// AuditFilter selects audit entries; zero values match everything.
type AuditFilter struct {
	From   time.Time
	To     time.Time
	Actor  string
	Action string
	Limit  int
}

// AuditLog is the audit_log repository (append-only; enforced by triggers).
type AuditLog struct{ db *DB }

// NewAuditLog returns the audit repository.
func NewAuditLog(db *DB) *AuditLog { return &AuditLog{db: db} }

// DefaultAuditLimit caps audit queries without an explicit limit.
const DefaultAuditLimit = 1000

// Append writes an entry; At defaults to now.
func (r *AuditLog) Append(ctx context.Context, e *AuditEntry) error {
	if e.ID == "" {
		e.ID = NewID()
	}
	now := r.db.now()
	if e.At.IsZero() {
		e.At = now
	}
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO audit_log (id, at, actor, ip, action, target, details, created_at,
		updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, e.ID, encodeTime(e.At), e.Actor, e.IP, e.Action, e.Target,
		rawOrEmpty(e.Details), encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// List returns entries matching f, newest first.
func (r *AuditLog) List(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	var where []string
	var args []any
	if !f.From.IsZero() {
		where, args = append(where, "at >= ?"), append(args, encodeTime(f.From))
	}
	if !f.To.IsZero() {
		where, args = append(where, "at <= ?"), append(args, encodeTime(f.To))
	}
	if f.Actor != "" {
		where, args = append(where, "actor = ?"), append(args, f.Actor)
	}
	if f.Action != "" {
		where, args = append(where, "action = ?"), append(args, f.Action)
	}
	limit := f.Limit
	if limit <= 0 || limit > DefaultAuditLimit {
		limit = DefaultAuditLimit
	}
	q := `SELECT id, at, actor, ip, action, target, details FROM audit_log`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ") //nolint:gosec // constant conditions; values are bound parameters
	}
	q += " ORDER BY at DESC LIMIT ?"
	rows, err := r.db.sql.QueryContext(ctx, q, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at, details string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.IP, &e.Action, &e.Target, &details); err != nil {
			return nil, mapErr(err)
		}
		e.At, e.Details = decodeTime(at), json.RawMessage(details)
		out = append(out, e)
	}
	return out, rows.Err()
}
