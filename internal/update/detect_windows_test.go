// SPDX-License-Identifier: Apache-2.0

package update

import (
	"strings"
	"testing"
)

func TestDetectInstallKindWindows(t *testing.T) {
	p := hostProbe{exists: func(string) bool { return true }, getenv: func(string) string { return "set" }}
	tests := []struct {
		service bool
		want    InstallKind
	}{
		{true, KindWindowsService},
		{false, KindManual},
	}
	for _, tc := range tests {
		b := Build{Version: "0.4.0", Distribution: DistributionArchive, WindowsService: tc.service}
		if got := detectInstallKind(b, p); got != tc.want {
			t.Errorf("service=%v: got %s, want %s", tc.service, got, tc.want)
		}
	}
}

func TestHelperInstalledWindows(t *testing.T) {
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		kind       InstallKind
		exe        string
		registered bool
		want       bool
	}{
		{"installed", KindWindowsService, paths.Binary, true, true},
		{"path case", KindWindowsService, strings.ToUpper(paths.Binary), true, true},
		{"not registered", KindWindowsService, paths.Binary, false, false},
		{"other binary", KindWindowsService, `D:\tools\tidemark.exe`, true, false},
		{"not a service", KindManual, paths.Binary, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := hostProbe{executable: func() (string, error) { return tc.exe, nil }, helperService: func() bool { return tc.registered }}
			if got := helperInstalled(tc.kind, p); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
