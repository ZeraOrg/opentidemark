// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// openRegular opens path for reading without following a final symlink (O_NOFOLLOW) and
// refuses anything but a regular file, judged on the opened descriptor.
func openRegular(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrUnsafeFile, filepath.Base(path))
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, filepath.Base(path))
	}
	if err != nil {
		_ = f.Close() // read-only descriptor; the refusal is the error that matters
		return nil, err
	}
	return f, nil
}

// replaceBinary installs bin+".new" as bin and keeps the old binary as bin+".previous": a hard
// link to the current file, then an atomic rename over bin (the running process keeps its
// inode).
func replaceBinary(bin string) error {
	prev := bin + PreviousSuffix
	if err := removeIfExists(prev); err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Base(prev), err)
	}
	if err := os.Link(bin, prev); err != nil {
		return fmt.Errorf("keep previous binary: %w", err)
	}
	if err := os.Rename(bin+newSuffix, bin); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

// restorePrevious puts bin+".previous" back as bin with one atomic rename.
func restorePrevious(bin string) error {
	if err := os.Rename(bin+PreviousSuffix, bin); err != nil {
		return fmt.Errorf("restore previous binary: %w", err)
	}
	return nil
}
