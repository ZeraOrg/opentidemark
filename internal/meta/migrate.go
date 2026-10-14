// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrationFS embed.FS

// Migration timing and locking.
const (
	migrationTimeout = 5 * time.Minute
	// pgMigrationLockKey is the advisory lock id taken while migrating ("tmmig").
	pgMigrationLockKey = 0x746d6d6967
	schemaInfoRowID    = 1
	unknownBinary      = "unknown"
)

// ErrIncompatibleBinary is returned when the database was migrated by a newer release whose
// schema this binary does not support (downgrade protection, SPEC §8.5 item 4).
var ErrIncompatibleBinary = errors.New("metadata database requires a newer OpenTidemark binary")

var minBinaryPattern = regexp.MustCompile(`(?m)^--\s*min_compatible_binary:\s*(\S+)\s*$`)

// Migration is one numbered SQL file of a dialect.
type Migration struct {
	Version   int
	Name      string
	MinBinary string
	SQL       string
}

// MigrationState is the status of one known migration (tidemark migrate --status).
type MigrationState struct {
	Migration
	Applied   bool
	AppliedAt time.Time
}

// MigrationReport describes what Open did.
type MigrationReport struct {
	FromVersion int
	Applied     []int
	BackupPath  string
}

// SchemaInfo is the single schema_info row.
type SchemaInfo struct {
	SchemaVersion       int
	MinCompatibleBinary string
	LastMigratedBy      string
	LastMigratedAt      time.Time
}

// LoadMigrations returns the embedded migrations of a driver ordered by version.
func LoadMigrations(driver string) ([]Migration, error) {
	files, err := fs.Glob(migrationFS, "migrations/"+driver+"/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no migrations for driver %q", driver)
	}
	sort.Strings(files)
	out := make([]Migration, 0, len(files))
	for _, f := range files {
		body, err := migrationFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", f, err)
		}
		base := path.Base(f)
		num, _, _ := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version prefix", base)
		}
		m := minBinaryPattern.FindStringSubmatch(string(body))
		if m == nil {
			return nil, fmt.Errorf("migration %s: missing min_compatible_binary header", base)
		}
		out = append(out, Migration{Version: v, Name: base, MinBinary: m[1], SQL: string(body)})
	}
	return out, nil
}

// bootstrapSQL creates the bookkeeping tables (identical in both dialects).
var bootstrapSQL = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS schema_info (
		id                    INTEGER PRIMARY KEY CHECK (id = 1),
		schema_version        INTEGER NOT NULL,
		min_compatible_binary TEXT NOT NULL,
		last_migrated_by      TEXT NOT NULL,
		last_migrated_at      TEXT NOT NULL)`,
}

// migrate applies pending migrations under the migration lock, one transaction each, after a
// backup, refusing to run when the database needs a newer binary.
func (db *DB) migrate(ctx context.Context) error {
	if db.opts.skipMigrate {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()
	migrations := db.opts.migrations
	if migrations == nil {
		var err error
		if migrations, err = LoadMigrations(db.dialect.name()); err != nil {
			return err
		}
	}
	c, err := db.sql.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration connection: %w", err)
	}
	defer c.Close()
	unlock, err := db.lockMigrations(ctx, c)
	if err != nil {
		return err
	}
	defer unlock()
	for _, stmt := range bootstrapSQL {
		if _, err := c.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create migration bookkeeping: %w", err)
		}
	}
	return db.applyPending(ctx, c, migrations)
}

func (db *DB) applyPending(ctx context.Context, c *sql.Conn, migrations []Migration) error {
	if err := db.checkCompatible(ctx); err != nil {
		return err
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}
	var pending []Migration
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			pending = append(pending, m)
		}
	}
	db.report.FromVersion = maxVersion(applied)
	if len(pending) == 0 {
		return nil
	}
	if len(applied) > 0 && db.opts.backupDir != "" {
		p, err := db.backupBeforeMigration(ctx, db.report.FromVersion)
		if err != nil {
			return fmt.Errorf("pre-migration backup failed; refusing to migrate: %w", err)
		}
		db.report.BackupPath = p
	}
	for _, m := range pending {
		if err := db.applyMigration(ctx, c, m); err != nil {
			return err
		}
		db.report.Applied = append(db.report.Applied, m.Version)
	}
	return nil
}

// lockMigrations takes the PostgreSQL advisory lock; SQLite serializes through BEGIN IMMEDIATE
// (the database file lock) and re-checks each version inside its transaction.
func (db *DB) lockMigrations(ctx context.Context, c *sql.Conn) (func(), error) {
	if db.dialect.name() != DriverPostgres {
		return func() {}, nil
	}
	if _, err := c.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, int64(pgMigrationLockKey)); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pingTimeout)
		defer cancel()
		_, _ = c.ExecContext(uctx, `SELECT pg_advisory_unlock($1)`, int64(pgMigrationLockKey)) // released with the session anyway
	}, nil
}

func (db *DB) applyMigration(ctx context.Context, c *sql.Conn, m Migration) error {
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", m.Name, err)
	}
	t := &Tx{tx: tx, d: db.dialect}
	if err := db.applyInTx(ctx, t, m); err != nil {
		_ = tx.Rollback() // the migration error is what the operator needs
		return fmt.Errorf("migration %s failed and was rolled back: %w", m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.Name, err)
	}
	return nil
}

func (db *DB) applyInTx(ctx context.Context, t *Tx, m Migration) error {
	var n int
	if err := t.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, m.Version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil // applied concurrently by another process
	}
	if _, err := t.tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	now := encodeTime(db.now())
	if _, err := t.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.Version, now); err != nil {
		return err
	}
	info, err := readSchemaInfo(ctx, t)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	info.SchemaVersion = max(info.SchemaVersion, m.Version)
	if CompareVersions(m.MinBinary, info.MinCompatibleBinary) > 0 || info.MinCompatibleBinary == "" {
		info.MinCompatibleBinary = m.MinBinary
	}
	by := db.opts.binaryVersion
	if by == "" {
		by = unknownBinary
	}
	_, err = t.ExecContext(ctx, `INSERT INTO schema_info (id, schema_version, min_compatible_binary, last_migrated_by,
		last_migrated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET schema_version = excluded.schema_version,
		min_compatible_binary = excluded.min_compatible_binary, last_migrated_by = excluded.last_migrated_by,
		last_migrated_at = excluded.last_migrated_at`,
		schemaInfoRowID, info.SchemaVersion, info.MinCompatibleBinary, by, now)
	return err
}

// queryer is satisfied by *Tx and *conn.
type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func readSchemaInfo(ctx context.Context, q queryer) (SchemaInfo, error) {
	var s SchemaInfo
	var at string
	err := q.QueryRowContext(ctx, `SELECT schema_version, min_compatible_binary, last_migrated_by, last_migrated_at
		FROM schema_info WHERE id = ?`, schemaInfoRowID).Scan(&s.SchemaVersion, &s.MinCompatibleBinary, &s.LastMigratedBy, &at)
	if err != nil {
		return SchemaInfo{}, mapErr(err)
	}
	s.LastMigratedAt = decodeTime(at)
	return s, nil
}

// SchemaInfo returns the schema_info row (ErrNotFound before the first migration).
func (db *DB) SchemaInfo(ctx context.Context) (SchemaInfo, error) {
	ok, err := db.tableExists(ctx, "schema_info")
	if err != nil || !ok {
		return SchemaInfo{}, errors.Join(ErrNotFound, err)
	}
	return readSchemaInfo(ctx, db.sql)
}

// checkCompatible refuses to run when the database requires a newer binary.
func (db *DB) checkCompatible(ctx context.Context) error {
	info, err := db.SchemaInfo(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return CheckBinaryCompatible(db.opts.binaryVersion, info.MinCompatibleBinary)
}

// CheckBinaryCompatible returns ErrIncompatibleBinary when binary is older than required.
// Development builds ("dev", non-semver) are always allowed.
func CheckBinaryCompatible(binary, required string) error {
	if !validVersion(binary) || !validVersion(required) {
		return nil
	}
	if CompareVersions(binary, required) < 0 {
		return fmt.Errorf("%w: database needs >= %s, this binary is %s (restore a backup or upgrade the binary)",
			ErrIncompatibleBinary, required, binary)
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]time.Time, error) {
	out := map[int]time.Time{}
	ok, err := db.tableExists(ctx, "schema_migrations")
	if err != nil || !ok {
		return out, err
	}
	rows, err := db.sql.QueryContext(ctx, `SELECT version, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		var at string
		if err := rows.Scan(&v, &at); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		out[v] = decodeTime(at)
	}
	return out, rows.Err()
}

// MigrationStatus lists every known migration with its applied state, plus versions found
// in the database that this binary does not know (Name empty).
func (db *DB) MigrationStatus(ctx context.Context) ([]MigrationState, error) {
	known, err := LoadMigrations(db.dialect.name())
	if err != nil {
		return nil, err
	}
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]MigrationState, 0, len(known))
	for _, m := range known {
		at, ok := applied[m.Version]
		out = append(out, MigrationState{Migration: m, Applied: ok, AppliedAt: at})
		delete(applied, m.Version)
	}
	for v, at := range applied {
		out = append(out, MigrationState{Migration: Migration{Version: v}, Applied: true, AppliedAt: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func (db *DB) tableExists(ctx context.Context, name string) (bool, error) {
	q := `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`
	if db.dialect.name() == DriverPostgres {
		q = `SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`
	}
	var n int
	if err := db.sql.QueryRowContext(ctx, q, name).Scan(&n); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return n > 0, nil
}

func maxVersion(m map[int]time.Time) int {
	v := 0
	for k := range m {
		v = max(v, k)
	}
	return v
}

// CompareVersions compares two semantic versions (an optional "v" prefix and any pre-release
// or build suffix are ignored). Invalid versions compare as equal.
func CompareVersions(a, b string) int {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return 0
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func validVersion(v string) bool { _, ok := parseVersion(v); return ok }

func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != len(out) {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
