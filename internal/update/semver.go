// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidVersion is returned for a string that is not a release version.
var ErrInvalidVersion = errors.New("invalid version")

// versionPattern is the release version of SPEC §17.6: SemVer 2.0 without a "v" prefix and
// without build metadata.
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`)

// Version is a parsed release version. Its zero value is not a valid version.
type Version struct {
	major, minor, patch uint64
	pre                 []string // pre-release identifiers; empty for a stable release
	raw                 string
}

// ParseVersion parses a release version (SPEC §17.6). Pre-release identifiers must also follow
// SemVer 2.0: not empty, and numeric ones without leading zeros.
func ParseVersion(s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
	}
	v := Version{raw: s}
	for i, dst := range []*uint64{&v.major, &v.minor, &v.patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
		*dst = n
	}
	if m[4] == "" {
		return v, nil
	}
	v.pre = strings.Split(m[4][1:], ".")
	for _, id := range v.pre {
		if id == "" || (isNumeric(id) && len(id) > 1 && id[0] == '0') {
			return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, s)
		}
	}
	return v, nil
}

// String returns the version as parsed.
func (v Version) String() string { return v.raw }

// Prerelease reports whether v has a pre-release suffix.
func (v Version) Prerelease() bool { return len(v.pre) > 0 }

// Compare returns -1, 0 or +1 as v has lower, equal or higher precedence than o (SemVer 2.0 §11).
func (v Version) Compare(o Version) int {
	for _, p := range [][2]uint64{{v.major, o.major}, {v.minor, o.minor}, {v.patch, o.patch}} {
		if c := cmpUint(p[0], p[1]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1 // a stable release ranks above its pre-releases
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(o.pre); i++ {
		if c := compareIdentifier(v.pre[i], o.pre[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(v.pre)), uint64(len(o.pre)))
}

// compareIdentifier orders pre-release identifiers: numeric ones numerically and below
// alphanumeric ones, alphanumeric ones in ASCII order.
func compareIdentifier(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		if c := cmpUint(uint64(len(a)), uint64(len(b))); c != 0 {
			return c // no leading zeros, so the longer number is larger (and may exceed uint64)
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func isNumeric(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
