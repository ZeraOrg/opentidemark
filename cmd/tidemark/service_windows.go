// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
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
)

func isWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// runService runs the server under the Service Control Manager.
func runService(args []string) error {
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
	go func() { done <- serveContext(ctx, h.args, os.Stderr) }()
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
	el, oerr := eventlog.Open(serviceName)
	if oerr != nil {
		return
	}
	defer el.Close()
	if err != nil {
		_ = el.Error(1, err.Error()) // best effort: the event log is diagnostic only
		return
	}
	_ = el.Info(1, "OpenTidemark stopped") // best effort
}

// serviceCommand implements "service install|uninstall|start|stop".
func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tidemark service install|uninstall|start|stop")
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
		return withService(m, func(s *mgr.Service) error {
			_ = eventlog.Remove(serviceName) // the event source may not exist
			return s.Delete()
		})
	case "start":
		return withService(m, func(s *mgr.Service) error { return s.Start() })
	case "stop":
		return withService(m, stop)
	default:
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func install(m *mgr.Mgr, extra []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
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

func stop(s *mgr.Service) error {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	deadline := time.Now().Add(controlTimeout)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the service to stop")
		}
		time.Sleep(time.Second)
		if st, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}
