// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrBlockedAddress is returned when an outbound request targets a forbidden address.
var ErrBlockedAddress = errors.New("destination address is not allowed")

// ErrBlockedURL is returned for URLs that may not be used for outbound requests.
var ErrBlockedURL = errors.New("URL is not allowed")

// blockedPrefixes are rejected unless private destinations are allowed (SPEC §15.6).
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),         // "this" network
	netip.MustParsePrefix("10.0.0.0/8"),        // RFC 1918
	netip.MustParsePrefix("100.64.0.0/10"),     // CGNAT
	netip.MustParsePrefix("127.0.0.0/8"),       // loopback
	netip.MustParsePrefix("169.254.0.0/16"),    // link-local incl. 169.254.169.254 metadata
	netip.MustParsePrefix("172.16.0.0/12"),     // RFC 1918
	netip.MustParsePrefix("192.0.0.0/24"),      // IETF protocol assignments
	netip.MustParsePrefix("192.168.0.0/16"),    // RFC 1918
	netip.MustParsePrefix("198.18.0.0/15"),     // benchmarking
	netip.MustParsePrefix("224.0.0.0/4"),       // multicast
	netip.MustParsePrefix("240.0.0.0/4"),       // reserved + broadcast
	netip.MustParsePrefix("::/128"),            // unspecified
	netip.MustParsePrefix("::1/128"),           // loopback
	netip.MustParsePrefix("64:ff9b::/96"),      // NAT64 (can reach IPv4 private space)
	netip.MustParsePrefix("fc00::/7"),          // RFC 4193 unique local
	netip.MustParsePrefix("fe80::/10"),         // link-local
	netip.MustParsePrefix("ff00::/8"),          // multicast
	netip.MustParsePrefix("fd00:ec2::254/128"), // AWS IPv6 metadata (inside fc00::/7, listed for clarity)
}

// AddressAllowed reports whether a is an acceptable outbound destination.
func AddressAllowed(a netip.Addr, allowPrivate bool) bool {
	if allowPrivate {
		return a.IsValid()
	}
	a = a.Unmap()
	if !a.IsValid() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// dialControl rejects connections to blocked addresses after DNS resolution.
func dialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, address)
		}
		if !AddressAllowed(ap.Addr(), allowPrivate) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, ap.Addr())
		}
		return nil
	}
}

// Outbound client limits.
const (
	outboundDialTimeout = 5 * time.Second
	outboundTLSTimeout  = 5 * time.Second
	outboundIdleConns   = 10
)

// SafeClient returns an HTTP client whose dialer refuses blocked addresses, which never uses
// environment proxies (they would bypass the check) and never follows redirects.
func SafeClient(allowPrivate bool, timeout time.Duration) *http.Client {
	return SafeClientTLS(allowPrivate, timeout, nil)
}

// SafeClientTLS is SafeClient with a TLS configuration (e.g. extra trusted roots); nil uses the
// defaults. Certificate verification must stay enabled in tlsCfg.
func SafeClientTLS(allowPrivate bool, timeout time.Duration, tlsCfg *tls.Config) *http.Client {
	dialer := &net.Dialer{Timeout: outboundDialTimeout, Control: dialControl(allowPrivate)}
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tlsCfg,
		TLSHandshakeTimeout: outboundTLSTimeout,
		MaxIdleConns:        outboundIdleConns,
		IdleConnTimeout:     time.Minute,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// ValidateURL checks an outbound URL before it is stored: https only (http only when private
// destinations are allowed), no credentials, and literal IP hosts must be allowed.
func ValidateURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: not an absolute URL", ErrBlockedURL)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowPrivate:
	default:
		return nil, fmt.Errorf("%w: scheme must be https", ErrBlockedURL)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in URL", ErrBlockedURL)
	}
	if a, err := netip.ParseAddr(u.Hostname()); err == nil && !AddressAllowed(a, allowPrivate) {
		return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, a)
	}
	return u, nil
}
