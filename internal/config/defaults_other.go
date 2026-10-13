// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package config

import "path/filepath"

// defaultDataDir is $HOME/.tidemark on development platforms such as macOS.
func defaultDataDir(env Env) string {
	home, _ := env.Lookup("HOME")
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".tidemark")
}
