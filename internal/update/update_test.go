// SPDX-License-Identifier: Apache-2.0

package update

import "testing"

func TestBuildDevelopment(t *testing.T) {
	tests := []struct {
		b    Build
		want bool
	}{
		{Build{Version: "0.4.0", Distribution: DistributionArchive}, false},
		{Build{Version: "0.5.0-rc.1", Distribution: DistributionContainer}, false},
		{Build{Version: "0.4.0"}, true},                                    // make build, go build
		{Build{Version: "0.4.0-dev"}, true},                                // make dev
		{Build{Version: "dev", Distribution: DistributionContainer}, true}, // local docker build
		{Build{Version: "v0.4.0-3-gabc", Distribution: DistributionArchive}, true},
		{Build{Version: "0.4.0", Distribution: "other"}, true},
	}
	for _, tc := range tests {
		if got := tc.b.Development(); got != tc.want {
			t.Errorf("%+v: Development() = %v", tc.b, got)
		}
	}
}

func TestInstallApply(t *testing.T) {
	tests := []struct {
		name string
		in   Install
		want string // "" = supported
	}{
		{"disabled", Install{Enabled: false, Kind: KindSystemd, Platform: "linux/amd64", Helper: true}, ReasonDisabled},
		{"dev", Install{Enabled: true, Kind: KindDev, Platform: "linux/amd64"}, ReasonDev},
		{"container", Install{Enabled: true, Kind: KindContainer, Platform: "linux/amd64"}, ReasonContainer},
		{"ha", Install{Enabled: true, Kind: KindSystemd, HA: true, Platform: "linux/amd64", Helper: true}, ReasonHA},
		{"platform", Install{Enabled: true, Kind: KindSystemd, Platform: "linux/riscv64", Helper: true}, ReasonUnsupportedPlatform},
		{"manual", Install{Enabled: true, Kind: KindManual, Platform: "windows/amd64"}, ReasonNotAService},
		{"systemd without helper", Install{Enabled: true, Kind: KindSystemd, Platform: "linux/arm64"}, ReasonHelperMissing},
		{"windows service without helper", Install{Enabled: true, Kind: KindWindowsService, Platform: "windows/amd64"}, ReasonHelperMissing},
		{"systemd", Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64", Helper: true}, ""},
		{"windows service", Install{Enabled: true, Kind: KindWindowsService, Platform: "windows/amd64", Helper: true}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.in.Apply()
			if got.Supported != (tc.want == "") || got.Reason != tc.want {
				t.Fatalf("got %+v, want reason %q", got, tc.want)
			}
		})
	}
}

func TestDetectInstallKindCommon(t *testing.T) {
	never := hostProbe{exists: func(string) bool { return false }, getenv: func(string) string { return "" }}
	tests := []struct {
		b    Build
		want InstallKind
	}{
		{Build{Version: "dev", WindowsService: true}, KindDev},
		{Build{Version: "0.4.0", Distribution: DistributionContainer}, KindContainer},
		{Build{Version: "0.4.0", Distribution: DistributionArchive}, KindManual},
	}
	for _, tc := range tests {
		if got := detectInstallKind(tc.b, never); got != tc.want {
			t.Errorf("%+v: %s, want %s", tc.b, got, tc.want)
		}
	}
}
