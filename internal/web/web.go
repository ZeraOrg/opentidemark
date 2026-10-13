// SPDX-License-Identifier: Apache-2.0

// Package web serves the embedded single-page application (SPEC §16) with an SPA fallback.
// The build copies web/dist into assets/dist; without it a placeholder page is served.
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:assets
var assets embed.FS

// Cache policies.
const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNone      = "no-cache"
)

// Handler serves the SPA from the embedded assets.
type Handler struct {
	files fs.FS
}

// New returns a handler for the built UI, or the placeholder page when the UI is absent.
func New() (*Handler, error) {
	return newHandler(assets)
}

func newHandler(root fs.FS) (*Handler, error) {
	for _, dir := range []string{"assets/dist", "assets/placeholder"} {
		sub, err := fs.Sub(root, dir)
		if err != nil {
			return nil, err
		}
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return &Handler{files: sub}, nil
		}
	}
	return nil, errors.New("no index.html in embedded web assets")
}

// IsPlaceholder reports whether the placeholder page is served (UI not built).
func (h *Handler) IsPlaceholder() bool {
	_, err := fs.Stat(h.files, "assets")
	return err != nil
}

// ServeHTTP serves files; unknown paths without a file extension get index.html.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if st, err := fs.Stat(h.files, name); err == nil && !st.IsDir() {
		h.serveFile(w, r, name)
		return
	}
	if path.Ext(name) != "" {
		http.NotFound(w, r)
		return
	}
	h.serveFile(w, r, "index.html")
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheNone)
	}
	http.ServeFileFS(w, r, h.files, name) //nolint:gosec // name is path.Clean'ed and h.files is an fs.FS, which rejects paths outside its root
}
