// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadyzWhileDraining(t *testing.T) {
	draining := false
	h := &Health{Checks: []Pinger{pingerFunc(func(context.Context) error { return nil })}, Draining: func() bool { return draining }}
	mux := http.NewServeMux()
	h.Register(mux)
	probe := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := probe("/readyz"); w.Code != http.StatusOK {
		t.Fatalf("ready before drain: %d", w.Code)
	}
	draining = true
	if w := probe("/readyz"); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "draining") {
		t.Fatalf("readyz while draining: %d %s", w.Code, w.Body.String())
	}
	if w := probe("/healthz"); w.Code != http.StatusOK {
		t.Fatalf("healthz must stay 200 while draining: %d", w.Code)
	}
}

func TestHealthzReportsFailure(t *testing.T) {
	var failure error
	h := &Health{Failure: func() error { return failure }}
	mux := http.NewServeMux()
	h.Register(mux)
	probe := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		return w
	}
	if w := probe(); w.Code != http.StatusOK {
		t.Fatalf("healthz before a failure: %d", w.Code)
	}
	failure = errors.New("write-ahead log failed: fsync wal: input/output error")
	w := probe()
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"failed"`) {
		t.Fatalf("healthz after a failure: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "fsync") {
		t.Fatalf("healthz must not expose error details: %s", w.Body.String())
	}
}
