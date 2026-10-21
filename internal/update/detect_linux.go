// SPDX-License-Identifier: Apache-2.0

package update

// Container markers written by Docker and Podman, and the helper's path unit.
const (
	dockerEnvFile    = "/.dockerenv"
	containerEnvFile = "/run/.containerenv"
	helperPathUnit   = "/etc/systemd/system/tidemark-update.path"
)

// helperInstalled: a systemd service running /usr/local/bin/tidemark with the helper's path unit
// installed (deploy/install.sh).
func helperInstalled(kind InstallKind, p hostProbe) bool {
	if kind != KindSystemd {
		return false
	}
	exe, err := p.executable()
	return err == nil && exe == linuxBinary && p.exists(helperPathUnit)
}

// detectPlatform: a container marker means a container even for an archive binary; systemd
// sets INVOCATION_ID for every unit it starts.
func detectPlatform(_ Build, p hostProbe) InstallKind {
	switch {
	case p.exists(dockerEnvFile) || p.exists(containerEnvFile):
		return KindContainer
	case p.getenv("INVOCATION_ID") != "":
		return KindSystemd
	default:
		return KindManual
	}
}
