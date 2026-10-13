// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"strings"
	"testing"
	"time"
)

// TestPathologicalInputsAreFast guards against super-linear parsing, compiling
// and completion on maximum-size hostile inputs.
func TestPathologicalInputsAreFast(t *testing.T) {
	cases := map[string]string{
		"words":    strings.Repeat("a ", 4000),
		"stars":    strings.Repeat("*", 8000),
		"quotes":   strings.Repeat("'", 8000),
		"bang":     strings.Repeat("!", 8000),
		"colons":   strings.Repeat("a:", 4000),
		"group":    "x:(" + strings.Repeat("a or ", 1600),
		"in":       "x in [" + strings.Repeat("1,", 3000),
		"regex":    "x:/" + strings.Repeat("a", 7990),
		"pipes":    strings.Repeat("|", 8000),
		"bt":       strings.Repeat("`", 8000),
		"slashes":  "x:" + strings.Repeat("/a", 3990),
		"longword": "@" + strings.Repeat("a", 8000),
		"parens":   strings.Repeat("(", 8000),
		"ors":      strings.Repeat("a or ", 1600),
		"eq":       strings.Repeat("a=b ", 2000),
		"nots":     strings.Repeat("not ", 2000),
		"dollar":   strings.Repeat("x:$v ", 1600),
		"unicode":  strings.Repeat("é ", 2700),
	}
	for name, q := range cases {
		start := time.Now()
		p, err := Parse(q)
		if err == nil {
			_, _ = Compile(p, goldenOptions())
		}
		_ = Complete(q, len(q)/2, "", nil)
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s took %s", name, d)
		}
	}
}
