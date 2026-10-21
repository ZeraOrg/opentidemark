// SPDX-License-Identifier: Apache-2.0

package update

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrChecksums is returned for a checksums.txt that is not in the exact sha256sum format, and
// ErrChecksumMismatch when a file's SHA-256 differs from its line.
var (
	ErrChecksums        = errors.New("invalid checksums.txt")
	ErrChecksumMismatch = errors.New("checksum mismatch")
)

// checksumLine is one sha256sum text-mode line: 64 lower-case hex digits, two spaces and a plain
// file name (the release asset names of SPEC §17.6).
var checksumLine = regexp.MustCompile(`^([0-9a-f]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$`)

// Checksums maps a release asset name to its SHA-256.
type Checksums map[string][32]byte

// ParseChecksums parses checksums.txt as `make checksums` writes it: LF-terminated lines, each
// "<sha256>  <name>", at least one, no duplicate names, nothing else.
func ParseChecksums(data []byte) (Checksums, error) {
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return nil, fmt.Errorf("%w: missing final newline", ErrChecksums)
	}
	out := Checksums{}
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		m := checksumLine.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("%w: line %d", ErrChecksums, i+1)
		}
		if _, dup := out[m[2]]; dup {
			return nil, fmt.Errorf("%w: %s listed twice", ErrChecksums, m[2])
		}
		var sum [32]byte
		if _, err := hex.Decode(sum[:], []byte(m[1])); err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrChecksums, i+1, err)
		}
		out[m[2]] = sum
	}
	return out, nil
}

// Lookup returns the SHA-256 of the asset name.
func (c Checksums) Lookup(name string) ([32]byte, bool) {
	sum, ok := c[name]
	return sum, ok
}
