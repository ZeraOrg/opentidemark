// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
)

// checkpointSlots are written alternately; Load picks the valid one with the highest sequence,
// so a torn checkpoint write never loses the previous checkpoint (no rename needed).
var checkpointSlots = [2]string{"checkpoint.0", "checkpoint.1"}

// Checkpoint is the consumer's persisted progress: everything before Committed is stored in
// ClickHouse. Pending, when set, is the batch being inserted: on restart exactly that range is
// re-sent with the same token so ClickHouse deduplicates it (SPEC §9.8).
type Checkpoint struct {
	Seq       uint64   `json:"seq"`
	Committed Pos      `json:"committed"`
	Pending   *Pending `json:"pending,omitempty"`
}

// Pending is an in-flight insert batch.
type Pending struct {
	Start Pos    `json:"start"`
	End   Pos    `json:"end"`
	Token string `json:"token"`
}

type checkpointFile struct {
	mu  sync.Mutex
	dir string
	seq uint64
}

// Checkpoint loads the latest valid checkpoint (zero value when none was saved).
func (l *Log) Checkpoint() (Checkpoint, error) {
	best := Checkpoint{}
	found := false
	for _, name := range checkpointSlots {
		cp, err := readCheckpoint(filepath.Join(l.dir, name))
		if err != nil {
			continue
		}
		if !found || cp.Seq > best.Seq {
			best, found = cp, true
		}
	}
	l.cp.mu.Lock()
	l.cp.seq = max(l.cp.seq, best.Seq)
	l.cp.mu.Unlock()
	if !found {
		l.mu.Lock()
		if len(l.segs) > 0 {
			best.Committed = Pos{Seg: l.segs[0].seq}
		}
		l.mu.Unlock()
	}
	return best, nil
}

// SaveCheckpoint durably stores cp (fsync) in the older slot.
func (l *Log) SaveCheckpoint(cp Checkpoint) error {
	l.cp.mu.Lock()
	defer l.cp.mu.Unlock()
	l.cp.seq++
	cp.Seq = l.cp.seq
	body, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	buf := make([]byte, headerBytes+len(body))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(body))) //nolint:gosec // a checkpoint is a few hundred bytes
	binary.LittleEndian.PutUint32(buf[4:8], crc32.Checksum(body, castagnoli))
	copy(buf[headerBytes:], body)
	path := filepath.Join(l.dir, checkpointSlots[cp.Seq%uint64(len(checkpointSlots))])
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("open checkpoint: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close() // reporting the write failure
		return fmt.Errorf("write checkpoint: %w", err)
	}
	if l.opts.Sync != SyncNone {
		if err := f.Sync(); err != nil {
			_ = f.Close() // reporting the sync failure
			return fmt.Errorf("fsync checkpoint: %w", err)
		}
	}
	return f.Close()
}

var errBadCheckpoint = errors.New("invalid checkpoint")

func readCheckpoint(path string) (Checkpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(raw) < headerBytes {
		return Checkpoint{}, errBadCheckpoint
	}
	n := binary.LittleEndian.Uint32(raw[0:4])
	if int(n) != len(raw)-headerBytes || crc32.Checksum(raw[headerBytes:], castagnoli) != binary.LittleEndian.Uint32(raw[4:8]) {
		return Checkpoint{}, errBadCheckpoint
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw[headerBytes:], &cp); err != nil {
		return Checkpoint{}, errBadCheckpoint
	}
	return cp, nil
}
