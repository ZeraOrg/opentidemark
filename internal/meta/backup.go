// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Backup settings (SPEC §8.5 item 3).
const (
	DefaultKeepBackups = 10
	backupDirPerm      = 0o700
	backupFilePerm     = 0o600
	backupTimeLayout   = "20060102T150405Z"
	backupPrefix       = "meta-"
	exportFormat       = "tidemark-meta-export/1"
)

// errIntegrity marks a backup that failed PRAGMA integrity_check.
var errIntegrity = errors.New("integrity check failed")

// ErrNotEmpty is returned by ImportJSON when the target already holds users.
var ErrNotEmpty = errors.New("target metadata database is not empty")

// exportTables lists every metadata table in foreign-key order (parents first).
var exportTables = []string{
	"users", "sessions", "connections", "api_tokens", "signals", "dashboards", "alert_rules", "alert_history",
	"channels", "streams", "indexed_fields", "purge_jobs", "audit_log", "settings", "leases", "import_jobs",
	"schema_migrations", "schema_info",
}

// bookkeepingTables are exported for reference but never imported (the target is migrated by
// the running binary).
var bookkeepingTables = map[string]bool{"schema_migrations": true, "schema_info": true}

var columnPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// Export is the logical JSON export of the metadata (PostgreSQL backups, `tidemark backup`).
type Export struct {
	Format     string                      `json:"format"`
	Driver     string                      `json:"driver"`
	ExportedAt time.Time                   `json:"exportedAt"`
	Tables     map[string][]map[string]any `json:"tables"`
}

// backupBeforeMigration writes meta-<fromVersion>-<UTC>.db (SQLite, VACUUM INTO) or .json
// (PostgreSQL, logical export) and prunes old backups.
func (db *DB) backupBeforeMigration(ctx context.Context, fromVersion int) (string, error) {
	p, err := db.Backup(ctx, db.opts.backupDir, fmt.Sprintf("%04d", fromVersion))
	if err != nil {
		return "", err
	}
	if err := PruneBackups(db.opts.backupDir, db.opts.maxBackups); err != nil {
		return p, err
	}
	return p, nil
}

// Backup writes a consistent copy of the metadata into dir and returns its path: SQLite uses
// VACUUM INTO, PostgreSQL a JSON export. label is placed after the "meta-" prefix.
func (db *DB) Backup(ctx context.Context, dir, label string) (string, error) {
	if err := os.MkdirAll(dir, backupDirPerm); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	stamp := db.now().UTC().Format(backupTimeLayout)
	if db.dialect.name() == DriverSQLite {
		p := filepath.Join(dir, backupPrefix+label+"-"+stamp+".db")
		if _, err := db.sql.ExecContext(ctx, `VACUUM INTO ?`, p); err != nil {
			return "", fmt.Errorf("vacuum into %s: %w", p, err)
		}
		return p, os.Chmod(p, backupFilePerm)
	}
	p := filepath.Join(dir, backupPrefix+label+"-"+stamp+".json")
	return p, db.exportToFile(ctx, p)
}

func (db *DB) exportToFile(ctx context.Context, p string) (err error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFilePerm)
	if err != nil {
		return fmt.Errorf("create export: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	w := bufio.NewWriter(f)
	if err := db.ExportJSON(ctx, w); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("write export: %w", err)
	}
	return f.Sync()
}

// ExportJSON writes every metadata table as JSON.
func (db *DB) ExportJSON(ctx context.Context, w io.Writer) error {
	out := Export{Format: exportFormat, Driver: db.dialect.name(), ExportedAt: db.now(), Tables: map[string][]map[string]any{}}
	for _, t := range exportTables {
		ok, err := db.tableExists(ctx, t)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		rows, err := db.dumpTable(ctx, t)
		if err != nil {
			return err
		}
		out.Tables[t] = rows
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode export: %w", err)
	}
	return nil
}

func (db *DB) dumpTable(ctx context.Context, table string) ([]map[string]any, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT * FROM `+quoteIdent(table))
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("export %s columns: %w", table, err)
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("export %s row: %w", table, err)
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				vals[i] = string(b)
			}
			row[c] = vals[i]
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// ImportJSON restores an Export into this (migrated, empty) database in one transaction.
func (db *DB) ImportJSON(ctx context.Context, r io.Reader) error {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var in Export
	if err := dec.Decode(&in); err != nil {
		return fmt.Errorf("decode export: %w", err)
	}
	if in.Format != exportFormat {
		return fmt.Errorf("unsupported export format %q", in.Format)
	}
	var users int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if users > 0 {
		return ErrNotEmpty
	}
	return db.inTx(ctx, func(tx *Tx) error {
		for _, t := range exportTables {
			if bookkeepingTables[t] {
				continue
			}
			for _, row := range in.Tables[t] {
				if err := insertRow(ctx, tx, t, row); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func insertRow(ctx context.Context, tx *Tx, table string, row map[string]any) error {
	cols := make([]string, 0, len(row))
	for c := range row {
		if !columnPattern.MatchString(c) {
			return fmt.Errorf("import %s: invalid column %q", table, c)
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	args := make([]any, len(cols))
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
		args[i] = importValue(row[c])
	}
	q := `INSERT INTO ` + quoteIdent(table) + ` (` + strings.Join(quoted, ", ") + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ") + `)`
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("import %s: %w", table, err)
	}
	return nil
}

// importValue converts JSON numbers back to int64 or float64.
func importValue(v any) any {
	n, ok := v.(json.Number)
	if !ok {
		return v
	}
	if i, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return i
	}
	f, err := n.Float64()
	if err != nil {
		return n.String()
	}
	return f
}

// PruneBackups keeps the newest keep "meta-*" backups in dir.
func PruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	type backup struct {
		name string
		mod  time.Time
	}
	var all []backup
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), backupPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return fmt.Errorf("stat backup: %w", err)
		}
		all = append(all, backup{name: e.Name(), mod: info.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].mod.Equal(all[j].mod) {
			return all[i].name > all[j].name
		}
		return all[i].mod.After(all[j].mod)
	})
	for i := keep; i < len(all); i++ {
		if err := os.Remove(filepath.Join(dir, all[i].name)); err != nil {
			return fmt.Errorf("remove old backup: %w", err)
		}
	}
	return nil
}

// RestoreSQLiteFile replaces the SQLite database at dst with the backup at src. The server
// must be stopped; the WAL/SHM side files of dst are removed first.
func RestoreSQLiteFile(src, dst string) error {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(src)+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open backup: %w", err)
	}
	var check string
	err = db.QueryRow(`PRAGMA integrity_check`).Scan(&check)
	_ = db.Close() // read-only handle; the integrity result is what matters
	if err != nil || check != "ok" {
		return fmt.Errorf("backup %s failed the integrity check (%s): %w", src, check, errors.Join(err, errIntegrity))
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dst + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", dst+suffix, err)
		}
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read backup: %w", err)
	}
	return writeFileSync(dst, raw)
}

func writeFileSync(p string, data []byte) (err error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, backupFilePerm)
	if err != nil {
		return fmt.Errorf("open %s: %w", p, err)
	}
	defer func() {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return f.Sync()
}
