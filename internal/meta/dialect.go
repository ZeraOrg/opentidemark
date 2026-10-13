// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Dialect names (TIDEMARK_META_DRIVER).
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// pgUniqueViolation is the SQLSTATE of a unique constraint violation.
const pgUniqueViolation = "23505"

// dialect is the small set of differences between SQLite and PostgreSQL (SPEC §8.4). Repository
// SQL is written once with "?" placeholders and the shared subset of both dialects.
type dialect interface {
	name() string
	rebind(query string) string
	isUniqueViolation(err error) bool
}

type sqliteDialect struct{}

func (sqliteDialect) name() string           { return DriverSQLite }
func (sqliteDialect) rebind(q string) string { return q }
func (sqliteDialect) isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

type postgresDialect struct{}

func (postgresDialect) name() string { return DriverPostgres }

// rebind turns "?" placeholders into $1, $2 … (quoted strings and identifiers are skipped).
func (postgresDialect) rebind(q string) string {
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	var quote byte
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func (postgresDialect) isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == pgUniqueViolation
}

// conn wraps *sql.DB so repositories keep the database/sql method names while queries are
// rewritten for the active dialect.
type conn struct {
	db *sql.DB
	d  dialect
}

// ExecContext runs a statement.
func (c *conn) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, c.d.rebind(q), args...)
}

// QueryContext runs a query.
func (c *conn) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, c.d.rebind(q), args...)
}

// QueryRowContext runs a single-row query.
func (c *conn) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, c.d.rebind(q), args...)
}

// Close closes the pool.
func (c *conn) Close() error { return c.db.Close() }

// Tx is a transaction with dialect-aware statements.
type Tx struct {
	tx *sql.Tx
	d  dialect
}

// ExecContext runs a statement in the transaction.
func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.d.rebind(q), args...)
}

// QueryContext runs a query in the transaction.
func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, t.d.rebind(q), args...)
}

// QueryRowContext runs a single-row query in the transaction.
func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, t.d.rebind(q), args...)
}
