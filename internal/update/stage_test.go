// SPDX-License-Identifier: Apache-2.0

package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeDownloads serves fixed bodies by URL and records the requests.
type fakeDownloads struct {
	mu     sync.Mutex
	bodies map[string][]byte
	urls   []string
}

func (f *fakeDownloads) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, req.URL.String())
	body, ok := f.bodies[req.URL.String()]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
}

func releaseServer(version string, files map[string][]byte) *fakeDownloads {
	f := &fakeDownloads{bodies: map[string][]byte{}}
	for name, body := range files {
		f.bodies[releaseDownloadBase+version+"/"+name] = body
	}
	return f
}

func readJSONFile(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestStage(t *testing.T) {
	p := Platform{OS: "linux", Arch: "amd64"}
	signer := newTestSigner(t)
	files := releaseFiles(t, signer, "2.0.0", p, []byte("new binary"))
	asset, _ := AssetName("2.0.0", p)
	dir := t.TempDir()
	stale := filepath.Join(dir, "tidemark_1.9.0_linux_amd64.tar.gz")
	if err := os.WriteFile(stale, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := releaseServer("2.0.0", files)
	s := NewStager(srv, dir, []string{signer.publicKey()}, p)
	if err := s.Stage(context.Background(), Request{ID: "req-1", Version: "2.0.0"}, StageHooks{}); err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{
		"https://github.com/ZeraOrg/opentidemark/releases/download/v2.0.0/checksums.txt.minisig",
		"https://github.com/ZeraOrg/opentidemark/releases/download/v2.0.0/checksums.txt",
		"https://github.com/ZeraOrg/opentidemark/releases/download/v2.0.0/" + asset,
	}
	if len(srv.urls) != 3 || srv.urls[0] != wantOrder[0] || srv.urls[1] != wantOrder[1] || srv.urls[2] != wantOrder[2] {
		t.Errorf("requests %q, want %q", srv.urls, wantOrder)
	}
	for _, name := range []string{asset, ChecksumsName, SignatureName} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, files[name]) {
			t.Errorf("staged %s: %v", name, err)
		}
	}
	var req Request
	readJSONFile(t, filepath.Join(dir, RequestFile), &req)
	if req != (Request{ID: "req-1", Version: "2.0.0"}) {
		t.Errorf("request.json = %+v", req)
	}
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale archive kept: %v", err)
	}
	assertOnlyFiles(t, dir, asset, ChecksumsName, SignatureName, RequestFile)
}

// assertOnlyFiles fails unless dir holds exactly names (no temporary files left behind).
func assertOnlyFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("unexpected file %s in %s", e.Name(), dir)
		}
		delete(want, e.Name())
	}
	for n := range want {
		t.Errorf("missing file %s", n)
	}
}

func TestStageRejects(t *testing.T) {
	p := Platform{OS: "windows", Arch: "amd64"}
	signer, other := newTestSigner(t), newTestSigner(t)
	asset, _ := AssetName("2.0.0", p)
	type mutate func(files map[string][]byte)
	cases := []struct {
		name   string
		req    Request
		mutate mutate
		want   error
	}{
		{"untrusted signature", Request{"r", "2.0.0"}, func(f map[string][]byte) {
			f[SignatureName] = other.sign(f[ChecksumsName], ChecksumsTrustedComment("2.0.0"), true)
		}, ErrSignatureKey},
		{"signature of another release", Request{"r", "2.0.0"}, func(f map[string][]byte) {
			f[SignatureName] = signer.sign(f[ChecksumsName], ChecksumsTrustedComment("1.0.0"), true)
		}, ErrSignatureComment},
		{"tampered archive", Request{"r", "2.0.0"}, func(f map[string][]byte) { f[asset] = append(f[asset], 0) }, ErrChecksumMismatch},
		{"archive not listed", Request{"r", "2.0.0"}, func(f map[string][]byte) {
			f[ChecksumsName] = []byte("0000000000000000000000000000000000000000000000000000000000000000  other.zip\n")
			f[SignatureName] = signer.sign(f[ChecksumsName], ChecksumsTrustedComment("2.0.0"), true)
		}, ErrChecksums},
		{"oversize signature", Request{"r", "2.0.0"}, func(f map[string][]byte) { f[SignatureName] = make([]byte, MaxSignatureBytes+1) }, ErrDownload},
		{"oversize checksums", Request{"r", "2.0.0"}, func(f map[string][]byte) { f[ChecksumsName] = make([]byte, MaxChecksumsBytes+1) }, ErrDownload},
		{"missing signature", Request{"r", "2.0.0"}, func(f map[string][]byte) { delete(f, SignatureName) }, ErrDownload},
		{"bad id", Request{"../x", "2.0.0"}, nil, ErrInvalidRequest},
		{"bad version", Request{"r", "v2.0.0"}, nil, ErrInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := releaseFiles(t, signer, "2.0.0", p, []byte("new binary"))
			if c.mutate != nil {
				c.mutate(files)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, RequestFile), []byte(`{"id":"old","version":"1.0.0"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			s := NewStager(releaseServer("2.0.0", files), dir, []string{signer.publicKey()}, p)
			if err := s.Stage(context.Background(), c.req, StageHooks{}); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if errors.Is(c.want, ErrInvalidRequest) {
				return
			}
			// Nothing is kept from a rejected download; the old request stays untouched.
			assertOnlyFiles(t, dir, RequestFile)
			assertFile(t, filepath.Join(dir, RequestFile), `{"id":"old","version":"1.0.0"}`)
		})
	}
}

func TestStageHooks(t *testing.T) {
	p := Platform{OS: "linux", Arch: "amd64"}
	signer := newTestSigner(t)
	files := releaseFiles(t, signer, "2.0.0", p, []byte("new binary"))
	asset, _ := AssetName("2.0.0", p)
	size := int64(len(files[asset]))
	var reserved, lastDone, lastTotal int64
	s := NewStager(releaseServer("2.0.0", files), t.TempDir(), []string{signer.publicKey()}, p)
	hooks := StageHooks{
		Reserve:  func(n int64) error { reserved = n; return nil },
		Progress: func(done, total int64) { lastDone, lastTotal = done, total },
	}
	if err := s.Stage(context.Background(), Request{ID: "req-1", Version: "2.0.0"}, hooks); err != nil {
		t.Fatal(err)
	}
	if reserved != size || lastDone != size || lastTotal != size {
		t.Errorf("reserved %d, progress %d/%d, want %d", reserved, lastDone, lastTotal, size)
	}

	// A refused reservation stops before the archive is written and keeps nothing.
	dir := t.TempDir()
	s = NewStager(releaseServer("2.0.0", files), dir, []string{signer.publicKey()}, p)
	full := errors.New("disk full")
	if err := s.Stage(context.Background(), Request{ID: "req-1", Version: "2.0.0"},
		StageHooks{Reserve: func(int64) error { return full }}); !errors.Is(err, full) {
		t.Fatalf("err = %v", err)
	}
	assertOnlyFiles(t, dir)
}
