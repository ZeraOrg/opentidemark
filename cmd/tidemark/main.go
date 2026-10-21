// SPDX-License-Identifier: Apache-2.0

// Command tidemark runs the OpenTidemark server (SPEC §6.1):
//
//	tidemark [serve] [flags]                      run the server (default)
//	tidemark version                              print the version
//	tidemark admin reset-password --username X    reset a password (prints a generated one)
//	tidemark backup --out <dir> [--meta-only]     metadata export + ClickHouse BACKUP
//	tidemark restore --from <dir> [--force] [--with-clickhouse]
//	tidemark migrate [--status|--dry-run]         apply or inspect schema migrations
//	tidemark doctor [--json]                      connectivity, schema, WAL, clock and disk checks
//	tidemark import clef|elastic|seq|kibana …      import data (SPEC §9.9); list | status | cancel
//	tidemark update check [--json]                ask GitHub once for a newer release (SPEC §17.7)
//	tidemark update install                       Linux update helper (run by tidemark-update.service)
//	tidemark service install|uninstall|start|stop Windows service management
//	tidemark healthcheck [--url URL]              exit 0 when GET /healthz answers 200 (containers)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ZeraOrg/opentidemark/internal/app"
	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/logging"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

// distribution is set by the release builds with -ldflags "-X main.distribution=<archive|container>"
// (SPEC §17.6); any other build is a development build without update checks (SPEC §17.7).
var distribution = ""

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, app.ErrRestartForUpdate) {
			os.Exit(exitRestartForUpdate)
		}
		fmt.Fprintln(os.Stderr, "tidemark:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if isWindowsService() {
		return runService(args)
	}
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args, stderr)
	case "version":
		_, err := fmt.Fprintln(stdout, "tidemark", version)
		return err
	case "admin":
		return admin(args, stdin, stdout)
	case "service":
		return serviceCommand(args)
	case "backup":
		return backupCommand(args, stdout, stderr)
	case "restore":
		return restoreCommand(args, stdout, stderr)
	case "migrate":
		return migrateCommand(args, stdout, stderr)
	case "doctor":
		return doctorCommand(args, stdout, stderr)
	case "import":
		return importCommand(args, stdout, stderr)
	case "update":
		return updateCommand(args, stdout, stderr)
	case "healthcheck":
		return healthcheck(context.Background(), args, &http.Client{Timeout: healthTimeout})
	default:
		return fmt.Errorf("unknown command %q (use serve, version, admin, backup, restore, migrate, doctor, import, update, service or healthcheck)", cmd)
	}
}

// serve runs until SIGINT/SIGTERM.
func serve(args []string, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveContext(ctx, args, stderr, false)
}

// serveContext loads the configuration, builds the app and runs it until ctx ends; asService is
// true under the Windows Service Control Manager.
func serveContext(ctx context.Context, args []string, stderr io.Writer, asService bool) error {
	cfg, err := config.Load(args, config.OSEnv{})
	if err != nil {
		return err
	}
	logger := logging.New(stderr, cfg.LogLevel)
	build := app.BuildInfo{Version: version, Distribution: distribution, WindowsService: asService}
	a, err := app.New(ctx, cfg, build, logger)
	if err != nil {
		return err
	}
	return a.Run(ctx)
}

// admin implements "admin reset-password --username <name> [--password-stdin] [config flags]".
func admin(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "reset-password" {
		return errors.New("usage: tidemark admin reset-password --username <name> [--password-stdin]")
	}
	username, fromStdin, rest, err := parseResetFlags(args[1:])
	if err != nil {
		return err
	}
	cfg, err := config.Load(rest, config.OSEnv{})
	if err != nil {
		return err
	}
	password, generated, err := resetPassword(stdin, fromStdin)
	if err != nil {
		return err
	}
	ctx := context.Background()
	db, err := app.NewOps(cfg, version, io.Discard, logging.New(io.Discard, 0)).OpenMeta(ctx) // sqlite or postgres
	if err != nil {
		return err
	}
	defer db.Close()
	if err := newUserService(db, io.Discard).ResetPassword(ctx, username, password); err != nil {
		return err
	}
	if generated {
		_, err = fmt.Fprintf(stdout, "New password for %s (change it after signing in):\n%s\n", username, password)
		return err
	}
	_, err = fmt.Fprintf(stdout, "Password for %s reset; it must be changed at next sign-in.\n", username)
	return err
}

func parseResetFlags(args []string) (username string, fromStdin bool, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--username" && i+1 < len(args):
			username = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--username="):
			username = strings.TrimPrefix(args[i], "--username=")
		case args[i] == "--password-stdin":
			fromStdin = true
		default:
			rest = append(rest, args[i])
		}
	}
	if username == "" {
		return "", false, nil, errors.New("--username is required")
	}
	return username, fromStdin, rest, nil
}

// resetPassword reads a password from stdin or generates a random one that satisfies the policy.
func resetPassword(stdin io.Reader, fromStdin bool) (string, bool, error) {
	if fromStdin {
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		return strings.TrimRight(line, "\r\n"), false, nil
	}
	raw, err := auth.GenerateKey("")
	if err != nil {
		return "", false, err
	}
	return raw[:24], true, nil
}

func newUserService(db *meta.DB, logOut io.Writer) *service.Users {
	logger := logging.New(logOut, 0)
	users, sessions, tokens := meta.NewUsers(db), meta.NewSessions(db), meta.NewAPITokens(db)
	clock := auth.SystemClock{}
	return service.NewUsers(service.UsersDeps{
		Store: users, Tokens: tokens, Settings: meta.NewSettings(db),
		Sessions: auth.NewSessionManager(sessions, users, clock, 0, 0),
		Hasher:   auth.NewPasswordHasher(auth.DefaultArgon2Params()), Policy: auth.NewPasswordPolicy(),
		Audit: service.NewAuditor(meta.NewAuditLog(db), logger, clock.Now), Now: clock.Now,
	})
}
