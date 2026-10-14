// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Connection is a named ingestion source with its own API key (SPEC §8.4, §15.3).
type Connection struct {
	ID                string
	Name              string
	Description       string
	KeyPrefix         string
	KeyHash           string
	PrevKeyHash       string     // previous key during a rotation grace period ("" if none)
	PrevKeyExpiresAt  *time.Time // end of the grace period
	Protocols         []string
	DefaultStream     string
	AllowedCIDRs      []string
	AllowedStreams    []string // streams the connection may write to; empty = any (SPEC §9.1)
	MinimumLevel      string   // "" = accept all levels
	AppliedProperties map[string]any
	RateLimitEPS      int
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
	LastUsedIP        string
	EventsReceived    int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Connections is the connections repository.
type Connections struct{ db *DB }

// NewConnections returns the connections repository.
func NewConnections(db *DB) *Connections { return &Connections{db: db} }

const connCols = `id, name, description, key_prefix, key_hash, prev_key_hash, prev_key_expires_at, protocols,
	default_stream, allowed_cidrs, minimum_level, applied_properties, rate_limit_eps, expires_at, revoked_at,
	last_used_at, last_used_ip, events_received, created_at, updated_at, allowed_streams`

func scanConnection(r rowScanner) (Connection, error) {
	var c Connection
	var prevHash, prevExp, expires, revoked, lastUsed sql.NullString
	var protocols, cidrs, applied, created, updated, streams string
	err := r.Scan(&c.ID, &c.Name, &c.Description, &c.KeyPrefix, &c.KeyHash, &prevHash, &prevExp, &protocols,
		&c.DefaultStream, &cidrs, &c.MinimumLevel, &applied, &c.RateLimitEPS, &expires, &revoked, &lastUsed,
		&c.LastUsedIP, &c.EventsReceived, &created, &updated, &streams)
	if err != nil {
		return Connection{}, mapErr(err)
	}
	c.PrevKeyHash = prevHash.String
	c.PrevKeyExpiresAt, c.ExpiresAt = decodeTimePtr(prevExp), decodeTimePtr(expires)
	c.RevokedAt, c.LastUsedAt = decodeTimePtr(revoked), decodeTimePtr(lastUsed)
	c.CreatedAt, c.UpdatedAt = decodeTime(created), decodeTime(updated)
	if err := unmarshalAll(
		jsonField{protocols, &c.Protocols}, jsonField{cidrs, &c.AllowedCIDRs}, jsonField{applied, &c.AppliedProperties},
		jsonField{streams, &c.AllowedStreams},
	); err != nil {
		return Connection{}, fmt.Errorf("connection %s: %w", c.ID, err)
	}
	return c, nil
}

// Create inserts a connection.
func (r *Connections) Create(ctx context.Context, c *Connection) error {
	if c.ID == "" {
		c.ID = NewID()
	}
	now := r.db.now()
	c.CreatedAt, c.UpdatedAt = now, now
	cols, err := c.jsonColumns()
	if err != nil {
		return err
	}
	_, err = r.db.sql.ExecContext(ctx, `INSERT INTO connections (`+connCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.Description, c.KeyPrefix, c.KeyHash, nullString(c.PrevKeyHash), encodeTimePtr(c.PrevKeyExpiresAt),
		cols.protocols, c.DefaultStream, cols.cidrs, c.MinimumLevel, cols.applied, c.RateLimitEPS, encodeTimePtr(c.ExpiresAt),
		encodeTimePtr(c.RevokedAt), encodeTimePtr(c.LastUsedAt), c.LastUsedIP, c.EventsReceived,
		encodeTime(now), encodeTime(now), cols.streams)
	return mapErr(err)
}

// connJSON holds the JSON-encoded columns of a connection.
type connJSON struct{ protocols, cidrs, applied, streams string }

func (c *Connection) jsonColumns() (connJSON, error) {
	var out connJSON
	props := c.AppliedProperties
	if props == nil {
		props = map[string]any{}
	}
	for _, f := range []struct {
		dst *string
		v   any
	}{
		{&out.protocols, nonNilStrings(c.Protocols)}, {&out.cidrs, nonNilStrings(c.AllowedCIDRs)},
		{&out.applied, props}, {&out.streams, nonNilStrings(c.AllowedStreams)},
	} {
		s, err := marshalJSON(f.v)
		if err != nil {
			return connJSON{}, err
		}
		*f.dst = s
	}
	return out, nil
}

// Get returns the connection with the given id.
func (r *Connections) Get(ctx context.Context, id string) (Connection, error) {
	return scanConnection(r.db.sql.QueryRowContext(ctx, `SELECT `+connCols+` FROM connections WHERE id = ?`, id))
}

// GetByName returns the connection with the given name.
func (r *Connections) GetByName(ctx context.Context, name string) (Connection, error) {
	return scanConnection(r.db.sql.QueryRowContext(ctx, `SELECT `+connCols+` FROM connections WHERE lower(name) = lower(?)`, name))
}

// GetByKeyHash finds a connection by its current key hash or by a previous key hash whose
// grace period has not ended at now.
func (r *Connections) GetByKeyHash(ctx context.Context, hash string, now time.Time) (Connection, error) {
	return scanConnection(r.db.sql.QueryRowContext(ctx, `SELECT `+connCols+` FROM connections
		WHERE key_hash = ? OR (prev_key_hash = ? AND prev_key_expires_at > ?)`, hash, hash, encodeTime(now)))
}

// List returns all connections ordered by name.
func (r *Connections) List(ctx context.Context) ([]Connection, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+connCols+` FROM connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Count returns the number of connections.
func (r *Connections) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM connections`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count connections: %w", err)
	}
	return n, nil
}

// Update writes the editable settings of a connection (not its keys or usage).
func (r *Connections) Update(ctx context.Context, c *Connection) error {
	c.UpdatedAt = r.db.now()
	cols, err := c.jsonColumns()
	if err != nil {
		return err
	}
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE connections SET name = ?, description = ?, protocols = ?,
		default_stream = ?, allowed_cidrs = ?, minimum_level = ?, applied_properties = ?, rate_limit_eps = ?,
		expires_at = ?, allowed_streams = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Description, cols.protocols, c.DefaultStream, cols.cidrs, c.MinimumLevel, cols.applied, c.RateLimitEPS,
		encodeTimePtr(c.ExpiresAt), cols.streams, encodeTime(c.UpdatedAt), c.ID))
}

// RotateKey replaces the key; the previous hash stays valid until graceUntil (nil = no grace).
func (r *Connections) RotateKey(ctx context.Context, id, prefix, hash string, graceUntil *time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE connections SET
		prev_key_hash = CASE WHEN CAST(? AS TEXT) IS NULL THEN NULL ELSE key_hash END,
		prev_key_expires_at = ?, key_prefix = ?, key_hash = ?, updated_at = ? WHERE id = ?`,
		encodeTimePtr(graceUntil), encodeTimePtr(graceUntil), prefix, hash, encodeTime(r.db.now()), id))
}

// Revoke marks the connection revoked at the given time.
func (r *Connections) Revoke(ctx context.Context, id string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE connections SET revoked_at = ?, prev_key_hash = NULL,
		prev_key_expires_at = NULL, updated_at = ? WHERE id = ?`, encodeTime(at), encodeTime(r.db.now()), id))
}

// AddUsage records last-used time/IP and adds n to the received-events counter.
func (r *Connections) AddUsage(ctx context.Context, id string, at time.Time, ip string, n int64) error {
	_, err := r.db.sql.ExecContext(ctx, `UPDATE connections SET last_used_at = ?, last_used_ip = ?,
		events_received = events_received + ? WHERE id = ?`, encodeTime(at), ip, n, id)
	return mapErr(err)
}

// Delete removes a connection.
func (r *Connections) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id))
}

// jsonField pairs a stored JSON text with its destination.
type jsonField struct {
	text string
	dst  any
}

func unmarshalAll(fields ...jsonField) error {
	for _, f := range fields {
		if f.text == "" {
			continue
		}
		if err := json.Unmarshal([]byte(f.text), f.dst); err != nil {
			return fmt.Errorf("decode stored json: %w", err)
		}
	}
	return nil
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode json: %w", err)
	}
	return string(b), nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
