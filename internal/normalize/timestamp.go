// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
)

// Epoch magnitude thresholds used to guess the unit of a numeric timestamp.
const (
	epochSecondsMax = 1e11 // below: seconds (until year 5138)
	epochMillisMax  = 1e14 // below: milliseconds
	epochMicrosMax  = 1e17 // below: microseconds; above: nanoseconds
)

// timeLayouts are tried in order for textual timestamps; zone-less layouts are read as UTC.
var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999Z0700",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04Z07:00",
	"2006-01-02T15:04",
	"2006-01-02",
}

// ParseTime interprets a timestamp value: RFC 3339 / ISO-8601 strings (zone-less ⇒ UTC) or
// non-negative epoch numbers whose unit (s/ms/µs/ns) is chosen by magnitude. The result is UTC.
func ParseTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		return parseTimeString(x)
	case json.Number:
		return parseEpochText(x.String())
	case bool, nil:
		return time.Time{}, false
	default:
		if s, ok := Scalar(v); ok {
			return parseEpochText(s)
		}
	}
	return time.Time{}, false
}

func parseTimeString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return parseEpochText(s)
}

// Digits of sub-second precision per epoch unit.
const (
	digitsSeconds = 0
	digitsMillis  = 3
	digitsMicros  = 6
	digitsNanos   = 9
)

// parseEpochText parses a decimal epoch exactly (no float rounding); exponent forms are
// first expanded to plain decimal text.
func parseEpochText(s string) (time.Time, bool) {
	if strings.ContainsAny(s, "eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return time.Time{}, false
		}
		s = strconv.FormatFloat(f, 'f', -1, 64)
	}
	intPart, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil || n < 0 || !allDigits(frac) {
		return time.Time{}, false
	}
	digits := epochDigits(float64(n))
	unit := pow10(digits)
	sec := n / unit
	nsec := (n % unit) * pow10(digitsNanos-digits)
	if frac != "" {
		width := digitsNanos - digits
		frac = (frac + strings.Repeat("0", digitsNanos))[:width]
		f, _ := strconv.ParseInt(frac, 10, 64) // frac holds only digits, checked above
		nsec += f
	}
	return time.Unix(sec, nsec).UTC(), true
}

func epochDigits(abs float64) int64 {
	switch {
	case abs < epochSecondsMax:
		return digitsSeconds
	case abs < epochMillisMax:
		return digitsMillis
	case abs < epochMicrosMax:
		return digitsMicros
	default:
		return digitsNanos
	}
}

func pow10(n int64) int64 {
	p := int64(1)
	for ; n > 0; n-- {
		p *= 10
	}
	return p
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
