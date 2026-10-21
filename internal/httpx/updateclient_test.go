// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const updateTestTimeout = 5 * time.Second

// redirectServer serves /hop/N, which redirects to /hop/N-1 until /hop/0 answers "ok".
// /to?u=<url> redirects to an arbitrary URL.
func redirectServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/to" {
			http.Redirect(w, r, r.URL.Query().Get("u"), http.StatusFound)
			return
		}
		n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/hop/"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if n == 0 {
			_, _ = io.WriteString(w, "ok")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serverTLS(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func get(t *testing.T, c *http.Client, u string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), updateTestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestUpdateClientRedirects(t *testing.T) {
	srv := redirectServer(t)
	c, err := newUpdateClient(UpdateClientOptions{AllowedHosts: []string{"127.0.0.1"}, Timeout: updateTestTimeout},
		serverTLS(srv), true)
	if err != nil {
		t.Fatal(err)
	}
	plain := strings.Replace(srv.URL, "https://", "http://", 1)
	tests := []struct {
		name string
		path string
		want error
	}{
		{"no redirect", "/hop/0", nil},
		{"max hops", fmt.Sprintf("/hop/%d", MaxUpdateRedirects), nil},
		{"too many hops", fmt.Sprintf("/hop/%d", MaxUpdateRedirects+1), ErrTooManyRedirects},
		{"off-list host", "/to?u=" + url.QueryEscape("https://evil.example/"), ErrHostNotAllowed},
		{"http scheme", "/to?u=" + url.QueryEscape(plain+"/hop/0"), ErrHostNotAllowed},
		{"credentials", "/to?u=" + url.QueryEscape("https://u:p@127.0.0.1/"), ErrHostNotAllowed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, err := get(t, c, srv.URL+tc.path)
			if tc.want == nil {
				if err != nil || body != "ok" {
					t.Fatalf("body %q err %v", body, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestUpdateClientInitialRequestChecked(t *testing.T) {
	var dialed atomic.Bool
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dialed.Store(true) }))
	defer srv.Close()
	c, err := newUpdateClient(UpdateClientOptions{AllowedHosts: []string{"api.github.com"}, Timeout: updateTestTimeout},
		serverTLS(srv), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, c, srv.URL); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	if dialed.Load() {
		t.Fatal("off-list host was contacted")
	}
}

func TestUpdateClientBlocksPrivateDestination(t *testing.T) {
	srv := redirectServer(t)
	c, err := UpdateClient(UpdateClientOptions{AllowedHosts: []string{"127.0.0.1"}, Timeout: updateTestTimeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, c, srv.URL+"/hop/0"); !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("err = %v, want ErrBlockedAddress", err)
	}
}

// connectProxy is a CONNECT-only forward proxy that requires the given Proxy-Authorization.
func connectProxy(t *testing.T, wantAuth string, used *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		used.Store(true)
		upstream, err := net.DialTimeout("tcp", r.Host, updateTestTimeout)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			_ = upstream.Close() // the test fails on the client side
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
		go func() { _, _ = io.Copy(upstream, conn); _ = upstream.Close() }()
		go func() { _, _ = io.Copy(conn, upstream); _ = conn.Close() }()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateClientProxy(t *testing.T) {
	target := redirectServer(t)
	var used atomic.Bool
	proxy := connectProxy(t, "Basic dXNlcjpzM2NyZXQ=", &used) // user:s3cret
	proxyURL, err := ParseProxyURL(strings.Replace(proxy.URL, "http://", "http://user:s3cret@", 1))
	if err != nil {
		t.Fatal(err)
	}
	// allowPrivate=false: the proxy (on loopback) may still be dialed because it is configured.
	c, err := newUpdateClient(UpdateClientOptions{AllowedHosts: []string{"127.0.0.1"}, Proxy: proxyURL,
		Timeout: updateTestTimeout}, serverTLS(target), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	body, err := get(t, c, target.URL+"/hop/2")
	if err != nil || body != "ok" {
		t.Fatalf("body %q err %v", body, err)
	}
	if !used.Load() {
		t.Fatal("request did not go through the proxy")
	}
	if _, err := get(t, c, target.URL+"/to?u="+url.QueryEscape("https://evil.example/")); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("off-list redirect through proxy: err = %v", err)
	}
}

func TestUpdateClientProxyErrorsHideCredentials(t *testing.T) {
	var used atomic.Bool
	wrongAuth := connectProxy(t, "Basic other", &used)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	for name, base := range map[string]string{"auth refused": wrongAuth.URL, "proxy down": closedURL} {
		t.Run(name, func(t *testing.T) {
			proxyURL, err := ParseProxyURL(strings.Replace(base, "http://", "http://user:s3cret@", 1))
			if err != nil {
				t.Fatal(err)
			}
			c, err := UpdateClient(UpdateClientOptions{AllowedHosts: []string{"api.github.com"}, Proxy: proxyURL,
				Timeout: updateTestTimeout})
			if err != nil {
				t.Fatal(err)
			}
			_, err = get(t, c, "https://api.github.com/")
			if err == nil {
				t.Fatal("request succeeded")
			}
			if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "user:") {
				t.Fatalf("error leaks credentials: %v", err)
			}
		})
	}
}

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		raw string
		ok  bool
	}{
		{"", true},
		{"http://proxy.internal:3128", true},
		{"https://user:pa55@proxy.internal", true},
		{"http://10.0.0.1:8080/", true},
		{"socks5://proxy:1080", false},
		{"proxy:3128", false},
		{"http://", false},
		{"http://proxy/path", false},
		{"http://proxy?x=1", false},
		{"http://proxy:99999", false},
		{"http://user:s3cret@proxy:bad", false},
		{"http://user:s3cret@[::1", false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := ParseProxyURL(tc.raw)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v", err)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidProxy) || strings.Contains(err.Error(), "s3cret") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if (tc.raw == "") != (u == nil) {
				t.Fatalf("url = %v", u)
			}
		})
	}
}

func TestProxyDisplay(t *testing.T) {
	u, err := ParseProxyURL("https://user:s3cret@proxy.internal:3128")
	if err != nil {
		t.Fatal(err)
	}
	if got := ProxyDisplay(u); got != "https://proxy.internal:3128" {
		t.Fatalf("got %q", got)
	}
	if ProxyDisplay(nil) != "" {
		t.Fatal("nil proxy")
	}
}

func TestUpdateClientOptionsValidated(t *testing.T) {
	if _, err := UpdateClient(UpdateClientOptions{}); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("empty allow-list: err = %v", err)
	}
	bad := &url.URL{Scheme: "socks5", Host: "proxy:1080"}
	if _, err := UpdateClient(UpdateClientOptions{AllowedHosts: []string{"x"}, Proxy: bad}); !errors.Is(err, ErrInvalidProxy) {
		t.Fatalf("bad proxy: err = %v", err)
	}
}
