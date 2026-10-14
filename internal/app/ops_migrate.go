// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
)

// MigrateMode selects what `tidemark migrate` does.
type MigrateMode int

// Migrate modes.
const (
	MigrateApply  MigrateMode = iota // apply pending migrations (with backup, under the locks)
	MigrateStatus                    // list applied and pending migrations
	MigrateDryRun                    // print the SQL that would run; change nothing
)

// Migrate runs `tidemark migrate [--status|--dry-run]` for the metadata database and ClickHouse.
func (o *Ops) Migrate(ctx context.Context, mode MigrateMode) error {
	metaErr := o.migrateMeta(ctx, mode)
	if metaErr != nil {
		o.printf("metadata: error: %v\n", metaErr)
	}
	chErr := o.migrateClickHouse(ctx, mode)
	if chErr != nil {
		o.printf("clickhouse: error: %v\n", chErr)
	}
	return errors.Join(metaErr, chErr)
}

func (o *Ops) migrateMeta(ctx context.Context, mode MigrateMode) error {
	if mode == MigrateApply {
		db, err := o.openMeta(ctx, true)
		if err != nil {
			return err
		}
		defer closeQuietly(db)
		rep := db.Migrations()
		o.printf("metadata (%s): applied %v (backup: %s)\n", db.Driver(), rep.Applied, orNone(rep.BackupPath))
		return nil
	}
	db, err := o.openMeta(ctx, false)
	if errors.Is(err, ErrNoMetadata) {
		o.printf("metadata (sqlite): %v\n", err)
		return nil
	}
	if err != nil {
		return err
	}
	defer closeQuietly(db)
	return o.reportMeta(ctx, db, mode)
}

func (o *Ops) reportMeta(ctx context.Context, db *meta.DB, mode MigrateMode) error {
	states, err := db.MigrationStatus(ctx)
	if err != nil {
		return err
	}
	info, infoErr := db.SchemaInfo(ctx)
	o.printf("metadata (%s): schema version %d, requires binary >= %s, this binary %s\n",
		db.Driver(), info.SchemaVersion, orNone(info.MinCompatibleBinary), o.version)
	if infoErr == nil {
		if err := meta.CheckBinaryCompatible(o.version, info.MinCompatibleBinary); err != nil {
			o.printf("  %v\n", err)
		}
	}
	for _, s := range states {
		switch {
		case s.Applied && s.Name == "":
			o.printf("  %04d  applied %s  (unknown to this binary)\n", s.Version, s.AppliedAt.Format("2006-01-02 15:04:05Z"))
		case s.Applied:
			o.printf("  %04d  %-28s applied %s\n", s.Version, s.Name, s.AppliedAt.Format("2006-01-02 15:04:05Z"))
		case mode == MigrateDryRun:
			o.printf("  %04d  %-28s pending; would run:\n%s\n", s.Version, s.Name, indent(s.SQL))
		default:
			o.printf("  %04d  %-28s pending (min binary %s)\n", s.Version, s.Name, s.MinBinary)
		}
	}
	return nil
}

func (o *Ops) migrateClickHouse(ctx context.Context, mode MigrateMode) error {
	conn, err := o.openWriter(ctx)
	if err != nil {
		return err
	}
	defer closeQuietly(conn)
	c := o.cfg.ClickHouse
	if mode == MigrateApply {
		db, err := o.openMeta(ctx, true)
		if err != nil {
			return err
		}
		defer closeQuietly(db)
		a := &App{cfg: o.cfg, logger: o.logger, meta: db}
		if err := a.migrateClickHouse(ctx, conn); err != nil {
			return err
		}
	}
	states, err := ch.NewMigrator(conn, c.Database, ch.WithCluster(c.Cluster)).Status(ctx)
	if err != nil {
		return err
	}
	o.printf("clickhouse (database %s, cluster %s):\n", c.Database, orNone(c.Cluster))
	for _, s := range states {
		switch {
		case s.Applied:
			o.printf("  %04d  %-28s applied\n", s.Version, s.Name)
		case mode == MigrateDryRun:
			o.printf("  %04d  %-28s pending; would run:\n%s\n", s.Version, s.Name, indent(strings.Join(s.Statements, ";\n")+";"))
		default:
			o.printf("  %04d  %-28s pending\n", s.Version, s.Name)
		}
	}
	return nil
}

func indent(sql string) string {
	lines := strings.Split(strings.TrimSpace(sql), "\n")
	for i, l := range lines {
		lines[i] = "        " + l
	}
	return strings.Join(lines, "\n")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
