// SPDX-License-Identifier: Apache-2.0

package config

// defaultDataDir is /var/lib/tidemark on Linux (SPEC §7).
func defaultDataDir(Env) string { return "/var/lib/tidemark" }
