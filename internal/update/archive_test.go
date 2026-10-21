// SPDX-License-Identifier: Apache-2.0

package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := map[Platform]string{
		{OS: "linux", Arch: "amd64"}:   "tidemark_1.2.3_linux_amd64.tar.gz",
		{OS: "linux", Arch: "arm64"}:   "tidemark_1.2.3_linux_arm64.tar.gz",
		{OS: "windows", Arch: "amd64"}: "tidemark_1.2.3_windows_amd64.zip",
	}
	for p, want := range cases {
		if got, err := AssetName("1.2.3", p); err != nil || got != want {
			t.Errorf("AssetName(%s) = %q, %v; want %q", p, got, err, want)
		}
	}
	for _, p := range []Platform{{OS: "darwin", Arch: "arm64"}, {OS: "windows", Arch: "arm64"}, {OS: "linux", Arch: "386"}} {
		if _, err := AssetName("1.2.3", p); !errors.Is(err, ErrUnsupportedPlatform) {
			t.Errorf("AssetName(%s) = %v, want ErrUnsupportedPlatform", p, err)
		}
	}
}

func TestExtractBinary(t *testing.T) {
	const v = "1.2.3"
	for _, p := range []Platform{{OS: "linux", Arch: "amd64"}, {OS: "windows", Arch: "amd64"}} {
		t.Run(p.OS, func(t *testing.T) {
			pack := makeTarGz
			if p.OS == "windows" {
				pack = makeZip
			}
			bin, base := binaryEntry(v, p), archiveBase(v, p)
			reg := func(name, body string) archiveEntry {
				return archiveEntry{name: name, body: []byte(body), typeflag: tar.TypeReg}
			}
			dir := archiveEntry{name: base + "/", typeflag: tar.TypeDir}
			cases := []struct {
				name    string
				entries []archiveEntry
				want    error
			}{
				{"valid", []archiveEntry{dir, reg(base+"/README.md", "r"), reg(bin, "new binary")}, nil},
				{"missing", []archiveEntry{dir, reg(base+"/README.md", "r")}, ErrArchive},
				{"other version", []archiveEntry{reg(binaryEntry("1.2.4", p), "x")}, ErrArchive},
				{"duplicate", []archiveEntry{reg(bin, "a"), reg(bin, "b")}, ErrArchive},
				{"duplicate after cleaning", []archiveEntry{reg(bin, "a"), reg(base+"/./"+bin[len(base)+1:], "b")}, ErrArchive},
				{"traversal", []archiveEntry{reg(base+"/../../etc/passwd", "x"), reg(bin, "a")}, ErrArchive},
				{"absolute", []archiveEntry{reg("/etc/passwd", "x"), reg(bin, "a")}, ErrArchive},
				{"backslash", []archiveEntry{reg(base+`\..\x`, "x"), reg(bin, "a")}, ErrArchive},
				{"drive", []archiveEntry{reg("C:/Windows/x", "x"), reg(bin, "a")}, ErrArchive},
				{"symlink", []archiveEntry{{name: base + "/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}, reg(bin, "a")}, ErrArchive},
				{"binary is a symlink", []archiveEntry{{name: bin, typeflag: tar.TypeSymlink, link: "/bin/sh"}}, ErrArchive},
			}
			if p.OS != "windows" {
				cases = append(cases, struct {
					name    string
					entries []archiveEntry
					want    error
				}{"hard link", []archiveEntry{reg(base+"/x", "a"), {name: bin, typeflag: tar.TypeLink, link: base + "/x"}}, ErrArchive})
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					data := pack(t, c.entries)
					var out bytes.Buffer
					err := ExtractBinary(bytes.NewReader(data), int64(len(data)), v, p, &out)
					if !errors.Is(err, c.want) {
						t.Fatalf("err = %v, want %v", err, c.want)
					}
					if c.want == nil && out.String() != "new binary" {
						t.Errorf("extracted %q", out.String())
					}
				})
			}
		})
	}
}

func TestExtractBinaryRejectsGarbageAndOversize(t *testing.T) {
	const v = "1.2.3"
	linux, windows := Platform{OS: "linux", Arch: "amd64"}, Platform{OS: "windows", Arch: "amd64"}
	garbage := []byte("not an archive")
	for _, p := range []Platform{linux, windows} {
		if err := ExtractBinary(bytes.NewReader(garbage), int64(len(garbage)), v, p, &bytes.Buffer{}); !errors.Is(err, ErrArchive) {
			t.Errorf("%s garbage: %v", p, err)
		}
	}
	// A zip entry that claims more than MaxBinaryBytes is refused before it is read.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{Name: binaryEntry(v, windows), Method: zip.Store, UncompressedSize64: MaxBinaryBytes + 1, CompressedSize64: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ExtractBinary(bytes.NewReader(buf.Bytes()), int64(buf.Len()), v, windows, &bytes.Buffer{}); !errors.Is(err, ErrArchive) || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversize zip entry: %v", err)
	}
	// The tar path checks the declared size and the bytes actually read.
	if err := copyBinary(&bytes.Buffer{}, strings.NewReader("x"), MaxBinaryBytes+1); !errors.Is(err, ErrArchive) {
		t.Errorf("oversize declared size: %v", err)
	}
	if err := copyBinary(&bytes.Buffer{}, strings.NewReader("short"), 10); !errors.Is(err, ErrArchive) {
		t.Errorf("short body: %v", err)
	}
	if err := ExtractBinary(bytes.NewReader(nil), 0, v, Platform{OS: "darwin", Arch: "arm64"}, &bytes.Buffer{}); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("darwin: %v", err)
	}
}
