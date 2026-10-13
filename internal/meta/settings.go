// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"encoding/json"
	"fmt"
)

// Settings is the key/value settings repository; values are JSON.
type Settings struct{ db *DB }

// NewSettings returns the settings repository.
func NewSettings(db *DB) *Settings { return &Settings{db: db} }

// Get returns the raw JSON value of key or ErrNotFound.
func (r *Settings) Get(ctx context.Context, key string) (json.RawMessage, error) {
	var v string
	if err := r.db.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return nil, mapErr(err)
	}
	return json.RawMessage(v), nil
}

// Set upserts key with a JSON value.
func (r *Settings) Set(ctx context.Context, key string, value json.RawMessage) error {
	if !json.Valid(value) {
		return fmt.Errorf("setting %s: invalid JSON", key)
	}
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, string(value), encodeTime(r.db.now()))
	return mapErr(err)
}

// Delete removes key (no error when absent).
func (r *Settings) Delete(ctx context.Context, key string) error {
	_, err := r.db.sql.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return mapErr(err)
}

// All returns every setting.
func (r *Settings) All(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, mapErr(err)
		}
		out[k] = json.RawMessage(v)
	}
	return out, rows.Err()
}
