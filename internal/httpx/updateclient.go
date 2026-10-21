// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxUpdateRedirects is the number of redirect hops the update client follows (SPEC §15.6).
const MaxUpdateRedirects = 3

// ErrHostNotAllowed is returned when the update client is asked to contact a URL that is not
// https or whose host is not on its allow-list (initial request or redirect).
var ErrHostNotAllowed = errors.New("host is not on the allow-list")

// ErrTooManyRedirects is returned when a redirect chain exceeds MaxUpdateRedirects hops.
var ErrTooManyRedirects = errors.New("too many redirects")

// ErrInvalidProxy is returned for an unusable proxy URL. Its messages never contain the URL, so
// proxy credentials cannot leak into logs.
var ErrInvalidProxy = errors.New("invalid proxy URL")

// UpdateClientOptions configures UpdateClient.
type UpdateClientOptions struct {
	// AllowedHosts are the exact host names (without port) the client may contact, for the
	// initial request and every redirect. It must not be empty.
	AllowedHosts []string
	// Proxy is an explicit forward proxy (see ParseProxyURL); nil connects directly.
	// Environment proxies (HTTPS_PROXY, …) are never used.
	Proxy *url.URL
	// Timeout bounds each request including redirects and reading the body.
	Timeout time.Duration
}

// UpdateClient returns the client used for update checks and downloads (SPEC §15.6, §17.7). It
// differs from SafeClient in two ways: it follows at most MaxUpdateRedirects redirects to https
// URLs whose host is on the allow-list, and it may use an explicit proxy. Without a proxy, private
// destinations are refused at dial time; with a proxy, only the operator-configured proxy is
// dialed and every request is tunnelled with CONNECT, while the allow-list still applies.
func UpdateClient(opts UpdateClientOptions) (*http.Client, error) {
	return newUpdateClient(opts, nil, false)
}

// newUpdateClient is UpdateClient with a TLS configuration and, for tests only, the ability to
// dial private addresses directly.
func newUpdateClient(opts UpdateClientOptions, tlsCfg *tls.Config, allowPrivate bool) (*http.Client, error) {
	allowed, err := newHostAllowList(opts.AllowedHosts)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: outboundTLSTimeout,
		MaxIdleConns:        outboundIdleConns,
		IdleConnTimeout:     time.Minute,
		ForceAttemptHTTP2:   true,
	}
	if opts.Proxy != nil {
		if err := checkProxyURL(opts.Proxy); err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(opts.Proxy)
		// With a fixed proxy the transport only ever dials the proxy, which the operator
		// configured and which may legitimately live on a private network.
		allowPrivate = true
	}
	dialer := &net.Dialer{Timeout: outboundDialTimeout, Control: dialControl(allowPrivate)}
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: &allowListTransport{next: transport, allowed: allowed},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxUpdateRedirects {
				return fmt.Errorf("%w: more than %d", ErrTooManyRedirects, MaxUpdateRedirects)
			}
			return allowed.check(req.URL)
		},
	}, nil
}

// hostAllowList is a set of lower-case host names.
type hostAllowList map[string]bool

func newHostAllowList(hosts []string) (hostAllowList, error) {
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%w: empty allow-list", ErrHostNotAllowed)
	}
	out := hostAllowList{}
	for _, h := range hosts {
		out[strings.ToLower(h)] = true
	}
	return out, nil
}

// check accepts https URLs without credentials whose host is on the list.
func (l hostAllowList) check(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrHostNotAllowed, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in URL", ErrHostNotAllowed)
	}
	if !l[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("%w: %s", ErrHostNotAllowed, u.Hostname())
	}
	return nil
}

// allowListTransport refuses requests to URLs outside the allow-list before anything is dialed.
type allowListTransport struct {
	next    *http.Transport
	allowed hostAllowList
}

// RoundTrip implements http.RoundTripper.
func (t *allowListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.allowed.check(req.URL); err != nil {
		if req.Body != nil {
			_ = req.Body.Close() // RoundTrip must close the body even when it fails
		}
		return nil, err
	}
	return t.next.RoundTrip(req)
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the transport.
func (t *allowListTransport) CloseIdleConnections() { t.next.CloseIdleConnections() }

// ParseProxyURL validates an explicit proxy URL: http or https, a host with an optional port,
// optional user:password, nothing else. An empty string means no proxy (nil, nil).
func ParseProxyURL(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot be parsed", ErrInvalidProxy)
	}
	if err := checkProxyURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func checkProxyURL(u *url.URL) error {
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("%w: scheme must be http or https", ErrInvalidProxy)
	case u.Hostname() == "":
		return fmt.Errorf("%w: host is missing", ErrInvalidProxy)
	case u.Path != "" && u.Path != "/", u.RawQuery != "", u.Fragment != "", u.Opaque != "":
		return fmt.Errorf("%w: only scheme, credentials, host and port are allowed", ErrInvalidProxy)
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > maxProxyPort {
			return fmt.Errorf("%w: invalid port", ErrInvalidProxy)
		}
	}
	return nil
}

// maxProxyPort is the highest TCP port.
const maxProxyPort = 65535

// ProxyDisplay returns the proxy as scheme://host[:port] for logs, without credentials.
func ProxyDisplay(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
