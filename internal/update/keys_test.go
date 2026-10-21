// SPDX-License-Identifier: Apache-2.0

package update

import "testing"

// TestTrustedSigningKeys checks the trusted set; TestParsePublicKey checks each key's id.
func TestTrustedSigningKeys(t *testing.T) {
	if got := TrustedSigningKeys(); len(got) != 2 || got[0] != CISigningKey || got[1] != BackupSigningKey {
		t.Errorf("TrustedSigningKeys() = %q", got)
	}
}

func TestChecksumsTrustedComment(t *testing.T) {
	if got := ChecksumsTrustedComment("1.2.3-rc.1"); got != "opentidemark v1.2.3-rc.1 checksums.txt" {
		t.Errorf("ChecksumsTrustedComment = %q", got)
	}
}
