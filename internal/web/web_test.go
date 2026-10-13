// SPDX-License-Identifier: Apache-2.0

package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestPlaceholder(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/events/123", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Tidemark") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
}

func TestSPA(t *testing.T) {
	root := fstest.MapFS{
		"assets/dist/index.html":        {Data: []byte("<html>app</html>")},
		"assets/dist/assets/app-123.js": {Data: []byte("console.log(1)")},
		"assets/dist/favicon.svg":       {Data: []byte("<svg/>")},
		"assets/placeholder/index.html": {Data: []byte("placeholder")},
		"assets/dist/assets/sub/x.css":  {Data: []byte("a{}")},
	}
	h, err := newHandler(root)
	if err != nil {
		t.Fatal(err)
	}
	if h.IsPlaceholder() {
		t.Error("dist build reported as placeholder")
	}
	tests := []struct {
		method, path string
		code         int
		body, cache  string
	}{
		{"GET", "/", 200, "app", "no-cache"},
		{"GET", "/dashboards/42/edit", 200, "app", "no-cache"},
		{"GET", "/assets/app-123.js", 200, "console", "public, max-age=31536000, immutable"},
		{"GET", "/favicon.svg", 200, "<svg/>", "no-cache"},
		{"GET", "/missing.js", 404, "", ""},
		{"GET", "/../../etc/passwd", 400, "", ""},
		{"GET", "/assets", 200, "app", "no-cache"},
		{"HEAD", "/", 200, "", "no-cache"},
		{"POST", "/", 405, "", ""},
	}
	for _, tt := range tests {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
		if w.Code != tt.code || !strings.Contains(w.Body.String(), tt.body) || w.Header().Get("Cache-Control") != tt.cache {
			t.Errorf("%s %s: %d %q %q", tt.method, tt.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
	}
	if _, err := newHandler(fstest.MapFS{}); err == nil {
		t.Error("empty fs accepted")
	}
	ph, _ := newHandler(fstest.MapFS{"assets/placeholder/index.html": {Data: []byte("p")}})
	if !ph.IsPlaceholder() {
		t.Error("placeholder not detected")
	}
}
