// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	spdxTag = "SPDX-License-Identifier: Apache-2.0"
	// spdxMaxLine is how far down the header may appear (shebangs, doctype, XML declaration).
	spdxMaxLine = 5
	// apacheLicenseSHA256 is the SHA-256 of the verbatim Apache License 2.0 text
	// (https://www.apache.org/licenses/LICENSE-2.0.txt) that LICENSE must contain (SPEC §19.1).
	apacheLicenseSHA256 = "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"
)

// skippedDirs are never scanned: VCS data, dependencies, build output and fixtures (SPEC §19.1).
var skippedDirs = map[string]bool{
	".git": true, ".claude": true, "node_modules": true, "dist": true, "bin": true, ".dev": true,
	"testdata": true, "vendor": true, "coverage": true, "test-results": true, "playwright-report": true,
	"dist-mock": true, "mock-public": true, // web mock-mode build output (untracked)
	"site": true, "__pycache__": true, // documentation site build output and Python cache (untracked, SPEC §17.4)
}

// exemptFiles are licence texts, generated files and data that cannot carry a comment.
var exemptFiles = map[string]bool{
	"LICENSE": true, "NOTICE": true, "go.sum": true, "THIRD_PARTY_NOTICES.md": true,
	"VERSION":                            true, // a bare version number read by tools (SPEC §17.6)
	"internal/auth/common_passwords.txt": true,
	"deploy/.env":                        true,
	// Third-party MIT code vendored unmodified with its own licence header (SPEC §17.6, §19.1).
	"docs-site/offline/iframe-worker.js": true,
}

// exemptExt are formats without comments (JSON) and binary assets.
var exemptExt = map[string]bool{
	".json": true, ".pb": true, ".png": true, ".jpg": true, ".ico": true, ".woff": true, ".woff2": true,
	".svg": true, ".exe": true, ".test": true, ".out": true, ".tsbuildinfo": true,
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func hasSPDX(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }() // read-only file
	sc := bufio.NewScanner(f)
	for i := 0; i < spdxMaxLine && sc.Scan(); i++ {
		if strings.Contains(sc.Text(), spdxTag) {
			return true, nil
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return false, err
	}
	return false, nil
}

func exempt(rel string) bool {
	return exemptFiles[rel] || exemptExt[strings.ToLower(filepath.Ext(rel))] ||
		strings.HasPrefix(rel, "internal/web/assets/dist/")
}

func TestSPDXHeaders(t *testing.T) {
	root := repoRoot(t)
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && skippedDirs[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil // a worktree's .git is a file
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() || exempt(rel) {
			return nil
		}
		ok, err := hasSPDX(path)
		if err != nil {
			return err
		}
		checked++
		if !ok {
			t.Errorf("%s: missing %q in the first %d lines", rel, spdxTag, spdxMaxLine)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no files checked")
	}
}

// TestLicenseIsApache2 keeps LICENSE the unmodified Apache License 2.0 text (SPEC §19.1).
func TestLicenseIsApache2(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != apacheLicenseSHA256 {
		t.Errorf("LICENSE SHA-256 is %s, want the verbatim Apache-2.0 text %s", got, apacheLicenseSHA256)
	}
}
