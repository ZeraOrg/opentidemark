// SPDX-License-Identifier: Apache-2.0

package jsonx

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckDepth(t *testing.T) {
	tests := []struct {
		in    string
		depth int
		err   bool
	}{
		{`{"a":1}`, 1, false},
		{`{"a":{"b":1}}`, 1, true},
		{`{"a":"{{{{{{{{"}`, 1, false},
		{`{"a":"\"{{"}`, 1, false},
		{strings.Repeat("[", 65) + strings.Repeat("]", 65), 64, true},
		{strings.Repeat("[", 64) + strings.Repeat("]", 64), 64, false},
	}
	for _, tt := range tests {
		err := CheckDepth([]byte(tt.in), tt.depth)
		if (err != nil) != tt.err {
			t.Errorf("CheckDepth(%q,%d) = %v", tt.in, tt.depth, err)
		}
	}
}

func TestDecode(t *testing.T) {
	v, err := Decode([]byte(`{"n":12345678901234567890,"f":1.5}`), MaxDepth)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["n"].(json.Number).String() != "12345678901234567890" {
		t.Errorf("number lost precision: %v", m["n"])
	}
	if _, err := Decode([]byte(`{"a":1} {"b":2}`), MaxDepth); !errors.Is(err, ErrTrailingData) {
		t.Errorf("trailing: %v", err)
	}
	if _, err := Decode([]byte(`{`), MaxDepth); err == nil {
		t.Error("expected syntax error")
	}
	if _, err := Decode([]byte(`[[[1]]]`), 2); !errors.Is(err, ErrTooDeep) {
		t.Errorf("depth: %v", err)
	}
	if _, err := DecodeObject([]byte(`[1]`), MaxDepth); err == nil {
		t.Error("array accepted as object")
	}
	if _, err := DecodeObject([]byte(`[1`), MaxDepth); err == nil {
		t.Error("broken accepted")
	}
	if o, err := DecodeObject([]byte(`{"a":"b"}`), MaxDepth); err != nil || o["a"] != "b" {
		t.Errorf("object: %v %v", o, err)
	}
}

func TestCompact(t *testing.T) {
	if got := Compact(map[string]any{"a": "<b>"}); got != `{"a":"<b>"}` {
		t.Errorf("Compact = %s", got)
	}
	if got := Compact(func() {}); got == "" {
		t.Error("unencodable value should fall back to fmt")
	}
}
