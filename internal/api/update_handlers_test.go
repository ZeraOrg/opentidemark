// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/update"
)

// fakeUpdater returns a fixed status and errors, counts checks and records applied versions.
type fakeUpdater struct {
	status   update.Status
	err      error
	checks   int
	applyErr error
	applied  []string
}

func (f *fakeUpdater) Apply(version string) (update.Status, error) {
	f.applied = append(f.applied, version)
	if f.applyErr != nil {
		return f.status, f.applyErr
	}
	s := f.status
	s.State, s.Progress = update.StateDownloading, &update.Progress{}
	return s, nil
}

func (f *fakeUpdater) Status() update.Status { return f.status }

func (f *fakeUpdater) CheckNow(context.Context) (update.Status, error) {
	f.checks++
	return f.status, f.err
}

func TestUpdateStatus(t *testing.T) {
	e := newEnv(t)
	at := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e.updates.status = update.Status{Enabled: true, Current: "0.4.0", NodeID: "n1", InstallKind: update.KindSystemd,
		Apply: update.Apply{Reason: update.ReasonHelperMissing}, Available: true, CheckedAt: &at, State: update.StateIdle,
		Latest: &update.Release{Version: "0.5.0", PublishedAt: at, NotesURL: "https://github.com/ZeraOrg/opentidemark/releases/tag/v0.5.0"}}
	w := e.do(e.sessions["admin"], "GET", "/system/update", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	got := decodeBody[map[string]any](t, w)
	want := map[string]any{"enabled": true, "current": "0.4.0", "nodeId": "n1", "installKind": "systemd",
		"available": true, "checkedAt": "2026-10-21T12:00:00Z", "state": "idle"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	apply, _ := got["apply"].(map[string]any)
	latest, _ := got["latest"].(map[string]any)
	if apply["supported"] != false || apply["reason"] != "helper-missing" || latest["version"] != "0.5.0" ||
		latest["prerelease"] != false || latest["notesUrl"] == nil || latest["publishedAt"] != "2026-10-21T12:00:00Z" {
		t.Errorf("apply %v latest %v", apply, latest)
	}
	if _, has := got["error"]; has {
		t.Error("error must be omitted when the check succeeded")
	}
}

func TestUpdateCheck(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
		retryAfter string
	}{
		{"checked", nil, http.StatusOK, "", ""},
		{"disabled", &update.UnavailableError{Reason: update.ReasonDisabled}, http.StatusConflict, "disabled", ""},
		{"dev build", &update.UnavailableError{Reason: update.ReasonDev}, http.StatusConflict, "dev", ""},
		{"too soon", &update.TooSoonError{RetryAfter: 20 * time.Second}, http.StatusTooManyRequests, "", "21"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.updates.err = tc.err
			w := e.do(e.sessions["admin"], "POST", "/system/update/check", nil)
			if w.Code != tc.wantStatus {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if e.updates.checks != 1 {
				t.Errorf("checks = %d", e.updates.checks)
			}
			if w.Header().Get("Retry-After") != tc.retryAfter {
				t.Errorf("Retry-After %q", w.Header().Get("Retry-After"))
			}
			if tc.wantStatus == http.StatusOK {
				if s := decodeBody[update.Status](t, w); s.Current != "0.4.0" {
					t.Errorf("status %+v", s)
				}
				return
			}
			body := decodeBody[errorBody](t, w)
			if tc.wantReason != "" && (body.Error.Code != "conflict" || body.Error.Details["reason"] != tc.wantReason) {
				t.Errorf("body %+v", body)
			}
			if tc.wantStatus == http.StatusTooManyRequests && body.Error.Code != "rate_limited" {
				t.Errorf("body %+v", body)
			}
		})
	}
}

func TestUpdateApply(t *testing.T) {
	tests := []struct {
		name       string
		body       any
		err        error
		wantStatus int
		wantDetail map[string]any
	}{
		{"accepted", map[string]string{"version": "0.5.0"}, nil, http.StatusAccepted, nil},
		{"not the latest", map[string]string{"version": "0.4.9"}, update.ErrVersionMismatch, http.StatusBadRequest,
			map[string]any{"field": "version"}},
		{"helper missing", map[string]string{"version": "0.5.0"}, &update.ApplyRefusedError{Reason: update.ReasonHelperMissing},
			http.StatusConflict, map[string]any{"reason": "helper-missing"}},
		{"ha", map[string]string{"version": "0.5.0"}, &update.ApplyRefusedError{Reason: update.ReasonHA},
			http.StatusConflict, map[string]any{"reason": "ha"}},
		{"in progress", map[string]string{"version": "0.5.0"}, &update.ApplyRefusedError{Reason: update.ReasonInProgress},
			http.StatusConflict, map[string]any{"reason": "in-progress"}},
		{"draining", map[string]string{"version": "0.5.0"}, &update.ApplyRefusedError{Reason: update.ReasonDraining},
			http.StatusConflict, map[string]any{"reason": "draining"}},
		{"unknown field", map[string]string{"version": "0.5.0", "force": "yes"}, nil, http.StatusBadRequest, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.updates.applyErr = tc.err
			w := e.do(e.sessions["admin"], "POST", "/system/update/apply", tc.body)
			if w.Code != tc.wantStatus {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			entries := decodeBody[[]auditView](t, e.do(e.sessions["admin"], "GET", "/audit?action=update.apply", nil))
			if tc.wantStatus != http.StatusAccepted {
				body := decodeBody[errorBody](t, w)
				for k, v := range tc.wantDetail {
					if body.Error.Details[k] != v {
						t.Errorf("details %v, want %s=%v", body.Error.Details, k, v)
					}
				}
				if len(entries) != 0 {
					t.Errorf("refused apply audited: %+v", entries)
				}
				return
			}
			if s := decodeBody[update.Status](t, w); s.State != update.StateDownloading || s.Progress == nil {
				t.Errorf("status %+v", s)
			}
			if len(e.updates.applied) != 1 || e.updates.applied[0] != "0.5.0" {
				t.Errorf("applied %v", e.updates.applied)
			}
			if len(entries) != 1 || entries[0].Target != "0.5.0" {
				t.Fatalf("audit %+v", entries)
			}
			var details map[string]string
			if err := json.Unmarshal(entries[0].Details, &details); err != nil || details["from"] != "0.4.0" || details["to"] != "0.5.0" {
				t.Errorf("details %s", entries[0].Details)
			}
		})
	}
}
