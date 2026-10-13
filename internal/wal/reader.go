// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

const readBufferBytes = 1 << 20

// recordReader decodes records from a stream: len uint32 | crc32c uint32 | payload.
type recordReader struct {
	r   *bufio.Reader
	hdr [headerBytes]byte
}

func newRecordReader(r io.Reader) *recordReader {
	return &recordReader{r: bufio.NewReaderSize(r, readBufferBytes)}
}

// next returns the record length on disk and its payload. A short header at the end is io.EOF;
// a short payload, an oversize length or a CRC mismatch is ErrCorrupt.
func (rr *recordReader) next() (int64, []byte, error) {
	if _, err := io.ReadFull(rr.r, rr.hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, fmt.Errorf("%w: torn header", ErrCorrupt)
		}
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(rr.hdr[0:4])
	sum := binary.LittleEndian.Uint32(rr.hdr[4:8])
	if n == 0 || n > MaxRecordBytes { // empty records are never written (zero-filled tails)
		return 0, nil, fmt.Errorf("%w: length %d", ErrCorrupt, n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(rr.r, payload); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, fmt.Errorf("%w: torn payload", ErrCorrupt)
		}
		return 0, nil, err
	}
	if crc32.Checksum(payload, castagnoli) != sum {
		return 0, nil, fmt.Errorf("%w: checksum mismatch", ErrCorrupt)
	}
	return int64(headerBytes) + int64(n), payload, nil
}

// segmentReader is the consumer's open handle on one segment. It never reads past limit (the
// durable end when it was opened), so its buffer cannot hold bytes of a write still in progress.
type segmentReader struct {
	seq   uint64
	off   int64
	limit int64
	f     *os.File
	rr    *recordReader
}

func (s *segmentReader) close() {
	if s.f != nil {
		_ = s.f.Close() // read-only handle
		s.f = nil
	}
}

// ReadFrom returns durable records starting at from, at most maxRecords and roughly maxBytes
// (at least one record when available). It crosses segment boundaries and closes the handle of
// a finished segment before moving on.
func (l *Log) ReadFrom(from Pos, maxRecords int, maxBytes int64) ([]Record, error) {
	durable := l.Durable()
	var out []Record
	var size int64
	pos := from
	for len(out) < maxRecords && (len(out) == 0 || size < maxBytes) && pos.Less(durable) {
		rec, next, err := l.readOne(pos, durable)
		if err != nil {
			return out, err
		}
		if rec == nil {
			pos = next
			continue
		}
		out = append(out, *rec)
		size += int64(len(rec.Payload))
		pos = rec.End
	}
	return out, nil
}

// readOne reads the record at pos; at the end of a closed segment it returns (nil, start of the
// next segment).
func (l *Log) readOne(pos, durable Pos) (*Record, Pos, error) {
	if pos.Seg < durable.Seg && pos.Off >= l.segmentSize(pos.Seg) {
		return nil, Pos{Seg: l.nextSegment(pos.Seg)}, nil
	}
	limit := durable.Off
	if pos.Seg < durable.Seg {
		limit = l.segmentSize(pos.Seg)
	}
	r, err := l.readerAt(pos, limit)
	if err != nil {
		return nil, pos, err
	}
	n, payload, err := r.rr.next()
	if err != nil {
		r.close()
		l.reader = nil
		return nil, pos, fmt.Errorf("read wal at %d:%d: %w", pos.Seg, pos.Off, err)
	}
	r.off += n
	return &Record{Pos: pos, End: Pos{Seg: pos.Seg, Off: r.off}, Payload: payload}, Pos{}, nil
}

// readerAt returns a reader positioned at pos that stops at limit, reusing the cached handle
// when it covers the same range.
func (l *Log) readerAt(pos Pos, limit int64) (*segmentReader, error) {
	if r := l.reader; r != nil && r.seq == pos.Seg && r.off == pos.Off && r.limit == limit && r.f != nil {
		return r, nil
	}
	if l.reader != nil {
		l.reader.close()
	}
	f, err := os.Open(segmentPath(l.dir, pos.Seg))
	if err != nil {
		return nil, fmt.Errorf("open segment for reading: %w", err)
	}
	section := io.NewSectionReader(f, pos.Off, limit-pos.Off)
	l.reader = &segmentReader{seq: pos.Seg, off: pos.Off, limit: limit, f: f, rr: newRecordReader(section)}
	return l.reader, nil
}

func (l *Log) segmentSize(seq uint64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.segs {
		if s.seq == seq {
			return s.size
		}
	}
	return 0
}

// nextSegment returns the first segment after seq (segments may be missing after a release).
func (l *Log) nextSegment(seq uint64) uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.segs {
		if s.seq > seq {
			return s.seq
		}
	}
	return seq + 1
}

// SkipCorrupt returns the position to continue from after a corrupt record at pos: the start
// of the next segment (the rest of a damaged segment cannot be framed reliably).
func (l *Log) SkipCorrupt(pos Pos) Pos {
	if l.reader != nil {
		l.reader.close()
		l.reader = nil
	}
	return Pos{Seg: l.nextSegment(pos.Seg)}
}
