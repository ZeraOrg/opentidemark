// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// IndexedField is a property promoted to a materialized ClickHouse column (SPEC §12.4).
type IndexedField struct {
	ID         string
	Source     string
	Key        string
	ColumnName string
	CreatedAt  time.Time
}

// IndexedFields is the indexed_fields repository.
type IndexedFields struct{ db *DB }

// NewIndexedFields returns the indexed_fields repository.
func NewIndexedFields(db *DB) *IndexedFields { return &IndexedFields{db: db} }

// Create inserts an indexed field.
func (r *IndexedFields) Create(ctx context.Context, f *IndexedField) error {
	if f.ID == "" {
		f.ID = NewID()
	}
	f.CreatedAt = r.db.now()
	now := encodeTime(f.CreatedAt)
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO indexed_fields (id, source, key, column_name, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`, f.ID, f.Source, f.Key, f.ColumnName, now, now)
	return mapErr(err)
}

// Get returns an indexed field by id.
func (r *IndexedFields) Get(ctx context.Context, id string) (IndexedField, error) {
	var f IndexedField
	var created string
	err := r.db.sql.QueryRowContext(ctx, `SELECT id, source, key, column_name, created_at FROM indexed_fields WHERE id = ?`, id).
		Scan(&f.ID, &f.Source, &f.Key, &f.ColumnName, &created)
	if err != nil {
		return IndexedField{}, mapErr(err)
	}
	f.CreatedAt = decodeTime(created)
	return f, nil
}

// List returns all indexed fields.
func (r *IndexedFields) List(ctx context.Context) ([]IndexedField, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT id, source, key, column_name, created_at FROM indexed_fields ORDER BY source, key`)
	if err != nil {
		return nil, fmt.Errorf("list indexed fields: %w", err)
	}
	defer rows.Close()
	var out []IndexedField
	for rows.Next() {
		var f IndexedField
		var created string
		if err := rows.Scan(&f.ID, &f.Source, &f.Key, &f.ColumnName, &created); err != nil {
			return nil, mapErr(err)
		}
		f.CreatedAt = decodeTime(created)
		out = append(out, f)
	}
	return out, rows.Err()
}

// Delete removes an indexed field.
func (r *IndexedFields) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM indexed_fields WHERE id = ?`, id))
}
