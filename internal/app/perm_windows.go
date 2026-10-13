// SPDX-License-Identifier: Apache-2.0

package app

import "golang.org/x/sys/windows"

// restrictFile limits the file's ACL to SYSTEM and Administrators (protected DACL) — the
// Windows equivalent of mode 0600 for a service-owned secret (SPEC §15.5).
func restrictFile(path string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;OW)")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
