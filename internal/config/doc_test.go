// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// docRow matches a variable row of docs/configuration.md: | `TIDEMARK_X` | <default> | … |
var docRow = regexp.MustCompile("(?m)^\\| `(TIDEMARK_[A-Z0-9_]+)` \\| ([^|]*) \\|")

// TestConfigurationDocMatchesVariables keeps the public configuration reference
// (docs/configuration.md) in sync with the variables and defaults the binary knows (SPEC §7).
func TestConfigurationDocMatchesVariables(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]string{}
	for _, m := range docRow.FindAllStringSubmatch(string(raw), -1) {
		if _, dup := documented[m[1]]; dup {
			t.Errorf("%s is documented twice", m[1])
		}
		documented[m[1]] = m[2]
	}
	for _, v := range allVars {
		cell, ok := documented[v]
		if !ok {
			t.Errorf("%s is not documented in docs/configuration.md", v)
			continue
		}
		if def, ok := defaults[v]; ok && cell != "`"+def+"`" {
			t.Errorf("%s: documented default %s, want `%s`", v, cell, def)
		}
	}
	for v := range documented {
		if !isKnown(v) {
			t.Errorf("docs/configuration.md documents unknown variable %s", v)
		}
	}
}
