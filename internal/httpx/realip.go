// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RealIP resolves client addresses, trusting X-Forwarded-For only from trusted proxies.
type RealIP struct {
	trusted []netip.Prefix
}

// NewRealIP returns a resolver trusting the given proxy networks.
func NewRealIP(trusted []netip.Prefix) *RealIP { return &RealIP{trusted: trusted} }

func (ri *RealIP) isTrusted(a netip.Addr) bool {
	for _, p := range ri.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the client address. When the peer is a trusted proxy, X-Forwarded-For is
// walked from the right and the first untrusted address wins.
func (ri *RealIP) ClientIP(r *http.Request) netip.Addr {
	remote := parseRemote(r.RemoteAddr)
	if !remote.IsValid() || !ri.isTrusted(remote) {
		return remote
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return remote
		}
		a = a.Unmap()
		if !ri.isTrusted(a) {
			return a
		}
		remote = a
	}
	return remote
}

// ClientIPString is ClientIP formatted for logs.
func (ri *RealIP) ClientIPString(r *http.Request) string {
	a := ri.ClientIP(r)
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

func parseRemote(addr string) netip.Addr {
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap.Addr().Unmap()
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}
