// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"

	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/update"
)

// Updater is the update API of the node (implemented by *update.Manager).
type Updater interface {
	Status() update.Status
	CheckNow(ctx context.Context) (update.Status, error)
	Apply(version string) (update.Status, error)
}

// UpdateAPI serves /system/update (admin, SPEC §11.5, §17.7).
type UpdateAPI struct {
	Updates  Updater
	Audit    *service.Auditor
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *UpdateAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/system/update", Admin, a.get},
		{http.MethodPost, "/system/update/check", Admin, a.check},
		{http.MethodPost, "/system/update/apply", Admin, a.apply},
	}
}

func (a *UpdateAPI) get(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, a.Updates.Status())
	return nil
}

// check runs the release check now. A failed lookup is part of the status (200); 429 within a
// minute of the previous check, 409 when the check is disabled or this is a development build.
func (a *UpdateAPI) check(w http.ResponseWriter, r *http.Request) error {
	s, err := a.Updates.CheckNow(r.Context())
	var tooSoon *update.TooSoonError
	if errors.As(err, &tooSoon) {
		return &rateLimited{retryAfter: tooSoon.RetryAfter}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s)
	return nil
}

// apply starts installing the latest release: 202 with the status, 400 (details.field=version)
// unless version is the latest release, 409 (details.reason) when the update cannot be applied.
func (a *UpdateAPI) apply(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Version string `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	s, err := a.Updates.Apply(body.Version)
	if err != nil {
		return err
	}
	a.Audit.Record(r.Context(), actor(r, a.ClientIP), service.ActionUpdateApply, body.Version,
		map[string]any{"from": s.Current, "to": body.Version})
	writeJSON(w, http.StatusAccepted, s)
	return nil
}
