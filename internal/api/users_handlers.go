// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/netip"

	"github.com/ZeraOrg/tidemark/internal/service"
)

// UsersAPI serves /users (admin).
type UsersAPI struct {
	Users    *service.Users
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *UsersAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/users", Admin, a.list},
		{http.MethodPost, "/users", Admin, a.create},
		{http.MethodGet, "/users/{id}", Admin, a.get},
		{http.MethodPatch, "/users/{id}", Admin, a.update},
		{http.MethodDelete, "/users/{id}", Admin, a.delete},
	}
}

func (a *UsersAPI) list(w http.ResponseWriter, r *http.Request) error {
	users, err := a.Users.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]userView, 0, len(users))
	for i := range users {
		out = append(out, toUserView(users[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type createUserRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

func (a *UsersAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in createUserRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	u, err := a.Users.Create(r.Context(), actor(r, a.ClientIP), service.NewUser(in))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toUserView(u))
	return nil
}

func (a *UsersAPI) get(w http.ResponseWriter, r *http.Request) error {
	u, err := a.Users.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toUserView(u))
	return nil
}

type updateUserRequest struct {
	DisplayName *string `json:"displayName"`
	Role        *string `json:"role"`
	Disabled    *bool   `json:"disabled"`
	Password    *string `json:"password"`
}

func (a *UsersAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in updateUserRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	u, err := a.Users.Update(r.Context(), actor(r, a.ClientIP), r.PathValue("id"), service.UserPatch(in))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toUserView(u))
	return nil
}

func (a *UsersAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Users.Delete(r.Context(), actor(r, a.ClientIP), principal(r).UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
