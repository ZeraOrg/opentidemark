// SPDX-License-Identifier: Apache-2.0

package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// minisign formats (https://jedisct1.github.io/minisign/): a public key is base64 of the
// algorithm "Ed", the 8-byte key id and the 32-byte Ed25519 key; a signature file holds an
// untrusted comment line, base64 of the algorithm, the key id and the 64-byte signature, a
// trusted comment line and base64 of the 64-byte global signature over signature || comment.
const (
	keyAlgorithm       = "Ed"
	sigAlgPrehashed    = "ED" // Ed25519 over BLAKE2b-512 of the file, minisign's default
	keyIDLen           = 8
	publicKeyLen       = 2 + keyIDLen + ed25519.PublicKeySize
	signatureLen       = 2 + keyIDLen + ed25519.SignatureSize
	untrustedPrefix    = "untrusted comment: "
	trustedPrefix      = "trusted comment: "
	signatureFileLines = 4
)

// Errors of signature verification. Every failure wraps ErrSignature.
var (
	ErrSignature        = errors.New("release signature rejected")
	ErrSignatureFormat  = fmt.Errorf("%w: malformed", ErrSignature)
	ErrSignatureKey     = fmt.Errorf("%w: not signed by a trusted key", ErrSignature)
	ErrSignatureLegacy  = fmt.Errorf("%w: legacy (not prehashed) signature", ErrSignature)
	ErrSignatureInvalid = fmt.Errorf("%w: signature does not match", ErrSignature)
	ErrSignatureComment = fmt.Errorf("%w: trusted comment does not name this release", ErrSignature)
)

// PublicKey is a parsed minisign Ed25519 public key.
type PublicKey struct {
	ID  [keyIDLen]byte
	Key ed25519.PublicKey
}

// IDString is the key id as minisign prints it (upper-case hex of the little-endian number).
func (k PublicKey) IDString() string {
	return keyIDString(k.ID)
}

func keyIDString(id [keyIDLen]byte) string {
	var b strings.Builder
	for i := keyIDLen - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "%02X", id[i])
	}
	return b.String()
}

// ParsePublicKey parses the base64 line of a minisign public key.
func ParsePublicKey(s string) (PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(raw) != publicKeyLen || string(raw[:2]) != keyAlgorithm {
		return PublicKey{}, fmt.Errorf("%w: public key", ErrSignatureFormat)
	}
	var k PublicKey
	copy(k.ID[:], raw[2:2+keyIDLen])
	k.Key = ed25519.PublicKey(bytes.Clone(raw[2+keyIDLen:]))
	return k, nil
}

// Signature is a parsed minisign signature file.
type Signature struct {
	Algorithm      string
	KeyID          [keyIDLen]byte
	Sig            []byte
	TrustedComment string
	GlobalSig      []byte
}

// ParseSignature parses a minisign signature file (LF or CRLF line ends, no further lines).
func ParseSignature(data []byte) (Signature, error) {
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) != signatureFileLines {
		return Signature{}, fmt.Errorf("%w: want %d lines", ErrSignatureFormat, signatureFileLines)
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	comment, ok := strings.CutPrefix(lines[2], trustedPrefix)
	if !strings.HasPrefix(lines[0], untrustedPrefix) || !ok {
		return Signature{}, fmt.Errorf("%w: comment lines", ErrSignatureFormat)
	}
	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != signatureLen {
		return Signature{}, fmt.Errorf("%w: signature line", ErrSignatureFormat)
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return Signature{}, fmt.Errorf("%w: global signature line", ErrSignatureFormat)
	}
	s := Signature{Algorithm: string(sig[:2]), Sig: sig[2+keyIDLen:], TrustedComment: comment, GlobalSig: global}
	copy(s.KeyID[:], sig[2:2+keyIDLen])
	return s, nil
}

// VerifyChecksumsSignature checks that sig is a prehashed minisign signature of checksums by
// one of trusted (base64 public keys) whose trusted comment is ChecksumsTrustedComment(version),
// in this order: format, algorithm, key id, file signature, global signature, comment.
func VerifyChecksumsSignature(checksums, sig []byte, version string, trusted []string) error {
	s, err := ParseSignature(sig)
	if err != nil {
		return err
	}
	if s.Algorithm != sigAlgPrehashed {
		if s.Algorithm == keyAlgorithm {
			return ErrSignatureLegacy
		}
		return fmt.Errorf("%w: algorithm %q", ErrSignatureFormat, s.Algorithm)
	}
	key, err := findKey(s.KeyID, trusted)
	if err != nil {
		return err
	}
	digest := blake2b.Sum512(checksums)
	if !ed25519.Verify(key.Key, digest[:], s.Sig) {
		return ErrSignatureInvalid
	}
	if !ed25519.Verify(key.Key, append(bytes.Clone(s.Sig), s.TrustedComment...), s.GlobalSig) {
		return fmt.Errorf("%w (trusted comment)", ErrSignatureInvalid)
	}
	if s.TrustedComment != ChecksumsTrustedComment(version) {
		return ErrSignatureComment
	}
	return nil
}

// findKey returns the trusted key with the signature's key id.
func findKey(id [keyIDLen]byte, trusted []string) (PublicKey, error) {
	for _, t := range trusted {
		k, err := ParsePublicKey(t)
		if err != nil {
			return PublicKey{}, err
		}
		if k.ID == id {
			return k, nil
		}
	}
	return PublicKey{}, fmt.Errorf("%w (key id %s)", ErrSignatureKey, keyIDString(id))
}
