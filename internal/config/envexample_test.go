// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// envExampleLine matches a documented variable, commented out or not: "#TIDEMARK_X=" or "TIDEMARK_X=".
var envExampleLine = regexp.MustCompile(`(?m)^#?(TIDEMARK_[A-Z0-9_]+)=`)

// TestEnvExampleDocumentsEveryVariable keeps deploy/.env.example in sync with the variables the
// binary knows (SPEC §17.2: it documents all variables).
func TestEnvExampleDocumentsEveryVariable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	for _, m := range envExampleLine.FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]] = true
	}
	for _, v := range allVars {
		if !documented[v] {
			t.Errorf("%s is not documented in deploy/.env.example", v)
		}
	}
	for v := range documented {
		if !isKnown(v) {
			t.Errorf("deploy/.env.example documents unknown variable %s", v)
		}
	}
}
