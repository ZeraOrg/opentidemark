// SPDX-License-Identifier: Apache-2.0

// Package meta stores Tidemark metadata (users, sessions, connections, …) in SQLite (single node)
// or PostgreSQL (HA) (SPEC §8.4). Repositories are written once against a small dialect
// abstraction; migrations are per dialect with identical version numbers, transactional, locked,
// backed up beforehand and guarded against downgrades (SPEC §8.5).
package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	// Register the pgx database/sql driver ("pgx") for PostgreSQL.
	_ "github.com/jackc/pgx/v5/stdlib"
	// Register the pure-Go "sqlite3" database/sql driver and its embedded SQLite build.
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// Sentinel errors returned by repositories.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// DBFileName is the SQLite file name inside the data directory.
const DBFileName = "tidemark.db"

const (
	busyTimeoutMillis = 10000
	maxOpenConns      = 4
	pgMaxOpenConns    = 16
	pgConnMaxIdle     = 5 * time.Minute
	pingTimeout       = 5 * time.Second
	timeLayout        = "2006-01-02T15:04:05.000000000Z07:00"
)

// DB wraps the metadata database handle.
type DB struct {
	sql     *conn
	now     func() time.Time
	dialect dialect
	opts    openOptions
	report  MigrationReport
}

// openOptions are set through Option values.
type openOptions struct {
	binaryVersion string
	backupDir     string
	skipMigrate   bool
	maxBackups    int
	migrations    []Migration // test override of the embedded migrations
}

// Option configures a DB.
type Option func(*DB)

// WithClock sets the clock used for created_at/updated_at values.
func WithClock(now func() time.Time) Option {
	return func(db *DB) { db.now = func() time.Time { return now().UTC() } }
}

// WithBinaryVersion sets the running binary's version for the downgrade check (SPEC §8.5 item 4).
// Unset, "dev" or non-semver versions skip the check.
func WithBinaryVersion(v string) Option { return func(db *DB) { db.opts.binaryVersion = v } }

// WithBackupDir enables the automatic pre-migration backup into dir (SPEC §8.5 item 3).
func WithBackupDir(dir string) Option { return func(db *DB) { db.opts.backupDir = dir } }

// WithoutMigrations opens the database without applying pending migrations (operator tooling:
// `tidemark migrate --status|--dry-run`, `doctor`).
func WithoutMigrations() Option { return func(db *DB) { db.opts.skipMigrate = true } }

// Open opens (creating if needed) the SQLite metadata database at path and applies migrations.
func Open(ctx context.Context, path string, opts ...Option) (*DB, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?" + url.Values{
		"_pragma": {
			"busy_timeout(" + strconv.Itoa(busyTimeoutMillis) + ")",
			"journal_mode(wal)",
			"foreign_keys(on)",
			"synchronous(normal)",
		},
		"_txlock": {"immediate"},
	}.Encode()
	sqldb, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	sqldb.SetMaxOpenConns(maxOpenConns)
	return finishOpen(ctx, sqldb, sqliteDialect{}, opts)
}

// OpenPostgres connects to PostgreSQL (TIDEMARK_META_DSN) and applies migrations.
func OpenPostgres(ctx context.Context, dsn string, opts ...Option) (*DB, error) {
	sqldb, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres metadata db: %w", err)
	}
	sqldb.SetMaxOpenConns(pgMaxOpenConns)
	sqldb.SetConnMaxIdleTime(pgConnMaxIdle)
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := sqldb.PingContext(pctx); err != nil {
		_ = sqldb.Close() // the ping error is the one worth reporting
		return nil, fmt.Errorf("connect postgres metadata db: %w", err)
	}
	return finishOpen(ctx, sqldb, postgresDialect{}, opts)
}

func finishOpen(ctx context.Context, sqldb *sql.DB, d dialect, opts []Option) (*DB, error) {
	db := &DB{sql: &conn{db: sqldb, d: d}, dialect: d, now: func() time.Time { return time.Now().UTC() },
		opts: openOptions{maxBackups: DefaultKeepBackups}}
	for _, o := range opts {
		o(db)
	}
	if err := db.migrate(ctx); err != nil {
		_ = sqldb.Close() // the migration error is the one worth reporting
		return nil, err
	}
	return db, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.sql.Close() }

// Driver returns "sqlite" or "postgres".
func (db *DB) Driver() string { return db.dialect.name() }

// Migrations reports what Open did (applied versions, backup path).
func (db *DB) Migrations() MigrationReport { return db.report }

// Ping checks that the database is reachable (readiness probe).
func (db *DB) Ping(ctx context.Context) error {
	var one int
	if err := db.sql.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("ping metadata db: %w", err)
	}
	return nil
}

// NewID returns a new UUIDv7 string id.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}

// inTx runs fn in a transaction, committing on success.
func (db *DB) inTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := db.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(&Tx{tx: tx, d: db.dialect}); err != nil {
		_ = tx.Rollback() // the original error is returned; rollback failure adds nothing actionable
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// encodeTime formats t as fixed-width RFC 3339 in UTC so values sort lexically.
func encodeTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func encodeTimePtr(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return encodeTime(*t)
}

func decodeTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func decodeTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t := decodeTime(ns.String)
	return &t
}

// mapErr converts driver errors of either dialect into package sentinels.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case sqliteDialect{}.isUniqueViolation(err), postgresDialect{}.isUniqueViolation(err):
		return fmt.Errorf("%w: %s", ErrConflict, err.Error())
	default:
		return err
	}
}

// expectOne returns ErrNotFound when an update/delete touched no rows.
func expectOne(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// boolInt converts a bool to the 0/1 storage form.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// quoteIdent quotes a trusted identifier from an allow-list for both dialects.
func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
