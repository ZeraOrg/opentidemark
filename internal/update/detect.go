// SPDX-License-Identifier: Apache-2.0

package update

import "os"

// hostProbe reads the facts install-kind and helper detection need (injected in tests).
type hostProbe struct {
	exists        func(path string) bool
	getenv        func(key string) string
	executable    func() (string, error)
	helperService func() bool // Windows: the update service is registered
}

func defaultProbe() hostProbe {
	return hostProbe{exists: fileExists, getenv: os.Getenv, executable: os.Executable, helperService: helperServiceRegistered}
}

// DetectInstallKind reports how the running binary was installed (SPEC §17.7). A development
// build is always KindDev; the rest is decided per operating system.
func DetectInstallKind(b Build) InstallKind {
	return detectInstallKind(b, defaultProbe())
}

// HelperInstalled reports whether the update helper of a service install of kind is installed
// for this binary (SPEC §17.7), decided per operating system.
func HelperInstalled(kind InstallKind) bool {
	return helperInstalled(kind, defaultProbe())
}

func detectInstallKind(b Build, p hostProbe) InstallKind {
	if b.Development() {
		return KindDev
	}
	if b.Distribution == DistributionContainer {
		return KindContainer
	}
	return detectPlatform(b, p)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
