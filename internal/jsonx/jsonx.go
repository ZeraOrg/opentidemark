// SPDX-License-Identifier: Apache-2.0

// Package jsonx decodes untrusted JSON with a nesting-depth limit and exact number handling.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxDepth is the default nesting limit for untrusted JSON (SPEC §15.4).
const MaxDepth = 64

// ErrTooDeep is returned when a document nests deeper than the allowed limit.
var ErrTooDeep = errors.New("json nesting too deep")

// ErrTrailingData is returned when a single-value document has extra content.
var ErrTrailingData = errors.New("unexpected data after JSON value")

// CheckDepth reports ErrTooDeep when data nests objects/arrays deeper than maxDepth.
// It scans bytes only and ignores brackets inside strings.
func CheckDepth(data []byte, maxDepth int) error {
	depth := 0
	inString, escaped := false, false
	for _, c := range data {
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return ErrTooDeep
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

// Decode parses exactly one JSON value, keeping numbers as json.Number.
func Decode(data []byte, maxDepth int) (any, error) {
	if err := CheckDepth(data, maxDepth); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrTrailingData
	}
	return v, nil
}

// DecodeObject parses one JSON object.
func DecodeObject(data []byte, maxDepth int) (map[string]any, error) {
	v, err := Decode(data, maxDepth)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	return m, nil
}

// Compact returns the compact JSON encoding of v without HTML escaping.
func Compact(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}
