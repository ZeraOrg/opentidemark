// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import "errors"

// isWindowsService is always false outside Windows.
func isWindowsService() bool { return false }

// runService is unreachable outside Windows.
func runService([]string) error { return errors.New("not running as a Windows service") }

// serviceCommand explains that service management is Windows-only (use systemd on Linux).
func serviceCommand([]string) error {
	return errors.New("the service command is only available on Windows; on Linux use deploy/tidemark.service (systemd)")
}
