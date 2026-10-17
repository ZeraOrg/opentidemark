// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Dead-letter file permissions: data may contain personal information.
const (
	deadLetterDirPerm  = 0o700
	deadLetterFilePerm = 0o600
	deadLetterExt      = ".ndjson"
)

// ErrDeadLetterTooBig: one batch alone is larger than the dead-letter cap; it is not written.
var ErrDeadLetterTooBig = errors.New("dead-letter batch is larger than TIDEMARK_DEADLETTER_MAX_BYTES")

// FileDeadLetter writes rows as NDJSON to <dir>/<signal>-<timestamp>-<seq>.ndjson (SPEC §9.1).
// With a cap, the oldest files are deleted to make room for a new one, so the folder never grows
// past maxBytes and the most recent failures stay available.
type FileDeadLetter struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64 // <= 0 = unlimited
	now      func() time.Time
	logger   *slog.Logger
	seq      int
}

// NewFileDeadLetter returns a dead-letter writer for dir (created on first use) that keeps the
// folder at or below maxBytes (<= 0 = unlimited).
func NewFileDeadLetter(dir string, maxBytes int64, now func() time.Time, logger *slog.Logger) *FileDeadLetter {
	return &FileDeadLetter{dir: dir, maxBytes: maxBytes, now: now, logger: logger}
}

// Write stores rows in a new file, first deleting the oldest files when the cap requires it.
func (d *FileDeadLetter) Write(signal string, rows []any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("encode dead-letter row: %w", err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(d.dir, deadLetterDirPerm); err != nil {
		return fmt.Errorf("create dead-letter dir: %w", err)
	}
	if err := d.makeRoom(int64(buf.Len())); err != nil {
		return err
	}
	d.seq++
	name := fmt.Sprintf("%s-%s-%d%s", signal, d.now().UTC().Format("20060102T150405.000000000Z"), d.seq, deadLetterExt)
	f, err := os.OpenFile(filepath.Join(d.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, deadLetterFilePerm)
	if err != nil {
		return fmt.Errorf("create dead-letter file: %w", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close() // the write error is the one to report
		return fmt.Errorf("write dead-letter file: %w", err)
	}
	return f.Close()
}

type deadLetterFile struct {
	name string
	size int64
	mod  time.Time
}

// makeRoom deletes the oldest dead-letter files until n more bytes fit under the cap.
func (d *FileDeadLetter) makeRoom(n int64) error {
	if d.maxBytes <= 0 {
		return nil
	}
	if n > d.maxBytes {
		return fmt.Errorf("%w: %d bytes", ErrDeadLetterTooBig, n)
	}
	files, total, err := d.list()
	if err != nil {
		return err
	}
	removed := 0
	for i := 0; total+n > d.maxBytes && i < len(files); i++ {
		if err := os.Remove(filepath.Join(d.dir, files[i].name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("delete old dead-letter file: %w", err)
		}
		total -= files[i].size
		removed++
	}
	if removed > 0 {
		d.logger.Warn("dead-letter folder at its size cap: deleted the oldest files", "files", removed,
			"max_bytes", d.maxBytes)
	}
	return nil
}

// list returns the dead-letter files oldest first and their total size.
func (d *FileDeadLetter) list() ([]deadLetterFile, int64, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, 0, fmt.Errorf("list dead-letter dir: %w", err)
	}
	var files []deadLetterFile
	var total int64
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), deadLetterExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // deleted meanwhile
		}
		files = append(files, deadLetterFile{name: e.Name(), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.Before(files[j].mod)
		}
		return files[i].name < files[j].name
	})
	return files, total, nil
}
