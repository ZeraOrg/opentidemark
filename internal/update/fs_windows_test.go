// SPDX-License-Identifier: Apache-2.0

package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// An open handle without FILE_SHARE_DELETE (os.Open, or a virus scanner) blocks a rename; the
// rename succeeds once it is closed, and fails unchanged when it stays open.
func TestRenameRetryingWhileHeld(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(from, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := renameRetrying(from, to, 3, time.Millisecond); err == nil || !retryableRename(err) {
		t.Fatalf("rename of a held file: %v", err)
	}
	assertFile(t, from, "x")
	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = held.Close()
		close(released)
	}()
	if err := renameRetrying(from, to, 100, 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	<-released
	assertFile(t, to, "x")
}
