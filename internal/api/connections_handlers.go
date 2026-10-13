// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/ZeraOrg/tidemark/internal/service"
)

// ConnectionsAPI serves /connections (admin).
type ConnectionsAPI struct {
	Connections *service.Connections
	PublicURL   *url.URL
	ClientIP    func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *ConnectionsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/connections", Admin, a.list},
		{http.MethodPost, "/connections", Admin, a.create},
		{http.MethodGet, "/connections/{id}", Admin, a.get},
		{http.MethodPatch, "/connections/{id}", Admin, a.update},
		{http.MethodDelete, "/connections/{id}", Admin, a.delete},
		{http.MethodPost, "/connections/{id}/rotate", Admin, a.rotate},
		{http.MethodPost, "/connections/{id}/revoke", Admin, a.revoke},
		{http.MethodGet, "/connections/{id}/snippets", Admin, a.snippets},
	}
}

// connectionRequest mirrors service.ConnectionInput for JSON (expiresAt: null clears it).
type connectionRequest struct {
	Name              *string         `json:"name"`
	Description       *string         `json:"description"`
	Protocols         *[]string       `json:"protocols"`
	DefaultStream     *string         `json:"defaultStream"`
	AllowedCIDRs      *[]string       `json:"allowedCidrs"`
	MinimumLevel      *string         `json:"minimumLevel"`
	AppliedProperties *map[string]any `json:"appliedProperties"`
	RateLimitEPS      *int            `json:"rateLimitEps"`
	ExpiresAt         optionalTime    `json:"expiresAt"`
	Key               *string         `json:"key"` // create only: admin-supplied key (SPEC §9.9)
}

// optionalTime distinguishes an absent field from an explicit null.
type optionalTime struct {
	set   bool
	value *time.Time
}

func (o *optionalTime) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		return nil
	}
	var t time.Time
	if err := t.UnmarshalJSON(b); err != nil {
		return err
	}
	o.value = &t
	return nil
}

func (c connectionRequest) input() service.ConnectionInput {
	in := service.ConnectionInput{Name: c.Name, Description: c.Description, Protocols: c.Protocols,
		DefaultStream: c.DefaultStream, AllowedCIDRs: c.AllowedCIDRs, MinimumLevel: c.MinimumLevel,
		AppliedProperties: c.AppliedProperties, RateLimitEPS: c.RateLimitEPS, Key: c.Key}
	if c.ExpiresAt.set {
		in.ExpiresAt = &c.ExpiresAt.value
	}
	return in
}

func (a *ConnectionsAPI) list(w http.ResponseWriter, r *http.Request) error {
	conns, err := a.Connections.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]connectionView, 0, len(conns))
	for i := range conns {
		out = append(out, toConnectionView(conns[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *ConnectionsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in connectionRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	c, key, err := a.Connections.Create(r.Context(), actor(r, a.ClientIP), in.input())
	if err != nil {
		return err
	}
	v := toConnectionView(c)
	v.Key = key
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func (a *ConnectionsAPI) get(w http.ResponseWriter, r *http.Request) error {
	c, err := a.Connections.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConnectionView(c))
	return nil
}

func (a *ConnectionsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in connectionRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := a.Connections.Update(r.Context(), actor(r, a.ClientIP), r.PathValue("id"), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConnectionView(c))
	return nil
}

func (a *ConnectionsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Connections.Delete(r.Context(), actor(r, a.ClientIP), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type rotateRequest struct {
	GraceMinutes int     `json:"graceMinutes"`
	Key          *string `json:"key"` // optional admin-supplied key (SPEC §9.9)
}

func (a *ConnectionsAPI) rotate(w http.ResponseWriter, r *http.Request) error {
	var in rotateRequest
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return err
		}
	}
	c, key, err := a.Connections.Rotate(r.Context(), actor(r, a.ClientIP), r.PathValue("id"), in.GraceMinutes, in.Key)
	if err != nil {
		return err
	}
	v := toConnectionView(c)
	v.Key = key
	writeJSON(w, http.StatusOK, v)
	return nil
}

func (a *ConnectionsAPI) revoke(w http.ResponseWriter, r *http.Request) error {
	c, err := a.Connections.Revoke(r.Context(), actor(r, a.ClientIP), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toConnectionView(c))
	return nil
}

func (a *ConnectionsAPI) snippets(w http.ResponseWriter, r *http.Request) error {
	c, err := a.Connections.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, service.Snippets(c, a.PublicURL))
	return nil
}
