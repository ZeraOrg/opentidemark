// SPDX-License-Identifier: Apache-2.0

// Package wal is the ingestion write-ahead log (SPEC §9.8): an append-only sequence of segment
// files per signal with CRC32C-protected records, group-committed fsyncs, torn-tail truncation
// on open, a crash-safe checkpoint, segment deletion and a shared size budget for backpressure.
//
// Files are never renamed over and are closed before they are deleted, so the same code is
// safe on Windows (where open files cannot be replaced or removed).
package wal

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Defaults and limits.
const (
	DefaultSegmentBytes = 64 << 20
	MaxRecordBytes      = 1 << 30
	headerBytes         = 8
	segmentSuffix       = ".wal"
	dirPerm             = 0o700
	filePerm            = 0o600
)

// SyncMode selects the durability of Append.
type SyncMode string

// Sync modes (TIDEMARK_WAL_SYNC).
const (
	SyncBatch SyncMode = "batch" // group commit: Append returns after the fsync covering the record
	SyncNone  SyncMode = "none"  // no fsync (tests only)
)

// Errors.
var (
	ErrFull    = errors.New("write-ahead log is full")
	ErrClosed  = errors.New("write-ahead log is closed")
	ErrCorrupt = errors.New("write-ahead log record is corrupt")
	ErrTooBig  = errors.New("write-ahead log record is too large")
	ErrEmpty   = errors.New("write-ahead log record is empty")
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Pos is a position in the log: segment sequence number and byte offset.
type Pos struct {
	Seg uint64 `json:"seg"`
	Off int64  `json:"off"`
}

// Less reports whether p is before q.
func (p Pos) Less(q Pos) bool { return p.Seg < q.Seg || p.Seg == q.Seg && p.Off < q.Off }

// Record is one payload with its location.
type Record struct {
	Pos     Pos // start of the record
	End     Pos // position after the record
	Payload []byte
}

// Options configure a Log.
type Options struct {
	SegmentBytes int64
	Sync         SyncMode
	Budget       *Budget // shared size cap; nil = unlimited
}

// RecoveryReport describes what Open repaired.
type RecoveryReport struct {
	Segments       int
	TruncatedBytes int64
	TruncatedAt    []Pos
}

// Log is one directory of segments. Append is safe for concurrent use; reads and releases are
// meant for a single consumer.
type Log struct {
	dir  string
	opts Options

	mu       sync.Mutex
	cond     *sync.Cond
	active   *os.File
	segs     []segment // on disk, ascending
	written  Pos
	durable  Pos
	syncErr  error
	closed   bool
	syncWake chan struct{}
	stop     chan struct{}
	notify   chan struct{}
	done     chan struct{}

	reader *segmentReader
	cp     *checkpointFile
}

type segment struct {
	seq  uint64
	size int64
}

// Open opens or creates the log in dir, truncating torn or corrupt record tails, and starts the
// group-commit syncer.
func Open(dir string, opts Options) (*Log, RecoveryReport, error) {
	if opts.SegmentBytes <= 0 {
		opts.SegmentBytes = DefaultSegmentBytes
	}
	if opts.Sync == "" {
		opts.Sync = SyncBatch
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, RecoveryReport{}, fmt.Errorf("create wal dir: %w", err)
	}
	l := &Log{dir: dir, opts: opts, syncWake: make(chan struct{}, 1), stop: make(chan struct{}), notify: make(chan struct{}, 1),
		done: make(chan struct{}), cp: &checkpointFile{dir: dir}}
	l.cond = sync.NewCond(&l.mu)
	rep, err := l.recover()
	if err != nil {
		return nil, rep, err
	}
	if err := l.openActive(); err != nil {
		return nil, rep, err
	}
	if _, err := l.Checkpoint(); err != nil { // primes the checkpoint sequence
		_ = l.active.Close() // reporting the checkpoint failure
		return nil, rep, err
	}
	go l.syncLoop()
	return l, rep, nil
}

func (l *Log) recover() (RecoveryReport, error) {
	var rep RecoveryReport
	seqs, err := listSegments(l.dir)
	if err != nil {
		return rep, err
	}
	for _, seq := range seqs {
		size, truncated, err := repairSegment(segmentPath(l.dir, seq))
		if err != nil {
			return rep, err
		}
		if truncated > 0 {
			rep.TruncatedBytes += truncated
			rep.TruncatedAt = append(rep.TruncatedAt, Pos{Seg: seq, Off: size})
		}
		l.segs = append(l.segs, segment{seq: seq, size: size})
		if l.opts.Budget != nil {
			l.opts.Budget.force(size)
		}
	}
	rep.Segments = len(l.segs)
	return rep, nil
}

// repairSegment scans a segment and truncates it at the first torn or corrupt record.
func repairSegment(path string) (size, truncated int64, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, filePerm)
	if err != nil {
		return 0, 0, fmt.Errorf("open segment: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat segment: %w", err)
	}
	valid, err := scanValid(f)
	if err != nil {
		return 0, 0, err
	}
	if valid == st.Size() {
		return valid, 0, nil
	}
	if err := f.Truncate(valid); err != nil {
		return 0, 0, fmt.Errorf("truncate torn tail: %w", err)
	}
	if err := f.Sync(); err != nil {
		return 0, 0, fmt.Errorf("sync truncated segment: %w", err)
	}
	return valid, st.Size() - valid, nil
}

// scanValid returns the length of the valid record prefix of r.
func scanValid(r io.Reader) (int64, error) {
	br := newRecordReader(r)
	var off int64
	for {
		n, _, err := br.next()
		switch {
		case err == nil:
			off += n
		case errors.Is(err, io.EOF), errors.Is(err, ErrCorrupt):
			return off, nil
		default:
			return off, err
		}
	}
}

func (l *Log) openActive() error {
	if len(l.segs) == 0 {
		return l.createSegment(1)
	}
	last := l.segs[len(l.segs)-1]
	f, err := os.OpenFile(segmentPath(l.dir, last.seq), os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("open active segment: %w", err)
	}
	if _, err := f.Seek(last.size, io.SeekStart); err != nil {
		_ = f.Close() // reporting the seek failure
		return fmt.Errorf("seek active segment: %w", err)
	}
	l.active = f
	l.written = Pos{Seg: last.seq, Off: last.size}
	l.durable = l.written
	return nil
}

// createSegment creates and activates segment seq; the caller holds l.mu or is opening.
func (l *Log) createSegment(seq uint64) error {
	f, err := os.OpenFile(segmentPath(l.dir, seq), os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("create segment: %w", err)
	}
	if err := syncDir(l.dir); err != nil {
		_ = f.Close() // reporting the directory sync failure
		return err
	}
	l.active = f
	l.segs = append(l.segs, segment{seq: seq})
	l.written = Pos{Seg: seq}
	l.durable = l.written
	return nil
}

// Append writes payload and, in batch sync mode, returns once an fsync covers it. It returns
// ErrFull when the size budget is exhausted (the caller answers with backpressure). ctx only
// bounds the wait for the fsync: its error means the record was written and becomes durable
// with the next group commit.
func (l *Log) Append(ctx context.Context, payload []byte) (Pos, error) {
	if len(payload) == 0 {
		return Pos{}, ErrEmpty
	}
	if len(payload) > MaxRecordBytes {
		return Pos{}, ErrTooBig
	}
	recLen := int64(headerBytes + len(payload))
	if l.opts.Budget != nil && !l.opts.Budget.reserve(recLen) {
		return Pos{}, ErrFull
	}
	end, err := l.write(payload, recLen)
	if err != nil {
		if l.opts.Budget != nil {
			l.opts.Budget.Release(recLen)
		}
		return Pos{}, err
	}
	if l.opts.Sync == SyncNone {
		l.markDurable(end)
		return end, nil
	}
	return end, l.waitDurable(ctx, end)
}

func (l *Log) write(payload []byte, recLen int64) (Pos, error) {
	buf := make([]byte, recLen)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(payload))) //nolint:gosec // bounded by MaxRecordBytes
	binary.LittleEndian.PutUint32(buf[4:8], crc32.Checksum(payload, castagnoli))
	copy(buf[headerBytes:], payload)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Pos{}, ErrClosed
	}
	if l.written.Off > 0 && l.written.Off+recLen > l.opts.SegmentBytes {
		if err := l.roll(); err != nil {
			return Pos{}, err
		}
	}
	if _, err := l.active.Write(buf); err != nil {
		return Pos{}, fmt.Errorf("write wal record: %w", err)
	}
	l.written.Off += recLen
	l.segs[len(l.segs)-1].size = l.written.Off
	return l.written, nil
}

// roll syncs and closes the active segment and starts the next one; the caller holds l.mu.
func (l *Log) roll() error {
	if err := l.syncActive(); err != nil {
		return err
	}
	if err := l.active.Close(); err != nil {
		return fmt.Errorf("close segment: %w", err)
	}
	l.durable = l.written
	l.cond.Broadcast()
	return l.createSegment(l.written.Seg + 1)
}

func (l *Log) syncActive() error {
	if l.opts.Sync == SyncNone {
		return nil
	}
	if err := l.active.Sync(); err != nil {
		return fmt.Errorf("fsync wal: %w", err)
	}
	return nil
}

func (l *Log) markDurable(end Pos) {
	l.mu.Lock()
	if l.durable.Less(end) {
		l.durable = end
	}
	l.mu.Unlock()
	l.signal()
}

func (l *Log) signal() {
	select {
	case l.notify <- struct{}{}:
	default:
	}
}

// waitDurable wakes the syncer and blocks until end is durable.
func (l *Log) waitDurable(ctx context.Context, end Pos) error {
	select {
	case l.syncWake <- struct{}{}:
	default:
	}
	stop := context.AfterFunc(ctx, func() {
		l.mu.Lock()
		l.cond.Broadcast()
		l.mu.Unlock()
	})
	defer stop()
	l.mu.Lock()
	defer l.mu.Unlock()
	for l.durable.Less(end) {
		switch {
		case l.syncErr != nil:
			return l.syncErr
		case l.closed:
			return ErrClosed
		case ctx.Err() != nil:
			return ctx.Err()
		}
		l.cond.Wait()
	}
	return nil
}

// syncLoop performs group commits: every fsync covers all records written before it.
func (l *Log) syncLoop() {
	defer close(l.done)
	for {
		select {
		case <-l.stop:
			return
		case <-l.syncWake:
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return
		}
		if l.durable.Less(l.written) {
			target := l.written
			if err := l.syncActive(); err != nil {
				l.syncErr = err
			} else {
				l.durable = target
			}
			l.cond.Broadcast()
		}
		l.mu.Unlock()
		l.signal()
	}
}

// Durable returns the end of the durable records.
func (l *Log) Durable() Pos {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.durable
}

// Notify is signalled whenever new records become durable.
func (l *Log) Notify() <-chan struct{} { return l.notify }

// Size returns the bytes of all segments on disk.
func (l *Log) Size() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var n int64
	for _, s := range l.segs {
		n += s.size
	}
	return n
}

// Segments returns the number of segment files.
func (l *Log) Segments() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.segs)
}

// Close syncs and closes the active segment and stops the syncer.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	err := l.syncActive()
	if cerr := l.active.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close segment: %w", cerr)
	}
	if err == nil {
		l.durable = l.written
	}
	l.cond.Broadcast()
	l.mu.Unlock()
	close(l.stop)
	<-l.done
	if l.reader != nil {
		l.reader.close()
	}
	return err
}

// Release deletes every segment that lies entirely before upTo (never the active one). The
// reader must have moved past them; it closes its handle when it advances, so files are closed
// before they are deleted.
func (l *Log) Release(upTo Pos) error {
	l.mu.Lock()
	var victims []segment
	keep := l.segs[:0:0]
	for i, s := range l.segs {
		if s.seq < upTo.Seg && i < len(l.segs)-1 {
			victims = append(victims, s)
			continue
		}
		keep = append(keep, s)
	}
	l.segs = keep
	l.mu.Unlock()
	for _, s := range victims {
		if l.reader != nil && l.reader.seq == s.seq {
			l.reader.close()
		}
		if err := os.Remove(segmentPath(l.dir, s.seq)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete segment: %w", err)
		}
		if l.opts.Budget != nil {
			l.opts.Budget.Release(s.size)
		}
	}
	return nil
}

func segmentPath(dir string, seq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("%016x%s", seq, segmentSuffix))
}

func listSegments(dir string) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list wal dir: %w", err)
	}
	var out []uint64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		seq, err := strconv.ParseUint(strings.TrimSuffix(name, segmentSuffix), 16, 64)
		if err != nil {
			continue
		}
		out = append(out, seq)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}
