// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecurityHeaders(t *testing.T) {
	for _, hsts := range []bool{false, true} {
		h := SecurityHeaders(hsts)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		want := map[string]string{
			"Content-Security-Policy":      "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
			"X-Frame-Options":              "DENY",
		}
		for k, v := range want {
			if w.Header().Get(k) != v {
				t.Errorf("%s = %q", k, w.Header().Get(k))
			}
		}
		if (w.Header().Get("Strict-Transport-Security") != "") != hsts {
			t.Errorf("hsts=%v header %q", hsts, w.Header().Get("Strict-Transport-Security"))
		}
	}
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = RequestIDFrom(r.Context()) }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if len(seen) != 24 || w.Header().Get(RequestIDHeader) != seen {
		t.Errorf("generated %q", seen)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set(RequestIDHeader, "abc-123")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen != "abc-123" {
		t.Errorf("kept %q", seen)
	}
	r.Header.Set(RequestIDHeader, "bad id\nwith newline")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen == "bad id\nwith newline" {
		t.Error("unsafe id accepted")
	}
	if RequestIDFrom(context.Background()) != "" {
		t.Error("empty ctx")
	}
}

func TestAccessLogRedactsAndRecovers(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/panic" {
			panic("boom")
		}
		w.(http.Flusher).Flush()
		_, _ = w.Write([]byte("hello"))
	}), RequestID, AccessLog(logger, func(*http.Request) string { return "1.2.3.4" }), Recover(logger))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/events/raw?apiKey=tmk_secretsecret&clef=1", nil))
	out := buf.String()
	if strings.Contains(out, "tmk_secretsecret") || !strings.Contains(out, "REDACTED") || !strings.Contains(out, `"status":200`) ||
		!strings.Contains(out, `"bytes":5`) || !strings.Contains(out, `"ip":"1.2.3.4"`) {
		t.Errorf("log %s", out)
	}
	buf.Reset()
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/panic", nil))
	if w.Code != 500 || !strings.Contains(w.Body.String(), `"code":"internal"`) || strings.Contains(w.Body.String(), "boom") {
		t.Errorf("panic response %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(buf.String(), "panic in handler") || !strings.Contains(buf.String(), `"status":500`) {
		t.Errorf("panic log %s", buf.String())
	}
	if RedactQuery(url.Values{}) != "" {
		t.Error("empty query")
	}
}

func TestRecoverRepanicsAbort(t *testing.T) {
	h := Recover(slog.New(slog.NewTextHandler(io.Discard, nil)))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if r := recover(); r != http.ErrAbortHandler { //nolint:errorlint // sentinel comparison
			t.Errorf("recovered %v", r)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestStatusWriterHijack(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := sw.Hijack(); err == nil {
		t.Error("recorder cannot hijack")
	}
	if sw.Unwrap() == nil {
		t.Error("unwrap")
	}
	sw.WriteHeader(204)
	sw.WriteHeader(500)
	if sw.status != 204 {
		t.Error("first status wins")
	}
	srv := httptest.NewServer(Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		c, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"))
			_ = c.Close()
		}
	}), AccessLog(slog.New(slog.NewTextHandler(io.Discard, nil)), func(*http.Request) string { return "" })))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "ok" {
		t.Errorf("hijacked body %q", body)
	}
}

func TestMaxBody(t *testing.T) {
	h := MaxBody(4)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(413)
		}
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("123456")))
	if w.Code != 413 {
		t.Errorf("%d", w.Code)
	}
}

func TestRealIP(t *testing.T) {
	ri := NewRealIP([]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")})
	tests := []struct {
		remote, xff, want string
	}{
		{"203.0.113.5:1234", "1.1.1.1", "203.0.113.5"},
		{"10.0.0.2:1234", "198.51.100.7", "198.51.100.7"},
		{"10.0.0.2:1234", "6.6.6.6, 198.51.100.7, 10.0.0.9", "198.51.100.7"},
		{"10.0.0.2:1234", "", "10.0.0.2"},
		{"10.0.0.2:1234", "garbage", "10.0.0.2"},
		{"10.0.0.2:1234", "10.1.1.1, 10.2.2.2", "10.1.1.1"},
		{"[::ffff:10.0.0.2]:80", "198.51.100.8", "198.51.100.8"},
		{"[2001:db8::1]:443", "", "2001:db8::1"},
		{"bogus", "", ""},
		{"192.0.2.1", "", "192.0.2.1"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := ri.ClientIPString(r); got != tt.want {
			t.Errorf("%s / %q: %q want %q", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestServerAndTLS(t *testing.T) {
	s := NewServer(":0", http.NotFoundHandler(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s.ReadHeaderTimeout != 10*time.Second || s.ReadTimeout != 60*time.Second || s.WriteTimeout != 120*time.Second ||
		s.IdleTimeout != 120*time.Second || s.MaxHeaderBytes != 65536 {
		t.Errorf("%+v", s)
	}
	_, _ = s.ErrorLog.Writer().Write([]byte("tls: handshake error"))
	if _, err := LoadTLS("missing.pem", "missing.key"); err == nil {
		t.Error("missing cert accepted")
	}
	dir := t.TempDir()
	cert, key := writeSelfSigned(t, dir)
	cfg, err := LoadTLS(cert, key)
	if err != nil || cfg.MinVersion != 0x0303 {
		t.Errorf("%v %v", cfg, err)
	}
}

func writeSelfSigned(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return certPath, keyPath
}

func TestAddressAllowed(t *testing.T) {
	blocked := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255", "::1", "::", "fc00::1", "fd12::1", "fe80::1",
		"ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::a00:1", "198.18.0.1"}
	allowed := []string{"1.1.1.1", "8.8.8.8", "172.32.0.1", "2606:4700:4700::1111", "203.0.113.9"}
	for _, s := range blocked {
		if AddressAllowed(netip.MustParseAddr(s), false) {
			t.Errorf("%s allowed", s)
		}
		if !AddressAllowed(netip.MustParseAddr(s), true) {
			t.Errorf("%s blocked with allowPrivate", s)
		}
	}
	for _, s := range allowed {
		if !AddressAllowed(netip.MustParseAddr(s), false) {
			t.Errorf("%s blocked", s)
		}
	}
	if AddressAllowed(netip.Addr{}, true) || AddressAllowed(netip.Addr{}, false) {
		t.Error("invalid address allowed")
	}
}

func TestSafeClientBlocksAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := SafeClient(false, 2*time.Second)
	_, err := c.Get(srv.URL) // 127.0.0.1
	if !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("loopback not blocked: %v", err)
	}
	// "localhost" resolves at dial time and must be blocked too (DNS-based bypass).
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	if _, err := c.Get("http://localhost:" + port); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("localhost not blocked: %v", err)
	}
	open := SafeClient(true, 2*time.Second)
	resp, err := open.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("redirect followed: %d", resp.StatusCode)
	}
	if err := dialControl(false)("tcp", "not-an-addr", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("unparseable address: %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	tests := []struct {
		raw     string
		private bool
		ok      bool
	}{
		{"https://hooks.slack.com/services/x", false, true},
		{"http://hooks.example.com/x", false, false},
		{"http://hooks.example.com/x", true, true},
		{"https://user:pw@example.com", false, false},
		{"https://127.0.0.1/x", false, false},
		{"https://[::1]/x", false, false},
		{"https://127.0.0.1/x", true, true},
		{"ftp://example.com", false, false},
		{"/relative", false, false},
		{"%zz", false, false},
	}
	for _, tt := range tests {
		_, err := ValidateURL(tt.raw, tt.private)
		if (err == nil) != tt.ok {
			t.Errorf("%s private=%v: %v", tt.raw, tt.private, err)
		}
	}
}
