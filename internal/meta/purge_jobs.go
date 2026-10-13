// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// Purge job statuses.
const (
	PurgePending = "pending"
	PurgeRunning = "running"
	PurgeDone    = "done"
	PurgeFailed  = "failed"
)

// PurgeJob records a delete request (SPEC §12.5).
type PurgeJob struct {
	ID           string
	Stream       string
	Source       string
	TQL          string
	From         time.Time
	To           time.Time
	Status       string
	RowsEstimate int64
	RequestedBy  string
	MutationID   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// PurgeJobs is the purge_jobs repository.
type PurgeJobs struct{ db *DB }

// NewPurgeJobs returns the purge_jobs repository.
func NewPurgeJobs(db *DB) *PurgeJobs { return &PurgeJobs{db: db} }

// Create inserts a purge job.
func (r *PurgeJobs) Create(ctx context.Context, j *PurgeJob) error {
	if j.ID == "" {
		j.ID = NewID()
	}
	now := r.db.now()
	j.CreatedAt, j.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO purge_jobs (id, stream, source, tql, "from", "to", status,
		rows_estimate, requested_by, mutation_id, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, j.Stream, j.Source, j.TQL, encodeTime(j.From), encodeTime(j.To), j.Status, j.RowsEstimate,
		j.RequestedBy, j.MutationID, encodeTime(now), encodeTime(now))
	return mapErr(err)
}

const purgeCols = `id, stream, source, tql, "from", "to", status, rows_estimate, requested_by, mutation_id, created_at, updated_at`

func scanPurgeJob(r rowScanner) (PurgeJob, error) {
	var j PurgeJob
	var from, to, created, updated string
	if err := r.Scan(&j.ID, &j.Stream, &j.Source, &j.TQL, &from, &to, &j.Status, &j.RowsEstimate, &j.RequestedBy,
		&j.MutationID, &created, &updated); err != nil {
		return PurgeJob{}, mapErr(err)
	}
	j.From, j.To, j.CreatedAt, j.UpdatedAt = decodeTime(from), decodeTime(to), decodeTime(created), decodeTime(updated)
	return j, nil
}

// Get returns a purge job by id.
func (r *PurgeJobs) Get(ctx context.Context, id string) (PurgeJob, error) {
	return scanPurgeJob(r.db.sql.QueryRowContext(ctx, `SELECT `+purgeCols+` FROM purge_jobs WHERE id = ?`, id))
}

// ListByStatus returns the jobs in one status, oldest first.
func (r *PurgeJobs) ListByStatus(ctx context.Context, status string) ([]PurgeJob, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+purgeCols+` FROM purge_jobs WHERE status = ? ORDER BY created_at`, status)
	if err != nil {
		return nil, fmt.Errorf("list purge jobs: %w", err)
	}
	defer rows.Close()
	var out []PurgeJob
	for rows.Next() {
		j, err := scanPurgeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// SetStatus updates status and mutation id.
func (r *PurgeJobs) SetStatus(ctx context.Context, id, status, mutationID string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE purge_jobs SET status = ?, mutation_id = ?, updated_at = ? WHERE id = ?`,
		status, mutationID, encodeTime(r.db.now()), id))
}
