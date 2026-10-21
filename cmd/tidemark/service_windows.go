// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/ZeraOrg/opentidemark/internal/update"
)

// Windows service settings (SPEC §17.3).
const (
	serviceName        = "Tidemark"
	serviceDisplayName = "OpenTidemark observability"
	serviceDescription = "Self-hosted logs, traces and metrics (https://github.com/ZeraOrg/opentidemark)"
	serviceAccount     = `NT SERVICE\Tidemark`
	restartDelay       = 5 * time.Second
	resetPeriodSeconds = 86400
	controlTimeout     = 30 * time.Second
	controlPoll        = time.Second
)

// Update service settings (SPEC §17.7): a demand-start LocalSystem service the Tidemark service
// may start and query, but not stop, configure or delete.
const (
	updateServiceName        = update.WindowsHelperService
	updateServiceDisplayName = "OpenTidemark update helper"
	updateServiceDescription = "Installs OpenTidemark updates staged by the Tidemark service"
	updateServiceArg         = "update-service"
	noSelfUpdateFlag         = "--no-self-update"
)

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// runService runs the server, or with "update-service" the update helper, under the Service
// Control Manager.
func runService(args []string) error {
	if len(args) > 0 && args[0] == updateServiceArg {
		return svc.Run(updateServiceName, &updateHandler{})
	}
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	return svc.Run(serviceName, &handler{args: args})
}

type handler struct{ args []string }

// Execute implements svc.Handler.
func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveContext(ctx, h.args, os.Stderr, true) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case err := <-done:
			cancel()
			logEvent(err)
			if err != nil {
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
			}
		}
	}
}

func logEvent(err error) {
	if err != nil {
		writeEvent(true, err.Error())
		return
	}
	writeEvent(false, "OpenTidemark stopped")
}

// writeEvent writes msg to the Application event log with the source Tidemark.
func writeEvent(isError bool, msg string) {
	el, oerr := eventlog.Open(serviceName)
	if oerr != nil {
		return
	}
	defer el.Close()
	if isError {
		_ = el.Error(1, msg) // best effort: the event log is diagnostic only
		return
	}
	_ = el.Info(1, msg) // best effort
}

// serviceCommand implements "service install [--no-self-update] [serve flags] | uninstall |
// install-update | uninstall-update | start | stop".
func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tidemark service install|uninstall|install-update|uninstall-update|start|stop")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }() // closing the SCM handle; the operation result is what matters
	switch args[0] {
	case "install":
		return install(m, args[1:])
	case "uninstall":
		if err := removeUpdateService(m); err != nil {
			return err
		}
		return withService(m, func(s *mgr.Service) error {
			_ = eventlog.Remove(serviceName) // the event source may not exist
			return s.Delete()
		})
	case "install-update":
		return installUpdateService(m)
	case "uninstall-update":
		return removeUpdateService(m)
	case "start":
		return withService(m, func(s *mgr.Service) error { return s.Start() })
	case "stop":
		return withService(m, func(s *mgr.Service) error { return stopService(s, controlTimeout, controlPoll) })
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

// executable is the absolute path of the running binary.
func executable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

func install(m *mgr.Mgr, extra []string) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	selfUpdate := !slices.Contains(extra, noSelfUpdateFlag)
	extra = slices.DeleteFunc(slices.Clone(extra), func(a string) bool { return a == noSelfUpdateFlag })
	cfg := mgr.Config{DisplayName: serviceDisplayName, Description: serviceDescription, StartType: mgr.StartAutomatic,
		DelayedAutoStart: true, ServiceStartName: serviceAccount}
	s, err := m.CreateService(serviceName, exe, cfg, append([]string{"serve"}, extra...)...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()
	actions := []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: restartDelay}, {Type: mgr.ServiceRestart, Delay: restartDelay},
		{Type: mgr.ServiceRestart, Delay: restartDelay}}
	if err := s.SetRecoveryActions(actions, resetPeriodSeconds); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	// A fatal error (a failed WAL, a failed listener) ends Execute with a non-zero exit code
	// instead of a crash; restart in that case too (SPEC §9.8).
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery on non-crash failures: %w", err)
	}
	if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil {
		return fmt.Errorf("register event source: %w", err)
	}
	if selfUpdate {
		return installUpdateService(m)
	}
	return nil
}

// installUpdateService registers TidemarkUpdate (or updates its binary path) and lets the
// Tidemark service account start and query it.
func installUpdateService(m *mgr.Mgr) error {
	exe, err := executable()
	if err != nil {
		return err
	}
	cfg := mgr.Config{DisplayName: updateServiceDisplayName, Description: updateServiceDescription, StartType: mgr.StartManual}
	s, err := m.OpenService(updateServiceName)
	if err == nil {
		cur, cerr := s.Config()
		if cerr == nil {
			cur.BinaryPathName = windows.EscapeArg(exe) + " " + updateServiceArg // as CreateService writes it
			cerr = s.UpdateConfig(cur)
		}
		if cerr != nil {
			_ = s.Close() // the update error is what matters
			return fmt.Errorf("update %s: %w", updateServiceName, cerr)
		}
	} else if s, err = m.CreateService(updateServiceName, exe, cfg, updateServiceArg); err != nil {
		return fmt.Errorf("create %s: %w", updateServiceName, err)
	}
	defer s.Close()
	sid, _, _, err := windows.LookupSID("", serviceAccount)
	if err != nil {
		return fmt.Errorf("look up %s: %w", serviceAccount, err)
	}
	sd, err := windows.SecurityDescriptorFromString(updateServiceSDDL(sid.String()))
	if err != nil {
		return fmt.Errorf("security descriptor: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("security descriptor: %w", err)
	}
	if err := windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set %s permissions: %w", updateServiceName, err)
	}
	return nil
}

// updateServiceSDDL is the DACL of TidemarkUpdate: the usual service defaults (SYSTEM and
// Administrators manage it, interactive and service logons query it) plus start (RP) and query
// status (LC) for the Tidemark service SID, which may not stop, change or delete it.
func updateServiceSDDL(tidemarkSID string) string {
	return "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)" +
		"(A;;LCRP;;;" + tidemarkSID + ")"
}

// removeUpdateService deletes TidemarkUpdate; a missing service is not an error.
func removeUpdateService(m *mgr.Mgr) error {
	s, err := m.OpenService(updateServiceName)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", updateServiceName, err)
	}
	defer s.Close()
	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete %s: %w", updateServiceName, err)
	}
	return nil
}

func withService(m *mgr.Mgr, fn func(*mgr.Service) error) error {
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	return fn(s)
}

// serviceControl is the part of *mgr.Service used to stop and start a service.
type serviceControl interface {
	Control(c svc.Cmd) (svc.Status, error)
	Query() (svc.Status, error)
	Start(args ...string) error
}

// stopService stops s and waits up to timeout until it has stopped; a service that is not
// running counts as stopped.
func stopService(s serviceControl, timeout, poll time.Duration) error {
	st, err := s.Control(svc.Stop)
	if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the service to stop")
		}
		time.Sleep(poll)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

// restartService stops s (the server drains as on any stop) and starts it again.
func restartService(s serviceControl, timeout, poll time.Duration) error {
	if err := stopService(s, timeout, poll); err != nil {
		return err
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}
