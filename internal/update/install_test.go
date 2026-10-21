// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// installFixture is a staged release next to an installed binary, all in temporary directories.
type installFixture struct {
	signer  testSigner
	p       Platform
	staging string
	work    string
	bin     string
	now     time.Time
}

const oldBinary = "old binary"

func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	f := &installFixture{signer: newTestSigner(t), p: hostPlatform(t), staging: t.TempDir(), work: t.TempDir(),
		now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
	name := "tidemark"
	if f.p.OS == "windows" {
		name += ".exe"
	}
	f.bin = filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(f.bin, []byte(oldBinary), 0o755); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return f
}

// stage writes a signed release of version and its request, as the Stager would.
func (f *installFixture) stage(t *testing.T, id, version, binary string) map[string][]byte {
	t.Helper()
	files := releaseFiles(t, f.signer, version, f.p, []byte(binary))
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(f.staging, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := json.Marshal(Request{ID: id, Version: version})
	if err := os.WriteFile(filepath.Join(f.staging, RequestFile), req, 0o600); err != nil {
		t.Fatal(err)
	}
	return files
}

func (f *installFixture) installer(t *testing.T, current string) *Installer {
	t.Helper()
	in, err := NewInstaller(InstallerConfig{StagingDir: f.staging, WorkDir: f.work, BinaryPath: f.bin, Current: current,
		Platform: f.p, Keys: []string{f.signer.publicKey()}, Now: func() time.Time { return f.now }, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s = %q (%v), want %q", filepath.Base(path), got, err, want)
	}
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists (%v)", filepath.Base(path), err)
	}
}

func TestInstallAndRollback(t *testing.T) {
	f := newInstallFixture(t)
	f.stage(t, "req-1", "2.0.0", "new binary")
	in := f.installer(t, "1.0.0")
	st, err := in.Install(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := InstallState{ID: "req-1", Phase: PhaseInstalled, From: "1.0.0", To: "2.0.0", At: f.now}
	if st != want {
		t.Errorf("state %+v, want %+v", st, want)
	}
	assertFile(t, f.bin, "new binary")
	assertFile(t, f.bin+PreviousSuffix, oldBinary)
	assertNoFile(t, f.bin+newSuffix)
	assertNoFile(t, filepath.Join(f.work, lockFile))
	if saved, ok, err := in.State(); err != nil || !ok || saved != want {
		t.Errorf("state.json = %+v, %v, %v", saved, ok, err)
	}

	// The same request again changes nothing and reports the recorded state.
	f.now = f.now.Add(time.Hour)
	if again, err := in.Install(context.Background()); err != nil || again != want {
		t.Errorf("repeated install = %+v, %v", again, err)
	}
	assertFile(t, f.bin+PreviousSuffix, oldBinary)

	rolled, err := in.Rollback("not confirmed")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Phase != PhaseRolledBack || rolled.Message != "not confirmed" || !rolled.At.Equal(f.now) {
		t.Errorf("rolled back state %+v", rolled)
	}
	assertFile(t, f.bin, oldBinary)
	if _, err := in.Rollback("again"); !errors.Is(err, ErrNoInstall) {
		t.Errorf("second rollback: %v", err)
	}
	// The rolled-back request is not installed again.
	if again, err := in.Install(context.Background()); err != nil || again.Phase != PhaseRolledBack {
		t.Errorf("install after rollback = %+v, %v", again, err)
	}
	assertFile(t, f.bin, oldBinary)
}

func TestInstallSecondUpdateReplacesPrevious(t *testing.T) {
	f := newInstallFixture(t)
	f.stage(t, "req-1", "2.0.0", "binary 2")
	if _, err := f.installer(t, "1.0.0").Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.stage(t, "req-2", "2.1.0", "binary 2.1")
	st, err := f.installer(t, "2.0.0").Install(context.Background())
	if err != nil || st.Phase != PhaseInstalled || st.From != "2.0.0" {
		t.Fatalf("second install = %+v, %v", st, err)
	}
	assertFile(t, f.bin, "binary 2.1")
	assertFile(t, f.bin+PreviousSuffix, "binary 2")
}

func TestInstallFailures(t *testing.T) {
	cases := []struct {
		name    string
		current string
		version string
		mutate  func(t *testing.T, f *installFixture, files map[string][]byte)
		want    error
	}{
		{"same version", "2.0.0", "2.0.0", nil, ErrNotNewer},
		{"downgrade", "2.0.1", "2.0.0", nil, ErrNotNewer},
		{"pre-release below its release", "2.0.0", "2.0.0-rc.1", nil, ErrNotNewer},
		{"archive changed after staging", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, files map[string][]byte) {
			asset, _ := AssetName("2.0.0", f.p)
			writeStaged(t, f, asset, append(files[asset], 0))
		}, ErrChecksumMismatch},
		{"signature replaced", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, files map[string][]byte) {
			other := newTestSigner(t)
			writeStaged(t, f, SignatureName, other.sign(files[ChecksumsName], ChecksumsTrustedComment("2.0.0"), true))
		}, ErrSignatureKey},
		{"older signed checksums", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, files map[string][]byte) {
			writeStaged(t, f, SignatureName, f.signer.sign(files[ChecksumsName], ChecksumsTrustedComment("1.5.0"), true))
		}, ErrSignatureComment},
		{"oversize signature", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, _ map[string][]byte) {
			writeStaged(t, f, SignatureName, make([]byte, MaxSignatureBytes+1))
		}, ErrUnsafeFile},
		{"archive missing", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, _ map[string][]byte) {
			asset, _ := AssetName("2.0.0", f.p)
			if err := os.Remove(filepath.Join(f.staging, asset)); err != nil {
				t.Fatal(err)
			}
		}, os.ErrNotExist},
		{"staged directory instead of file", "1.0.0", "2.0.0", func(t *testing.T, f *installFixture, _ map[string][]byte) {
			if err := os.Remove(filepath.Join(f.staging, ChecksumsName)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(f.staging, ChecksumsName), 0o700); err != nil {
				t.Fatal(err)
			}
		}, ErrUnsafeFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newInstallFixture(t)
			files := f.stage(t, "req-1", c.version, "new binary")
			if c.mutate != nil {
				c.mutate(t, f, files)
			}
			st, err := f.installer(t, c.current).Install(context.Background())
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if st.Phase != PhaseFailed || st.ID != "req-1" || st.Message == "" {
				t.Errorf("state %+v", st)
			}
			assertFile(t, f.bin, oldBinary)
			assertNoFile(t, f.bin+PreviousSuffix)
			assertNoFile(t, f.bin+newSuffix)
			assertOnlyFiles(t, f.work, StateFile)
		})
	}
}

func writeStaged(t *testing.T, f *installFixture, name string, body []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.staging, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesSymlinkedStagedFile(t *testing.T) {
	f := newInstallFixture(t)
	files := f.stage(t, "req-1", "2.0.0", "new binary")
	asset, _ := AssetName("2.0.0", f.p)
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, files[asset], 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.staging, asset)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	if _, err := f.installer(t, "1.0.0").Install(context.Background()); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("err = %v, want ErrUnsafeFile", err)
	}
	assertFile(t, f.bin, oldBinary)
}

func TestInstallRequestAndLock(t *testing.T) {
	f := newInstallFixture(t)
	in := f.installer(t, "1.0.0")
	if _, err := in.Install(context.Background()); !errors.Is(err, ErrNothingStaged) {
		t.Errorf("nothing staged: %v", err)
	}
	writeStaged(t, f, RequestFile, []byte(`{"id":"a/b","version":"2.0.0"}`))
	if _, err := in.Install(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("bad request id: %v", err)
	}
	f.stage(t, "req-1", "2.0.0", "new binary")
	// The lock's age is judged by its modification time, so the clock must be the real one here.
	f.now = time.Now()
	lock := filepath.Join(f.work, lockFile)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Install(context.Background()); !errors.Is(err, ErrLocked) {
		t.Errorf("held lock: %v", err)
	}
	if _, err := in.Rollback("x"); !errors.Is(err, ErrLocked) {
		t.Errorf("rollback under a held lock: %v", err)
	}
	// A lock older than lockStaleAfter belongs to a helper that died and is taken over.
	f.now = time.Now().Add(lockStaleAfter + time.Minute)
	if st, err := in.Install(context.Background()); err != nil || st.Phase != PhaseInstalled {
		t.Errorf("stale lock: %+v, %v", st, err)
	}
	assertNoFile(t, lock)
}

func TestNewInstallerValidates(t *testing.T) {
	f := newInstallFixture(t)
	good := InstallerConfig{StagingDir: f.staging, WorkDir: f.work, BinaryPath: f.bin, Current: "1.0.0",
		Platform: f.p, Keys: []string{f.signer.publicKey()}, Now: time.Now}
	bad := []func(c *InstallerConfig){
		func(c *InstallerConfig) { c.Current = "dev" },
		func(c *InstallerConfig) { c.StagingDir = "" },
		func(c *InstallerConfig) { c.Keys = nil },
		func(c *InstallerConfig) { c.Now = nil },
	}
	for i, mutate := range bad {
		c := good
		mutate(&c)
		if _, err := NewInstaller(c); !errors.Is(err, ErrInstallerSetup) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	c := good
	c.Platform = Platform{OS: "darwin", Arch: "arm64"}
	if _, err := NewInstaller(c); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Errorf("darwin: %v", err)
	}
}

func TestWaitConfirmed(t *testing.T) {
	f := newInstallFixture(t)
	in := f.installer(t, "1.0.0")
	if err := in.WaitConfirmed(context.Background(), time.Millisecond); !errors.Is(err, ErrNoInstall) {
		t.Errorf("before any install: %v", err)
	}
	f.stage(t, "req-1", "2.0.0", "new binary")
	if _, err := in.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	confirm := filepath.Join(f.staging, ConfirmFile)
	for _, wrong := range []string{`{"id":"req-0","version":"2.0.0"}`, `{"id":"req-1","version":"1.0.0"}`, `not json`} {
		writeStaged(t, f, ConfirmFile, []byte(wrong))
		if err := in.WaitConfirmed(context.Background(), 20*time.Millisecond); !errors.Is(err, ErrNotConfirmed) {
			t.Errorf("confirmation %s: %v", wrong, err)
		}
	}
	if err := os.Remove(confirm); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- in.WaitConfirmed(context.Background(), 10*time.Second) }()
	writeStaged(t, f, ConfirmFile, []byte(`{"id":"req-1","version":"2.0.0"}`))
	if err := <-done; err != nil {
		t.Errorf("confirmed: %v", err)
	}
	if ConfirmReadyAfter != 30*time.Second || ConfirmTimeout != 10*time.Minute {
		t.Errorf("SPEC §17.7 timings changed: ready %v, timeout %v", ConfirmReadyAfter, ConfirmTimeout)
	}
}
