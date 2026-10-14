// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// readRepoFile returns a repository file as text.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(raw), "\r\n", "\n")
}

// section returns the text from the line starting with heading up to the next heading of the
// same or a higher level.
func section(t *testing.T, text, heading string) string {
	t.Helper()
	start := strings.Index(text, "\n"+heading+"\n")
	if start < 0 {
		t.Fatalf("heading %q not found", heading)
	}
	body := text[start+len(heading)+2:]
	level := len(strings.Fields(heading)[0]) // number of leading '#'
	next := regexp.MustCompile(`(?m)^#{1,` + strconv.Itoa(level) + `} `)
	if loc := next.FindStringIndex(body); loc != nil {
		body = body[:loc[0]]
	}
	return body
}

// firstFence returns the lines inside the first ``` fenced block of text.
func firstFence(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	inside := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case !inside && strings.HasPrefix(line, "```"):
			inside = true
		case inside && strings.HasPrefix(line, "```"):
			return out
		case inside:
			out = append(out, line)
		}
	}
	t.Fatal("no fenced block found")
	return nil
}

// TestTQLDocExamplesMatchSpec keeps the example block of the TQL reference identical to SPEC
// §10.1 (internal/tql compiles every line of the doc, SPEC §18).
func TestTQLDocExamplesMatchSpec(t *testing.T) {
	spec := firstFence(t, section(t, readRepoFile(t, "SPEC.md"), "### 10.1 Overview by example"))
	doc := firstFence(t, section(t, readRepoFile(t, "docs/query-language.md"), "## Overview by example"))
	if !slices.Equal(spec, doc) {
		t.Errorf("docs/query-language.md examples differ from SPEC §10.1:\nspec: %q\ndoc:  %q", spec, doc)
	}
}

var (
	specVarRow  = regexp.MustCompile("(?m)^\\| (`TIDEMARK_[^|]+) \\|")
	specVarName = regexp.MustCompile("`(TIDEMARK_[A-Z0-9_]+|_[A-Z0-9_]+)`")
	docVarName  = regexp.MustCompile("(?m)^\\| `(TIDEMARK_[A-Z0-9_]+)` \\|")
)

// specVariables returns the variables of the SPEC §7 table; a row may abbreviate siblings of its
// first name as "`_SUFFIX`", which replaces that name's last segment.
func specVariables(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, row := range specVarRow.FindAllStringSubmatch(section(t, readRepoFile(t, "SPEC.md"), "## 7. Configuration"), -1) {
		first := ""
		for _, m := range specVarName.FindAllStringSubmatch(row[1], -1) {
			name := m[1]
			if strings.HasPrefix(name, "_") && first != "" {
				name = first[:strings.LastIndex(first, "_")] + name
			} else if first == "" {
				first = name
			}
			out[name] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no variables found in SPEC §7")
	}
	return out
}

// TestManualPagesInNav checks that every chapter of the operations manual (docs/manual/*.md) is
// in the navigation of the documentation site (mkdocs.yml, SPEC §17.4), so a chapter cannot be
// published without a way to reach it.
func TestManualPagesInNav(t *testing.T) {
	nav := readRepoFile(t, "mkdocs.yml")
	pages, err := filepath.Glob(filepath.Join(repoRoot(t), "docs", "manual", "*.md"))
	if err != nil || len(pages) == 0 {
		t.Fatalf("docs/manual/*.md: %v (%d files)", err, len(pages))
	}
	for _, page := range pages {
		if entry := "manual/" + filepath.Base(page); !strings.Contains(nav, ": "+entry+"\n") {
			t.Errorf("mkdocs.yml nav lacks %s", entry)
		}
	}
}

// TestConfigurationDocMatchesSpec checks that docs/configuration.md documents only SPEC §7
// variables (internal/config checks it against the binary's variables and defaults, SPEC §18).
func TestConfigurationDocMatchesSpec(t *testing.T) {
	spec := specVariables(t)
	for _, m := range docVarName.FindAllStringSubmatch(readRepoFile(t, "docs/configuration.md"), -1) {
		if !spec[m[1]] {
			t.Errorf("docs/configuration.md documents %s, which SPEC §7 does not define", m[1])
		}
	}
}
