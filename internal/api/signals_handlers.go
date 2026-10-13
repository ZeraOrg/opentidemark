// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"time"

	"github.com/ZeraOrg/tidemark/internal/service"
)

// SignalsAPI serves /signals (SPEC §11.4): viewers read, editors create and change their own
// signals (admins any).
type SignalsAPI struct {
	Signals *service.Signals
}

// Routes implements Registrar.
func (a *SignalsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/signals", Viewer, a.list},
		{http.MethodPost, "/signals", Editor, a.create},
		{http.MethodGet, "/signals/{id}", Viewer, a.get},
		{http.MethodPut, "/signals/{id}", Editor, a.update},
		{http.MethodDelete, "/signals/{id}", Editor, a.delete},
	}
}

func (a *SignalsAPI) list(w http.ResponseWriter, r *http.Request) error {
	sigs, err := a.Signals.List(r.Context(), principal(r))
	if err != nil {
		return err
	}
	out := make([]signalView, 0, len(sigs))
	for _, s := range sigs {
		out = append(out, toSignalView(s))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *SignalsAPI) get(w http.ResponseWriter, r *http.Request) error {
	s, err := a.Signals.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSignalView(s))
	return nil
}

type signalRequest struct {
	Name      string     `json:"name"`
	TQL       string     `json:"tql"`
	Color     string     `json:"color"`
	Shared    bool       `json:"shared"`
	Source    string     `json:"source"`
	UpdatedAt *time.Time `json:"updatedAt"` // PUT: the updatedAt the client last saw
}

func (in signalRequest) input() service.SignalInput {
	return service.SignalInput{Name: in.Name, TQL: in.TQL, Color: in.Color, Shared: in.Shared, Source: in.Source}
}

func (a *SignalsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in signalRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	s, err := a.Signals.Create(r.Context(), principal(r), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toSignalView(s))
	return nil
}

func (a *SignalsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in signalRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	var expected time.Time
	if in.UpdatedAt != nil {
		expected = *in.UpdatedAt
	}
	s, err := a.Signals.Update(r.Context(), principal(r), r.PathValue("id"), in.input(), expected)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toSignalView(s))
	return nil
}

func (a *SignalsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Signals.Delete(r.Context(), principal(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
