// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// KeySource tells where a key was found.
type KeySource string

// Key sources in priority order (SPEC §9.1).
const (
	KeyNone        KeySource = ""
	KeyTidemark    KeySource = "x-tidemark-key"
	KeySeqHeader   KeySource = "x-seq-apikey"
	KeyBearer      KeySource = "bearer"
	KeyESAPIKey    KeySource = "apikey"
	KeyBasic       KeySource = "basic"
	KeyQueryAPIKey KeySource = "query"
)

// KeyFromRequest extracts the connection key in the order of SPEC §9.1. The ?apiKey=
// query parameter is only consulted when allowQuery is true (Seq endpoints).
func KeyFromRequest(r *http.Request, allowQuery bool) (string, KeySource) {
	if k := strings.TrimSpace(r.Header.Get("X-Tidemark-Key")); k != "" {
		return k, KeyTidemark
	}
	if k := strings.TrimSpace(r.Header.Get("X-Seq-ApiKey")); k != "" {
		return k, KeySeqHeader
	}
	if k, src := keyFromAuthorization(r.Header.Get("Authorization")); k != "" {
		return k, src
	}
	if allowQuery {
		if k := strings.TrimSpace(r.URL.Query().Get("apiKey")); k != "" {
			return k, KeyQueryAPIKey
		}
	}
	return "", KeyNone
}

func keyFromAuthorization(h string) (string, KeySource) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok {
		return "", KeyNone
	}
	value = strings.TrimSpace(value)
	switch strings.ToLower(scheme) {
	case "bearer":
		return value, KeyBearer
	case "apikey":
		// Elasticsearch API keys are base64("id:key"); plain keys are accepted too.
		if dec, err := base64.StdEncoding.DecodeString(value); err == nil {
			if _, key, found := strings.Cut(string(dec), ":"); found && key != "" {
				return key, KeyESAPIKey
			}
		}
		return value, KeyESAPIKey
	case "basic":
		dec, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return "", KeyNone
		}
		if _, pw, found := strings.Cut(string(dec), ":"); found && pw != "" {
			return pw, KeyBasic
		}
	}
	return "", KeyNone
}
