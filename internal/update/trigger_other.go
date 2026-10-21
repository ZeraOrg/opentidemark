// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package update

// StartHelper: no helper exists on other systems.
func StartHelper() error { return ErrUnsupportedPlatform }

func helperServiceRegistered() bool { return false }
