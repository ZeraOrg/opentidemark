// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
)

// SettingsAPI serves /settings (admin).
type SettingsAPI struct {
	Settings *service.Settings
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *SettingsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/settings", Admin, a.get},
		{http.MethodPatch, "/settings", Admin, a.patch},
	}
}

func (a *SettingsAPI) get(w http.ResponseWriter, r *http.Request) error {
	s, err := a.Settings.All(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}

func (a *SettingsAPI) patch(w http.ResponseWriter, r *http.Request) error {
	var in map[string]json.RawMessage
	if err := decode(r, &in); err != nil {
		return err
	}
	s, err := a.Settings.Patch(r.Context(), actor(r, a.ClientIP), in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}

// AuditAPI serves /audit (admin).
type AuditAPI struct {
	Audit *service.Auditor
	Now   func() time.Time
}

// Routes implements Registrar.
func (a *AuditAPI) Routes() []Route {
	return []Route{{http.MethodGet, "/audit", Admin, a.list}}
}

func (a *AuditAPI) list(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := meta.AuditFilter{Actor: q.Get("actor"), Action: q.Get("action")}
	now := a.Now()
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := ParseTimeParam(v, now)
			if err != nil {
				return err
			}
			*dst = t
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return errBadRequest("limit must be a positive integer")
		}
		f.Limit = n
	}
	entries, err := a.Audit.List(r.Context(), f)
	if err != nil {
		return err
	}
	out := make([]auditView, 0, len(entries))
	for i := range entries {
		out = append(out, auditView(entries[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// SystemAPI serves /system (admin).
type SystemAPI struct {
	System *service.System
}

// Routes implements Registrar.
func (a *SystemAPI) Routes() []Route {
	return []Route{{http.MethodGet, "/system", Admin, a.get}}
}

func (a *SystemAPI) get(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, a.System.Report(r.Context()))
	return nil
}
