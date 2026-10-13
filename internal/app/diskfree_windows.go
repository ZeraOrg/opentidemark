// SPDX-License-Identifier: Apache-2.0

package app

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// diskSpace returns the bytes available to the caller and the total size of the volume
// holding path.
func diskSpace(path string) (free, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, fmt.Errorf("disk space %s: %w", path, err)
	}
	var avail, size, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &size, &totalFree); err != nil {
		return 0, 0, fmt.Errorf("GetDiskFreeSpaceEx %s: %w", path, err)
	}
	return avail, size, nil
}
