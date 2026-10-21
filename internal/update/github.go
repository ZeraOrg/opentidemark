// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// GitHub API settings (SPEC §17.7).
const (
	GitHubAPIBase     = "https://api.github.com"
	releasesPath      = "/repos/ZeraOrg/opentidemark/releases"
	releaseTagURL     = "https://github.com/ZeraOrg/opentidemark/releases/tag/v"
	userAgent         = "OpenTidemark-update-check"
	apiVersion        = "2022-11-28"
	maxResponseBytes  = 1 << 20
	prereleasePerPage = 30
)

// Errors of a release lookup; the checker reports them as Status.Error codes.
var (
	ErrUnreachable     = errors.New("release source unreachable")
	ErrRateLimited     = errors.New("release source rate limit")
	ErrInvalidResponse = errors.New("invalid release source response")
	ErrNotFound        = errors.New("release source not found")
)

// RateLimitError is a rate-limit answer; Until is when the next request may be sent (zero when
// the server did not say).
type RateLimitError struct{ Until time.Time }

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }

// Is makes errors.Is(err, ErrRateLimited) true.
func (e *RateLimitError) Is(target error) bool { return target == ErrRateLimited }

// ReleaseInfo is a release as the source lists it.
type ReleaseInfo struct {
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

// GitHubSource lists releases of the public repository through the GitHub REST API, with
// conditional requests (ETag) so unchanged answers do not count against the rate limit.
type GitHubSource struct {
	client *http.Client
	base   string
	now    func() time.Time

	mu    sync.Mutex
	cache map[string]cachedAnswer // by request URL
}

type cachedAnswer struct {
	etag     string
	releases []ReleaseInfo
}

// NewGitHubSource returns a source using client (the update client of httpx) against base
// (GitHubAPIBase; tests pass an httptest URL).
func NewGitHubSource(client *http.Client, base string, now func() time.Time) *GitHubSource {
	return &GitHubSource{client: client, base: base, now: now, cache: map[string]cachedAnswer{}}
}

// Releases returns the latest stable release, or with prereleases the 30 newest releases.
func (g *GitHubSource) Releases(ctx context.Context, prereleases bool) ([]ReleaseInfo, error) {
	url := g.base + releasesPath + "/latest"
	if prereleases {
		url = g.base + releasesPath + "?per_page=" + strconv.Itoa(prereleasePerPage)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", userAgent)
	g.mu.Lock()
	cached, haveCache := g.cache[url]
	g.mu.Unlock()
	if haveCache && cached.etag != "" {
		req.Header.Set("If-None-Match", cached.etag)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && haveCache:
		return cached.releases, nil
	case resp.StatusCode == http.StatusOK:
		releases, err := decodeReleases(resp.Body, prereleases)
		if err != nil {
			return nil, err
		}
		g.mu.Lock()
		g.cache[url] = cachedAnswer{etag: resp.Header.Get("ETag"), releases: releases}
		g.mu.Unlock()
		return releases, nil
	default:
		return nil, g.statusError(resp)
	}
}

// statusError maps an unexpected answer to the error kinds of the checker.
func (g *GitHubSource) statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && isRateLimit(resp.Header):
		return &RateLimitError{Until: g.retryAt(resp.Header)}
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= http.StatusInternalServerError:
		return fmt.Errorf("%w: status %d", ErrUnreachable, resp.StatusCode)
	default:
		return fmt.Errorf("%w: status %d", ErrInvalidResponse, resp.StatusCode)
	}
}

// isRateLimit recognises GitHub's primary (remaining 0) and secondary (Retry-After) limits.
func isRateLimit(h http.Header) bool {
	return h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0"
}

// retryAt reads Retry-After (seconds) or X-RateLimit-Reset (Unix seconds).
func (g *GitHubSource) retryAt(h http.Header) time.Time {
	if s, err := strconv.ParseInt(h.Get("Retry-After"), 10, 64); err == nil && s >= 0 {
		return g.now().Add(time.Duration(s) * time.Second)
	}
	if s, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && s > 0 {
		return time.Unix(s, 0)
	}
	return time.Time{}
}

// decodeReleases reads at most maxResponseBytes: one release object, or a list of them.
func decodeReleases(body io.Reader, list bool) ([]ReleaseInfo, error) {
	raw, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnreachable, err)
	}
	if len(raw) > maxResponseBytes {
		return nil, fmt.Errorf("%w: response larger than %d bytes", ErrInvalidResponse, maxResponseBytes)
	}
	if list {
		var out []ReleaseInfo
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidResponse, err)
		}
		return out, nil
	}
	var one ReleaseInfo
	if err := json.Unmarshal(raw, &one); err != nil || one.Tag == "" {
		return nil, fmt.Errorf("%w: not a release object", ErrInvalidResponse)
	}
	return []ReleaseInfo{one}, nil
}
