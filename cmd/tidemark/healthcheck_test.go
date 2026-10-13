// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	ctx := context.Background()

	cases := []struct {
		name    string
		args    []string
		status  int
		wantErr bool
	}{
		{"healthy", []string{"--url", srv.URL + "/healthz"}, http.StatusOK, false},
		{"unhealthy", []string{"--url", srv.URL + "/healthz"}, http.StatusServiceUnavailable, true},
		{"wrong path", []string{"--url", srv.URL + "/nope"}, http.StatusOK, true},
		{"bad scheme", []string{"--url", "file:///etc/passwd"}, http.StatusOK, true},
		{"no host", []string{"--url", "http://"}, http.StatusOK, true},
		{"unknown flag", []string{"--bogus"}, http.StatusOK, true},
		{"unreachable", []string{"--url", "http://127.0.0.1:1/healthz"}, http.StatusOK, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			status = c.status
			err := healthcheck(ctx, c.args, srv.Client())
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.name == "unhealthy" && !errors.Is(err, errUnhealthy) {
				t.Errorf("err = %v, want errUnhealthy", err)
			}
		})
	}
}

func TestHealthcheckCommand(t *testing.T) {
	if err := run([]string{"healthcheck", "--url", "http://127.0.0.1:1/healthz"}, nil, nil, nil); err == nil {
		t.Error("probe of a closed port succeeded")
	}
}
