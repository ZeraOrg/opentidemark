// SPDX-License-Identifier: Apache-2.0

package update

import (
	"os"
	"path/filepath"
	"testing"
)

// The Linux helper runs as root without capabilities and reads the staged files through the
// service's group; state.json is read back by the service (SPEC §17.7).
func TestWriteFileAtomicMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []os.FileMode{sharedFileMode, publicFileMode} {
		if err := writeFileAtomic(dir, "f", []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(dir, "f"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != mode {
			t.Errorf("mode %v, want %v", fi.Mode().Perm(), mode)
		}
	}
}
