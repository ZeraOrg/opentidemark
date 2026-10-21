// SPDX-License-Identifier: Apache-2.0

package update

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrUnsafeFile is returned for a protocol or staged file that is not a plain regular file
// (a symlink, hard-to-trust reparse point, device, directory) or is larger than allowed.
var ErrUnsafeFile = errors.New("unsafe update file")

// Modes of the files the updater writes. Files the helper reads from the staging directory are
// group-readable: the Linux helper runs as root without capabilities and reads them through the
// service's group. state.json is readable by the service.
const (
	privateFileMode = 0o600
	sharedFileMode  = 0o640
	publicFileMode  = 0o644
)

// readRegular reads the regular file path, refusing links and anything over limit bytes.
func readRegular(path string, limit int64) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnsafeFile, filepath.Base(path), limit)
	}
	return data, nil
}

// copyRegular copies the regular file src (at most limit bytes, links refused) to a new private
// file dst and returns its SHA-256.
func copyRegular(src, dst string, limit int64) ([32]byte, error) {
	in, err := openRegular(src)
	if err != nil {
		return [32]byte{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, privateFileMode)
	if err != nil {
		return [32]byte{}, fmt.Errorf("create %s: %w", filepath.Base(dst), err)
	}
	sum, err := copyHashed(out, in, limit)
	if cerr := out.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close %s: %w", filepath.Base(dst), cerr)
	}
	if err != nil {
		return [32]byte{}, fmt.Errorf("%s: %w", filepath.Base(src), err)
	}
	return sum, nil
}

// copyHashed copies at most limit bytes from src to dst (an error beyond that), syncs dst when it
// is a file and returns the SHA-256 of the bytes copied.
func copyHashed(dst io.Writer, src io.Reader, limit int64) ([32]byte, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, limit+1))
	if err != nil {
		return [32]byte{}, err
	}
	if n > limit {
		return [32]byte{}, fmt.Errorf("%w: larger than %d bytes", ErrUnsafeFile, limit)
	}
	if f, ok := dst.(*os.File); ok {
		if err := f.Sync(); err != nil {
			return [32]byte{}, err
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// writeFileAtomic writes data to dir/name with mode through a synced temporary file and a
// rename, so a reader sees the old file or the complete new one.
func writeFileAtomic(dir, name string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	err = tmp.Chmod(mode)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		_ = os.Remove(tmp.Name()) // best effort: the write already failed and is reported
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// removeIfExists removes path; a missing file is not an error.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
