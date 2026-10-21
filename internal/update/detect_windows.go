// SPDX-License-Identifier: Apache-2.0

package update

import (
	"path/filepath"
	"strings"
)

// helperInstalled: the Windows service running %ProgramFiles%\Tidemark\tidemark.exe with the
// update service registered (tidemark service install-update).
func helperInstalled(kind InstallKind, p hostProbe) bool {
	if kind != KindWindowsService {
		return false
	}
	paths, err := DefaultPaths()
	if err != nil {
		return false
	}
	exe, err := p.executable()
	return err == nil && strings.EqualFold(filepath.Clean(exe), paths.Binary) && p.helperService()
}

// detectPlatform: the Windows service is recognised by cmd/tidemark through the Service Control
// Manager and passed in Build.
func detectPlatform(b Build, _ hostProbe) InstallKind {
	if b.WindowsService {
		return KindWindowsService
	}
	return KindManual
}
