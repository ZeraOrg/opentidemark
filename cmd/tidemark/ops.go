// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ZeraOrg/opentidemark/internal/app"
	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/logging"
)

// cmdFlags are the command's own flags: name ⇒ takes a value. Everything else is passed to
// config.Load (so --data-dir, --clickhouse-addr … work with every command).
type cmdFlags map[string]bool

// split separates the command's own flags (--name value, --name=value, --switch) from the
// configuration flags.
func (f cmdFlags) split(args []string) (map[string]string, []string, error) {
	own, rest := map[string]string{}, []string(nil)
	for i := 0; i < len(args); i++ {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		takesValue, ok := f[name]
		switch {
		case !strings.HasPrefix(args[i], "--") || !ok:
			rest = append(rest, args[i])
		case !takesValue && hasValue:
			return nil, nil, fmt.Errorf("--%s takes no value", name)
		case !takesValue:
			own[name] = "true"
		case hasValue:
			own[name] = value
		case i+1 < len(args):
			own[name] = args[i+1]
			i++
		default:
			return nil, nil, fmt.Errorf("--%s needs a value", name)
		}
	}
	return own, rest, nil
}

// withOps loads the configuration and runs fn with operator commands that stop on SIGINT/SIGTERM.
func withOps(rest []string, stdout, stderr io.Writer, fn func(ctx context.Context, o *app.Ops) error) error {
	cfg, err := config.Load(rest, config.OSEnv{})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fn(ctx, app.NewOps(cfg, version, stdout, logging.New(stderr, cfg.LogLevel)))
}

// backupCommand implements "backup --out <dir> [--meta-only]".
func backupCommand(args []string, stdout, stderr io.Writer) error {
	own, rest, err := cmdFlags{"out": true, "meta-only": false}.split(args)
	if err != nil {
		return err
	}
	if own["out"] == "" {
		return errors.New("usage: tidemark backup --out <dir> [--meta-only]")
	}
	return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error {
		_, err := o.Backup(ctx, app.BackupOptions{Out: own["out"], MetaOnly: own["meta-only"] == "true"})
		return err
	})
}

// restoreCommand implements "restore --from <backup dir> [--force] [--with-clickhouse]".
func restoreCommand(args []string, stdout, stderr io.Writer) error {
	own, rest, err := cmdFlags{"from": true, "force": false, "with-clickhouse": false}.split(args)
	if err != nil {
		return err
	}
	if own["from"] == "" {
		return errors.New("usage: tidemark restore --from <backup dir> [--force] [--with-clickhouse] (stop the server first)")
	}
	return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error {
		return o.Restore(ctx, app.RestoreOptions{From: own["from"], Force: own["force"] == "true",
			WithClickHouse: own["with-clickhouse"] == "true"})
	})
}

// migrateCommand implements "migrate [--status|--dry-run]".
func migrateCommand(args []string, stdout, stderr io.Writer) error {
	own, rest, err := cmdFlags{"status": false, "dry-run": false}.split(args)
	if err != nil {
		return err
	}
	mode := app.MigrateApply
	switch {
	case own["status"] != "" && own["dry-run"] != "":
		return errors.New("use either --status or --dry-run")
	case own["status"] != "":
		mode = app.MigrateStatus
	case own["dry-run"] != "":
		mode = app.MigrateDryRun
	}
	return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error { return o.Migrate(ctx, mode) })
}

// doctorCommand implements "doctor [--json]".
func doctorCommand(args []string, stdout, stderr io.Writer) error {
	own, rest, err := cmdFlags{"json": false}.split(args)
	if err != nil {
		return err
	}
	return withOps(rest, stdout, stderr, func(ctx context.Context, o *app.Ops) error {
		_, err := o.Doctor(ctx, own["json"] == "true")
		return err
	})
}
