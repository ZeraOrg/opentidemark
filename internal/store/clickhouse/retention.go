// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/retention"
)

// ErrUnknownTable is returned for retention rules on tables outside the allow-list.
var ErrUnknownTable = errors.New("unknown table")

var retentionTables = []string{retention.TableEvents, retention.TableSpans, retention.TableMetrics}

// RetentionManager applies TTL rules idempotently (implements store.RetentionApplier).
type RetentionManager struct {
	conn driver.Conn
	db   string
}

// NewRetentionManager returns a manager for database db.
func NewRetentionManager(conn driver.Conn, db string) (*RetentionManager, error) {
	if !ValidDatabase(db) {
		return nil, ErrInvalidDatabase
	}
	return &RetentionManager{conn: conn, db: db}, nil
}

// ApplyRetention runs ALTER TABLE … MODIFY TTL for every rule whose TTL differs from the
// last applied one and returns nil when everything is up to date.
func (m *RetentionManager) ApplyRetention(ctx context.Context, rules []retention.Rule) error {
	_, err := m.Apply(ctx, rules)
	return err
}

// Apply is ApplyRetention returning the tables that were altered.
func (m *RetentionManager) Apply(ctx context.Context, rules []retention.Rule) ([]string, error) {
	current, err := m.current(ctx)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, r := range rules {
		if !slices.Contains(retentionTables, r.Table) {
			return changed, fmt.Errorf("%w: %q", ErrUnknownTable, r.Table)
		}
		if current[r.Table] == r.TTL {
			continue
		}
		if err := m.conn.Exec(ctx, "ALTER TABLE "+m.db+"."+r.Table+" MODIFY TTL "+r.TTL); err != nil {
			return changed, fmt.Errorf("modify TTL of %s: %w", r.Table, err)
		}
		if err := m.conn.Exec(ctx, "INSERT INTO "+m.db+".retention_state (tbl, ttl, applied_at) VALUES (?, ?, now64(3))",
			r.Table, r.TTL); err != nil {
			return changed, fmt.Errorf("record TTL of %s: %w", r.Table, err)
		}
		changed = append(changed, r.Table)
	}
	return changed, nil
}

func (m *RetentionManager) current(ctx context.Context) (map[string]string, error) {
	rows, err := m.conn.Query(ctx, "SELECT tbl, argMax(ttl, applied_at) FROM "+m.db+".retention_state GROUP BY tbl")
	if err != nil {
		return nil, fmt.Errorf("read retention state: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var t, ttl string
		if err := rows.Scan(&t, &ttl); err != nil {
			return nil, fmt.Errorf("scan retention state: %w", err)
		}
		out[t] = ttl
	}
	return out, rows.Err()
}
