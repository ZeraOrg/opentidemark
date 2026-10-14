// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
)

// Backup layout (SPEC §8.5 item 8).
const (
	BackupFormat       = "tidemark-backup/1"
	backupDirPrefix    = "tidemark-backup-"
	backupStampLayout  = "20060102T150405Z"
	backupManifestName = "manifest.json"
	backupExportName   = "meta.json"
	backupNativeLabel  = "native"
	chBackupDisk       = "backups"
	backupDirMode      = 0o700
	backupFileMode     = 0o600
)

var (
	chTablePattern      = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)
	chBackupNamePattern = regexp.MustCompile(`^tidemark-[0-9]{8}T[0-9]{6}Z$`)
	// ErrBadBackup is returned for a backup directory whose manifest is missing or invalid.
	ErrBadBackup = errors.New("invalid backup")
)

// BackupManifest describes one backup directory (manifest.json).
type BackupManifest struct {
	Format     string            `json:"format"`
	Version    string            `json:"version"`
	CreatedAt  time.Time         `json:"createdAt"`
	NodeID     string            `json:"nodeId"`
	Meta       BackupMeta        `json:"meta"`
	ClickHouse *BackupClickHouse `json:"clickhouse,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// BackupMeta is the metadata part: a logical JSON export (always) and, for SQLite, a native
// VACUUM INTO copy.
type BackupMeta struct {
	Driver        string `json:"driver"`
	SchemaVersion int    `json:"schemaVersion"`
	Export        string `json:"export"`
	NativeFile    string `json:"nativeFile,omitempty"`
}

// BackupClickHouse is the telemetry part: a ClickHouse BACKUP on the "backups" disk.
type BackupClickHouse struct {
	Database string   `json:"database"`
	Disk     string   `json:"disk"`
	Name     string   `json:"name"`
	Tables   []string `json:"tables"`
}

// BackupOptions select what Backup writes.
type BackupOptions struct {
	Out      string // parent directory; the backup is a new subdirectory
	MetaOnly bool   // skip ClickHouse
}

// Backup writes <out>/tidemark-backup-<UTC stamp>/ with the metadata (JSON export, plus the
// SQLite file) and, unless MetaOnly, a ClickHouse BACKUP of every table when a "backups" disk is
// configured. It returns the backup directory.
func (o *Ops) Backup(ctx context.Context, opts BackupOptions) (string, error) {
	now := time.Now().UTC()
	stamp := now.Format(backupStampLayout)
	dir := filepath.Join(opts.Out, backupDirPrefix+stamp)
	if err := os.MkdirAll(dir, backupDirMode); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	m := BackupManifest{Format: BackupFormat, Version: o.version, CreatedAt: now, NodeID: o.cfg.NodeID}
	if err := o.backupMeta(ctx, dir, &m.Meta); err != nil {
		return dir, err
	}
	if !opts.MetaOnly {
		chm, warn, err := o.backupClickHouse(ctx, "tidemark-"+stamp)
		if err != nil {
			return dir, err
		}
		m.ClickHouse = chm
		if warn != "" {
			m.Warnings = append(m.Warnings, warn)
			o.printf("warning: %s\n", warn)
		}
	}
	if err := writeJSONFile(filepath.Join(dir, backupManifestName), m); err != nil {
		return dir, err
	}
	o.printf("backup written to %s\n", dir)
	return dir, nil
}

func (o *Ops) backupMeta(ctx context.Context, dir string, bm *BackupMeta) error {
	db, err := o.openMeta(ctx, false)
	if err != nil {
		return err
	}
	defer closeQuietly(db)
	bm.Driver = db.Driver()
	if info, err := db.SchemaInfo(ctx); err == nil {
		bm.SchemaVersion = info.SchemaVersion
	}
	if bm.Driver == meta.DriverSQLite {
		p, err := db.Backup(ctx, dir, backupNativeLabel)
		if err != nil {
			return err
		}
		bm.NativeFile = filepath.Base(p)
	}
	bm.Export = backupExportName
	f, err := os.OpenFile(filepath.Join(dir, backupExportName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFileMode)
	if err != nil {
		return fmt.Errorf("create metadata export: %w", err)
	}
	w := bufio.NewWriter(f)
	err = db.ExportJSON(ctx, w)
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// backupClickHouse runs BACKUP when the "backups" disk exists; otherwise it returns a warning.
func (o *Ops) backupClickHouse(ctx context.Context, name string) (*BackupClickHouse, string, error) {
	conn, err := o.openWriter(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("clickhouse (use --meta-only to skip it): %w", err)
	}
	defer closeQuietly(conn)
	qctx, cancel := context.WithTimeout(ctx, opsQueryTimeout)
	defer cancel()
	var disks uint64
	if err := conn.QueryRow(qctx, "SELECT count() FROM system.disks WHERE name = ?", chBackupDisk).Scan(&disks); err != nil {
		return nil, "", fmt.Errorf("check backup disk: %w", err)
	}
	if disks == 0 {
		return nil, "ClickHouse has no 'backups' disk configured; telemetry was not backed up (see docs)", nil
	}
	db := o.cfg.ClickHouse.Database
	tables, err := chTables(qctx, conn, db)
	if err != nil {
		return nil, "", err
	}
	if len(tables) == 0 {
		return nil, "the ClickHouse database has no tables; telemetry was not backed up", nil
	}
	bctx, bcancel := context.WithTimeout(ctx, clickhouseBackupTimeout)
	defer bcancel()
	if err := conn.Exec(bctx, "BACKUP "+tableList(db, tables, "")+" TO Disk('"+chBackupDisk+"', '"+name+"')"); err != nil {
		return nil, "", fmt.Errorf("clickhouse backup: %w", err)
	}
	o.printf("clickhouse: %d tables backed up to disk %q as %s\n", len(tables), chBackupDisk, name)
	return &BackupClickHouse{Database: db, Disk: chBackupDisk, Name: name, Tables: tables}, "", nil
}

// chMigrationsTable is not backed up: restoring into a database the server already migrated
// must not collide with its bookkeeping, and migrations are idempotent.
const chMigrationsTable = "schema_migrations"

// chTables lists the MergeTree-family data tables of db (identifiers validated before use in
// DDL).
func chTables(ctx context.Context, conn driver.Conn, db string) ([]string, error) {
	rows, err := conn.Query(ctx, "SELECT name FROM system.tables WHERE database = ? AND engine LIKE '%MergeTree' ORDER BY name", db)
	if err != nil {
		return nil, fmt.Errorf("list clickhouse tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("list clickhouse tables: %w", err)
		}
		if chTablePattern.MatchString(n) && n != chMigrationsTable {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// tableList renders "TABLE db.t1, TABLE db.t2" (with " AS target.t" when target is set).
func tableList(db string, tables []string, target string) string {
	parts := make([]string, len(tables))
	for i, t := range tables {
		parts[i] = "TABLE " + db + "." + t
		if target != "" {
			parts[i] += " AS " + target + "." + t
		}
	}
	return strings.Join(parts, ", ")
}

func writeJSONFile(p string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(p), err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFileMode)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(p), err)
	}
	_, err = f.Write(append(raw, '\n'))
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// ReadBackupManifest loads and validates dir/manifest.json; every identifier that will be
// used in SQL is checked against an allow-list pattern.
func ReadBackupManifest(dir string) (BackupManifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, backupManifestName))
	if err != nil {
		return BackupManifest{}, fmt.Errorf("%w: %w", ErrBadBackup, err)
	}
	var m BackupManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return BackupManifest{}, fmt.Errorf("%w: manifest: %w", ErrBadBackup, err)
	}
	if m.Format != BackupFormat {
		return BackupManifest{}, fmt.Errorf("%w: unsupported format %q", ErrBadBackup, m.Format)
	}
	if m.Meta.Export != backupExportName || (m.Meta.NativeFile != "" && m.Meta.NativeFile != filepath.Base(m.Meta.NativeFile)) {
		return BackupManifest{}, fmt.Errorf("%w: metadata file names", ErrBadBackup)
	}
	if c := m.ClickHouse; c != nil {
		if !ch.ValidDatabase(c.Database) || c.Disk != chBackupDisk || !chBackupNamePattern.MatchString(c.Name) || len(c.Tables) == 0 {
			return BackupManifest{}, fmt.Errorf("%w: clickhouse section", ErrBadBackup)
		}
		for _, t := range c.Tables {
			if !chTablePattern.MatchString(t) {
				return BackupManifest{}, fmt.Errorf("%w: table name %q", ErrBadBackup, t)
			}
		}
	}
	return m, nil
}
