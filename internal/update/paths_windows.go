// SPDX-License-Identifier: Apache-2.0

package update

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// Locations of the Windows install (deploy/install.ps1): the program directory under Program
// Files (administrators only can write there) and the data directory under ProgramData.
const (
	windowsDir     = "Tidemark"
	windowsBinary  = "tidemark.exe"
	windowsStaging = "update"
	windowsWork    = "state"
)

// DefaultPaths returns the helper protocol locations of this operating system: the binary in
// %ProgramFiles%\Tidemark, the staging directory %ProgramData%\Tidemark\update (the service's
// data directory) and the helper's work directory %ProgramFiles%\Tidemark\state.
func DefaultPaths() (HelperPaths, error) {
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return HelperPaths{}, fmt.Errorf("program files folder: %w", err)
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return HelperPaths{}, fmt.Errorf("program data folder: %w", err)
	}
	return windowsPaths(programFiles, programData), nil
}

func windowsPaths(programFiles, programData string) HelperPaths {
	dir := filepath.Join(programFiles, windowsDir)
	return HelperPaths{Binary: filepath.Join(dir, windowsBinary), Staging: filepath.Join(programData, windowsDir, windowsStaging),
		Work: filepath.Join(dir, windowsWork)}
}
