// SPDX-License-Identifier: Apache-2.0

package update

import "testing"

func TestWindowsPaths(t *testing.T) {
	got := windowsPaths(`C:\Program Files`, `C:\ProgramData`)
	want := HelperPaths{Binary: `C:\Program Files\Tidemark\tidemark.exe`, Staging: `C:\ProgramData\Tidemark\update`,
		Work: `C:\Program Files\Tidemark\state`}
	if got != want {
		t.Errorf("paths %+v, want %+v", got, want)
	}
	if got.StatePath() != `C:\Program Files\Tidemark\state\state.json` {
		t.Errorf("state path %s", got.StatePath())
	}
	if _, err := DefaultPaths(); err != nil {
		t.Error(err)
	}
}
