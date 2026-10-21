// SPDX-License-Identifier: Apache-2.0

package update

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The files in testdata/minisign were made with minisign 0.12 and a throwaway TEST key generated
// for this purpose only (`minisign -G -W`; its secret key was discarded, only test.pub is kept):
//
//	minisign -S -s test.key -m checksums.txt -t "opentidemark v1.2.3 checksums.txt"
//	minisign -S -l -s test.key -m checksums.txt -x checksums.txt.legacy.minisig -t "opentidemark v1.2.3 checksums.txt"
//
// They prove that the verifier accepts what the real tool writes.
const fixtureKeyID = "8A981F70EAC01025"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "minisign", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestVerifyMinisignFixtures(t *testing.T) {
	pubLines := strings.Split(strings.TrimSpace(string(readFixture(t, "test.pub"))), "\n")
	pub := strings.TrimSpace(pubLines[len(pubLines)-1])
	key, err := ParsePublicKey(pub)
	if err != nil || key.IDString() != fixtureKeyID {
		t.Fatalf("test.pub: %v, id %s", err, key.IDString())
	}
	sums := readFixture(t, "checksums.txt")
	sig := readFixture(t, "checksums.txt.minisig")
	crlf := bytes.ReplaceAll(bytes.ReplaceAll(sig, []byte("\r\n"), []byte("\n")), []byte("\n"), []byte("\r\n"))
	cases := []struct {
		name    string
		sums    []byte
		sig     []byte
		version string
		keys    []string
		want    error
	}{
		{"valid", sums, sig, "1.2.3", []string{pub}, nil},
		{"valid with CRLF lines (minisign on Windows)", sums, crlf, "1.2.3", []string{pub}, nil},
		{"valid as second trusted key", sums, sig, "1.2.3", []string{CISigningKey, pub}, nil},
		{"other version", sums, sig, "1.2.4", []string{pub}, ErrSignatureComment},
		{"release keys only", sums, sig, "1.2.3", TrustedSigningKeys(), ErrSignatureKey},
		{"tampered file", append(bytes.Clone(sums), '\n'), sig, "1.2.3", []string{pub}, ErrSignatureInvalid},
		{"legacy signature", sums, readFixture(t, "checksums.txt.legacy.minisig"), "1.2.3", []string{pub}, ErrSignatureLegacy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyChecksumsSignature(c.sums, c.sig, c.version, c.keys)
			if !errors.Is(err, c.want) || (c.want != nil && !errors.Is(err, ErrSignature)) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestVerifyChecksumsSignature(t *testing.T) {
	signer, other := newTestSigner(t), newTestSigner(t)
	sums := []byte("0000000000000000000000000000000000000000000000000000000000000000  tidemark_2.0.0_linux_amd64.tar.gz\n")
	comment := ChecksumsTrustedComment("2.0.0")
	valid := signer.sign(sums, comment, true)
	lines := strings.Split(string(valid), "\n")
	tamperedComment := []byte(strings.Join([]string{lines[0], lines[1], "trusted comment: opentidemark v2.0.1 checksums.txt", lines[3], ""}, "\n"))
	cases := []struct {
		name string
		sums []byte
		sig  []byte
		keys []string
		want error
	}{
		{"valid", sums, valid, []string{signer.publicKey()}, nil},
		{"either trusted key", sums, valid, []string{other.publicKey(), signer.publicKey()}, nil},
		{"wrong key id", sums, valid, []string{other.publicKey()}, ErrSignatureKey},
		{"tampered file", bytes.ToUpper(sums), valid, []string{signer.publicKey()}, ErrSignatureInvalid},
		{"tampered trusted comment", sums, tamperedComment, []string{signer.publicKey()}, ErrSignatureInvalid},
		{"wrong version in comment", sums, signer.sign(sums, ChecksumsTrustedComment("1.9.0"), true), []string{signer.publicKey()}, ErrSignatureComment},
		{"comment without the exact text", sums, signer.sign(sums, "timestamp:1 file:checksums.txt hashed", true), []string{signer.publicKey()}, ErrSignatureComment},
		{"legacy Ed", sums, signer.sign(sums, comment, false), []string{signer.publicKey()}, ErrSignatureLegacy},
		{"garbage", sums, []byte("not a signature"), []string{signer.publicKey()}, ErrSignatureFormat},
		{"empty", sums, nil, []string{signer.publicKey()}, ErrSignatureFormat},
		{"extra line", sums, append(bytes.Clone(valid), "more\n"...), []string{signer.publicKey()}, ErrSignatureFormat},
		{"bad base64", sums, bytes.Replace(valid, []byte(lines[1]), []byte("!!!!"), 1), []string{signer.publicKey()}, ErrSignatureFormat},
		{"no untrusted comment", sums, bytes.Replace(valid, []byte("untrusted comment: "), nil, 1), []string{signer.publicKey()}, ErrSignatureFormat},
		{"malformed trusted key", sums, valid, []string{"RWQnotakey"}, ErrSignatureFormat},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := VerifyChecksumsSignature(c.sums, c.sig, "2.0.0", c.keys)
			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestParsePublicKey(t *testing.T) {
	for _, c := range []struct{ key, id string }{{CISigningKey, CISigningKeyID}, {BackupSigningKey, BackupSigningKeyID}} {
		k, err := ParsePublicKey(c.key)
		if err != nil || k.IDString() != c.id {
			t.Errorf("%s: %v, id %s, want %s", c.key, err, k.IDString(), c.id)
		}
	}
	for _, bad := range []string{"", "RWQ", "UldR" + strings.Repeat("A", 52)} {
		if _, err := ParsePublicKey(bad); !errors.Is(err, ErrSignatureFormat) {
			t.Errorf("ParsePublicKey(%q) = %v", bad, err)
		}
	}
}
