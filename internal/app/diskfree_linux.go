// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// diskSpace returns the bytes available to unprivileged users and the total size of the file
// system holding path.
func diskSpace(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize) //nolint:gosec // block size is positive
	return st.Bavail * bsize, st.Blocks * bsize, nil
}
