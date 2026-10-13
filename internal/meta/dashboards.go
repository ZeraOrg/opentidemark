// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Dashboard is a dashboard document (SPEC §13.1).
type Dashboard struct {
	ID          string
	Name        string
	Description string
	OwnerID     string
	Shared      bool
	Doc         json.RawMessage
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Dashboards is the dashboards repository.
type Dashboards struct{ db *DB }

// NewDashboards returns the dashboards repository.
func NewDashboards(db *DB) *Dashboards { return &Dashboards{db: db} }

const dashboardCols = `id, name, description, owner_id, shared, doc, version, created_at, updated_at`

func scanDashboard(r rowScanner) (Dashboard, error) {
	var d Dashboard
	var doc, created, updated string
	if err := r.Scan(&d.ID, &d.Name, &d.Description, &d.OwnerID, &d.Shared, &doc, &d.Version, &created, &updated); err != nil {
		return Dashboard{}, mapErr(err)
	}
	d.Doc = json.RawMessage(doc)
	d.CreatedAt, d.UpdatedAt = decodeTime(created), decodeTime(updated)
	return d, nil
}

// Create inserts a dashboard with version 1.
func (r *Dashboards) Create(ctx context.Context, d *Dashboard) error {
	if d.ID == "" {
		d.ID = NewID()
	}
	now := r.db.now()
	d.CreatedAt, d.UpdatedAt, d.Version = now, now, 1
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO dashboards (`+dashboardCols+`) VALUES (?,?,?,?,?,?,?,?,?)`,
		d.ID, d.Name, d.Description, d.OwnerID, boolInt(d.Shared), rawOrEmpty(d.Doc), d.Version, encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns a dashboard by id.
func (r *Dashboards) Get(ctx context.Context, id string) (Dashboard, error) {
	return scanDashboard(r.db.sql.QueryRowContext(ctx, `SELECT `+dashboardCols+` FROM dashboards WHERE id = ?`, id))
}

// ListVisible returns the dashboards owned by userID or shared.
func (r *Dashboards) ListVisible(ctx context.Context, userID string) ([]Dashboard, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+dashboardCols+` FROM dashboards WHERE owner_id = ? OR shared = 1 ORDER BY name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list dashboards: %w", err)
	}
	defer rows.Close()
	var out []Dashboard
	for rows.Next() {
		d, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Update writes a dashboard if d.Version matches the stored version (optimistic concurrency);
// on success the version is incremented. A mismatch returns ErrConflict.
func (r *Dashboards) Update(ctx context.Context, d *Dashboard) error {
	now := r.db.now()
	res, err := r.db.sql.ExecContext(ctx, `UPDATE dashboards SET name = ?, description = ?, shared = ?, doc = ?,
		version = version + 1, updated_at = ? WHERE id = ? AND version = ?`,
		d.Name, d.Description, boolInt(d.Shared), rawOrEmpty(d.Doc), encodeTime(now), d.ID, d.Version)
	if err := versionedResult(ctx, res, err, func() error { _, e := r.Get(ctx, d.ID); return e }); err != nil {
		return err
	}
	d.Version++
	d.UpdatedAt = now
	return nil
}

// Delete removes a dashboard.
func (r *Dashboards) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM dashboards WHERE id = ?`, id))
}

// versionedResult distinguishes "not found" from "version mismatch" after a guarded update.
func versionedResult(_ context.Context, res interface{ RowsAffected() (int64, error) }, err error, exists func() error) error {
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n > 0 {
		return nil
	}
	if err := exists(); err != nil {
		return err
	}
	return fmt.Errorf("%w: version mismatch", ErrConflict)
}

func rawOrEmpty(r json.RawMessage) string {
	if len(r) == 0 {
		return "{}"
	}
	return string(r)
}
