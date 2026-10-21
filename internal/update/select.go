// SPDX-License-Identifier: Apache-2.0

package update

import "strings"

// Select returns the release to offer to a node running current, or nil (SPEC §17.7): drafts,
// tags other than v<version>, and versions not above current are ignored; a stable node only
// considers stable releases, a pre-release node every release; the highest precedence wins.
// The notes URL is built from the version, never taken from the response.
func Select(current Version, releases []ReleaseInfo) *Release {
	var best *Version
	var bestInfo ReleaseInfo
	for _, r := range releases {
		v, ok := candidate(current, r)
		if ok && (best == nil || v.Compare(*best) > 0) {
			best, bestInfo = &v, r
		}
	}
	if best == nil {
		return nil
	}
	return &Release{Version: best.String(), Prerelease: best.Prerelease(), PublishedAt: bestInfo.PublishedAt.UTC(),
		NotesURL: releaseTagURL + best.String()}
}

// candidate parses r's version and reports whether it may be offered to current.
func candidate(current Version, r ReleaseInfo) (Version, bool) {
	if r.Draft || !strings.HasPrefix(r.Tag, "v") {
		return Version{}, false
	}
	v, err := ParseVersion(strings.TrimPrefix(r.Tag, "v"))
	if err != nil || v.Compare(current) <= 0 {
		return Version{}, false
	}
	if !current.Prerelease() && (v.Prerelease() || r.Prerelease) {
		return Version{}, false
	}
	return v, true
}
