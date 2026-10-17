// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// tamper flips one bit of the sealed ciphertext, so the result always differs from sealed.
func tamper(t *testing.T, sealed string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(sealed, sealedPrefix))
	if err != nil {
		t.Fatalf("decode sealed value: %v", err)
	}
	raw[len(raw)-1] ^= 1
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestSecretBoxRoundTrip(t *testing.T) {
	box, err := NewSecretBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, pt := range []string{"", "s", "https://hooks.slack.com/services/T/B/x", strings.Repeat("ü", 300)} {
		sealed, err := box.Seal(pt)
		if err != nil {
			t.Fatal(err)
		}
		if pt != "" && (!IsSealed(sealed) || (len(pt) > 8 && strings.Contains(sealed, pt))) {
			t.Errorf("Seal(%q) = %q", pt, sealed)
		}
		got, err := box.Open(sealed)
		if err != nil || got != pt {
			t.Errorf("Open(Seal(%q)) = %q, %v", pt, got, err)
		}
	}
	a, _ := box.Seal("x")
	b, _ := box.Seal("x")
	if a == b {
		t.Error("nonces must differ")
	}
}

func TestSecretBoxErrors(t *testing.T) {
	if _, err := NewSecretBox([]byte("short")); !errors.Is(err, ErrSecretBox) {
		t.Errorf("short key: %v", err)
	}
	box, _ := NewSecretBox(bytes.Repeat([]byte{1}, 32))
	other, _ := NewSecretBox(bytes.Repeat([]byte{2}, 32))
	sealed, _ := box.Seal("secret")
	for name, in := range map[string]string{
		"plain":     "secret",
		"bad b64":   sealedPrefix + "!!!",
		"too short": sealedPrefix + "AA",
		"tampered":  tamper(t, sealed),
	} {
		if _, err := box.Open(in); !errors.Is(err, ErrSecretBox) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := other.Open(sealed); !errors.Is(err, ErrSecretBox) {
		t.Errorf("wrong key: %v", err)
	}
}
