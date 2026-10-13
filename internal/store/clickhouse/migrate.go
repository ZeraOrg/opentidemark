// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const migrationTimeout = 5 * time.Minute

// Migration is one numbered SQL file.
type Migration struct {
	Version    uint32
	Name       string
	Statements []string
}

// LoadMigrations parses the embedded migrations for database db (the {db} placeholder).
func LoadMigrations(db string) ([]Migration, error) {
	if !ValidDatabase(db) {
		return nil, ErrInvalidDatabase
	}
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)
	out := make([]Migration, 0, len(files))
	for _, f := range files {
		body, err := migrationFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		base := path.Base(f)
		num, _, _ := strings.Cut(base, "_")
		v, err := strconv.ParseUint(num, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("migration %s: bad version prefix", base)
		}
		out = append(out, Migration{Version: uint32(v), Name: base, Statements: SplitStatements(strings.ReplaceAll(string(body), "{db}", db))})
	}
	return out, nil
}

// SplitStatements removes "--" comment lines and splits on semicolons at line ends.
func SplitStatements(sql string) []string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, part := range strings.Split(b.String(), ";\n") {
		if s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(part), ";")); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Migrator applies migrations with the writer connection.
type Migrator struct {
	conn    driver.Conn
	db      string
	cluster string
}

// MigratorOption configures a Migrator.
type MigratorOption func(*Migrator)

// WithCluster renders every CREATE statement ON CLUSTER with replicated engines (SPEC §17.5).
func WithCluster(name string) MigratorOption { return func(m *Migrator) { m.cluster = name } }

// NewMigrator returns a migrator for database db.
func NewMigrator(conn driver.Conn, db string, opts ...MigratorOption) *Migrator {
	m := &Migrator{conn: conn, db: db}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Plan returns the migrations with every statement rendered as it would be executed (cluster
// mode applied); used by Up and by `tidemark migrate --dry-run`.
func (m *Migrator) Plan() ([]Migration, error) {
	migrations, err := LoadMigrations(m.db)
	if err != nil {
		return nil, err
	}
	for i := range migrations {
		for j, stmt := range migrations[i].Statements {
			r, err := RenderForCluster(stmt, m.cluster)
			if err != nil {
				return nil, fmt.Errorf("migration %s: %w", migrations[i].Name, err)
			}
			migrations[i].Statements[j] = r
		}
	}
	return migrations, nil
}

// bootstrap creates the database and the schema_migrations table.
func (m *Migrator) bootstrap(ctx context.Context) error {
	stmts := []string{"CREATE DATABASE IF NOT EXISTS " + m.db,
		"CREATE TABLE IF NOT EXISTS " + m.db + ".schema_migrations (version UInt32, applied_at DateTime) ENGINE = MergeTree ORDER BY version"}
	for _, s := range stmts {
		r, err := RenderForCluster(s, m.cluster)
		if err != nil {
			return err
		}
		if err := m.conn.Exec(ctx, r); err != nil {
			return fmt.Errorf("create migration bookkeeping: %w", err)
		}
	}
	return nil
}

// Pending returns the versions not yet applied (database and table are created if missing).
func (m *Migrator) Pending(ctx context.Context) ([]Migration, error) {
	migrations, err := m.Plan()
	if err != nil {
		return nil, err
	}
	if err := m.bootstrap(ctx); err != nil {
		return nil, err
	}
	applied, err := m.applied(ctx)
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, mig := range migrations {
		if !applied[mig.Version] {
			out = append(out, mig)
		}
	}
	return out, nil
}

// Up creates the database and applies pending migrations in order, recording each in
// {db}.schema_migrations. It returns the versions applied.
func (m *Migrator) Up(ctx context.Context) ([]uint32, error) {
	ctx, cancel := context.WithTimeout(ctx, migrationTimeout)
	defer cancel()
	pending, err := m.Pending(ctx)
	if err != nil {
		return nil, err
	}
	var done []uint32
	for _, mig := range pending {
		for i, stmt := range mig.Statements {
			if err := m.conn.Exec(ctx, stmt); err != nil {
				return done, fmt.Errorf("migration %s statement %d: %w", mig.Name, i+1, err)
			}
		}
		if err := m.conn.Exec(ctx, "INSERT INTO "+m.db+".schema_migrations (version, applied_at) VALUES (?, now())", mig.Version); err != nil {
			return done, fmt.Errorf("record migration %s: %w", mig.Name, err)
		}
		done = append(done, mig.Version)
	}
	return done, nil
}

func (m *Migrator) applied(ctx context.Context) (map[uint32]bool, error) {
	rows, err := m.conn.Query(ctx, "SELECT version FROM "+m.db+".schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	out := map[uint32]bool{}
	for rows.Next() {
		var v uint32
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		out[v] = true
	}
	return out, rows.Err()
}

// MigrationStatus is one migration with its applied state (tidemark migrate --status).
type MigrationStatus struct {
	Migration
	Applied bool
}

// Status lists every migration (rendered for the configured mode) and whether it is applied,
// without creating the database or the bookkeeping table.
func (m *Migrator) Status(ctx context.Context) ([]MigrationStatus, error) {
	plan, err := m.Plan()
	if err != nil {
		return nil, err
	}
	var n uint64
	if err := m.conn.QueryRow(ctx, "SELECT count() FROM system.tables WHERE database = ? AND name = 'schema_migrations'",
		m.db).Scan(&n); err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	applied := map[uint32]bool{}
	if n > 0 {
		if applied, err = m.applied(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]MigrationStatus, len(plan))
	for i, mig := range plan {
		out[i] = MigrationStatus{Migration: mig, Applied: applied[mig.Version]}
	}
	return out, nil
}
