// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in  string
		ok  bool
		pre bool
	}{
		{"0.1.0", true, false},
		{"1.22.333", true, false},
		{"0.2.0-rc.1", true, true},
		{"1.0.0-alpha-beta.x.7", true, true},
		{"1.0.0-0", true, true},
		{"v1.0.0", false, false},
		{"1.0", false, false},
		{"01.0.0", false, false},
		{"1.0.0+build.1", false, false},
		{"1.0.0-", false, false},
		{"1.0.0-rc..1", false, false},
		{"1.0.0-rc.01", false, false},
		{"1.0.0-rc.1.", false, false},
		{"dev", false, false},
		{"0.4.0-3-gabc1234", true, true}, // git describe output is a valid pre-release version
		{"99999999999999999999.0.0", false, false},
		{"", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			v, err := ParseVersion(tc.in)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidVersion) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if v.Prerelease() != tc.pre || v.String() != tc.in {
				t.Fatalf("prerelease %v string %q", v.Prerelease(), v.String())
			}
		})
	}
}

func TestVersionCompare(t *testing.T) {
	// Ascending precedence, from the SemVer 2.0 specification §11 plus numeric edge cases.
	ordered := []string{
		"0.9.9", "0.10.0-rc.1", "0.10.0", "1.0.0-0", "1.0.0-9", "1.0.0-10", "1.0.0-99999999999999999999",
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11",
		"1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
	}
	vs := make([]Version, len(ordered))
	for i, s := range ordered {
		v, err := ParseVersion(s)
		if err != nil {
			t.Fatal(err)
		}
		vs[i] = v
	}
	for i := range vs {
		for j := range vs {
			want := cmpUint(uint64(i), uint64(j))
			if got := vs[i].Compare(vs[j]); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}
