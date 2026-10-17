// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openLog(t *testing.T, dir string, opts Options) (*Log, RecoveryReport) {
	t.Helper()
	l, rep, err := Open(dir, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return l, rep
}

func appendN(t *testing.T, l *Log, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if _, err := l.Append(context.Background(), []byte(fmt.Sprintf("record-%03d", i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func readAll(t *testing.T, l *Log, from Pos) []Record {
	t.Helper()
	recs, err := l.ReadFrom(from, 1<<20, 1<<30)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return recs
}

func TestAppendReadAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{SegmentBytes: 64})
	appendN(t, l, 0, 20)
	if l.Segments() < 3 {
		t.Fatalf("expected several segments, got %d", l.Segments())
	}
	recs := readAll(t, l, Pos{})
	if len(recs) != 20 {
		t.Fatalf("read %d records", len(recs))
	}
	for i, r := range recs {
		if string(r.Payload) != fmt.Sprintf("record-%03d", i) {
			t.Fatalf("record %d = %q", i, r.Payload)
		}
	}
	if more := readAll(t, l, recs[len(recs)-1].End); len(more) != 0 {
		t.Fatalf("records past the end: %d", len(more))
	}
	mid := recs[10].Pos
	if got := readAll(t, l, mid); len(got) != 10 || string(got[0].Payload) != "record-010" {
		t.Fatalf("read from middle: %d", len(got))
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, rep := openLog(t, dir, Options{SegmentBytes: 64})
	defer l2.Close()
	if rep.TruncatedBytes != 0 || len(readAll(t, l2, Pos{})) != 20 {
		t.Fatalf("reopen lost data: %+v", rep)
	}
}

// lastSegment returns the path of the newest segment file.
func lastSegment(t *testing.T, dir string) string {
	t.Helper()
	seqs, err := listSegments(dir)
	if err != nil || len(seqs) == 0 {
		t.Fatalf("segments: %v %v", seqs, err)
	}
	return segmentPath(dir, seqs[len(seqs)-1])
}

// TestAppendWithCancelledContextStillWrites: ctx only bounds the wait for the fsync, so a
// caller that gives up never loses its record (the pipeline relies on this, SPEC §9.8).
func TestAppendWithCancelledContextStillWrites(t *testing.T) {
	l, _ := openLog(t, t.TempDir(), Options{})
	defer func() { _ = l.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	end, err := l.Append(ctx, []byte("abandoned"))
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("append: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); l.Durable().Less(end); {
		if time.Now().After(deadline) {
			t.Fatal("record never became durable")
		}
		time.Sleep(time.Millisecond)
	}
	if recs := readAll(t, l, Pos{}); len(recs) != 1 || string(recs[0].Payload) != "abandoned" {
		t.Fatalf("records %+v", recs)
	}
}

func TestCrashRecovery(t *testing.T) {
	tests := []struct {
		name    string
		damage  func(t *testing.T, path string)
		wantRec int
	}{
		{"kill mid-write (torn payload)", func(t *testing.T, p string) { truncateBy(t, p, 3) }, 4},
		{"kill mid-header", func(t *testing.T, p string) { truncateBy(t, p, int64(len("record-004"))+headerBytes-2) }, 4},
		{"corrupt tail checksum", func(t *testing.T, p string) { flipByte(t, p, -2) }, 4},
		{"garbage length", func(t *testing.T, p string) { appendBytes(t, p, []byte{0xff, 0xff, 0xff, 0x7f, 1, 2, 3, 4}) }, 5},
		{"zero-filled tail", func(t *testing.T, p string) { appendBytes(t, p, make([]byte, 64)) }, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l, _ := openLog(t, dir, Options{})
			appendN(t, l, 0, 5)
			_ = l.Close()
			tc.damage(t, lastSegment(t, dir))
			l2, rep := openLog(t, dir, Options{})
			defer l2.Close()
			if rep.TruncatedBytes == 0 {
				t.Errorf("nothing truncated: %+v", rep)
			}
			recs := readAll(t, l2, Pos{})
			if len(recs) != tc.wantRec {
				t.Fatalf("recovered %d records, want %d", len(recs), tc.wantRec)
			}
			appendN(t, l2, 100, 1)
			recs = readAll(t, l2, Pos{})
			if len(recs) != tc.wantRec+1 || string(recs[len(recs)-1].Payload) != "record-100" {
				t.Fatalf("append after recovery: %d records", len(recs))
			}
		})
	}
}

func truncateBy(t *testing.T, path string, n int64) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, st.Size()-n); err != nil {
		t.Fatal(err)
	}
}

func flipByte(t *testing.T, path string, fromEnd int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)+fromEnd] ^= 0xff
	if err := os.WriteFile(path, raw, filePerm); err != nil {
		t.Fatal(err)
	}
}

func appendBytes(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointSlots(t *testing.T) {
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{})
	if cp, _ := l.Checkpoint(); cp.Committed.Seg != 1 || cp.Pending != nil {
		t.Fatalf("initial checkpoint %+v", cp)
	}
	first := Checkpoint{Committed: Pos{Seg: 1, Off: 10}, Pending: &Pending{Start: Pos{Seg: 1, Off: 10}, End: Pos{Seg: 1, Off: 30}, Token: "t1"}}
	if err := l.SaveCheckpoint(first); err != nil {
		t.Fatal(err)
	}
	if err := l.SaveCheckpoint(Checkpoint{Committed: Pos{Seg: 1, Off: 30}}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	l2, _ := openLog(t, dir, Options{})
	cp, _ := l2.Checkpoint()
	if cp.Committed != (Pos{Seg: 1, Off: 30}) || cp.Pending != nil {
		t.Fatalf("latest checkpoint %+v", cp)
	}
	_ = l2.Close()
	// A torn write of the newest slot falls back to the previous checkpoint.
	newest := filepath.Join(dir, checkpointSlots[cp.Seq%2])
	truncateBy(t, newest, 5)
	l3, _ := openLog(t, dir, Options{})
	defer l3.Close()
	cp, _ = l3.Checkpoint()
	if cp.Committed != first.Committed || cp.Pending == nil || cp.Pending.Token != "t1" {
		t.Fatalf("fallback checkpoint %+v", cp)
	}
	if err := l3.SaveCheckpoint(Checkpoint{Committed: Pos{Seg: 1, Off: 40}}); err != nil {
		t.Fatal(err)
	}
	if cp, _ := l3.Checkpoint(); cp.Committed.Off != 40 {
		t.Fatalf("save after fallback %+v", cp)
	}
}

func TestBudgetBackpressureAndRelease(t *testing.T) {
	dir := t.TempDir()
	b := NewBudget(200)
	l, _ := openLog(t, dir, Options{SegmentBytes: 64, Budget: b})
	defer l.Close()
	var err error
	n := 0
	for ; n < 100; n++ {
		if _, err = l.Append(context.Background(), []byte("0123456789012345")); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrFull) || n == 0 {
		t.Fatalf("expected ErrFull after some appends, got %v after %d", err, n)
	}
	if b.Used() > b.Max() {
		t.Fatalf("budget exceeded: %d > %d", b.Used(), b.Max())
	}
	recs := readAll(t, l, Pos{})
	before := l.Segments()
	if err := l.Release(recs[len(recs)-1].End); err != nil {
		t.Fatal(err)
	}
	if l.Segments() != 1 || before < 2 {
		t.Fatalf("segments %d -> %d", before, l.Segments())
	}
	if seqs, _ := listSegments(dir); len(seqs) != 1 {
		t.Fatalf("files left: %v", seqs)
	}
	if _, err := l.Append(context.Background(), []byte("0123456789012345")); err != nil {
		t.Fatalf("append after release: %v", err)
	}
}

func TestBudgetCountsExistingSegments(t *testing.T) {
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{})
	appendN(t, l, 0, 3)
	size := l.Size()
	_ = l.Close()
	b := NewBudget(1 << 20)
	l2, _ := openLog(t, dir, Options{Budget: b})
	defer l2.Close()
	if b.Used() != size || size == 0 {
		t.Fatalf("budget %d, size %d", b.Used(), size)
	}
}

func TestGroupCommitConcurrent(t *testing.T) {
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{SegmentBytes: 4096})
	var wg sync.WaitGroup
	const writers, each = 16, 25
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				end, err := l.Append(context.Background(), []byte(fmt.Sprintf("w%02d-%02d", w, i)))
				if err != nil {
					t.Error(err)
					return
				}
				if l.Durable().Less(end) {
					t.Error("Append returned before the record was durable")
				}
			}
		}()
	}
	wg.Wait()
	if got := len(readAll(t, l, Pos{})); got != writers*each {
		t.Fatalf("read %d records", got)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Append(context.Background(), []byte("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("append after close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func TestSyncNoneAndNotify(t *testing.T) {
	l, _ := openLog(t, t.TempDir(), Options{Sync: SyncNone})
	defer l.Close()
	appendN(t, l, 0, 1)
	select {
	case <-l.Notify():
	default:
		t.Error("no notification after append")
	}
	if _, err := l.Append(context.Background(), make([]byte, MaxRecordBytes+1)); !errors.Is(err, ErrTooBig) {
		t.Errorf("oversize record: %v", err)
	}
	if _, err := l.Append(context.Background(), nil); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty record: %v", err)
	}
}

func TestSkipCorrupt(t *testing.T) {
	dir := t.TempDir()
	l, _ := openLog(t, dir, Options{SegmentBytes: 40})
	appendN(t, l, 0, 4)
	if l.Segments() < 2 {
		t.Fatal("need two segments")
	}
	if next := l.SkipCorrupt(Pos{Seg: 1, Off: 5}); next.Seg != 2 || next.Off != 0 {
		t.Errorf("skip -> %+v", next)
	}
	_ = l.Close()
}

// errDiskFull stands in for ENOSPC in the fault-injection tests.
var errDiskFull = errors.New("no space left on device")

// faultyFile wraps the active segment: while short is set, a write stores only that many bytes
// and fails, like a write that hits a full disk; truncateErr makes Truncate fail.
type faultyFile struct {
	segmentFile
	short       int
	truncateErr error
}

func (f *faultyFile) Write(p []byte) (int, error) {
	if f.short <= 0 {
		return f.segmentFile.Write(p)
	}
	n, err := f.segmentFile.Write(p[:min(f.short, len(p))])
	if err != nil {
		return n, err
	}
	return n, errDiskFull
}

func (f *faultyFile) Truncate(size int64) error {
	if f.truncateErr != nil {
		return f.truncateErr
	}
	return f.segmentFile.Truncate(size)
}

func injectFault(l *Log) *faultyFile {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := &faultyFile{segmentFile: l.active}
	l.active = f
	return f
}

func wantPayloads(t *testing.T, recs []Record, want ...string) {
	t.Helper()
	got := make([]string, len(recs))
	for i, r := range recs {
		got[i] = string(r.Payload)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}
}

// Regression: a partial write (disk full) must not leave bytes that hide later acknowledged
// records from the reader and from crash recovery.
func TestPartialWriteDoesNotLoseLaterRecords(t *testing.T) {
	tests := []struct {
		name        string
		truncateErr bool // the first undo fails too (still no space)
	}{
		{name: "undone at once"},
		{name: "undone before the next append", truncateErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l, _ := openLog(t, dir, Options{})
			appendN(t, l, 0, 1)
			fault := injectFault(l)
			fault.short = 5
			if tc.truncateErr {
				fault.truncateErr = errDiskFull
			}
			if _, err := l.Append(context.Background(), []byte("lost")); !errors.Is(err, errDiskFull) {
				t.Fatalf("append on a full disk: err = %v", err)
			}
			fault.short = 0
			fault.truncateErr = nil
			appendN(t, l, 1, 2)
			wantPayloads(t, readAll(t, l, Pos{}), "record-000", "record-001", "record-002")
			if err := l.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			l2, rep := openLog(t, dir, Options{})
			defer l2.Close()
			if rep.TruncatedBytes != 0 {
				t.Fatalf("recovery truncated %d bytes of acknowledged records", rep.TruncatedBytes)
			}
			wantPayloads(t, readAll(t, l2, Pos{}), "record-000", "record-001", "record-002")
		})
	}
}

// While the partial bytes cannot be removed, appends keep failing instead of writing behind them.
func TestAppendFailsUntilPartialWriteIsUndone(t *testing.T) {
	l, _ := openLog(t, t.TempDir(), Options{})
	defer l.Close()
	fault := injectFault(l)
	fault.short = 3
	fault.truncateErr = errDiskFull
	if _, err := l.Append(context.Background(), []byte("lost")); err == nil {
		t.Fatal("append on a full disk succeeded")
	}
	fault.short = 0
	if _, err := l.Append(context.Background(), []byte("blocked")); !errors.Is(err, errDiskFull) {
		t.Fatalf("append behind undiscarded bytes: err = %v", err)
	}
	fault.truncateErr = nil
	appendN(t, l, 0, 1)
	wantPayloads(t, readAll(t, l, Pos{}), "record-000")
}
