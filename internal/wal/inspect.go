// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Stats describes a log directory without modifying it (tidemark doctor).
type Stats struct {
	Segments     int
	Bytes        int64 // all segment bytes on disk
	Uncommitted  int64 // bytes after the checkpoint's committed position
	TornBytes    int64 // bytes after the last valid record of each segment (truncated at next open)
	Checkpoint   Checkpoint
	HasPending   bool
	FirstSegment uint64
	LastSegment  uint64
}

// Inspect reads the segments and checkpoint of dir read-only. A missing directory yields zero
// stats. It is safe to run while a server has the log open.
func Inspect(dir string) (Stats, error) {
	var st Stats
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	seqs, err := listSegments(dir)
	if err != nil {
		return st, err
	}
	cp, found := loadCheckpoint(dir)
	st.Checkpoint, st.HasPending = cp, cp.Pending != nil
	for _, seq := range seqs {
		size, valid, err := inspectSegment(segmentPath(dir, seq))
		if err != nil {
			return st, err
		}
		st.Segments++
		st.Bytes += size
		st.TornBytes += size - valid
		switch {
		case !found || seq > cp.Committed.Seg:
			st.Uncommitted += valid
		case seq == cp.Committed.Seg:
			st.Uncommitted += max(valid-cp.Committed.Off, 0)
		}
	}
	if len(seqs) > 0 {
		st.FirstSegment, st.LastSegment = seqs[0], seqs[len(seqs)-1]
	}
	return st, nil
}

func loadCheckpoint(dir string) (Checkpoint, bool) {
	best, found := Checkpoint{}, false
	for _, name := range checkpointSlots {
		cp, err := readCheckpoint(filepath.Join(dir, name))
		if err == nil && (!found || cp.Seq > best.Seq) {
			best, found = cp, true
		}
	}
	return best, found
}

func inspectSegment(path string) (size, valid int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat segment: %w", err)
	}
	valid, err = scanValid(f)
	return info.Size(), valid, err
}
