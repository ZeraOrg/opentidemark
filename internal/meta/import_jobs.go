// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Import job statuses (SPEC §9.9).
const (
	ImportQueued    = "queued"
	ImportRunning   = "running"
	ImportCompleted = "completed"
	ImportFailed    = "failed"
	ImportCancelled = "cancelled"
)

// ImportJob is one importer run with its resumable checkpoint.
type ImportJob struct {
	ID              string
	Kind            string
	Name            string
	Config          json.RawMessage // secrets encrypted by the service (SPEC §15.5)
	Status          string
	Checkpoint      json.RawMessage
	RowsImported    int64
	RowsTotal       int64
	Errors          int64
	LastError       string
	Warnings        []string
	CancelRequested bool
	RequestedBy     string
	StartedAt       *time.Time
	FinishedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ImportProgress is the mutable progress of a running job.
type ImportProgress struct {
	Checkpoint   json.RawMessage
	RowsImported int64
	RowsTotal    int64
	Errors       int64
	LastError    string
}

// ImportJobs is the import_jobs repository.
type ImportJobs struct{ db *DB }

// NewImportJobs returns the import jobs repository.
func NewImportJobs(db *DB) *ImportJobs { return &ImportJobs{db: db} }

const importCols = `id, kind, name, config, status, checkpoint, rows_imported, rows_total, errors, last_error, warnings,
	cancel_requested, requested_by, started_at, finished_at, created_at, updated_at`

func scanImportJob(r rowScanner) (ImportJob, error) {
	var j ImportJob
	var cfg, cp, warnings, created, updated string
	var started, finished sql.NullString
	err := r.Scan(&j.ID, &j.Kind, &j.Name, &cfg, &j.Status, &cp, &j.RowsImported, &j.RowsTotal, &j.Errors, &j.LastError,
		&warnings, &j.CancelRequested, &j.RequestedBy, &started, &finished, &created, &updated)
	if err != nil {
		return ImportJob{}, mapErr(err)
	}
	j.Config, j.Checkpoint = json.RawMessage(cfg), json.RawMessage(cp)
	j.StartedAt, j.FinishedAt = decodeTimePtr(started), decodeTimePtr(finished)
	j.CreatedAt, j.UpdatedAt = decodeTime(created), decodeTime(updated)
	if err := unmarshalAll(jsonField{warnings, &j.Warnings}); err != nil {
		return ImportJob{}, fmt.Errorf("import job %s: %w", j.ID, err)
	}
	return j, nil
}

// Create inserts a queued job.
func (r *ImportJobs) Create(ctx context.Context, j *ImportJob) error {
	if j.ID == "" {
		j.ID = NewID()
	}
	if j.Status == "" {
		j.Status = ImportQueued
	}
	now := r.db.now()
	j.CreatedAt, j.UpdatedAt = now, now
	warnings, err := marshalJSON(nonNilStrings(j.Warnings))
	if err != nil {
		return err
	}
	_, err = r.db.sql.ExecContext(ctx, `INSERT INTO import_jobs (`+importCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Kind, j.Name, rawOrEmpty(j.Config), j.Status, rawOrEmpty(j.Checkpoint), j.RowsImported, j.RowsTotal,
		j.Errors, j.LastError, warnings, boolInt(j.CancelRequested), j.RequestedBy, encodeTimePtr(j.StartedAt),
		encodeTimePtr(j.FinishedAt), encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns one job.
func (r *ImportJobs) Get(ctx context.Context, id string) (ImportJob, error) {
	return scanImportJob(r.db.sql.QueryRowContext(ctx, `SELECT `+importCols+` FROM import_jobs WHERE id = ?`, id))
}

// List returns the newest jobs first (at most limit).
func (r *ImportJobs) List(ctx context.Context, limit int) ([]ImportJob, error) {
	return r.query(ctx, `SELECT `+importCols+` FROM import_jobs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// ListRunnable returns queued and running jobs, oldest first.
func (r *ImportJobs) ListRunnable(ctx context.Context) ([]ImportJob, error) {
	return r.query(ctx, `SELECT `+importCols+` FROM import_jobs WHERE status IN ('queued', 'running') ORDER BY created_at, id`)
}

func (r *ImportJobs) query(ctx context.Context, q string, args ...any) ([]ImportJob, error) {
	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list import jobs: %w", err)
	}
	defer rows.Close()
	var out []ImportJob
	for rows.Next() {
		j, err := scanImportJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MarkRunning sets status running (keeping the first started_at).
func (r *ImportJobs) MarkRunning(ctx context.Context, id string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET status = ?, started_at = COALESCE(started_at, ?),
		updated_at = ? WHERE id = ?`, ImportRunning, encodeTime(at), encodeTime(r.db.now()), id))
}

// SaveProgress stores the checkpoint and counters after a committed page.
func (r *ImportJobs) SaveProgress(ctx context.Context, id string, p ImportProgress) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET checkpoint = ?, rows_imported = ?, rows_total = ?,
		errors = ?, last_error = ?, updated_at = ? WHERE id = ?`, rawOrEmpty(p.Checkpoint), p.RowsImported, p.RowsTotal,
		p.Errors, p.LastError, encodeTime(r.db.now()), id))
}

// SetWarnings replaces the job's warnings.
func (r *ImportJobs) SetWarnings(ctx context.Context, id string, warnings []string) error {
	w, err := marshalJSON(nonNilStrings(warnings))
	if err != nil {
		return err
	}
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET warnings = ?, updated_at = ? WHERE id = ?`,
		w, encodeTime(r.db.now()), id))
}

// Finish sets a terminal status.
func (r *ImportJobs) Finish(ctx context.Context, id, status, lastError string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET status = ?, last_error = ?, finished_at = ?,
		updated_at = ? WHERE id = ?`, status, lastError, encodeTime(at), encodeTime(r.db.now()), id))
}

// RequestCancel flags a job for cancellation; a queued job is cancelled immediately.
func (r *ImportJobs) RequestCancel(ctx context.Context, id string, at time.Time) error {
	if err := expectOne(r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET cancel_requested = 1, updated_at = ? WHERE id = ?`,
		encodeTime(r.db.now()), id)); err != nil {
		return err
	}
	_, err := r.db.sql.ExecContext(ctx, `UPDATE import_jobs SET status = ?, finished_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, ImportCancelled, encodeTime(at), encodeTime(r.db.now()), id, ImportQueued)
	return mapErr(err)
}
