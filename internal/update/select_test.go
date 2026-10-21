// SPDX-License-Identifier: Apache-2.0

package update

import (
	"testing"
	"time"
)

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func rel(tag string) ReleaseInfo { return ReleaseInfo{Tag: tag, PublishedAt: testNow} }

func TestSelect(t *testing.T) {
	draft := rel("v9.0.0")
	draft.Draft = true
	flagged := rel("v0.6.0") // stable version number but marked as pre-release on GitHub
	flagged.Prerelease = true
	tests := []struct {
		name     string
		current  string
		releases []ReleaseInfo
		want     string // "" = nothing offered
	}{
		{"newer stable", "0.4.0", []ReleaseInfo{rel("v0.5.0")}, "0.5.0"},
		{"same version", "0.4.0", []ReleaseInfo{rel("v0.4.0")}, ""},
		{"never downgrade", "0.4.0", []ReleaseInfo{rel("v0.3.9")}, ""},
		{"draft ignored", "0.4.0", []ReleaseInfo{draft}, ""},
		{"tag without v", "0.4.0", []ReleaseInfo{rel("0.5.0")}, ""},
		{"tag with build metadata", "0.4.0", []ReleaseInfo{rel("v0.5.0+b1")}, ""},
		{"odd tag", "0.4.0", []ReleaseInfo{rel("nightly"), rel("v0.5")}, ""},
		{"stable skips pre-release", "0.4.0", []ReleaseInfo{rel("v0.5.0-rc.1")}, ""},
		{"stable skips flagged pre-release", "0.4.0", []ReleaseInfo{flagged}, ""},
		{"highest wins", "0.4.0", []ReleaseInfo{rel("v0.5.0"), rel("v0.10.0"), rel("v0.9.1")}, "0.10.0"},
		{"pre-release sees pre-releases", "0.5.0-rc.1", []ReleaseInfo{rel("v0.5.0-rc.2"), rel("v0.4.9")}, "0.5.0-rc.2"},
		{"pre-release sees stable", "0.5.0-rc.1", []ReleaseInfo{rel("v0.5.0-rc.2"), rel("v0.5.0")}, "0.5.0"},
		{"pre-release precedence", "0.5.0-rc.1", []ReleaseInfo{rel("v0.5.0-rc.10"), rel("v0.5.0-rc.9")}, "0.5.0-rc.10"},
		{"pre-release no downgrade", "0.5.0-rc.2", []ReleaseInfo{rel("v0.5.0-rc.1"), rel("v0.4.0")}, ""},
		{"empty", "0.4.0", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Select(mustVersion(t, tc.current), tc.releases)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("offered %+v", got)
				}
				return
			}
			if got == nil || got.Version != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
			if got.NotesURL != "https://github.com/ZeraOrg/opentidemark/releases/tag/v"+tc.want {
				t.Errorf("notes URL %q", got.NotesURL)
			}
			if got.Prerelease != mustVersion(t, tc.want).Prerelease() || !got.PublishedAt.Equal(testNow) ||
				got.PublishedAt.Location() != time.UTC {
				t.Errorf("release %+v", got)
			}
		})
	}
}
