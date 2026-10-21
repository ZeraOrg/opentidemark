// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// WindowsHelperService is the name of the Windows update service (SPEC §17.7).
const WindowsHelperService = "TidemarkUpdate"

// ErrHelperBusy is returned when the update service is already running.
var ErrHelperBusy = errors.New("the update service is already running")

// StartHelper starts the update service. It asks the Service Control Manager only for the rights
// the service account has: SC_MANAGER_CONNECT and SERVICE_START.
func StartHelper() error {
	return withHelperService(windows.SERVICE_START, func(s windows.Handle) error {
		err := windows.StartService(s, 0, nil)
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return ErrHelperBusy
		}
		return err
	})
}

// helperServiceRegistered reports whether the update service exists and this account may query
// it.
func helperServiceRegistered() bool {
	return withHelperService(windows.SERVICE_QUERY_STATUS, func(windows.Handle) error { return nil }) == nil
}

func withHelperService(access uint32, fn func(windows.Handle) error) error {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("connect to service manager: %w", err)
	}
	defer func() { _ = windows.CloseServiceHandle(m) }() // closing a handle; the call's result is what matters
	name, err := windows.UTF16PtrFromString(WindowsHelperService)
	if err != nil {
		return err
	}
	s, err := windows.OpenService(m, name, access)
	if err != nil {
		return fmt.Errorf("open %s: %w", WindowsHelperService, err)
	}
	defer func() { _ = windows.CloseServiceHandle(s) }() // closing a handle
	return fn(s)
}
