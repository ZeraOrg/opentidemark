// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
)

// Notify sends a PostgreSQL NOTIFY on channel (cross-node cache invalidation, SPEC §17.5).
// With SQLite there are no other nodes and Notify does nothing.
func (db *DB) Notify(ctx context.Context, channel, payload string) error {
	if db.dialect.name() != DriverPostgres {
		return nil
	}
	if _, err := db.sql.ExecContext(ctx, `SELECT pg_notify(?, ?)`, channel, payload); err != nil {
		return fmt.Errorf("notify %s: %w", channel, err)
	}
	return nil
}
