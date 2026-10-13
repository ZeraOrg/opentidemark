// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"encoding/base64"
	"net/http/httptest"
	"testing"
)

func TestKeyFromRequest(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	tests := []struct {
		name       string
		headers    map[string]string
		query      string
		allowQuery bool
		key        string
		src        KeySource
	}{
		{"tidemark header", map[string]string{"X-Tidemark-Key": "k1", "X-Seq-ApiKey": "k2"}, "", false, "k1", KeyTidemark},
		{"seq header", map[string]string{"X-Seq-ApiKey": "k2", "Authorization": "Bearer k3"}, "", false, "k2", KeySeqHeader},
		{"bearer", map[string]string{"Authorization": "Bearer k3"}, "", false, "k3", KeyBearer},
		{"apikey base64", map[string]string{"Authorization": "ApiKey " + b64([]byte("id1:k4"))}, "", false, "k4", KeyESAPIKey},
		{"apikey plain", map[string]string{"Authorization": "ApiKey tmk_plain"}, "", false, "tmk_plain", KeyESAPIKey},
		{"basic", map[string]string{"Authorization": "Basic " + b64([]byte("elastic:k5"))}, "", false, "k5", KeyBasic},
		{"basic no password", map[string]string{"Authorization": "Basic " + b64([]byte("elastic:"))}, "", false, "", KeyNone},
		{"basic garbage", map[string]string{"Authorization": "Basic !!!"}, "", false, "", KeyNone},
		{"unknown scheme", map[string]string{"Authorization": "Digest x"}, "", false, "", KeyNone},
		{"no scheme", map[string]string{"Authorization": "token"}, "", false, "", KeyNone},
		{"query allowed", nil, "apiKey=k6", true, "k6", KeyQueryAPIKey},
		{"query not allowed", nil, "apiKey=k6", false, "", KeyNone},
		{"none", nil, "", true, "", KeyNone},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/api/events/raw?"+tt.query, nil)
		for k, v := range tt.headers {
			r.Header.Set(k, v)
		}
		key, src := KeyFromRequest(r, tt.allowQuery)
		if key != tt.key || src != tt.src {
			t.Errorf("%s: got %q,%q want %q,%q", tt.name, key, src, tt.key, tt.src)
		}
	}
}
