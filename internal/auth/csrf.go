// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// CSRFValid implements the double-submit check: the X-Tidemark-CSRF header must equal the
// tm_csrf cookie (constant-time). When publicURL is set and the request carries an Origin
// header, the origin must match it as well.
func CSRFValid(r *http.Request, publicURL *url.URL) bool {
	if publicURL != nil {
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, publicURL) {
			return false
		}
	}
	c, err := r.Cookie(CSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	h := r.Header.Get(CSRFHeader)
	return h != "" && subtle.ConstantTimeCompare([]byte(h), []byte(c.Value)) == 1
}

func sameOrigin(origin string, public *url.URL) bool {
	o, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(o.Scheme, public.Scheme) && strings.EqualFold(o.Host, public.Host)
}

// SafeMethod reports whether a method is exempt from CSRF checks.
func SafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
