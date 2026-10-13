// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	// semVer is SemVer 2.0 without build metadata and without a "v" prefix (SPEC §17.6).
	semVer = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)
	// changelogHeading matches the level-2 headings of CHANGELOG.md.
	changelogHeading = regexp.MustCompile(`(?m)^## (.*)$`)
	// releaseHeading is "[<version>] - <YYYY-MM-DD>".
	releaseHeading = regexp.MustCompile(`^\[([^\]]+)\] - (\d{4}-\d{2}-\d{2})$`)
)

const unreleasedHeading = "[Unreleased]"

// TestVersionAndChangelog keeps VERSION a plain semantic version and CHANGELOG.md in the Keep a
// Changelog shape the release workflow relies on (SPEC §17.6): "[Unreleased]" first, then dated
// release sections, one of them for VERSION with a non-empty body.
func TestVersionAndChangelog(t *testing.T) {
	raw := readRepoFile(t, "VERSION")
	version := strings.TrimSpace(raw)
	if raw != version+"\n" || !semVer.MatchString(version) {
		t.Fatalf("VERSION must be one line holding a semantic version without a 'v', got %q", raw)
	}
	changelog := readRepoFile(t, "CHANGELOG.md")
	headings := changelogHeading.FindAllStringSubmatch(changelog, -1)
	if len(headings) == 0 || headings[0][1] != unreleasedHeading {
		t.Fatalf("CHANGELOG.md must start with the section ## %s", unreleasedHeading)
	}
	found := false
	for _, h := range headings[1:] {
		m := releaseHeading.FindStringSubmatch(h[1])
		if m == nil || !semVer.MatchString(m[1]) {
			t.Errorf("CHANGELOG.md heading %q is not \"[<version>] - <YYYY-MM-DD>\"", h[1])
			continue
		}
		if _, err := time.Parse(time.DateOnly, m[2]); err != nil {
			t.Errorf("CHANGELOG.md heading %q: %v", h[1], err)
		}
		if m[1] == version {
			found = true
			if strings.TrimSpace(section(t, changelog, h[0])) == "" {
				t.Errorf("CHANGELOG.md section %q is empty", h[1])
			}
		}
	}
	if !found {
		t.Errorf("CHANGELOG.md has no section ## [%s] - <YYYY-MM-DD> for VERSION", version)
	}
}

var (
	makeList = regexp.MustCompile(`(?m)^(DIST_FILES(?:_LINUX|_WINDOWS)?) := (.+)$`)
	psList   = regexp.MustCompile(`(?m)^\$(DistFiles(?:Linux|Windows)?) = @\((.+)\)\s*$`)
)

// TestDistFileListsMatch keeps the archive contents of `make dist` and `.\build.ps1 dist` equal
// (SPEC §17.6) and checks that every listed file exists.
func TestDistFileListsMatch(t *testing.T) {
	makeLists := map[string][]string{}
	for _, m := range makeList.FindAllStringSubmatch(readRepoFile(t, "Makefile"), -1) {
		makeLists[m[1]] = strings.Fields(m[2])
	}
	psLists := map[string][]string{}
	for _, m := range psList.FindAllStringSubmatch(readRepoFile(t, "build.ps1"), -1) {
		var files []string
		for _, f := range strings.Split(m[2], ",") {
			files = append(files, strings.Trim(strings.TrimSpace(f), "'"))
		}
		psLists[m[1]] = files
	}
	pairs := map[string]string{"DIST_FILES": "DistFiles", "DIST_FILES_LINUX": "DistFilesLinux", "DIST_FILES_WINDOWS": "DistFilesWindows"}
	root := repoRoot(t)
	for mk, ps := range pairs {
		if len(makeLists[mk]) == 0 || !slices.Equal(makeLists[mk], psLists[ps]) {
			t.Errorf("Makefile %s = %q, build.ps1 $%s = %q", mk, makeLists[mk], ps, psLists[ps])
		}
		for _, f := range makeLists[mk] {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err != nil {
				t.Errorf("%s lists %s: %v", mk, f, err)
			}
		}
	}
}
