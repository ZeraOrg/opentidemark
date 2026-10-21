// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package update

import "os"

// Other systems have no release archive (SPEC §17.1), so nothing is staged or installed there.

func openRegular(string) (*os.File, error) { return nil, ErrUnsupportedPlatform }

func replaceBinary(string) error { return ErrUnsupportedPlatform }

func restorePrevious(string) error { return ErrUnsupportedPlatform }
