// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// Lease is one leader-election row (SPEC §17.5).
type Lease struct {
	Name      string
	Holder    string
	ExpiresAt time.Time
}

// Leases is the leases repository. Times come from the caller's clock so that every node uses
// the same comparison basis (node clocks must be in sync; `tidemark doctor` checks the skew).
type Leases struct{ db *DB }

// NewLeases returns the leases repository.
func NewLeases(db *DB) *Leases { return &Leases{db: db} }

// TryAcquire takes or renews the lease name for holder until now+ttl. It succeeds when the
// lease is free, expired or already held by holder; the conditional UPDATE guarantees that at
// most one holder wins.
func (r *Leases) TryAcquire(ctx context.Context, name, holder string, now time.Time, ttl time.Duration) (bool, error) {
	exp, at := encodeTime(now.Add(ttl)), encodeTime(now)
	res, err := r.db.sql.ExecContext(ctx, `UPDATE leases SET holder = ?, expires_at = ?, updated_at = ?
		WHERE name = ? AND (holder = ? OR expires_at <= ?)`, holder, exp, at, name, holder, at)
	if err != nil {
		return false, fmt.Errorf("renew lease %s: %w", name, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, fmt.Errorf("renew lease %s: %w", name, err)
	} else if n == 1 {
		return true, nil
	}
	res, err = r.db.sql.ExecContext(ctx, `INSERT INTO leases (name, holder, expires_at, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO NOTHING`, name, holder, exp, at)
	if err != nil {
		return false, fmt.Errorf("create lease %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("create lease %s: %w", name, err)
	}
	return n == 1, nil
}

// Release gives the lease up early (only when holder still owns it).
func (r *Leases) Release(ctx context.Context, name, holder string, now time.Time) error {
	_, err := r.db.sql.ExecContext(ctx, `UPDATE leases SET expires_at = ?, updated_at = ? WHERE name = ? AND holder = ?`,
		encodeTime(time.Unix(0, 0)), encodeTime(now), name, holder)
	return mapErr(err)
}

// List returns every lease ordered by name.
func (r *Leases) List(ctx context.Context) ([]Lease, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT name, holder, expires_at FROM leases ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()
	var out []Lease
	for rows.Next() {
		var l Lease
		var exp string
		if err := rows.Scan(&l.Name, &l.Holder, &exp); err != nil {
			return nil, mapErr(err)
		}
		l.ExpiresAt = decodeTime(exp)
		out = append(out, l)
	}
	return out, rows.Err()
}
