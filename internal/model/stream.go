// SPDX-License-Identifier: Apache-2.0

package model

import (
	"regexp"
	"strings"
)

// DefaultStream is used when no stream name is given or the input normalizes to nothing.
const DefaultStream = "default"

// MaxStreamNameLen is the maximum length of a stream name.
const MaxStreamNameLen = 64

// streamPattern is the validation regex for stream names (SPEC §8.1).
var streamPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidStreamName reports whether s is a valid, already-normalized stream name.
func ValidStreamName(s string) bool { return streamPattern.MatchString(s) }

// NormalizeStream converts arbitrary input into a valid stream name: lower-case, invalid
// characters replaced by '-', leading '.', '_' and '-' trimmed, truncated to 64 characters;
// empty results become "default".
func NormalizeStream(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if len(out) > MaxStreamNameLen {
		out = out[:MaxStreamNameLen]
	}
	if out == "" {
		return DefaultStream
	}
	return out
}
