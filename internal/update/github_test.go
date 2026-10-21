// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)

const releaseJSON = `{"tag_name":"v0.5.0","draft":false,"prerelease":false,"published_at":"2026-10-20T08:00:00Z",
	"html_url":"https://evil.example/","body":"ignored"}`

func newSource(t *testing.T, h http.HandlerFunc) *GitHubSource {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return NewGitHubSource(srv.Client(), srv.URL, func() time.Time { return testNow })
}

func TestGitHubSourceRequest(t *testing.T) {
	var gotPath string
	var gotHeader http.Header
	src := newSource(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotHeader = r.URL.RequestURI(), r.Header
		if strings.Contains(r.URL.RawQuery, "per_page") {
			_, _ = w.Write([]byte("[" + releaseJSON + "]"))
			return
		}
		_, _ = w.Write([]byte(releaseJSON))
	})
	for _, tc := range []struct {
		pre  bool
		path string
	}{
		{false, "/repos/ZeraOrg/opentidemark/releases/latest"},
		{true, "/repos/ZeraOrg/opentidemark/releases?per_page=30"},
	} {
		rs, err := src.Releases(context.Background(), tc.pre)
		if err != nil || len(rs) != 1 || rs[0].Tag != "v0.5.0" || !rs[0].PublishedAt.Equal(time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)) {
			t.Fatalf("pre=%v: %+v %v", tc.pre, rs, err)
		}
		if gotPath != tc.path {
			t.Errorf("path %q, want %q", gotPath, tc.path)
		}
		want := map[string]string{"Accept": "application/vnd.github+json", "X-Github-Api-Version": "2022-11-28",
			"User-Agent": "OpenTidemark-update-check"}
		for k, v := range want {
			if gotHeader.Get(k) != v {
				t.Errorf("%s = %q, want %q", k, gotHeader.Get(k), v)
			}
		}
		for _, k := range []string{"Authorization", "Cookie"} {
			if gotHeader.Get(k) != "" {
				t.Errorf("unexpected header %s", k)
			}
		}
	}
}

func TestGitHubSourceETag(t *testing.T) {
	calls := 0
	src := newSource(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("If-None-Match") == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		_, _ = w.Write([]byte(releaseJSON))
	})
	for i := 0; i < 2; i++ {
		rs, err := src.Releases(context.Background(), false)
		if err != nil || len(rs) != 1 || rs[0].Tag != "v0.5.0" {
			t.Fatalf("call %d: %+v %v", i, rs, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestGitHubSourceErrors(t *testing.T) {
	reset := testNow.Add(30 * time.Minute)
	tests := []struct {
		name  string
		h     http.HandlerFunc
		want  error
		until time.Time
	}{
		{"403 rate limit reset", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
			w.WriteHeader(http.StatusForbidden)
		}, ErrRateLimited, reset},
		{"429 retry-after", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
		}, ErrRateLimited, testNow.Add(2 * time.Minute)},
		{"403 forbidden", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }, ErrInvalidResponse, time.Time{}},
		{"404", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }, ErrNotFound, time.Time{}},
		{"502", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, ErrUnreachable, time.Time{}},
		{"304 without cache", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) }, ErrInvalidResponse, time.Time{}},
		{"bad json", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{not json")) }, ErrInvalidResponse, time.Time{}},
		{"not a release", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{}")) }, ErrInvalidResponse, time.Time{}},
		{"oversize", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","body":"` + strings.Repeat("x", maxResponseBytes) + `"}`))
		}, ErrInvalidResponse, time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newSource(t, tc.h).Releases(context.Background(), false)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var rl *RateLimitError
			if errors.As(err, &rl) && !rl.Until.Equal(tc.until) {
				t.Fatalf("until = %v, want %v", rl.Until, tc.until)
			}
		})
	}
}

func TestGitHubSourceUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	src := NewGitHubSource(http.DefaultClient, srv.URL, func() time.Time { return testNow })
	if _, err := src.Releases(context.Background(), false); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
}
