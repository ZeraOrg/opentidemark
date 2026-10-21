// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !windows

package update

// detectPlatform: other systems have no supported service installation (SPEC §17.1).
func detectPlatform(Build, hostProbe) InstallKind { return KindManual }

// helperInstalled: no helper exists on other systems.
func helperInstalled(InstallKind, hostProbe) bool { return false }
