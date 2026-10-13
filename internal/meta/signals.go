// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// Signal is a saved, named TQL filter.
type Signal struct {
	ID        string
	Name      string
	TQL       string
	Color     string
	OwnerID   string
	Shared    bool
	Source    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Signals is the signals repository.
type Signals struct{ db *DB }

// NewSignals returns the signals repository.
func NewSignals(db *DB) *Signals { return &Signals{db: db} }

const signalCols = `id, name, tql, color, owner_id, shared, source, created_at, updated_at`

func scanSignal(r rowScanner) (Signal, error) {
	var s Signal
	var created, updated string
	if err := r.Scan(&s.ID, &s.Name, &s.TQL, &s.Color, &s.OwnerID, &s.Shared, &s.Source, &created, &updated); err != nil {
		return Signal{}, mapErr(err)
	}
	s.CreatedAt, s.UpdatedAt = decodeTime(created), decodeTime(updated)
	return s, nil
}

// Create inserts a signal.
func (r *Signals) Create(ctx context.Context, s *Signal) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	now := r.db.now()
	s.CreatedAt, s.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO signals (`+signalCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.TQL, s.Color, s.OwnerID, boolInt(s.Shared), s.Source, encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns a signal by id.
func (r *Signals) Get(ctx context.Context, id string) (Signal, error) {
	return scanSignal(r.db.sql.QueryRowContext(ctx, `SELECT `+signalCols+` FROM signals WHERE id = ?`, id))
}

// ListVisible returns the signals owned by userID or shared.
func (r *Signals) ListVisible(ctx context.Context, userID string) ([]Signal, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+signalCols+` FROM signals WHERE owner_id = ? OR shared = 1 ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list signals: %w", err)
	}
	defer rows.Close()
	var out []Signal
	for rows.Next() {
		s, err := scanSignal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Update writes a signal.
func (r *Signals) Update(ctx context.Context, s *Signal) error {
	s.UpdatedAt = r.db.now()
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE signals SET name = ?, tql = ?, color = ?, shared = ?, source = ?,
		updated_at = ? WHERE id = ?`, s.Name, s.TQL, s.Color, boolInt(s.Shared), s.Source, encodeTime(s.UpdatedAt), s.ID))
}

// UpdateIfUnchanged writes a signal only if its stored updated_at still equals expected
// (optimistic concurrency, SPEC §11.4); a mismatch returns ErrConflict.
func (r *Signals) UpdateIfUnchanged(ctx context.Context, s *Signal, expected time.Time) error {
	updated := r.db.now()
	res, err := r.db.sql.ExecContext(ctx, `UPDATE signals SET name = ?, tql = ?, color = ?, shared = ?, source = ?,
		updated_at = ? WHERE id = ? AND updated_at = ?`, s.Name, s.TQL, s.Color, boolInt(s.Shared), s.Source,
		encodeTime(updated), s.ID, encodeTime(expected))
	if err := versionedResult(ctx, res, err, func() error { _, e := r.Get(ctx, s.ID); return e }); err != nil {
		return err
	}
	s.UpdatedAt = updated
	return nil
}

// Delete removes a signal.
func (r *Signals) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM signals WHERE id = ?`, id))
}
