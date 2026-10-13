// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	ref := time.Date(2026, 10, 13, 8, 30, 15, 0, time.UTC)
	refMs := ref.Add(123 * time.Millisecond)
	tests := []struct {
		in   any
		want time.Time
		ok   bool
	}{
		{"2026-10-13T08:30:15Z", ref, true},
		{"2026-10-13T10:30:15+02:00", ref, true},
		{"2026-10-13T08:30:15.123Z", refMs, true},
		{"2026-10-13T08:30:15.1230000+00:00", refMs, true},
		{"2026-10-13T08:30:15", ref, true},
		{"2026-10-13T08:30:15.123", refMs, true},
		{"2026-10-13 08:30:15", ref, true},
		{"2026-10-13 08:30:15.123", refMs, true},
		{"2026-10-13 10:30:15+02:00", ref, true},
		{"2026-10-13T08:30:15+0000", ref, true},
		{"2026-10-13", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), true},
		{"1791880215", ref, true},
		{"1791880215.123", refMs, true},
		{json.Number("1791880215"), ref, true},
		{json.Number("1791880215123"), refMs, true},
		{json.Number("1791880215123000"), refMs, true},
		{json.Number("1791880215123000000"), refMs, true},
		{json.Number("1791880215.5"), ref.Add(500 * time.Millisecond), true},
		{float64(1791880215), ref, true},
		{float64(1791880215123), refMs, true},
		{int64(1791880215123000), refMs, true},
		{"", time.Time{}, false},
		{"yesterday", time.Time{}, false},
		{"NaN", time.Time{}, false},
		{true, time.Time{}, false},
		{nil, time.Time{}, false},
		{map[string]any{}, time.Time{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseTime(tt.in)
		if ok != tt.ok || !got.Equal(tt.want) {
			t.Errorf("ParseTime(%#v) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
		if ok && got.Location() != time.UTC {
			t.Errorf("ParseTime(%#v) not UTC", tt.in)
		}
	}
}

func TestIDs(t *testing.T) {
	tests := []struct{ in, trace, span string }{
		{"4BF92F3577B34DA6A3CE929D0E0E4736", "4bf92f3577b34da6a3ce929d0e0e4736", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"00f067aa0ba902b7", "000000000000000000f067aa0ba902b7", "00f067aa0ba902b7"},
		{"4bf92f35-77b3-4da6-a3ce-929d0e0e4736", "4bf92f3577b34da6a3ce929d0e0e4736", "4bf92f35-77b3-4da6-a3ce-929d0e0e4736"},
		{"", "", ""},
		{"00000000000000000000000000000000", "", "00000000000000000000000000000000"},
		{"not-hex", "not-hex", "not-hex"},
	}
	for _, tt := range tests {
		if got := NormalizeTraceID(tt.in); got != tt.trace {
			t.Errorf("trace(%q) = %q want %q", tt.in, got, tt.trace)
		}
		if got := NormalizeSpanID(tt.in); got != tt.span {
			t.Errorf("span(%q) = %q want %q", tt.in, got, tt.span)
		}
	}
	long := make([]byte, 200)
	for i := range long {
		long[i] = 'z'
	}
	if len(NormalizeTraceID(string(long))) != maxRawIDLen {
		t.Error("long id not truncated")
	}
	if HexID(nil) != "" || HexID(make([]byte, 8)) != "" || HexID([]byte{0xab, 0x01}) != "ab01" {
		t.Error("HexID")
	}
}

func TestParseEpochEdgeCases(t *testing.T) {
	bad := []string{"-5", "1.2.3", "1.x", "1e400", "abc"}
	for _, s := range bad {
		if _, ok := parseEpochText(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
	got, ok := parseEpochText("1.7918802151230e9")
	if !ok || !got.Equal(time.Date(2026, 10, 13, 8, 30, 15, 123000000, time.UTC)) {
		t.Errorf("exponent: %v %v", got, ok)
	}
	if got, ok := ParseTime(uint64(1791880215)); !ok || got.Unix() != 1791880215 {
		t.Errorf("uint64: %v", got)
	}
}
