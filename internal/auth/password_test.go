// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestPasswordHashVerify(t *testing.T) {
	h := NewPasswordHasher(fastParams())
	phc, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("phc = %s", phc)
	}
	other, _ := h.Hash("correct horse battery staple")
	if other == phc {
		t.Error("salt not random")
	}
	if ok, err := h.Verify("correct horse battery staple", phc); !ok || err != nil {
		t.Errorf("verify: %v %v", ok, err)
	}
	if ok, _ := h.Verify("wrong", phc); ok {
		t.Error("wrong password verified")
	}
}

func TestDefaultParams(t *testing.T) {
	p := DefaultArgon2Params()
	if p.MemoryKiB != 65536 || p.Time != 3 || p.Threads != 2 || p.SaltLen != 16 || p.KeyLen != 32 {
		t.Errorf("%+v", p)
	}
}

func TestParsePHCErrors(t *testing.T) {
	h := NewPasswordHasher(fastParams())
	bad := []string{
		"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=x,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=100,p=1$c2FsdA$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$!!!$a2V5",
		"$argon2id$v=19$m=64,t=1,p=1$$a2V5",
		"$argon2id$v=x$m=64,t=1,p=1$c2FsdA$a2V5",
	}
	for _, s := range bad {
		if _, err := h.Verify("x", s); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%q: %v", s, err)
		}
	}
}

func TestPasswordPolicy(t *testing.T) {
	p := NewPasswordPolicy()
	if p.CommonCount() < 9000 {
		t.Fatalf("common list too small: %d", p.CommonCount())
	}
	tests := []struct {
		user, pw string
		want     error
	}{
		{"alice", "short", ErrPasswordTooShort},
		{"alice", strings.Repeat("x", 257), ErrPasswordTooLong},
		{"averylongusername", "AVeryLongUsername", ErrPasswordUsername},
		{"alice", "1234567890qwerty", nil},
		{"alice", "qwertyuiopasdfghjkl", nil},
		{"alice", "TidemarkRocks-2026!", nil},
		{"alice", "ééééééééééé", ErrPasswordTooShort},
		{"alice", "éééééééééééé", nil},
	}
	for _, tt := range tests {
		if err := p.Validate(tt.user, tt.pw); !errors.Is(err, tt.want) {
			t.Errorf("Validate(%q,%q) = %v want %v", tt.user, tt.pw, err, tt.want)
		}
	}
	// Find a ≥12-char entry of the embedded list and make sure it is rejected, any case.
	for common := range p.common {
		if len([]rune(common)) >= MinPasswordLen && !strings.EqualFold(common, "alice") {
			if err := p.Validate("alice", strings.ToUpper(common)); !errors.Is(err, ErrPasswordCommon) {
				t.Errorf("common %q accepted: %v", common, err)
			}
			return
		}
	}
	t.Error("no long common password in list")
}
