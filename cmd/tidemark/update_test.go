// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/update"
)

func TestUpdateCommandUsage(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{{"update"}, {"update", "bogus"}, {"update", "install", "extra"}, {"update", "check", "--json=1"}} {
		if err := run(args, nil, &out, &out); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestUpdateCheckDevelopmentBuild(t *testing.T) {
	// A development build never asks GitHub (SPEC §17.7).
	var out bytes.Buffer
	err := run([]string{"update", "check", "--data-dir", t.TempDir()}, nil, &out, &out)
	if !errors.Is(err, update.ErrUnavailable) || !strings.Contains(err.Error(), update.ReasonDev) {
		t.Errorf("err = %v", err)
	}
}

func TestPrintUpdateStatus(t *testing.T) {
	published := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		st   update.Status
		want string
	}{
		{"available", update.Status{Current: "1.0.0", Latest: &update.Release{Version: "1.1.0", PublishedAt: published,
			NotesURL: "https://github.com/ZeraOrg/opentidemark/releases/tag/v1.1.0"}},
			"OpenTidemark 1.0.0: version 1.1.0 is available (published 2026-10-01)\nRelease notes: https://github.com/ZeraOrg/opentidemark/releases/tag/v1.1.0\n"},
		{"up to date", update.Status{Current: "1.0.0"}, "OpenTidemark 1.0.0 is up to date\n"},
		{"failed", update.Status{Current: "1.0.0", Error: update.CodeRateLimited},
			"OpenTidemark 1.0.0: the update check failed (rate_limited)\n"},
	}
	for _, tc := range tests {
		var out bytes.Buffer
		if err := printUpdateStatus(&out, tc.st); err != nil || out.String() != tc.want {
			t.Errorf("%s: %q, %v", tc.name, out.String(), err)
		}
	}
}
