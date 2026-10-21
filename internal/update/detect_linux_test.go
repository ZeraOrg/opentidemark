// SPDX-License-Identifier: Apache-2.0

package update

import "testing"

func TestDetectInstallKindLinux(t *testing.T) {
	archive := Build{Version: "0.4.0", Distribution: DistributionArchive}
	tests := []struct {
		name  string
		files map[string]bool
		env   map[string]string
		want  InstallKind
	}{
		{"docker", map[string]bool{"/.dockerenv": true}, nil, KindContainer},
		{"podman", map[string]bool{"/run/.containerenv": true}, map[string]string{"INVOCATION_ID": "x"}, KindContainer},
		{"systemd", nil, map[string]string{"INVOCATION_ID": "0123abcd"}, KindSystemd},
		{"manual", nil, nil, KindManual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := hostProbe{exists: func(p string) bool { return tc.files[p] }, getenv: func(k string) string { return tc.env[k] }}
			if got := detectInstallKind(archive, p); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestHelperInstalledLinux(t *testing.T) {
	tests := []struct {
		name string
		kind InstallKind
		exe  string
		unit bool
		want bool
	}{
		{"installed", KindSystemd, "/usr/local/bin/tidemark", true, true},
		{"no path unit", KindSystemd, "/usr/local/bin/tidemark", false, false},
		{"other binary", KindSystemd, "/opt/tidemark/tidemark", true, false},
		{"not a service", KindManual, "/usr/local/bin/tidemark", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := hostProbe{exists: func(path string) bool { return tc.unit && path == helperPathUnit },
				executable: func() (string, error) { return tc.exe, nil }}
			if got := helperInstalled(tc.kind, p); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
