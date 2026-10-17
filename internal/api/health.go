// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"net/http"
	"time"
)

// Pinger checks a dependency.
type Pinger interface {
	Ping(ctx context.Context) error
}

const readinessTimeout = 2 * time.Second

// Health serves /healthz (process alive and not failed) and /readyz (ClickHouse + metadata
// reachable, and not draining). Output is intentionally minimal (SPEC §11.6, §17.5).
type Health struct {
	Checks   []Pinger
	Draining func() bool  // optional: true once a graceful drain has started
	Failure  func() error // optional: non-nil once the process can no longer work (failed WAL)
}

// Register adds the probes to mux.
func (h *Health) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.live)
	mux.HandleFunc("GET /readyz", h.ready)
}

func (h *Health) live(w http.ResponseWriter, _ *http.Request) {
	if h.Failure != nil && h.Failure() != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Health) ready(w http.ResponseWriter, r *http.Request) {
	if h.Draining != nil && h.Draining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()
	for _, c := range h.Checks {
		if err := c.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
