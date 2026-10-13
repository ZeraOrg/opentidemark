// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package app

import "errors"

// diskSpace is not implemented on development-only platforms (macOS); doctor reports a warning.
func diskSpace(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk space check not supported on this platform")
}
