// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Health probe defaults (SPEC §17.2): the distroless image has no shell or curl, so container
// health checks run the binary itself against GET /healthz.
const (
	defaultHealthURL = "http://127.0.0.1:8080/healthz"
	healthTimeout    = 5 * time.Second
	healthBodyLimit  = 4096
)

// errUnhealthy is returned when the probe gets a non-200 answer.
var errUnhealthy = errors.New("unhealthy")

// healthcheck implements "tidemark healthcheck [--url URL]": exit 0 when the URL answers 200.
func healthcheck(ctx context.Context, args []string, client *http.Client) error {
	fs := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	target := fs.String("url", defaultHealthURL, "health endpoint to probe")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("usage: tidemark healthcheck [--url URL]: %w", err)
	}
	u, err := url.Parse(*target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("healthcheck: invalid --url %q", *target)
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()                               // read-only probe; the status code is the result
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, healthBodyLimit)) // drain for keep-alive; content is irrelevant
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %w: HTTP %d", errUnhealthy, resp.StatusCode)
	}
	return nil
}
