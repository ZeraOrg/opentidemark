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
	makeList = regexp.MustCompile(`(?m)^(DIST_FILES(?:_LINUX|_WINDOWS|_DOCKER)?) := (.+)$`)
	psList   = regexp.MustCompile(`(?m)^\$(DistFiles(?:Linux|Windows|Docker)?) = @\((.+)\)\s*$`)
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
	pairs := map[string]string{"DIST_FILES": "DistFiles", "DIST_FILES_LINUX": "DistFilesLinux", "DIST_FILES_WINDOWS": "DistFilesWindows", "DIST_FILES_DOCKER": "DistFilesDocker"}
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

const (
	// distImage is the published image the Docker bundle pins (SPEC §17.6).
	distImage = "ghcr.io/zeraorg/tidemark"
	// sourceImageLine is the tidemark service's image line in deploy/docker-compose.yml.
	sourceImageLine = "image: ${TM_IMAGE:-tidemark:local}"
)

var (
	makeDistImage = regexp.MustCompile(`(?m)^DIST_IMAGE := (\S+)$`)
	psDistImage   = regexp.MustCompile(`(?m)^\$DistImage = '([^']+)'$`)
	// composeBuildBlock is the build: block of the tidemark service (4-space key, 6+-space body).
	composeBuildBlock = regexp.MustCompile(`(?m)^    build:\n(?:      .*\n)+`)
	anyBuildKey       = regexp.MustCompile(`(?m)^\s*build:`)
	// markdownLink captures the target of [text](target) links.
	markdownLink = regexp.MustCompile(`\]\(([^)\s]+)`)
)

// distCompose applies the rewrite `make dist` (sed) and `.\build.ps1 dist` (Convert-DistCompose)
// make to deploy/docker-compose.yml for the Docker bundle (SPEC §17.6).
func distCompose(src, image string) string {
	out := composeBuildBlock.ReplaceAllString(src, "")
	out = strings.ReplaceAll(out, sourceImageLine, "image: ${TM_IMAGE:-"+image+"}")
	return strings.ReplaceAll(out, "deploy/.env", ".env")
}

// TestDistDockerCompose checks that the Docker bundle's compose file never builds and pins the
// release image, and that both build tools use the same image.
func TestDistDockerCompose(t *testing.T) {
	mk := makeDistImage.FindStringSubmatch(readRepoFile(t, "Makefile"))
	ps := psDistImage.FindStringSubmatch(readRepoFile(t, "build.ps1"))
	if mk == nil || ps == nil || mk[1] != distImage || ps[1] != distImage {
		t.Errorf("Makefile DIST_IMAGE = %q, build.ps1 $DistImage = %q, want %s", mk, ps, distImage)
	}
	src := readRepoFile(t, "deploy/docker-compose.yml")
	if strings.Count(src, sourceImageLine) != 1 || len(composeBuildBlock.FindAllString(src, -1)) != 1 {
		t.Fatalf("deploy/docker-compose.yml needs exactly one %q and one build: block for the bundle rewrite", sourceImageLine)
	}
	image := distImage + ":1.2.3"
	got := distCompose(src, image)
	if anyBuildKey.MatchString(got) {
		t.Errorf("bundle docker-compose.yml still has a build: key:\n%s", got)
	}
	if !strings.Contains(got, "image: ${TM_IMAGE:-"+image+"}") {
		t.Errorf("bundle docker-compose.yml does not pin %s", image)
	}
	if strings.Contains(got, "deploy/.env") {
		t.Error("bundle docker-compose.yml still points at deploy/.env")
	}
	removed := strings.Count(src, "\n") - strings.Count(got, "\n")
	if want := strings.Count(composeBuildBlock.FindString(src), "\n"); removed != want {
		t.Errorf("rewrite removed %d lines, want only the %d lines of the build: block", removed, want)
	}
}

// TestInstallDocs checks the INSTALL.md sources of the release archives (SPEC §17.6): one per
// archive kind, and only absolute links, because the file is read outside the repository.
func TestInstallDocs(t *testing.T) {
	for _, kind := range []string{"linux", "windows", "docker"} {
		rel := "deploy/install/INSTALL-" + kind + ".md"
		text := readRepoFile(t, rel)
		for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
			if !strings.HasPrefix(m[1], "https://") {
				t.Errorf("%s: link %q must be an absolute https:// URL", rel, m[1])
			}
		}
	}
}

// TestReleaseDocsBundle keeps the offline documentation bundle part of every release (SPEC §17.6):
// the release workflow builds it with `make docs-offline` from the pinned requirements, both task
// runners offer the target, the offline configuration inherits the site configuration with
// Material's offline plugin and its own copy of the worker shim, and the release notes list the
// asset.
func TestReleaseDocsBundle(t *testing.T) {
	checks := []struct{ file, want string }{
		{".github/workflows/release.yml", "make docs-offline"},
		{".github/workflows/release.yml", "cache-dependency-path: docs-site/requirements.txt"},
		{"Makefile", "\ndocs-offline:"},
		{"Makefile", "mkdocs build --strict -f mkdocs.offline.yml"},
		{"build.ps1", "'docs-offline'"},
		{"build.ps1", "mkdocs.offline.yml"},
		{"mkdocs.offline.yml", "INHERIT: mkdocs.yml"},
		{"mkdocs.offline.yml", "  - offline"},
		{"mkdocs.offline.yml", "assets/javascripts/iframe-worker.js"},
		{"docs-site/hooks.py", "docs-site/offline/iframe-worker.js"},
		{"scripts/release-notes.sh", "_docs.zip"},
	}
	for _, c := range checks {
		if !strings.Contains(readRepoFile(t, c.file), c.want) {
			t.Errorf("%s must contain %q", c.file, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot(t), "docs-site", "offline", "iframe-worker.js")); err != nil {
		t.Error(err)
	}
}

var osvScannerVersions = map[string]*regexp.Regexp{
	"Makefile":                       regexp.MustCompile(`(?m)^OSV_SCANNER_VERSION \?= (v\S+)$`),
	"build.ps1":                      regexp.MustCompile(`(?m)^\$OsvScannerVersion = '(v[^']+)'$`),
	".github/workflows/security.yml": regexp.MustCompile(`(?m)^  OSV_SCANNER_VERSION: (v\S+)`),
	".github/workflows/release.yml":  regexp.MustCompile(`(?m)^  OSV_SCANNER_VERSION: (v\S+)`),
}

// TestSupplyChainScanPinned keeps the OSV-Scanner that gates every release on one pinned version
// in the local gate, the weekly security run and the release workflow (SPEC §15.9, §17.6).
func TestSupplyChainScanPinned(t *testing.T) {
	want := ""
	for file, re := range osvScannerVersions {
		m := re.FindStringSubmatch(readRepoFile(t, file))
		if m == nil {
			t.Errorf("%s: OSV-Scanner version not pinned", file)
			continue
		}
		if want == "" {
			want = m[1]
		}
		if m[1] != want {
			t.Errorf("%s pins OSV-Scanner %s, another file %s; they must be equal", file, m[1], want)
		}
	}
	rel := readRepoFile(t, ".github/workflows/release.yml")
	if !strings.Contains(rel, "needs: [prepare, supply-chain]") {
		t.Error("release.yml: the archives job must need the supply-chain job, so a finding blocks the release")
	}
}

var trivyVersion = regexp.MustCompile(`(?m)^  TRIVY_VERSION: (v\S+)`)

// TestReleaseScanUsesSecurityTrivy keeps the image scan that gates a release on the same pinned
// Trivy version as security.yml (SPEC §17.4, §17.6).
func TestReleaseScanUsesSecurityTrivy(t *testing.T) {
	sec := trivyVersion.FindStringSubmatch(readRepoFile(t, ".github/workflows/security.yml"))
	rel := trivyVersion.FindStringSubmatch(readRepoFile(t, ".github/workflows/release.yml"))
	if sec == nil || rel == nil || sec[1] != rel[1] {
		t.Errorf("TRIVY_VERSION: security.yml %q, release.yml %q; they must be set and equal", sec, rel)
	}
}
