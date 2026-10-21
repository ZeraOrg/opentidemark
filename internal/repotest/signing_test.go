// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ZeraOrg/opentidemark/internal/update"
)

// minisignPublicKey matches a minisign Ed25519 public key in base64 ("Ed" + key id + key).
var minisignPublicKey = regexp.MustCompile(`RW[A-Za-z0-9+/]{54}`)

// signingKeyFiles are the public files that print the release signing keys (SPEC §17.6).
var signingKeyFiles = []string{
	"SECURITY.md",
	"deploy/install/INSTALL-linux.md",
	"deploy/install/INSTALL-windows.md",
	"deploy/install/INSTALL-docker.md",
	"docs/installation.md",
	"scripts/release-notes.sh",
	".github/workflows/release.yml",
}

// TestUpdateSigningKeysDocumented keeps the published release signing keys equal to the keys
// embedded in the binary (internal/update/keys.go): every file names both keys and no other
// minisign key, and SECURITY.md gives both key ids.
func TestUpdateSigningKeysDocumented(t *testing.T) {
	trusted := update.TrustedSigningKeys()
	for _, rel := range signingKeyFiles {
		text := readRepoFile(t, rel)
		for _, key := range trusted {
			if !strings.Contains(text, key) {
				t.Errorf("%s must name the release signing key %s", rel, key)
			}
		}
		for _, key := range minisignPublicKey.FindAllString(text, -1) {
			if !slices.Contains(trusted, key) {
				t.Errorf("%s names the minisign key %s, which internal/update does not trust", rel, key)
			}
		}
	}
	security := readRepoFile(t, "SECURITY.md")
	for _, id := range []string{update.CISigningKeyID, update.BackupSigningKeyID} {
		if !strings.Contains(security, id) {
			t.Errorf("SECURITY.md must give the key id %s", id)
		}
	}
}

// workflowJob matches the start of a job (two-space indented key) in a workflow file.
var workflowJob = regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n`)

// workflowJobs splits the jobs: section of a workflow into job id → job text.
func workflowJobs(t *testing.T, workflow string) map[string]string {
	t.Helper()
	_, jobs, ok := strings.Cut(workflow, "\njobs:\n")
	if !ok {
		t.Fatal("workflow has no jobs: section")
	}
	out := map[string]string{}
	locs := workflowJob.FindAllStringSubmatchIndex(jobs, -1)
	for i, loc := range locs {
		end := len(jobs)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[jobs[loc[2]:loc[3]]] = jobs[loc[0]:end]
	}
	return out
}

// inOrder reports whether every part occurs in text, each after the previous one.
func inOrder(text string, parts ...string) bool {
	for _, p := range parts {
		i := strings.Index(text, p)
		if i < 0 {
			return false
		}
		text = text[i+len(p):]
	}
	return true
}

var (
	minisignSHA256  = regexp.MustCompile(`(?m)^  MINISIGN_SHA256: [0-9a-f]{64}$`)
	minisignVersion = regexp.MustCompile(`(?m)^  MINISIGN_VERSION: "\d+\.\d+(\.\d+)?"$`)
)

// TestReleaseSignsChecksums keeps the minisign signature of checksums.txt in every release
// (SPEC §17.6): the sign job alone uses the environment "release" (in the public repository
// only; private repositories may not support environments), runs after the archives and
// the documentation bundle exist, installs the pinned minisign, signs with the trusted comment
// the updater requires, fails without the key and attests afterwards; the release uploads the
// signature and its notes show how to verify it.
func TestReleaseSignsChecksums(t *testing.T) {
	workflow := readRepoFile(t, ".github/workflows/release.yml")
	jobs := workflowJobs(t, workflow)
	sign, build, release := jobs["sign"], jobs["build"], jobs["release"]
	if sign == "" || build == "" || release == "" {
		t.Fatalf("release.yml needs the jobs build, sign and release (found %d jobs)", len(jobs))
	}
	const signEnv = "\n    environment: ${{ github.repository == 'ZeraOrg/opentidemark' && 'release' || '' }}\n"
	if strings.Count(workflow, "environment:") != 1 || !strings.Contains(sign, signEnv) {
		t.Error("release.yml: only the sign job may use an environment, and it must be \"release\"")
	}
	ciJobs := workflowJobs(t, readRepoFile(t, ".github/workflows/ci.yml"))
	if !strings.Contains(ciJobs["release"], "\n    secrets: inherit\n") {
		t.Error("ci.yml: the release job must pass its secrets (secrets: inherit), or the sign job gets no key")
	}
	if strings.Count(workflow, "secrets.") != 1 || !strings.Contains(sign, "${{ secrets.RELEASE_SIGNING_KEY }}") {
		t.Error("release.yml: the signing key secret must be used by the sign job only")
	}
	if !strings.Contains(build, "make docs-offline") || strings.Contains(build, "attest-build-provenance") || strings.Contains(build, "id-token") {
		t.Error("release.yml: the build job builds dist/ (docs bundle included) and neither signs nor attests")
	}
	comment := update.ChecksumsTrustedComment("${VERSION}")
	if !strings.Contains(sign, "needs: [prepare, build]") || !inOrder(sign,
		"name: dist", "sha256sum -c -", "minisign -S", `-t "`+comment+`"`, comment, "attest-build-provenance",
		"subject-checksums: dist/checksums.txt", "path: dist/checksums.txt.minisig") {
		t.Error("release.yml: the sign job must download dist, check the minisign archive, sign with the trusted comment, verify, attest and upload the signature, in that order")
	}
	if !strings.Contains(sign, `elif [[ "$PUBLIC" == true ]]; then`) || !strings.Contains(sign, "exit 1") {
		t.Error("release.yml: the public release must fail without the signing key")
	}
	if !minisignSHA256.MatchString(workflow) || !minisignVersion.MatchString(workflow) {
		t.Error("release.yml: minisign must be pinned by MINISIGN_VERSION and MINISIGN_SHA256")
	}
	if !strings.Contains(release, "needs: [prepare, build, sign, image]") || !inOrder(release, "name: dist", "name: signature", "gh release create") {
		t.Error("release.yml: the release job must wait for the sign job and publish the signature with dist/")
	}
	notes := readRepoFile(t, "scripts/release-notes.sh")
	if !strings.Contains(notes, "minisign -Vm checksums.txt -P "+update.CISigningKey) || !strings.Contains(notes, "gh attestation verify") {
		t.Error("scripts/release-notes.sh must show the minisign and attestation verification commands")
	}
}
