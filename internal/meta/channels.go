// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Channel is an alert notification channel. Secret fields inside Config are stored encrypted
// by the channel service (SPEC §15.5); this repository stores the JSON as given.
type Channel struct {
	ID        string
	Name      string
	Kind      string
	Config    json.RawMessage
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Channels is the channels repository.
type Channels struct{ db *DB }

// NewChannels returns the channels repository.
func NewChannels(db *DB) *Channels { return &Channels{db: db} }

const channelCols = `id, name, kind, config, version, created_at, updated_at`

func scanChannel(r rowScanner) (Channel, error) {
	var c Channel
	var cfg, created, updated string
	if err := r.Scan(&c.ID, &c.Name, &c.Kind, &cfg, &c.Version, &created, &updated); err != nil {
		return Channel{}, mapErr(err)
	}
	c.Config = json.RawMessage(cfg)
	c.CreatedAt, c.UpdatedAt = decodeTime(created), decodeTime(updated)
	return c, nil
}

// Create inserts a channel with version 1.
func (r *Channels) Create(ctx context.Context, c *Channel) error {
	if c.ID == "" {
		c.ID = NewID()
	}
	now := r.db.now()
	c.CreatedAt, c.UpdatedAt, c.Version = now, now, 1
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO channels (`+channelCols+`) VALUES (?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Kind, rawOrEmpty(c.Config), c.Version, encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns a channel by id.
func (r *Channels) Get(ctx context.Context, id string) (Channel, error) {
	return scanChannel(r.db.sql.QueryRowContext(ctx, `SELECT `+channelCols+` FROM channels WHERE id = ?`, id))
}

// List returns all channels.
func (r *Channels) List(ctx context.Context) ([]Channel, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+channelCols+` FROM channels ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Update writes a channel with optimistic concurrency on Version.
func (r *Channels) Update(ctx context.Context, c *Channel) error {
	now := r.db.now()
	res, err := r.db.sql.ExecContext(ctx, `UPDATE channels SET name = ?, kind = ?, config = ?, version = version + 1,
		updated_at = ? WHERE id = ? AND version = ?`, c.Name, c.Kind, rawOrEmpty(c.Config), encodeTime(now), c.ID, c.Version)
	if err := versionedResult(ctx, res, err, func() error { _, e := r.Get(ctx, c.ID); return e }); err != nil {
		return err
	}
	c.Version++
	c.UpdatedAt = now
	return nil
}

// Delete removes a channel.
func (r *Channels) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id))
}
