// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// ErrRestoreTargetExists is returned when restoring over existing SQLite metadata without
// --force.
var ErrRestoreTargetExists = errors.New("the metadata database already exists; stop the server and pass --force (a safety copy is kept)")

// RestoreOptions select what Restore does. The server must be stopped.
type RestoreOptions struct {
	From           string // backup directory (contains manifest.json)
	Force          bool   // replace an existing SQLite database (a safety copy is written first)
	WithClickHouse bool   // also RESTORE the ClickHouse tables (they must not hold data)
}

// Restore restores the metadata from a backup directory and, when asked, the ClickHouse
// tables. SQLite targets get the native copy when the backup was taken from SQLite, otherwise
// a fresh database plus the JSON export; PostgreSQL targets must be empty (meta.ErrNotEmpty).
// The restored database is then migrated to this binary's schema.
func (o *Ops) Restore(ctx context.Context, opts RestoreOptions) error {
	m, err := ReadBackupManifest(opts.From)
	if err != nil {
		return err
	}
	if opts.WithClickHouse && m.ClickHouse == nil {
		return fmt.Errorf("%w: the backup holds no ClickHouse data", ErrBadBackup)
	}
	if o.cfg.HA() {
		err = o.restorePostgres(ctx, opts.From)
	} else {
		err = o.restoreSQLite(ctx, opts, m)
	}
	if err != nil {
		return err
	}
	o.printf("metadata restored from %s (schema version %d, taken by %s)\n", opts.From, m.Meta.SchemaVersion, m.Version)
	if !opts.WithClickHouse {
		return nil
	}
	return o.restoreClickHouse(ctx, *m.ClickHouse)
}

func (o *Ops) restoreSQLite(ctx context.Context, opts RestoreOptions, m BackupManifest) error {
	dst := o.sqlitePath()
	if _, err := os.Stat(dst); err == nil {
		if !opts.Force {
			return ErrRestoreTargetExists
		}
		if err := o.safetyCopy(ctx); err != nil {
			return err
		}
	}
	if m.Meta.Driver == meta.DriverSQLite && m.Meta.NativeFile != "" {
		if err := meta.RestoreSQLiteFile(filepath.Join(opts.From, m.Meta.NativeFile), dst); err != nil {
			return err
		}
		db, err := o.openMeta(ctx, true) // migrates to this binary's schema, with downgrade protection
		if err != nil {
			return err
		}
		return db.Close()
	}
	for _, p := range []string{dst, dst + "-wal", dst + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	return o.importExport(ctx, opts.From)
}

func (o *Ops) restorePostgres(ctx context.Context, from string) error {
	return o.importExport(ctx, from)
}

// importExport migrates the (empty) target and imports meta.json into it.
func (o *Ops) importExport(ctx context.Context, from string) error {
	f, err := os.Open(filepath.Join(from, backupExportName))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadBackup, err)
	}
	defer closeQuietly(f)
	db, err := o.openMeta(ctx, true)
	if err != nil {
		return err
	}
	defer closeQuietly(db)
	if err := db.ImportJSON(ctx, f); err != nil {
		return fmt.Errorf("import metadata: %w", err)
	}
	return nil
}

// safetyCopy backs up the database about to be replaced into TIDEMARK_BACKUP_DIR.
func (o *Ops) safetyCopy(ctx context.Context) error {
	db, err := o.openMeta(ctx, false)
	if err != nil {
		return err
	}
	defer closeQuietly(db)
	p, err := db.Backup(ctx, o.cfg.BackupDir, "pre-restore")
	if err != nil {
		return fmt.Errorf("safety copy before restore: %w", err)
	}
	o.printf("existing metadata saved to %s\n", p)
	return nil
}

func (o *Ops) restoreClickHouse(ctx context.Context, b BackupClickHouse) error {
	conn, err := o.openWriter(ctx)
	if err != nil {
		return err
	}
	defer closeQuietly(conn)
	target := o.cfg.ClickHouse.Database
	rctx, cancel := context.WithTimeout(ctx, clickhouseBackupTimeout)
	defer cancel()
	stmt := "RESTORE " + tableList(b.Database, b.Tables, target) + " FROM Disk('" + chBackupDisk + "', '" + b.Name + "')"
	if err := conn.Exec(rctx, stmt); err != nil {
		return fmt.Errorf("clickhouse restore (target tables must be empty or absent): %w", err)
	}
	o.printf("clickhouse: %d tables restored into %s\n", len(b.Tables), target)
	return nil
}
