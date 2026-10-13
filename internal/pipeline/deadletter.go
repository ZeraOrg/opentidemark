// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Dead-letter file permissions: data may contain personal information.
const (
	deadLetterDirPerm  = 0o700
	deadLetterFilePerm = 0o600
)

// FileDeadLetter writes rows as NDJSON to <dir>/<signal>-<timestamp>.ndjson (SPEC §9.1).
type FileDeadLetter struct {
	mu  sync.Mutex
	dir string
	now func() time.Time
	seq int
}

// NewFileDeadLetter returns a dead-letter writer for dir (created on first use).
func NewFileDeadLetter(dir string, now func() time.Time) *FileDeadLetter {
	return &FileDeadLetter{dir: dir, now: now}
}

// Write stores rows in a new file.
func (d *FileDeadLetter) Write(signal string, rows []any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.dir, deadLetterDirPerm); err != nil {
		return fmt.Errorf("create dead-letter dir: %w", err)
	}
	d.seq++
	name := fmt.Sprintf("%s-%s-%d.ndjson", signal, d.now().UTC().Format("20060102T150405.000000000Z"), d.seq)
	f, err := os.OpenFile(filepath.Join(d.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, deadLetterFilePerm)
	if err != nil {
		return fmt.Errorf("create dead-letter file: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			_ = f.Close() // the encode error is the one to report
			return fmt.Errorf("encode dead-letter row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close() // the flush error is the one to report
		return fmt.Errorf("write dead-letter file: %w", err)
	}
	return f.Close()
}
