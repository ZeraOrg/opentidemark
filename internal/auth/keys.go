// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

// Key formats (SPEC §15.3).
const (
	ConnectionKeyPrefix = "tmk_"
	UserTokenPrefix     = "tmt_"
	keyRandomLen        = 40
	KeyPrefixLen        = 8
	base62Alphabet      = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

// GenerateKey returns prefix + 40 random base62 characters (≈ 238 bits of entropy).
func GenerateKey(prefix string) (string, error) {
	var b strings.Builder
	b.Grow(len(prefix) + keyRandomLen)
	b.WriteString(prefix)
	limit := big.NewInt(int64(len(base62Alphabet)))
	for range keyRandomLen {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate key: %w", err)
		}
		b.WriteByte(base62Alphabet[n.Int64()])
	}
	return b.String(), nil
}

// HashKey returns the lower-case hex SHA-256 of a key; only this hash is stored.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// KeyPrefix returns the first 8 characters of a key, shown in the UI to identify it.
func KeyPrefix(raw string) string {
	if len(raw) <= KeyPrefixLen {
		return raw
	}
	return raw[:KeyPrefixLen]
}

// Redact renders a key for logs as its prefix followed by an ellipsis ("tmk_abcd…").
func Redact(raw string) string {
	if raw == "" {
		return ""
	}
	return KeyPrefix(raw) + "…"
}

// RedactInText replaces every tmk_/tmt_ key found in s with its redacted form.
func RedactInText(s string) string {
	for _, prefix := range []string{ConnectionKeyPrefix, UserTokenPrefix} {
		var b strings.Builder
		rest := s
		for {
			i := strings.Index(rest, prefix)
			if i < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:i])
			end := i + len(prefix)
			for end < len(rest) && strings.IndexByte(base62Alphabet, rest[end]) >= 0 {
				end++
			}
			if end-i > KeyPrefixLen {
				b.WriteString(Redact(rest[i:end]))
			} else {
				b.WriteString(rest[i:end]) // already redacted or too short to be a key
			}
			rest = rest[end:]
		}
		s = b.String()
	}
	return s
}
