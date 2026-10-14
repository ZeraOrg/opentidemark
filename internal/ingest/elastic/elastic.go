// SPDX-License-Identifier: Apache-2.0

// Package elastic implements the Elasticsearch `_bulk` ingestion API (SPEC §9.3).
package elastic

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
)

// apiVersion is the Elasticsearch API level whose `_bulk` request and response format the
// endpoint implements. Shippers read its major version to choose the request format (SPEC §9.3).
const apiVersion = "8.11.0"

// setupPrefixes are management APIs acknowledged without effect so shippers start up.
var setupPrefixes = []string{"_index_template", "_template", "_ilm", "_ingest", "_data_stream", "_component_template"}

// Handler serves the `_bulk` ingestion API under a path prefix ("" or "/es").
type Handler struct {
	d      *ingest.Deps
	prefix string
}

// New returns a handler for paths below prefix.
func New(d *ingest.Deps, prefix string) *Handler {
	return &Handler{d: d, prefix: strings.TrimSuffix(prefix, "/")}
}

// Register mounts the handler on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	if h.prefix == "" {
		mux.Handle("/", h)
		return
	}
	mux.Handle(h.prefix+"/", h)
	mux.Handle(h.prefix, h)
}

// ServeHTTP authenticates the request and routes it.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, h.prefix)
	segs := splitPath(path)
	req, err := h.d.Authenticate(r, auth.ProtocolElastic, false)
	if err != nil {
		if ingest.Status(err) == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `ApiKey, Basic realm="tidemark"`)
		}
		writeError(w, ingest.Status(err), "security_exception", ingest.PublicMessage(err))
		return
	}
	h.route(w, r, req, segs)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request, req *ingest.Request, segs []string) {
	m := r.Method
	switch {
	case len(segs) == 0 && (m == http.MethodGet || m == http.MethodHead):
		h.info(w)
	case len(segs) == 1 && segs[0] == "_bulk" && (m == http.MethodPost || m == http.MethodPut):
		h.bulk(w, r, req, "")
	case len(segs) == 2 && segs[1] == "_bulk" && !isSystem(segs[0]) && (m == http.MethodPost || m == http.MethodPut):
		h.bulk(w, r, req, segs[0])
	case isDocPath(segs) && (m == http.MethodPost || m == http.MethodPut):
		id := ""
		if len(segs) == 3 {
			id = segs[2]
		}
		h.single(w, r, req, segs[0], id)
	case len(segs) == 1 && segs[0] == "_license" && m == http.MethodGet:
		ingest.WriteJSON(w, http.StatusOK, map[string]any{"license": map[string]string{"status": "active", "type": "basic"}})
	case len(segs) == 1 && segs[0] == "_xpack" && m == http.MethodGet:
		ingest.WriteJSON(w, http.StatusOK, map[string]any{"features": map[string]any{}})
	case len(segs) == 2 && segs[0] == "_cluster" && segs[1] == "health" && m == http.MethodGet:
		ingest.WriteJSON(w, http.StatusOK, map[string]string{"status": "green"})
	case len(segs) >= 1 && isSetup(segs[0]):
		acknowledge(w, m)
	case len(segs) == 1 && !isSystem(segs[0]) && (m == http.MethodPut || m == http.MethodHead):
		acknowledge(w, m)
	default:
		writeError(w, http.StatusNotFound, "unsupported_operation",
			"OpenTidemark does not support "+m+" "+r.URL.Path)
	}
}

// info answers the cluster-info probe minimally: the server's own name and the API level.
func (h *Handler) info(w http.ResponseWriter) {
	ingest.WriteJSON(w, http.StatusOK, map[string]any{
		"name": "tidemark", "cluster_name": "tidemark",
		"version": map[string]string{"number": apiVersion},
	})
}

func acknowledge(w http.ResponseWriter, method string) {
	switch method {
	case http.MethodHead:
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		ingest.WriteJSON(w, http.StatusOK, map[string]any{})
	case http.MethodPut, http.MethodPost, http.MethodDelete:
		ingest.WriteJSON(w, http.StatusOK, map[string]bool{"acknowledged": true})
	default:
		writeError(w, http.StatusMethodNotAllowed, "unsupported_operation", "method not allowed")
	}
}

func isDocPath(segs []string) bool {
	if len(segs) < 2 || len(segs) > 3 || isSystem(segs[0]) {
		return false
	}
	switch segs[1] {
	case "_doc":
		return true
	case "_create":
		return len(segs) == 3
	default:
		return false
	}
}

func isSetup(seg string) bool {
	for _, p := range setupPrefixes {
		if seg == p {
			return true
		}
	}
	return false
}

// isSystem reports API (underscore) segments that are never index names.
func isSystem(seg string) bool { return strings.HasPrefix(seg, "_") }

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// dateSuffix matches trailing date suffixes of time-based index names.
var dateSuffix = regexp.MustCompile(`-(\d{4}\.\d{2}\.\d{2}|\d{4}\.\d{2}|\d{8})$`)

// StreamFromIndex maps an index name to a stream name (SPEC §9.3): lower-cased, data-stream
// prefixes kept, trailing -YYYY.MM.DD / -YYYY.MM / -YYYYMMDD stripped.
func StreamFromIndex(index string) string {
	return dateSuffix.ReplaceAllString(strings.ToLower(strings.TrimSpace(index)), "")
}

// writeError writes an ES-shaped error.
func writeError(w http.ResponseWriter, status int, typ, reason string) {
	ingest.WriteJSON(w, status, map[string]any{
		"error":  map[string]string{"type": typ, "reason": reason},
		"status": status,
	})
}

func tookMillis(start time.Time, now time.Time) int64 { return now.Sub(start).Milliseconds() }
