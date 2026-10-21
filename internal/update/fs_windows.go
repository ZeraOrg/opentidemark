// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// oldSuffix names a binary renamed aside during a rollback: a running .exe cannot be replaced
// or deleted on Windows, but it can be renamed.
const oldSuffix = ".old"

// A virus scanner may hold a fresh binary open for a moment; renames are retried for about five
// seconds on sharing violations and access denied before the install fails unchanged.
const (
	renameAttempts   = 10
	renameRetryDelay = 500 * time.Millisecond
)

// rename renames from to to, retrying while the file is held by another process.
func rename(from, to string) error { return renameRetrying(from, to, renameAttempts, renameRetryDelay) }

func renameRetrying(from, to string, attempts int, delay time.Duration) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(delay)
		}
		if err = os.Rename(from, to); err == nil || !retryableRename(err) {
			return err
		}
	}
	return err
}

// retryableRename reports a sharing violation or access denied: what an open handle without
// FILE_SHARE_DELETE (typically a scanner) causes.
func retryableRename(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

// openRegular opens path for reading and refuses anything but a regular file: symlinks,
// junctions and every other reparse point are rejected before opening, and the opened handle
// must still be the same file (so it was not swapped in between).
func openRegular(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || isReparsePoint(before) {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err == nil && !os.SameFile(before, after) {
		err = fmt.Errorf("%w: %s changed while opening", ErrUnsafeFile, filepath.Base(path))
	}
	if err != nil {
		_ = f.Close() // read-only handle; the refusal is the error that matters
		return nil, err
	}
	return f, nil
}

// isReparsePoint reports the FILE_ATTRIBUTE_REPARSE_POINT attribute.
func isReparsePoint(fi os.FileInfo) bool {
	attrs, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	return ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// replaceBinary installs bin+".new" as bin and keeps the old binary as bin+".previous". The
// current binary may be running, so it is renamed aside instead of overwritten; if the new one
// cannot take its place, the old one is renamed back.
func replaceBinary(bin string) error {
	prev := bin + PreviousSuffix
	_ = removeIfExists(bin + oldSuffix) // left by a rollback whose failed binary still ran; retried next time
	if err := removeIfExists(prev); err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Base(prev), err)
	}
	if err := rename(bin, prev); err != nil {
		return fmt.Errorf("keep previous binary: %w", err)
	}
	if err := rename(bin+newSuffix, bin); err != nil {
		if rerr := rename(prev, bin); rerr != nil {
			return fmt.Errorf("install binary: %w", errors.Join(err, rerr))
		}
		return fmt.Errorf("install binary: %w", err)
	}
	return nil
}

// restorePrevious renames the (possibly running) failed binary aside to bin+".old" and puts
// bin+".previous" back as bin. The aside copy is removed when Windows allows it.
func restorePrevious(bin string) error {
	old := bin + oldSuffix
	if err := removeIfExists(old); err != nil {
		return fmt.Errorf("remove %s: %w", filepath.Base(old), err)
	}
	if err := rename(bin, old); err != nil {
		return fmt.Errorf("move failed binary aside: %w", err)
	}
	if err := rename(bin+PreviousSuffix, bin); err != nil {
		if rerr := rename(old, bin); rerr != nil {
			return fmt.Errorf("restore previous binary: %w", errors.Join(err, rerr))
		}
		return fmt.Errorf("restore previous binary: %w", err)
	}
	_ = removeIfExists(old) // fails while the failed binary still runs; the next install retries
	return nil
}
