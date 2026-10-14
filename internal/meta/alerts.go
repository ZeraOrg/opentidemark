// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// AlertRule is an alert rule with its evaluation state (SPEC §14.1).
type AlertRule struct {
	ID         string
	Name       string
	Enabled    bool
	Doc        json.RawMessage
	State      string
	StateSince *time.Time
	LastEvalAt *time.Time
	LastValue  *float64
	LastError  string
	EvalState  json.RawMessage // per-group evaluation state (alerting.RuleState)
	Streams    []string        // streams the rule reads; nil = every stream (SPEC §14.1, §15.10)
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// AlertHistory is one state transition of a rule.
type AlertHistory struct {
	ID        string
	RuleID    string
	At        time.Time
	FromState string
	ToState   string
	Value     *float64
	Message   string
	GroupKey  string
}

// AlertRules is the alert_rules + alert_history repository.
type AlertRules struct{ db *DB }

// NewAlertRules returns the alert repository.
func NewAlertRules(db *DB) *AlertRules { return &AlertRules{db: db} }

const ruleCols = `id, name, enabled, doc, state, state_since, last_eval_at, last_value, last_error, eval_state, version,
	created_at, updated_at, streams`

func scanRule(r rowScanner) (AlertRule, error) {
	var a AlertRule
	var doc, evalState, created, updated string
	var since, eval, streams sql.NullString
	var value sql.NullFloat64
	if err := r.Scan(&a.ID, &a.Name, &a.Enabled, &doc, &a.State, &since, &eval, &value, &a.LastError, &evalState, &a.Version,
		&created, &updated, &streams); err != nil {
		return AlertRule{}, mapErr(err)
	}
	if streams.Valid {
		a.Streams = []string{}
		if err := json.Unmarshal([]byte(streams.String), &a.Streams); err != nil {
			return AlertRule{}, fmt.Errorf("decode alert rule streams: %w", err)
		}
	}
	a.Doc, a.EvalState = json.RawMessage(doc), json.RawMessage(evalState)
	a.StateSince, a.LastEvalAt = decodeTimePtr(since), decodeTimePtr(eval)
	if value.Valid {
		v := value.Float64
		a.LastValue = &v
	}
	a.CreatedAt, a.UpdatedAt = decodeTime(created), decodeTime(updated)
	return a, nil
}

// Create inserts a rule in state "ok" with version 1.
func (r *AlertRules) Create(ctx context.Context, a *AlertRule) error {
	if a.ID == "" {
		a.ID = NewID()
	}
	if a.State == "" {
		a.State = "ok"
	}
	streams, err := ruleStreams(a.Streams)
	if err != nil {
		return err
	}
	now := r.db.now()
	a.CreatedAt, a.UpdatedAt, a.Version = now, now, 1
	_, err = r.db.sql.ExecContext(ctx, `INSERT INTO alert_rules (`+ruleCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, boolInt(a.Enabled), rawOrEmpty(a.Doc), a.State, encodeTimePtr(a.StateSince),
		encodeTimePtr(a.LastEvalAt), a.LastValue, a.LastError, rawOrEmpty(a.EvalState), a.Version, encodeTime(now), encodeTime(now),
		streams)
	return mapErr(err)
}

// Get returns a rule by id.
func (r *AlertRules) Get(ctx context.Context, id string) (AlertRule, error) {
	return scanRule(r.db.sql.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id = ?`, id))
}

// List returns all rules ordered by name.
func (r *AlertRules) List(ctx context.Context) ([]AlertRule, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()
	var out []AlertRule
	for rows.Next() {
		a, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ruleStreams encodes the streams column: NULL for every stream, else a JSON array.
func ruleStreams(streams []string) (any, error) {
	if streams == nil {
		return nil, nil
	}
	return marshalJSON(streams)
}

// Update writes the definition (name, enabled, doc, streams) with optimistic concurrency on
// Version.
func (r *AlertRules) Update(ctx context.Context, a *AlertRule) error {
	streams, err := ruleStreams(a.Streams)
	if err != nil {
		return err
	}
	now := r.db.now()
	res, err := r.db.sql.ExecContext(ctx, `UPDATE alert_rules SET name = ?, enabled = ?, doc = ?, streams = ?,
		version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, a.Name, boolInt(a.Enabled), rawOrEmpty(a.Doc), streams,
		encodeTime(now), a.ID, a.Version)
	if err := versionedResult(ctx, res, err, func() error { _, e := r.Get(ctx, a.ID); return e }); err != nil {
		return err
	}
	a.Version++
	a.UpdatedAt = now
	return nil
}

// SaveState records the evaluation outcome of a rule.
func (r *AlertRules) SaveState(ctx context.Context, a AlertRule) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE alert_rules SET state = ?, state_since = ?, last_eval_at = ?,
		last_value = ?, last_error = ?, eval_state = ? WHERE id = ?`, a.State, encodeTimePtr(a.StateSince),
		encodeTimePtr(a.LastEvalAt), a.LastValue, a.LastError, rawOrEmpty(a.EvalState), a.ID))
}

// Delete removes a rule (history cascades).
func (r *AlertRules) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id))
}

// AddHistory appends a state transition.
func (r *AlertRules) AddHistory(ctx context.Context, h *AlertHistory) error {
	if h.ID == "" {
		h.ID = NewID()
	}
	now := encodeTime(r.db.now())
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO alert_history (id, rule_id, at, from_state, to_state, value, message,
		group_key, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, h.ID, h.RuleID, encodeTime(h.At), h.FromState,
		h.ToState, h.Value, h.Message, h.GroupKey, now, now)
	return mapErr(err)
}

// History returns the newest transitions of a rule (at most limit).
func (r *AlertRules) History(ctx context.Context, ruleID string, limit int) ([]AlertHistory, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT id, rule_id, at, from_state, to_state, value, message, group_key
		FROM alert_history WHERE rule_id = ? ORDER BY at DESC, id DESC LIMIT ?`, ruleID, limit)
	if err != nil {
		return nil, fmt.Errorf("alert history: %w", err)
	}
	defer rows.Close()
	var out []AlertHistory
	for rows.Next() {
		var h AlertHistory
		var at string
		var value sql.NullFloat64
		if err := rows.Scan(&h.ID, &h.RuleID, &at, &h.FromState, &h.ToState, &value, &h.Message, &h.GroupKey); err != nil {
			return nil, mapErr(err)
		}
		h.At = decodeTime(at)
		if value.Valid {
			v := value.Float64
			h.Value = &v
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
