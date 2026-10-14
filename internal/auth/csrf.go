// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// CSRFValid implements the double-submit check: the X-Tidemark-CSRF header must equal the
// tm_csrf cookie (constant-time). When the request carries an Origin header, it must be the
// public URL or the host the request was sent to (same-origin, e.g. a LAN address that differs
// from the public URL).
func CSRFValid(r *http.Request, publicURL *url.URL) bool {
	if origin := r.Header.Get("Origin"); origin != "" && !allowedOrigin(origin, r.Host, publicURL) {
		return false
	}
	c, err := r.Cookie(CSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get(CSRFHeader)
	return h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(c.Value)) == 1
}

func allowedOrigin(origin, requestHost string, public *url.URL) bool {
	o, err := url.Parse(origin)
	if err != nil || o.Host == "" {
		return false
	}
	if public != nil && strings.EqualFold(o.Scheme, public.Scheme) && strings.EqualFold(o.Host, public.Host) {
		return true
	}
	return strings.EqualFold(o.Host, requestHost)
}

// SafeMethod reports whether a method is exempt from CSRF checks.
func SafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
