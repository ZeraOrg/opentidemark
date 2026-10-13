// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"regexp"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	re := regexp.MustCompile(`^tmk_[0-9A-Za-z]{40}$`)
	seen := map[string]bool{}
	for range 50 {
		k, err := GenerateKey(ConnectionKeyPrefix)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(k) || seen[k] {
			t.Fatalf("bad or duplicate key %q", k)
		}
		seen[k] = true
	}
	tok, _ := GenerateKey(UserTokenPrefix)
	if tok[:4] != "tmt_" {
		t.Error(tok)
	}
}

func TestHashPrefixRedact(t *testing.T) {
	if HashKey("abc") != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error("sha256 mismatch")
	}
	if KeyPrefix("tmk_abcdefgh") != "tmk_abcd" || KeyPrefix("tmk") != "tmk" {
		t.Error("prefix")
	}
	if Redact("tmk_abcdefghij") != "tmk_abcd…" || Redact("") != "" {
		t.Error("redact")
	}
	in := "key=tmk_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd other tmt_ZZZZZZZZ9 end"
	want := "key=tmk_AbCd… other tmt_ZZZZ… end"
	if got := RedactInText(in); got != want {
		t.Errorf("RedactInText = %q want %q", got, want)
	}
	if RedactInText("nothing here") != "nothing here" {
		t.Error("changed plain text")
	}
}

func TestRoles(t *testing.T) {
	tests := []struct {
		have, need Role
		ok         bool
	}{
		{RoleAdmin, RoleViewer, true}, {RoleAdmin, RoleAdmin, true}, {RoleEditor, RoleAdmin, false},
		{RoleEditor, RoleEditor, true}, {RoleViewer, RoleEditor, false}, {Role("root"), RoleViewer, false},
		{"", RoleViewer, false},
	}
	for _, tt := range tests {
		if got := tt.have.Allows(tt.need); got != tt.ok {
			t.Errorf("%s allows %s = %v", tt.have, tt.need, got)
		}
	}
	if MinRole(RoleAdmin, RoleViewer) != RoleViewer || MinRole(RoleViewer, RoleEditor) != RoleViewer {
		t.Error("MinRole")
	}
	if ScopeRole("read") != RoleViewer || ScopeRole("write") != RoleEditor || ScopeRole("admin") != RoleAdmin || ScopeRole("x") != "" {
		t.Error("ScopeRole")
	}
	if !RoleAdmin.Valid() || Role("x").Valid() {
		t.Error("Valid")
	}
}
