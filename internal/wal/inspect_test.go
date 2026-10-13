// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInspectIsReadOnly(t *testing.T) {
	if st, err := Inspect(filepath.Join(t.TempDir(), "missing")); err != nil || st.Segments != 0 {
		t.Fatalf("missing dir: %+v %v", st, err)
	}
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{SegmentBytes: 64})
	appendN(t, l, 0, 10) // 18 bytes each, three per segment
	recs := readAll(t, l, Pos{})
	if err := l.SaveCheckpoint(Checkpoint{Committed: recs[4].End,
		Pending: &Pending{Start: recs[4].End, End: recs[6].End, Token: "t"}}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	last := lastSegment(t, dir)
	appendBytes(t, last, []byte{1, 2, 3})
	before, _ := os.Stat(last)

	st, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Segments != 4 || st.TornBytes != 3 || !st.HasPending || st.FirstSegment != 1 || st.LastSegment != 4 {
		t.Errorf("stats %+v", st)
	}
	if want := int64(5 * 18); st.Uncommitted != want {
		t.Errorf("uncommitted %d, want %d", st.Uncommitted, want)
	}
	if st.Bytes != 10*18+3 {
		t.Errorf("bytes %d", st.Bytes)
	}
	if after, _ := os.Stat(last); after.Size() != before.Size() {
		t.Error("Inspect modified the segment")
	}
}
