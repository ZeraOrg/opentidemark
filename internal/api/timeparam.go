// SPDX-License-Identifier: Apache-2.0

package api

import (
	"strconv"
	"strings"
	"time"
)

// ParseTimeParam parses RFC 3339 times and relative values "now", "now-15m", "now-7d"
// (units s, m, h, d, w), as used by the from/to parameters (SPEC §11.1).
func ParseTimeParam(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "now" {
		return now, nil
	}
	if rest, ok := strings.CutPrefix(s, "now-"); ok {
		d, err := parseRelative(rest)
		if err != nil {
			return time.Time{}, err
		}
		return now.Add(-d), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errBadRequest("invalid time %q: use RFC 3339 or now-<n><unit>", s)
	}
	return t.UTC(), nil
}

var relativeUnits = map[byte]time.Duration{
	's': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour,
}

func parseRelative(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, errBadRequest("invalid relative time %q", s)
	}
	unit, ok := relativeUnits[s[len(s)-1]]
	n, err := strconv.Atoi(s[:len(s)-1])
	if !ok || err != nil || n < 0 {
		return 0, errBadRequest("invalid relative time %q", s)
	}
	return time.Duration(n) * unit, nil
}
