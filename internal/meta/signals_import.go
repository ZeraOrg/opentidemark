// SPDX-License-Identifier: Apache-2.0

package meta

import "context"

// CreateImported inserts a signal created by an importer (SPEC §9.9). Filters that could not
// be translated to TQL are stored with disabled = 1 so they are kept for manual review but are
// never applied.
func (r *Signals) CreateImported(ctx context.Context, s *Signal, disabled bool) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	now := r.db.now()
	s.CreatedAt, s.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO signals (`+signalCols+`, disabled) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.TQL, s.Color, s.OwnerID, boolInt(s.Shared), s.Source, encodeTime(now), encodeTime(now), boolInt(disabled))
	return mapErr(err)
}

// IsDisabled reports whether a signal is disabled.
func (r *Signals) IsDisabled(ctx context.Context, id string) (bool, error) {
	var d bool
	if err := r.db.sql.QueryRowContext(ctx, `SELECT disabled FROM signals WHERE id = ?`, id).Scan(&d); err != nil {
		return false, mapErr(err)
	}
	return d, nil
}
