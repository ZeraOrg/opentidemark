// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package update

// DefaultPaths: no helper is installed on this operating system.
func DefaultPaths() (HelperPaths, error) { return HelperPaths{}, ErrUnsupportedPlatform }
