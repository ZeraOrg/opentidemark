// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/ZeraOrg/opentidemark/internal/app"
	"github.com/ZeraOrg/opentidemark/internal/logging"
	"github.com/ZeraOrg/opentidemark/internal/update"
)

// Update service (SPEC §17.7).
const (
	// helperStopTimeout covers the server's drain (TIDEMARK_DRAIN_SECONDS) and shutdown.
	helperStopTimeout = 2 * time.Minute
	helperLogFile     = "update.log"
	helperLogMode     = 0o644
	helperWorkDirMode = 0o755 // state.json and update.log are read by the service and admins
)

// updateHandler runs the update helper as the TidemarkUpdate service. It accepts no stop
// request: stopping it while it waits for the confirmation would roll the update back. It ends
// on its own after at most the confirmation timeout.
type updateHandler struct{}

// Execute implements svc.Handler.
func (updateHandler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- runUpdateService(context.Background()) }()
	status <- svc.Status{State: svc.Running}
	for {
		select {
		case err := <-done:
			if err != nil {
				writeEvent(true, "OpenTidemark update: "+err.Error())
				return true, 1
			}
			return false, 0
		case c := <-req:
			if c.Cmd == svc.Interrogate {
				status <- c.CurrentStatus
			}
		}
	}
}

// runUpdateService installs the staged update with a log in the helper's work directory
// (update.log, the last run) and restarts the Tidemark service through the SCM.
func runUpdateService(ctx context.Context) error {
	paths, err := update.DefaultPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Work, helperWorkDirMode); err != nil {
		return fmt.Errorf("update work directory: %w", err)
	}
	logPath := filepath.Join(paths.Work, helperLogFile)
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, helperLogMode) //nolint:gosec // fixed path under Program Files
	if err != nil {
		return fmt.Errorf("open %s: %w", helperLogFile, err)
	}
	defer f.Close()
	return app.RunUpdateHelper(ctx, version, restartTidemark, logging.New(f, slog.LevelInfo))
}

// restartTidemark stops the Tidemark service (it drains as on any stop) and starts it again.
func restartTidemark(context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }() // closing the SCM handle; the restart result is what matters
	return withService(m, func(s *mgr.Service) error { return restartService(s, helperStopTimeout, controlPoll) })
}
