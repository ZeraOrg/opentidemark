// SPDX-License-Identifier: Apache-2.0

package update

// Release signing keys (SPEC §17.6, §17.7): the minisign public keys whose signature over a
// release's checksums.txt is trusted. A valid signature by either key is accepted. The CI key
// signs every release; the backup key is kept offline and signs only when the CI key has to be
// replaced. SECURITY.md and the INSTALL guides publish the same keys (internal/repotest).
const (
	// CISigningKeyID is the key id of CISigningKey as minisign prints it.
	CISigningKeyID = "E3D864A9712D58F3"
	// CISigningKey is the public key of the release workflow's signing key.
	CISigningKey = "RWTzWC1xqWTY457VdX92WvxpPeXXOUhpiGzch3af0NEoe9ENK8bEbiDE"
	// BackupSigningKeyID is the key id of BackupSigningKey as minisign prints it.
	BackupSigningKeyID = "A9028169BE5C889E"
	// BackupSigningKey is the public key of the offline backup signing key.
	BackupSigningKey = "RWSeiFy+aYECqVbvFDNIW//gdsHLox72jDPcY3swGgJm/WQTA+0s9nwP"
)

// TrustedSigningKeys returns the minisign public keys trusted for release checksums.
func TrustedSigningKeys() []string {
	return []string{CISigningKey, BackupSigningKey}
}

// ChecksumsTrustedComment is the trusted comment the release workflow signs with checksums.txt of
// version (without "v"). Requiring it binds a signature to its release, so an older signed
// checksums.txt cannot be served for a newer tag.
func ChecksumsTrustedComment(version string) string {
	return "opentidemark v" + version + " checksums.txt"
}
