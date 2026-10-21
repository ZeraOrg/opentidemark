// SPDX-License-Identifier: Apache-2.0

package update

// Locations of the Linux install (deploy/install.sh, deploy/tidemark-update.service).
const (
	linuxBinary  = "/usr/local/bin/tidemark"
	linuxStaging = "/var/lib/tidemark/update"
	linuxWork    = "/var/lib/tidemark-update"
)

// DefaultPaths returns the helper protocol locations of this operating system.
func DefaultPaths() (HelperPaths, error) {
	return HelperPaths{Binary: linuxBinary, Staging: linuxStaging, Work: linuxWork}, nil
}
