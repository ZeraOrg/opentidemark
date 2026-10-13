// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"
)

// snappyStreamMagic starts every framed snappy stream (stream identifier chunk).
var snappyStreamMagic = []byte("\xff\x06\x00\x00sNaPpY")

// ReadBody reads the request body enforcing the compressed limit and, after decoding the
// Content-Encoding (gzip, deflate, zstd; snappy only when allowSnappy), the decompressed limit.
func ReadBody(r *http.Request, lim Limits, allowSnappy bool) ([]byte, error) {
	if r.ContentLength > lim.MaxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	raw, err := readLimited(r.Body, lim.MaxBodyBytes)
	if err != nil {
		return nil, err
	}
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	return Decode(raw, enc, lim.MaxDecompressedBytes, allowSnappy)
}

// Decode decompresses data per the encoding name with a decompressed-size limit.
func Decode(data []byte, encoding string, maxBytes int64, allowSnappy bool) ([]byte, error) {
	switch encoding {
	case "", "identity":
		if int64(len(data)) > maxBytes {
			return nil, ErrBodyTooLarge
		}
		return data, nil
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid gzip: %w", ErrBadRequest, err)
		}
		defer zr.Close()
		return readDecoded(zr, maxBytes)
	case "deflate":
		return decodeDeflate(data, maxBytes)
	case "zstd":
		zr, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderMaxMemory(uint64(max(maxBytes, 0))), zstd.WithDecoderConcurrency(1)) //nolint:gosec // maxBytes is clamped to >= 0
		if err != nil {
			return nil, fmt.Errorf("%w: invalid zstd: %w", ErrBadRequest, err)
		}
		defer zr.Close()
		out, err := readDecoded(zr, maxBytes)
		if errors.Is(err, zstd.ErrWindowSizeExceeded) || errors.Is(err, zstd.ErrDecoderSizeExceeded) {
			return nil, ErrBodyTooLarge
		}
		return out, err
	case "snappy", "x-snappy-framed":
		if !allowSnappy {
			return nil, ErrUnsupportedEncoding
		}
		return decodeSnappy(data, maxBytes)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedEncoding, encoding)
	}
}

// decodeDeflate accepts zlib-wrapped (RFC 1950, the HTTP meaning) and raw deflate data.
func decodeDeflate(data []byte, maxBytes int64) ([]byte, error) {
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		defer zr.Close()
		return readDecoded(zr, maxBytes)
	}
	fr := flate.NewReader(bytes.NewReader(data))
	defer fr.Close()
	return readDecoded(fr, maxBytes)
}

// decodeSnappy handles framed streams and block format (checking the declared size first).
func decodeSnappy(data []byte, maxBytes int64) ([]byte, error) {
	if bytes.HasPrefix(data, snappyStreamMagic) {
		return readDecoded(snappy.NewReader(bytes.NewReader(data)), maxBytes)
	}
	n, err := snappy.DecodedLen(data)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid snappy: %w", ErrBadRequest, err)
	}
	if int64(n) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	out, err := snappy.Decode(nil, data)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid snappy: %w", ErrBadRequest, err)
	}
	return out, nil
}

func readLimited(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, ErrBodyTooLarge
		}
		return nil, fmt.Errorf("%w: read body: %w", ErrBadRequest, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	return data, nil
}

func readDecoded(r io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if int64(len(data)) > maxBytes {
		return nil, ErrBodyTooLarge
	}
	if err != nil {
		return nil, fmt.Errorf("%w: decompress: %w", ErrBadRequest, err)
	}
	return data, nil
}
