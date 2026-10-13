// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ZeraOrg/tidemark/internal/ingest/seq"
	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// CLEF file reading limits.
const (
	clefPageLines   = 5000
	maxCLEFLineSize = 16 << 20
	readBufferSize  = 256 << 10
)

// gzipMagic starts every gzip stream.
var gzipMagic = []byte{0x1f, 0x8b}

// clefCheckpoint is the position in the file list: file index, bytes consumed from the
// (decompressed) content and lines read.
type clefCheckpoint struct {
	File   int   `json:"file"`
	Offset int64 `json:"offset"`
	Line   int64 `json:"line"`
}

// clefSource reads CLEF/NDJSON files (optionally gzip-compressed) line by line (SPEC §9.9).
type clefSource struct {
	cfg   CLEFConfig
	file  *os.File
	rd    *bufio.Reader
	pos   clefCheckpoint // position of rd
	valid bool           // rd is positioned at pos
}

func newCLEFSource(cfg CLEFConfig) *clefSource { return &clefSource{cfg: cfg} }

// Next implements Source.
func (s *clefSource) Next(_ context.Context, raw json.RawMessage) (Page, error) {
	var cp clefCheckpoint
	if err := decodeCheckpoint(raw, &cp); err != nil {
		return Page{}, err
	}
	if cp.File >= len(s.cfg.Files) {
		return clefPage(Page{Done: true}, cp)
	}
	if !s.valid || s.pos != cp {
		if err := s.seek(cp); err != nil {
			return Page{}, err
		}
	}
	p := Page{}
	for len(p.Events)+int(p.Skipped) < clefPageLines {
		line, err := s.readLine()
		if errors.Is(err, io.EOF) {
			s.closeFile()
			s.pos = clefCheckpoint{File: s.pos.File + 1}
			s.valid = false
			break
		}
		if err != nil {
			s.valid = false
			return Page{}, err
		}
		s.addLine(&p, line)
	}
	p.Done = s.pos.File >= len(s.cfg.Files)
	return clefPage(p, s.pos)
}

func clefPage(p Page, cp clefCheckpoint) (Page, error) {
	raw, err := json.Marshal(cp)
	if err != nil {
		return Page{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	p.Checkpoint, p.Key = raw, string(raw) // pages end at deterministic line boundaries
	return p, nil
}

func (s *clefSource) addLine(p *Page, line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	if len(line) > maxCLEFLineSize {
		p.Skipped++
		p.LastError = fmt.Sprintf("%s line %d: line longer than %d bytes", s.name(), s.pos.Line, maxCLEFLineSize)
		return
	}
	obj, err := jsonx.DecodeObject(line, jsonx.MaxDepth)
	if err != nil {
		p.Skipped++
		p.LastError = fmt.Sprintf("%s line %d: %v", s.name(), s.pos.Line, err)
		return
	}
	ev := seq.EventFromCLEF(obj)
	if s.cfg.Stream != "" {
		ev.Stream = s.cfg.Stream
	} else if ev.Stream != "" {
		ev.Stream = model.NormalizeStream(ev.Stream)
	}
	p.Events = append(p.Events, ev)
}

func (s *clefSource) name() string { return s.cfg.Files[s.pos.File].Name }

// readLine returns the next line (without the size limit applied: an over-long line is
// consumed in full and reported by addLine).
func (s *clefSource) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := s.rd.ReadSlice('\n')
		s.pos.Offset += int64(len(chunk))
		if len(buf) <= maxCLEFLineSize {
			buf = append(buf, chunk...)
		}
		switch {
		case err == nil:
			s.pos.Line++
			return buf, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			s.pos.Line++
			return buf, nil
		default:
			return nil, err
		}
	}
}

// seek opens the checkpoint's file and skips the bytes already imported.
func (s *clefSource) seek(cp clefCheckpoint) error {
	s.closeFile()
	f, err := os.Open(s.cfg.Files[cp.File].Path)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrPermanent, s.cfg.Files[cp.File].Name, err)
	}
	s.file = f
	content, err := contentReader(f)
	if err != nil {
		return err
	}
	s.rd = bufio.NewReaderSize(content, readBufferSize)
	if _, err := io.CopyN(io.Discard, s.rd, cp.Offset); err != nil {
		return fmt.Errorf("%w: resume %s at byte %d: %w", ErrPermanent, s.cfg.Files[cp.File].Name, cp.Offset, err)
	}
	s.pos, s.valid = cp, true
	return nil
}

// contentReader transparently decompresses gzip files (detected by their magic bytes).
func contentReader(f *os.File) (io.Reader, error) {
	br := bufio.NewReaderSize(f, readBufferSize)
	head, err := br.Peek(len(gzipMagic))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read %s: %w", f.Name(), err)
	}
	if !bytes.Equal(head, gzipMagic) {
		return br, nil
	}
	zr, err := gzip.NewReader(br)
	if err != nil {
		return nil, fmt.Errorf("%w: gzip: %w", ErrPermanent, err)
	}
	return zr, nil
}

func (s *clefSource) closeFile() {
	if s.file != nil {
		_ = s.file.Close() // read-only file
		s.file, s.rd = nil, nil
	}
}

// Close implements Source.
func (s *clefSource) Close(context.Context, bool) error {
	s.closeFile()
	return nil
}
