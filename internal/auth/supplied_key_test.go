// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestValidateSuppliedKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		ok   bool
	}{
		{"seq api key (20 alphanumerics)", "Xk3mQ9pLr2Tz8vNw4Hs7", true},
		{"es api key secret (22 base64url)", "ui2lp2axTNmsyakw9tvNnw", true},
		{"hex token 32", "9f86d081884c7d659a2feaa0c55ad015", true},
		{"too short", "Xk3mQ9pLr2Tz8vNw4Hs", false},
		{"too long", strings.Repeat("aB3$", 65), false},
		{"repeating pattern", strings.Repeat("aB3$", 10), false},
		{"space", "Xk3mQ9pLr2 z8vNw4Hs7a", false},
		{"non ascii", "Xk3mQ9pLr2Tz8vNw4Hsé7", false},
		{"repeated char", strings.Repeat("a", 40), false},
		{"sequence", "abcdefghijklmnopqrstuvwxyz0123456789", false},
		{"dictionary-ish", "Password1234567890ab", false},
		{"lowercase only 20 random", "qmzhtrkwpvnbxlgdjcfs", false}, // 16 counted chars x 4.7 = 75 bits
		{"lowercase only 24 random", "qmzhtrkwpvnbxlgdjcfsyqoe", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSuppliedKey(tt.key)
			if (err == nil) != tt.ok {
				t.Fatalf("ValidateSuppliedKey(%q) = %v, want ok=%v (bits %.1f)", tt.key, err, tt.ok, EstimateKeyBits(tt.key))
			}
			if err != nil && !errors.Is(err, ErrWeakKey) {
				t.Errorf("error %v is not ErrWeakKey", err)
			}
		})
	}
}

func TestGeneratedKeysPass(t *testing.T) {
	var long strings.Builder
	for long.Len() < SuppliedKeyMaxLen {
		k, _ := GenerateKey("")
		long.WriteString(k)
	}
	if err := ValidateSuppliedKey(long.String()[:SuppliedKeyMaxLen]); err != nil {
		t.Fatalf("random key of maximum length rejected: %v", err)
	}
	for range 200 {
		k, err := GenerateKey(ConnectionKeyPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateSuppliedKey(k); err != nil {
			t.Fatalf("generated key %s rejected: %v", k, err)
		}
	}
}

func TestEstimateKeyBits(t *testing.T) {
	if b := EstimateKeyBits(""); b != 0 {
		t.Errorf("empty: %v", b)
	}
	if b := EstimateKeyBits("aaaa"); b > 5 {
		t.Errorf("run counted more than once: %v", b)
	}
	if b, want := EstimateKeyBits("a!"), 2*math.Log2(58); math.Abs(b-want) > 1e-9 {
		t.Errorf("a!: %v bits, want %v (lower + symbol classes)", b, want)
	}
	if SuppliedKeyPrefix("abcdefgh") != "abcd" || SuppliedKeyPrefix("ab") != "ab" {
		t.Error("prefix")
	}
}
