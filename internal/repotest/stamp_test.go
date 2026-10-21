// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"strings"
	"testing"
)

// TestDistributionStamp checks that the release builds stamp the binary's distribution, which
// enables the update check (SPEC §17.6, §17.7): `make dist` and `.\build.ps1 dist` as archive,
// the Dockerfile as container. Without the stamp a release would behave as a development build.
func TestDistributionStamp(t *testing.T) {
	checks := []struct{ file, want string }{
		{"Makefile", `-X main.version=$(DIST_VERSION) -X main.distribution=archive"`},
		{"build.ps1", `-X main.distribution=$Distribution`},
		{"build.ps1", `-Version $version -Distribution 'archive'`},
		{"Dockerfile", `-X main.version=${VERSION} -X main.distribution=container"`},
		{"cmd/tidemark/main.go", "var distribution = \"\""},
	}
	for _, c := range checks {
		if !strings.Contains(readRepoFile(t, c.file), c.want) {
			t.Errorf("%s must contain %q", c.file, c.want)
		}
	}
	if strings.Contains(readRepoFile(t, "Makefile"), "LDFLAGS := -s -w -X main.version=$(VERSION) -X main.distribution") {
		t.Error("Makefile: only dist may stamp a distribution; build and cross are development builds")
	}
}
