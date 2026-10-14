// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/httpx"
)

// maxResponseBytes bounds one source response (a page of up to 10,000 documents).
const maxResponseBytes = 512 << 20

// maxErrorBody is how much of an error response is kept for the job's last error.
const maxErrorBody = 512

// newSourceClient returns the SSRF-safe client for import sources: private addresses allowed
// (sources are usually internal), redirects disabled, TLS verification always on, plus the
// optional extra CA bundle (SPEC §9.9).
func newSourceClient(caPEM string) (*http.Client, error) {
	var cfg *tls.Config
	if caPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, fmt.Errorf("%w: invalid CA certificate", ErrPermanent)
		}
		cfg = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	}
	return httpx.SafeClientTLS(true, sourceRequestTimeout, cfg), nil
}

// StatusError is a non-2xx response of a source.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("source returned HTTP %d: %s", e.Status, e.Body)
}

// Is makes client errors (4xx except 408 and 429) match ErrPermanent.
func (e *StatusError) Is(target error) bool {
	return target == ErrPermanent && e.Status >= 400 && e.Status < 500 &&
		e.Status != http.StatusRequestTimeout && e.Status != http.StatusTooManyRequests
}

// requester sends JSON requests with fixed headers.
type requester struct {
	client  *http.Client
	base    string
	headers http.Header
}

func newRequester(base string, a Auth, caPEM string, extra http.Header) (*requester, error) {
	client, err := newSourceClient(caPEM)
	if err != nil {
		return nil, err
	}
	h := http.Header{"Accept": {"application/json"}}
	for k, v := range extra {
		h[k] = v
	}
	switch a.Type {
	case AuthBasic:
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.Username+":"+a.Password)))
	case AuthAPIKey:
		h.Set("Authorization", "ApiKey "+a.APIKey)
	case AuthBearer:
		h.Set("Authorization", "Bearer "+a.Token)
	}
	return &requester{client: client, base: strings.TrimRight(base, "/"), headers: h}, nil
}

// do sends method path (relative to the base URL) with an optional JSON body and decodes a
// JSON response into out (nil = discard). Non-2xx responses return a *StatusError.
func (r *requester) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+path, rd)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPermanent, err)
	}
	for k, v := range r.headers {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }() // read-only response
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)) // best effort detail
		return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		return err
	}
	return decodeLimited(resp.Body, out)
}

func decodeLimited(rd io.Reader, out any) error {
	lr := &io.LimitedReader{R: rd, N: maxResponseBytes + 1}
	dec := json.NewDecoder(lr)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		if lr.N <= 0 {
			return fmt.Errorf("%w: response larger than %d bytes", ErrPermanent, maxResponseBytes)
		}
		return fmt.Errorf("%w: decode response: %w", ErrPermanent, err)
	}
	return nil
}

// isStatus reports whether err is a StatusError with one of the codes.
func isStatus(err error, codes ...int) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	for _, c := range codes {
		if se.Status == c {
			return true
		}
	}
	return false
}
