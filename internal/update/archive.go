// SPDX-License-Identifier: Apache-2.0

package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// MaxBinaryBytes caps the extracted binary (SPEC §17.7).
const MaxBinaryBytes = 256 << 20

// ErrArchive is returned for an archive that is not a well-formed release archive.
var (
	ErrArchive = errors.New("invalid release archive")
	// ErrUnsupportedPlatform is returned for a platform without a release archive.
	ErrUnsupportedPlatform = errors.New("no release archive for this platform")
)

// Platform is a GOOS/GOARCH pair.
type Platform struct{ OS, Arch string }

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// archiveBase is the directory every archive holds: tidemark_<version>_<os>_<arch> (SPEC §17.6).
func archiveBase(version string, p Platform) string {
	return "tidemark_" + version + "_" + p.OS + "_" + p.Arch
}

// AssetName is the release asset holding the binary for p: a .zip on Windows, else a .tar.gz.
func AssetName(version string, p Platform) (string, error) {
	if !releasePlatforms[p.String()] {
		return "", fmt.Errorf("%w: %s", ErrUnsupportedPlatform, p)
	}
	if p.OS == "windows" {
		return archiveBase(version, p) + ".zip", nil
	}
	return archiveBase(version, p) + ".tar.gz", nil
}

// binaryEntry is the archive path of the binary.
func binaryEntry(version string, p Platform) string {
	name := "tidemark"
	if p.OS == "windows" {
		name += ".exe"
	}
	return archiveBase(version, p) + "/" + name
}

// entryPath validates an archive entry name and returns it cleaned: relative, slash-separated,
// no ".." element, no backslash, colon or NUL (which could name a drive or stream on Windows).
func entryPath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) {
		return "", fmt.Errorf("%w: unsafe entry %q", ErrArchive, name)
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == ".." {
			return "", fmt.Errorf("%w: unsafe entry %q", ErrArchive, name)
		}
	}
	return path.Clean(name), nil
}

// ExtractBinary copies the binary of the release archive r (size bytes) for version and p to
// dst. Every entry is checked: only regular files and directories with safe names are allowed,
// the binary must appear exactly once and be at most MaxBinaryBytes. On error dst may hold a
// partial copy, which the caller discards.
func ExtractBinary(r io.ReaderAt, size int64, version string, p Platform, dst io.Writer) error {
	if _, err := AssetName(version, p); err != nil {
		return err
	}
	want := binaryEntry(version, p)
	if p.OS == "windows" {
		return extractZip(r, size, want, dst)
	}
	return extractTarGz(io.NewSectionReader(r, 0, size), want, dst)
}

func extractTarGz(r io.Reader, want string, dst io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrArchive, err)
		}
		name, err := entryPath(h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return fmt.Errorf("%w: %q is not a regular file or directory", ErrArchive, h.Name)
		}
		if name != want {
			continue
		}
		if found {
			return fmt.Errorf("%w: %s appears twice", ErrArchive, want)
		}
		found = true
		if err := copyBinary(dst, tr, h.Size); err != nil {
			return err
		}
	}
	return foundOrMissing(found, want)
}

func extractZip(r io.ReaderAt, size int64, want string, dst io.Writer) error {
	zr, err := zip.NewReader(r, size)
	if err != nil && !errors.Is(err, zip.ErrInsecurePath) {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	found := false
	for _, f := range zr.File {
		name, err := entryPath(f.Name)
		if err != nil {
			return err
		}
		mode := f.Mode()
		if mode.IsDir() {
			continue
		}
		if !mode.IsRegular() {
			return fmt.Errorf("%w: %q is not a regular file or directory", ErrArchive, f.Name)
		}
		if name != want {
			continue
		}
		if found {
			return fmt.Errorf("%w: %s appears twice", ErrArchive, want)
		}
		found = true
		if err := copyZipEntry(f, dst); err != nil {
			return err
		}
	}
	return foundOrMissing(found, want)
}

func copyZipEntry(f *zip.File, dst io.Writer) error {
	if f.UncompressedSize64 > MaxBinaryBytes {
		return fmt.Errorf("%w: %s is larger than %d bytes", ErrArchive, f.Name, MaxBinaryBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	defer rc.Close()
	return copyBinary(dst, rc, int64(f.UncompressedSize64)) //nolint:gosec // checked against MaxBinaryBytes above
}

// copyBinary copies exactly declared bytes (at most MaxBinaryBytes) from src to dst.
func copyBinary(dst io.Writer, src io.Reader, declared int64) error {
	if declared < 0 || declared > MaxBinaryBytes {
		return fmt.Errorf("%w: binary larger than %d bytes", ErrArchive, MaxBinaryBytes)
	}
	n, err := io.Copy(dst, io.LimitReader(src, MaxBinaryBytes+1))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrArchive, err)
	}
	if n != declared {
		return fmt.Errorf("%w: binary is %d bytes, header says %d", ErrArchive, n, declared)
	}
	return nil
}

func foundOrMissing(found bool, want string) error {
	if !found {
		return fmt.Errorf("%w: %s missing", ErrArchive, want)
	}
	return nil
}
