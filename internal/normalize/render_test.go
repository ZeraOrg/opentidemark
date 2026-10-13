// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"encoding/json"
	"testing"
)

func TestRender(t *testing.T) {
	props := map[string]any{
		"User":    "alice",
		"Elapsed": json.Number("34.5"),
		"Count":   int64(3),
		"Ok":      true,
		"Obj":     map[string]any{"a": json.Number("1")},
		"Arr":     []any{"x", json.Number("2")},
		"0":       "zero",
		"Nil":     nil,
		"Float":   2.5,
	}
	tests := []struct{ tmpl, want string }{
		{"plain text", "plain text"},
		{"User {User} logged in", "User alice logged in"},
		{"{@Obj}", `{"a":1}`},
		{"{$Arr}", `["x",2]`},
		{"took {Elapsed:0.00} ms", "took 34.5 ms"},
		{"[{User,10}]", "[alice]"},
		{"[{User,-10:l}]", "[alice]"},
		{"pos {0}", "pos zero"},
		{"{{escaped}} {User}", "{escaped} alice"},
		{"}} closing", "} closing"},
		{"missing {Nope}", "missing {Nope}"},
		{"bad {Us er}", "bad {Us er}"},
		{"empty {}", "empty {}"},
		{"unterminated {User", "unterminated {User"},
		{"{Count} {Ok} {Nil} {Float}", "3 true null 2.5"},
		{"{@}", "{@}"},
		{"", ""},
		{"ünï {User} ✓", "ünï alice ✓"},
	}
	for _, tt := range tests {
		if got := Render(tt.tmpl, props); got != tt.want {
			t.Errorf("Render(%q) = %q want %q", tt.tmpl, got, tt.want)
		}
	}
}

func TestScalarAndNumber(t *testing.T) {
	tests := []struct {
		in      any
		text    string
		num     float64
		isNum   bool
		isScale bool
	}{
		{"s", "s", 0, false, true},
		{json.Number("12"), "12", 12, true, true},
		{json.Number("bad"), "bad", 0, false, true},
		{true, "true", 1, true, true},
		{false, "false", 0, true, true},
		{1.25, "1.25", 1.25, true, true},
		{float32(0.5), "0.5", 0.5, true, true},
		{7, "7", 7, true, true},
		{int64(-8), "-8", -8, true, true},
		{int32(9), "9", 9, true, true},
		{uint64(10), "10", 10, true, true},
		{uint32(11), "11", 11, true, true},
		{[]byte{1, 2}, "AQI=", 0, false, true},
		{1e22, "1e+22", 1e22, true, true},
		{1e-7, "1e-07", 1e-7, true, true},
		{0.0, "0", 0, true, true},
		{nil, "", 0, false, false},
		{map[string]any{}, "", 0, false, false},
	}
	for _, tt := range tests {
		s, ok := Scalar(tt.in)
		if ok != tt.isScale || (ok && s != tt.text) {
			t.Errorf("Scalar(%#v) = %q,%v", tt.in, s, ok)
		}
		n, ok := Number(tt.in)
		if ok != tt.isNum || (ok && n != tt.num) {
			t.Errorf("Number(%#v) = %v,%v", tt.in, n, ok)
		}
	}
	if Text(nil) != "null" || Text([]any{"a"}) != `["a"]` {
		t.Error("Text")
	}
}

func TestIsInteger(t *testing.T) {
	tests := []struct {
		in any
		ok bool
	}{
		{json.Number("3"), true}, {json.Number("3.5"), false}, {3, true}, {int64(3), true},
		{int32(3), true}, {3.0, true}, {3.5, false}, {"3", false},
	}
	for _, tt := range tests {
		if _, ok := isInteger(tt.in); ok != tt.ok {
			t.Errorf("isInteger(%#v) = %v", tt.in, ok)
		}
	}
}
