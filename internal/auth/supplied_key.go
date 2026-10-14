// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"fmt"
	"math"
)

// Admin-supplied connection keys (SPEC §9.9, §15.3): existing Seq API keys or Elasticsearch
// API-key secrets may be reused as OpenTidemark connection keys during a migration.
const (
	SuppliedKeyMinLen     = 20
	SuppliedKeyMaxLen     = 256
	SuppliedKeyMinBits    = 80
	SuppliedKeyPrefixLen  = 4 // characters shown in the UI; excluded from the entropy estimate
	firstPrintable        = '!'
	lastPrintable         = '~'
	lowerClassSize        = 26
	upperClassSize        = 26
	digitClassSize        = 10
	symbolClassSize       = 32 // printable ASCII 0x21–0x7E minus letters and digits
	sequentialStepAllowed = 1
)

// ErrWeakKey is returned for supplied keys that violate the length, alphabet or entropy rules.
var ErrWeakKey = errors.New("connection key is too weak")

// ValidateSuppliedKey checks an admin-supplied key: 20–256 printable ASCII characters without
// spaces, and at least 80 bits of estimated entropy over the characters after the 4-character
// display prefix (EstimateKeyBits).
func ValidateSuppliedKey(raw string) error {
	if len(raw) < SuppliedKeyMinLen || len(raw) > SuppliedKeyMaxLen {
		return fmt.Errorf("%w: must be %d-%d characters", ErrWeakKey, SuppliedKeyMinLen, SuppliedKeyMaxLen)
	}
	for i := range len(raw) {
		if raw[i] < firstPrintable || raw[i] > lastPrintable {
			return fmt.Errorf("%w: only printable ASCII characters without spaces are allowed", ErrWeakKey)
		}
	}
	if bits := EstimateKeyBits(raw[SuppliedKeyPrefixLen:]); bits < SuppliedKeyMinBits {
		return fmt.Errorf("%w: estimated entropy %.0f bits, at least %d required", ErrWeakKey, bits, SuppliedKeyMinBits)
	}
	return nil
}

// EstimateKeyBits estimates the entropy of s: every character contributes log2(pool), where
// pool is the total size of the character classes present (lower case 26, upper case 26,
// digits 10, symbols 32), except a character that repeats the previous one, continues a
// sequence (abc, 321) or completes a character pair already seen. Runs like "aaaa" or
// "123456" and repeated patterns like "aB3$aB3$" therefore count (almost) once.
func EstimateKeyBits(s string) float64 {
	pool := charsetPool(s)
	if pool == 0 {
		return 0
	}
	per := math.Log2(float64(pool))
	seen := map[[2]byte]bool{}
	var bits float64
	for i := range len(s) {
		if i > 0 {
			d := int(s[i]) - int(s[i-1])
			pair := [2]byte{s[i-1], s[i]}
			if d >= -sequentialStepAllowed && d <= sequentialStepAllowed || seen[pair] {
				continue
			}
			seen[pair] = true
		}
		bits += per
	}
	return bits
}

func charsetPool(s string) int {
	var lower, upper, digit, symbol bool
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'z':
			lower = true
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= '0' && c <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	pool := 0
	for _, c := range []struct {
		present bool
		size    int
	}{{lower, lowerClassSize}, {upper, upperClassSize}, {digit, digitClassSize}, {symbol, symbolClassSize}} {
		if c.present {
			pool += c.size
		}
	}
	return pool
}

// SuppliedKeyPrefix is the part of a supplied key shown in the UI (4 characters, so short
// migrated keys keep enough secret entropy).
func SuppliedKeyPrefix(raw string) string {
	if len(raw) <= SuppliedKeyPrefixLen {
		return raw
	}
	return raw[:SuppliedKeyPrefixLen]
}
