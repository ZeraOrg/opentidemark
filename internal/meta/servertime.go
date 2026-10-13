// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"time"
)

// ServerTime returns the database server's clock (PostgreSQL only; ok is false for SQLite,
// which shares the node's clock). Used by `tidemark doctor` to report clock skew, which matters
// for leases (SPEC §17.5).
func (db *DB) ServerTime(ctx context.Context) (t time.Time, ok bool, err error) {
	if db.dialect.name() != DriverPostgres {
		return time.Time{}, false, nil
	}
	var s string
	if err := db.sql.QueryRowContext(ctx, `SELECT to_char(clock_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')`).Scan(&s); err != nil {
		return time.Time{}, false, fmt.Errorf("server time: %w", err)
	}
	t, err = time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("server time %q: %w", s, err)
	}
	return t, true, nil
}
