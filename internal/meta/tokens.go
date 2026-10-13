// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Token scopes.
const (
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// APIToken is a user-owned token for the query API.
type APIToken struct {
	ID         string
	Name       string
	UserID     string
	KeyPrefix  string
	KeyHash    string
	Scope      string
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// APITokens is the api_tokens repository.
type APITokens struct{ db *DB }

// NewAPITokens returns the api_tokens repository.
func NewAPITokens(db *DB) *APITokens { return &APITokens{db: db} }

const tokenCols = `id, name, user_id, key_prefix, key_hash, scopes, expires_at, revoked_at, last_used_at, created_at, updated_at` //nolint:gosec // G101 false positive: a column list

func scanToken(r rowScanner) (APIToken, error) {
	var t APIToken
	var expires, revoked, lastUsed sql.NullString
	var created, updated string
	if err := r.Scan(&t.ID, &t.Name, &t.UserID, &t.KeyPrefix, &t.KeyHash, &t.Scope, &expires, &revoked, &lastUsed,
		&created, &updated); err != nil {
		return APIToken{}, mapErr(err)
	}
	t.ExpiresAt, t.RevokedAt, t.LastUsedAt = decodeTimePtr(expires), decodeTimePtr(revoked), decodeTimePtr(lastUsed)
	t.CreatedAt, t.UpdatedAt = decodeTime(created), decodeTime(updated)
	return t, nil
}

// Create inserts a token.
func (r *APITokens) Create(ctx context.Context, t *APIToken) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	now := r.db.now()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, t.UserID, t.KeyPrefix, t.KeyHash, t.Scope, encodeTimePtr(t.ExpiresAt), encodeTimePtr(t.RevokedAt),
		encodeTimePtr(t.LastUsedAt), encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// GetByHash returns a token by key hash.
func (r *APITokens) GetByHash(ctx context.Context, hash string) (APIToken, error) {
	return scanToken(r.db.sql.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE key_hash = ?`, hash))
}

// Get returns a token by id.
func (r *APITokens) Get(ctx context.Context, id string) (APIToken, error) {
	return scanToken(r.db.sql.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
}

// ListForUser returns the tokens owned by a user.
func (r *APITokens) ListForUser(ctx context.Context, userID string) ([]APIToken, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke marks a token revoked.
func (r *APITokens) Revoke(ctx context.Context, id string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?, updated_at = ? WHERE id = ?`,
		encodeTime(at), encodeTime(r.db.now()), id))
}

// RevokeForUser revokes every active token of a user.
func (r *APITokens) RevokeForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := r.db.sql.ExecContext(ctx, `UPDATE api_tokens SET revoked_at = ?, updated_at = ?
		WHERE user_id = ? AND revoked_at IS NULL`, encodeTime(at), encodeTime(r.db.now()), userID)
	return mapErr(err)
}

// Touch sets last_used_at.
func (r *APITokens) Touch(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.sql.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, encodeTime(at), id)
	return mapErr(err)
}

// Delete removes a token.
func (r *APITokens) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id))
}
