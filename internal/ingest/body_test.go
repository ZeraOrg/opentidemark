// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
)

func compress(t *testing.T, enc string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.WriteCloser
	switch enc {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w = zlib.NewWriter(&buf)
	case "rawdeflate":
		fw, _ := flate.NewWriter(&buf, flate.BestSpeed)
		w = fw
	case "zstd":
		zw, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		w = zw
	case "snappy-framed":
		w = snappy.NewBufferedWriter(&buf)
	case "snappy-block":
		return snappy.Encode(nil, data)
	default:
		return data
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadBodyEncodings(t *testing.T) {
	payload := []byte(strings.Repeat(`{"message":"hello"}`+"\n", 100))
	lim := Limits{MaxBodyBytes: 1 << 16, MaxDecompressedBytes: 1 << 16}
	tests := []struct {
		header, kind string
		snappyOK     bool
		want         error
	}{
		{"", "", false, nil},
		{"identity", "", false, nil},
		{"gzip", "gzip", false, nil},
		{"x-gzip", "gzip", false, nil},
		{"deflate", "deflate", false, nil},
		{"deflate", "rawdeflate", false, nil},
		{"zstd", "zstd", false, nil},
		{"snappy", "snappy-framed", true, nil},
		{"snappy", "snappy-block", true, nil},
		{"x-snappy-framed", "snappy-framed", true, nil},
		{"snappy", "snappy-block", false, ErrUnsupportedEncoding},
		{"br", "", false, ErrUnsupportedEncoding},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/", bytes.NewReader(compress(t, tt.kind, payload)))
		if tt.header != "" {
			r.Header.Set("Content-Encoding", tt.header)
		}
		got, err := ReadBody(r, lim, tt.snappyOK)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s/%s: err %v want %v", tt.header, tt.kind, err, tt.want)
			continue
		}
		if err == nil && !bytes.Equal(got, payload) {
			t.Errorf("%s/%s: payload mismatch", tt.header, tt.kind)
		}
	}
}

func TestDecompressionBombs(t *testing.T) {
	bomb := bytes.Repeat([]byte("A"), 2<<20) // 2 MiB of one byte compresses to a few KiB
	lim := Limits{MaxBodyBytes: 1 << 20, MaxDecompressedBytes: 1 << 20}
	for _, kind := range []string{"gzip", "deflate", "zstd", "snappy-framed", "snappy-block"} {
		enc := map[string]string{"gzip": "gzip", "deflate": "deflate", "zstd": "zstd", "snappy-framed": "snappy", "snappy-block": "snappy"}[kind]
		data := compress(t, kind, bomb)
		if int64(len(data)) > lim.MaxBodyBytes {
			t.Fatalf("%s: compressed size %d exceeds the body limit; test is invalid", kind, len(data))
		}
		r := httptest.NewRequest("POST", "/", bytes.NewReader(data))
		r.Header.Set("Content-Encoding", enc)
		if _, err := ReadBody(r, lim, true); !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("%s bomb: %v", kind, err)
		}
	}
}

func TestBodyLimits(t *testing.T) {
	lim := Limits{MaxBodyBytes: 10, MaxDecompressedBytes: 100}
	r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 11)))
	if _, err := ReadBody(r, lim, false); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("content-length: %v", err)
	}
	r = httptest.NewRequest("POST", "/", io.NopCloser(strings.NewReader(strings.Repeat("x", 11))))
	r.ContentLength = -1
	if _, err := ReadBody(r, lim, false); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("streamed: %v", err)
	}
	if _, err := Decode([]byte(strings.Repeat("x", 11)), "", 10, false); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("identity over decompressed limit: %v", err)
	}
	for enc, data := range map[string]string{"gzip": "garbage!", "zstd": "garbage!", "snappy": "\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff\xff"} {
		if _, err := Decode([]byte(data), enc, 100, true); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s garbage: %v", enc, err)
		}
	}
	if _, err := Decode(snappy.Encode(nil, []byte("hello"))[:3], "snappy", 100, true); !errors.Is(err, ErrBadRequest) {
		t.Errorf("truncated snappy block: %v", err)
	}
	if _, err := Decode([]byte("\xff\x06\x00\x00sNaPpY\x01\x00"), "snappy", 100, true); !errors.Is(err, ErrBadRequest) {
		t.Errorf("broken framed snappy: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadBodyIOError(t *testing.T) {
	r := httptest.NewRequest("POST", "/", errReader{})
	if _, err := ReadBody(r, Limits{MaxBodyBytes: 10, MaxDecompressedBytes: 10}, false); !errors.Is(err, ErrBadRequest) {
		t.Errorf("io error: %v", err)
	}
}
