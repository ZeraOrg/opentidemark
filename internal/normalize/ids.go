// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"encoding/hex"
	"strings"
)

// Hex lengths of W3C trace and span identifiers.
const (
	traceIDHexLen = 32
	spanIDHexLen  = 16
	maxRawIDLen   = 128
)

// NormalizeTraceID returns a 32-character lower-case hex trace id when possible
// (dashes removed, 64-bit ids left-padded with zeros). Other non-empty inputs are kept
// lower-cased and truncated to 128 characters.
func NormalizeTraceID(s string) string { return normalizeID(s, traceIDHexLen) } //nolint:revive // established name used by every ingest protocol

// NormalizeSpanID returns a 16-character lower-case hex span id when possible.
func NormalizeSpanID(s string) string { return normalizeID(s, spanIDHexLen) } //nolint:revive // established name used by every ingest protocol

func normalizeID(s string, width int) string {
	t := strings.ToLower(strings.TrimSpace(s))
	compact := strings.ReplaceAll(t, "-", "")
	if isHex(compact) && len(compact) <= width && len(compact) > 0 {
		if allZero(compact) {
			return ""
		}
		return strings.Repeat("0", width-len(compact)) + compact
	}
	if len(t) > maxRawIDLen {
		t = t[:maxRawIDLen]
	}
	return t
}

// HexID encodes binary ids (OTLP) as lower-case hex; all-zero or empty ids become "".
func HexID(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := hex.EncodeToString(b)
	if allZero(s) {
		return ""
	}
	return s
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func allZero(s string) bool { return strings.Trim(s, "0") == "" }
