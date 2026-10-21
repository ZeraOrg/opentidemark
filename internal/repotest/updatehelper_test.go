// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"strings"
	"testing"
)

// TestUpdateHelperUnitHardened keeps the Linux update helper as SPEC §15.9 and §17.7 describe it:
// root without capabilities or network, writing only to /usr/local/bin and its own state
// directory, started by a PathModified unit on the staged request; and the service unit
// restarting on exit status 3.
func TestUpdateHelperUnitHardened(t *testing.T) {
	units := map[string][]string{
		"deploy/tidemark-update.service": {
			"Type=oneshot", "User=root", "SupplementaryGroups=tidemark",
			"ExecStart=/usr/local/bin/tidemark update install", "TimeoutStartSec=15min",
			"StateDirectory=tidemark-update", "StateDirectoryMode=0755", "ProtectSystem=strict",
			"ReadWritePaths=/usr/local/bin", "PrivateNetwork=yes", "NoNewPrivileges=yes", "CapabilityBoundingSet=",
			"AmbientCapabilities=", "PrivateTmp=yes", "ProtectHome=yes", "SystemCallFilter=@system-service",
			"RestrictAddressFamilies=AF_UNIX", "LockPersonality=yes",
		},
		"deploy/tidemark-update.path": {
			"PathModified=/var/lib/tidemark/update/request.json", "Unit=tidemark-update.service",
		},
		"deploy/tidemark.service": {"SuccessExitStatus=3", "RestartForceExitStatus=3"},
	}
	for file, want := range units {
		lines := map[string]bool{}
		for _, l := range strings.Split(readRepoFile(t, file), "\n") {
			lines[strings.TrimSpace(l)] = true
		}
		for _, w := range want {
			if !lines[w] {
				t.Errorf("%s: missing line %q", file, w)
			}
		}
	}
	helper := readRepoFile(t, "deploy/tidemark-update.service")
	for _, forbidden := range []string{"ReadWritePaths=/var/lib/tidemark\n", "CAP_", "PathExists"} {
		if strings.Contains(helper, forbidden) {
			t.Errorf("deploy/tidemark-update.service must not contain %q", forbidden)
		}
	}
}
