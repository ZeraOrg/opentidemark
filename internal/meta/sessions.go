// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// Session is a server-side login session. ID is the SHA-256 hex of the cookie token.
type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
}

// Sessions is the sessions repository.
type Sessions struct{ db *DB }

// NewSessions returns the sessions repository.
func NewSessions(db *DB) *Sessions { return &Sessions{db: db} }

// Create inserts a session.
func (r *Sessions) Create(ctx context.Context, s Session) error {
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO sessions (id, user_id, created_at, updated_at, last_seen_at,
		expires_at, ip, user_agent) VALUES (?,?,?,?,?,?,?,?)`,
		s.ID, s.UserID, encodeTime(s.CreatedAt), encodeTime(s.CreatedAt), encodeTime(s.LastSeenAt),
		encodeTime(s.ExpiresAt), s.IP, s.UserAgent)
	return mapErr(err)
}

// Get returns a session by its hashed id.
func (r *Sessions) Get(ctx context.Context, id string) (Session, error) {
	var s Session
	var created, seen, expires string
	err := r.db.sql.QueryRowContext(ctx, `SELECT id, user_id, created_at, last_seen_at, expires_at, ip, user_agent
		FROM sessions WHERE id = ?`, id).Scan(&s.ID, &s.UserID, &created, &seen, &expires, &s.IP, &s.UserAgent)
	if err != nil {
		return Session{}, mapErr(err)
	}
	s.CreatedAt, s.LastSeenAt, s.ExpiresAt = decodeTime(created), decodeTime(seen), decodeTime(expires)
	return s, nil
}

// Touch updates last_seen_at.
func (r *Sessions) Touch(ctx context.Context, id string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ?, updated_at = ? WHERE id = ?`,
		encodeTime(at), encodeTime(at), id))
}

// Delete removes one session.
func (r *Sessions) Delete(ctx context.Context, id string) error {
	_, err := r.db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return mapErr(err)
}

// DeleteForUser removes all sessions of a user except keepID (may be empty).
func (r *Sessions) DeleteForUser(ctx context.Context, userID, keepID string) (int64, error) {
	res, err := r.db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keepID)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.RowsAffected()
}

// DeleteExpired removes sessions whose absolute expiry or idle deadline has passed.
func (r *Sessions) DeleteExpired(ctx context.Context, now time.Time, idle time.Duration) (int64, error) {
	res, err := r.db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR last_seen_at <= ?`,
		encodeTime(now), encodeTime(now.Add(-idle)))
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return res.RowsAffected()
}
