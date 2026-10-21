// SPDX-License-Identifier: Apache-2.0

package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// testSigner is a throwaway minisign key pair generated per test; the real release keys never
// sign anything in tests.
type testSigner struct {
	priv ed25519.PrivateKey
	id   [keyIDLen]byte
}

func newTestSigner(t *testing.T) testSigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var s testSigner
	s.priv = priv
	if _, err := rand.Read(s.id[:]); err != nil {
		t.Fatal(err)
	}
	return s
}

// publicKey is the base64 line of the minisign public key.
func (s testSigner) publicKey() string {
	raw := append([]byte(keyAlgorithm), s.id[:]...)
	raw = append(raw, s.priv.Public().(ed25519.PublicKey)...)
	return base64.StdEncoding.EncodeToString(raw)
}

// sign writes a minisign signature file of data: prehashed ("ED") or legacy ("Ed").
func (s testSigner) sign(data []byte, comment string, prehashed bool) []byte {
	alg, msg := keyAlgorithm, data
	if prehashed {
		digest := blake2b.Sum512(data)
		alg, msg = sigAlgPrehashed, digest[:]
	}
	sig := ed25519.Sign(s.priv, msg)
	global := ed25519.Sign(s.priv, append(bytes.Clone(sig), comment...))
	line := append([]byte(alg), s.id[:]...)
	line = append(line, sig...)
	return fmt.Appendf(nil, "untrusted comment: signature from minisign secret key\n%s\ntrusted comment: %s\n%s\n",
		base64.StdEncoding.EncodeToString(line), comment, base64.StdEncoding.EncodeToString(global))
}

// archiveEntry is one entry of a test archive.
type archiveEntry struct {
	name     string
	body     []byte
	typeflag byte // tar type; zip: tar.TypeSymlink makes a symlink entry, tar.TypeDir a directory
	link     string
}

func makeTarGz(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typeflag, Mode: 0o755, Linkname: e.link}
		if e.typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		body := e.body
		switch e.typeflag {
		case tar.TypeSymlink:
			h.SetMode(os.ModeSymlink | 0o777)
			body = []byte(e.link)
		case tar.TypeDir:
			h.SetMode(os.ModeDir | 0o755)
		default:
			h.SetMode(0o755)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// hostPlatform is the platform the tests install for; release archives exist for it on the
// systems the tests run on (Linux and Windows on amd64 or arm64).
func hostPlatform(t *testing.T) Platform {
	t.Helper()
	p := Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if _, err := AssetName("1.0.0", p); err != nil {
		t.Skipf("no release archive for %s", p)
	}
	return p
}

// releaseArchive builds the release archive of version for p holding binary.
func releaseArchive(t *testing.T, version string, p Platform, binary []byte) []byte {
	t.Helper()
	base := archiveBase(version, p)
	entries := []archiveEntry{
		{name: base + "/", typeflag: tar.TypeDir},
		{name: base + "/INSTALL.md", body: []byte("# install\n"), typeflag: tar.TypeReg},
		{name: binaryEntry(version, p), body: binary, typeflag: tar.TypeReg},
	}
	if p.OS == "windows" {
		return makeZip(t, entries)
	}
	return makeTarGz(t, entries)
}

// releaseFiles returns the files of a signed release: asset, checksums.txt and its signature.
func releaseFiles(t *testing.T, s testSigner, version string, p Platform, binary []byte) map[string][]byte {
	t.Helper()
	asset, err := AssetName(version, p)
	if err != nil {
		t.Fatal(err)
	}
	archive := releaseArchive(t, version, p, binary)
	sum := sha256.Sum256(archive)
	other := sha256.Sum256([]byte("docs"))
	sums := fmt.Appendf(nil, "%s  %s\n%s  tidemark_%s_docs.zip\n", hex.EncodeToString(sum[:]), asset, hex.EncodeToString(other[:]), version)
	return map[string][]byte{
		asset:         archive,
		ChecksumsName: sums,
		SignatureName: s.sign(sums, ChecksumsTrustedComment(version), true),
	}
}
