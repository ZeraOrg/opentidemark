// SPDX-License-Identifier: Apache-2.0

// Package auth implements passwords, sessions, CSRF, login throttling, connection keys,
// API tokens, roles and rate limiting (SPEC §15.2–§15.4).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost parameters.
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
	SaltLen   uint32
	KeyLen    uint32
}

// DefaultArgon2Params are the production parameters (SPEC §15.2): m=64 MiB, t=3, p=2.
func DefaultArgon2Params() Argon2Params {
	const (
		memoryKiB = 64 * 1024
		timeCost  = 3
		threads   = 2
		saltLen   = 16
		keyLen    = 32
	)
	return Argon2Params{MemoryKiB: memoryKiB, Time: timeCost, Threads: threads, SaltLen: saltLen, KeyLen: keyLen}
}

// ErrMalformedHash is returned for hashes that are not argon2id PHC strings.
var ErrMalformedHash = errors.New("malformed password hash")

// PasswordHasher hashes and verifies passwords with argon2id in PHC string format.
type PasswordHasher struct {
	params Argon2Params
}

// NewPasswordHasher returns a hasher using p.
func NewPasswordHasher(p Argon2Params) *PasswordHasher { return &PasswordHasher{params: p} }

// Hash returns "$argon2id$v=19$m=…,t=…,p=…$<salt>$<key>" (unpadded standard base64).
func (h *PasswordHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.params.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.MemoryKiB, h.params.Threads, h.params.KeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, h.params.MemoryKiB, h.params.Time,
		h.params.Threads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether password matches the PHC hash (constant-time comparison).
func (h *PasswordHasher) Verify(password, phc string) (bool, error) {
	p, salt, key, err := parsePHC(phc)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, lenU32(key))
	return subtle.ConstantTimeCompare(got, key) == 1, nil
}

// NeedsRehash reports whether phc was produced with parameters other than the hasher's current
// ones (cost, salt or key length). Malformed hashes are not re-hashed (they cannot verify).
func (h *PasswordHasher) NeedsRehash(phc string) bool {
	p, salt, key, err := parsePHC(phc)
	if err != nil {
		return false
	}
	return p.MemoryKiB != h.params.MemoryKiB || p.Time != h.params.Time || p.Threads != h.params.Threads ||
		lenU32(salt) != h.params.SaltLen || lenU32(key) != h.params.KeyLen
}

// lenU32 returns len(b) as uint32, saturating (decoded PHC fields are far below the limit).
func lenU32(b []byte) uint32 {
	n := len(b)
	if n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}

// Limits on parameters accepted from stored hashes, so a tampered hash cannot exhaust memory.
const (
	maxMemoryKiB = 1024 * 1024
	maxTimeCost  = 16
	phcParts     = 6
)

func parsePHC(phc string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != phcParts || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	if p.MemoryKiB == 0 || p.MemoryKiB > maxMemoryKiB || p.Time == 0 || p.Time > maxTimeCost || p.Threads == 0 {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) == 0 || len(key) == 0 {
		return Argon2Params{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
