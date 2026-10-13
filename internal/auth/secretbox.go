// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Secret box format (SPEC §15.5): "enc:v1:" + base64url(nonce || AES-256-GCM ciphertext).
const (
	sealedPrefix = "enc:v1:"
	secretKeyLen = 32
)

// ErrSecretBox is wrapped by every sealing/opening failure.
var ErrSecretBox = errors.New("secret box")

// SecretBox encrypts secrets at rest with AES-256-GCM using the key from
// TIDEMARK_SECRET_KEY_FILE (SPEC §15.5).
type SecretBox struct{ aead cipher.AEAD }

// NewSecretBox returns a box for a 32-byte key.
func NewSecretBox(key []byte) (*SecretBox, error) {
	if len(key) != secretKeyLen {
		return nil, fmt.Errorf("%w: key must be %d bytes", ErrSecretBox, secretKeyLen)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretBox, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSecretBox, err)
	}
	return &SecretBox{aead: aead}, nil
}

// Seal encrypts plaintext; the empty string stays empty.
func (b *SecretBox) Seal(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("%w: nonce: %w", ErrSecretBox, err)
	}
	out := b.aead.Seal(nonce, nonce, []byte(plaintext), []byte(sealedPrefix))
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value produced by Seal; the empty string stays empty.
func (b *SecretBox) Open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	enc, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return "", fmt.Errorf("%w: value is not sealed", ErrSecretBox)
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) < b.aead.NonceSize() {
		return "", fmt.Errorf("%w: malformed value", ErrSecretBox)
	}
	n := b.aead.NonceSize()
	pt, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(sealedPrefix))
	if err != nil {
		return "", fmt.Errorf("%w: decrypt: %w", ErrSecretBox, err)
	}
	return string(pt), nil
}

// IsSealed reports whether v looks like a value produced by Seal.
func IsSealed(v string) bool { return strings.HasPrefix(v, sealedPrefix) }
