// SPDX-License-Identifier: Apache-2.0

package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// secretKeyLen is the AES-256 key size used for secrets at rest (SPEC §15.5).
const secretKeyLen = 32

// ensureSecretKey creates the key file on first start (0600; restricted ACL on Windows) and
// returns its contents.
func ensureSecretKey(path string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err == nil {
		if len(b) != secretKeyLen {
			return nil, fmt.Errorf("secret key %s must be exactly %d bytes", path, secretKeyLen)
		}
		return b, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read secret key: %w", err)
	}
	key := make([]byte, secretKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate secret key: %w", err)
	}
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, privateFilePerm)
	if err != nil {
		return nil, fmt.Errorf("create secret key: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close() // report the write error
		return nil, fmt.Errorf("write secret key: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := restrictFile(path); err != nil {
		return nil, fmt.Errorf("restrict secret key: %w", err)
	}
	return key, nil
}

// File and directory permissions for the data directory.
const (
	privateFilePerm = 0o600
	dataDirPerm     = 0o750
)
