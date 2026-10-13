// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LevelRule is a per-stream retention override for some levels (SPEC §12.3).
type LevelRule struct {
	Levels []string `json:"levels"`
	Days   int      `json:"days"`
}

// Stream is a named dataset with retention settings. Nil retention values use the defaults.
type Stream struct {
	ID                   string
	Name                 string
	Description          string
	RetentionDays        *int
	LevelRules           []LevelRule
	SpansRetentionDays   *int
	MetricsRetentionDays *int
	Paused               bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Streams is the streams repository.
type Streams struct{ db *DB }

// NewStreams returns the streams repository.
func NewStreams(db *DB) *Streams { return &Streams{db: db} }

const streamCols = `id, name, description, retention_days, level_rules, spans_retention_days, metrics_retention_days,
	paused, created_at, updated_at`

func scanStream(r rowScanner) (Stream, error) {
	var s Stream
	var days, spans, metrics sql.NullInt64
	var rules, created, updated string
	if err := r.Scan(&s.ID, &s.Name, &s.Description, &days, &rules, &spans, &metrics, &s.Paused, &created, &updated); err != nil {
		return Stream{}, mapErr(err)
	}
	s.RetentionDays, s.SpansRetentionDays, s.MetricsRetentionDays = intPtr(days), intPtr(spans), intPtr(metrics)
	s.CreatedAt, s.UpdatedAt = decodeTime(created), decodeTime(updated)
	if err := unmarshalAll(jsonField{rules, &s.LevelRules}); err != nil {
		return Stream{}, fmt.Errorf("stream %s: %w", s.Name, err)
	}
	return s, nil
}

// Create inserts a stream.
func (r *Streams) Create(ctx context.Context, s *Stream) error {
	if s.ID == "" {
		s.ID = NewID()
	}
	now := r.db.now()
	s.CreatedAt, s.UpdatedAt = now, now
	rules, err := marshalJSON(nonNilRules(s.LevelRules))
	if err != nil {
		return err
	}
	_, err = r.db.sql.ExecContext(ctx, `INSERT INTO streams (`+streamCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.Description, s.RetentionDays, rules, s.SpansRetentionDays, s.MetricsRetentionDays,
		boolInt(s.Paused), encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// GetByName returns a stream by name.
func (r *Streams) GetByName(ctx context.Context, name string) (Stream, error) {
	return scanStream(r.db.sql.QueryRowContext(ctx, `SELECT `+streamCols+` FROM streams WHERE name = ?`, name))
}

// List returns all streams ordered by name.
func (r *Streams) List(ctx context.Context) ([]Stream, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+streamCols+` FROM streams ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list streams: %w", err)
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		s, err := scanStream(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Update writes the editable fields of a stream.
func (r *Streams) Update(ctx context.Context, s *Stream) error {
	s.UpdatedAt = r.db.now()
	rules, err := marshalJSON(nonNilRules(s.LevelRules))
	if err != nil {
		return err
	}
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE streams SET description = ?, retention_days = ?, level_rules = ?,
		spans_retention_days = ?, metrics_retention_days = ?, paused = ?, updated_at = ? WHERE name = ?`,
		s.Description, s.RetentionDays, rules, s.SpansRetentionDays, s.MetricsRetentionDays, boolInt(s.Paused),
		encodeTime(s.UpdatedAt), s.Name))
}

// Delete removes a stream by name.
func (r *Streams) Delete(ctx context.Context, name string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM streams WHERE name = ?`, name))
}

func intPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func nonNilRules(r []LevelRule) []LevelRule {
	if r == nil {
		return []LevelRule{}
	}
	return r
}
