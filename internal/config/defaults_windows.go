// SPDX-License-Identifier: Apache-2.0

package config

import "path/filepath"

// defaultDataDir is %ProgramData%\Tidemark on Windows (SPEC §7).
func defaultDataDir(env Env) string {
	base, ok := env.Lookup("ProgramData")
	if !ok || base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "Tidemark")
}
