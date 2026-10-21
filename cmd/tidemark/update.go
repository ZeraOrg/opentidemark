// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ZeraOrg/opentidemark/internal/app"
	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/logging"
	"github.com/ZeraOrg/opentidemark/internal/update"
)

// exitRestartForUpdate is the exit status that makes systemd start the binary the update helper
// installed (deploy/tidemark.service: SuccessExitStatus and RestartForceExitStatus, SPEC §17.7).
const exitRestartForUpdate = 3

// errUpdateCheckFailed is returned by "update check" when GitHub could not be asked.
var errUpdateCheckFailed = errors.New("update check failed")

const updateUsage = "usage: tidemark update check [--json] | tidemark update install"

// updateCommand implements "update check [--json]" and "update install" (the Linux helper).
func updateCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(updateUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "check":
		return updateCheck(ctx, args[1:], stdout)
	case "install":
		if len(args) > 1 {
			return errors.New(updateUsage)
		}
		return updateInstall(ctx, logging.New(stderr, slog.LevelInfo))
	default:
		return errors.New(updateUsage)
	}
}

// updateCheck asks GitHub once and prints the status (text, or the API's JSON with --json).
func updateCheck(ctx context.Context, args []string, stdout io.Writer) error {
	own, rest, err := cmdFlags{"json": false}.split(args)
	if err != nil {
		return err
	}
	cfg, err := config.Load(rest, config.OSEnv{})
	if err != nil {
		return err
	}
	st, err := app.CheckForUpdate(ctx, cfg, app.BuildInfo{Version: version, Distribution: distribution})
	if err != nil {
		return err
	}
	if own["json"] == "true" {
		err = json.NewEncoder(stdout).Encode(st)
	} else {
		err = printUpdateStatus(stdout, st)
	}
	if err == nil && st.Error != "" {
		err = fmt.Errorf("%w: %s", errUpdateCheckFailed, st.Error)
	}
	return err
}

func printUpdateStatus(w io.Writer, st update.Status) error {
	var err error
	switch {
	case st.Error != "":
		_, err = fmt.Fprintf(w, "OpenTidemark %s: the update check failed (%s)\n", st.Current, st.Error)
	case st.Latest != nil:
		_, err = fmt.Fprintf(w, "OpenTidemark %s: version %s is available (published %s)\nRelease notes: %s\n",
			st.Current, st.Latest.Version, st.Latest.PublishedAt.Format("2006-01-02"), st.Latest.NotesURL)
	default:
		_, err = fmt.Fprintf(w, "OpenTidemark %s is up to date\n", st.Current)
	}
	return err
}
