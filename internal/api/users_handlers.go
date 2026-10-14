// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/netip"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// UsersAPI serves /users (admin). User objects include the user's teams.
type UsersAPI struct {
	Users    *service.Users
	Teams    *service.Teams
	ClientIP func(*http.Request) netip.Addr
}

// userTeamsView is a user with its teams (SPEC §11.2).
type userTeamsView struct {
	userView
	Teams []teamRefView `json:"teams"`
}

// withTeams returns the view of u with its teams loaded.
func (a *UsersAPI) withTeams(r *http.Request, u meta.User) (userTeamsView, error) {
	refs, err := a.Teams.UserTeams(r.Context(), u.ID)
	if err != nil {
		return userTeamsView{}, err
	}
	return userTeamsView{userView: toUserView(u), Teams: toTeamRefViews(refs)}, nil
}

// writeUser writes u with its teams.
func (a *UsersAPI) writeUser(w http.ResponseWriter, r *http.Request, status int, u meta.User) error {
	v, err := a.withTeams(r, u)
	if err != nil {
		return err
	}
	writeJSON(w, status, v)
	return nil
}

// Routes implements Registrar.
func (a *UsersAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/users", Admin, a.list},
		{http.MethodPost, "/users", Admin, a.create},
		{http.MethodGet, "/users/{id}", Admin, a.get},
		{http.MethodPatch, "/users/{id}", Admin, a.update},
		{http.MethodDelete, "/users/{id}", Admin, a.delete},
		{http.MethodPut, "/users/{id}/teams", Admin, a.setTeams},
	}
}

func (a *UsersAPI) list(w http.ResponseWriter, r *http.Request) error {
	users, err := a.Users.List(r.Context())
	if err != nil {
		return err
	}
	teams, err := a.Teams.Memberships(r.Context())
	if err != nil {
		return err
	}
	out := make([]userTeamsView, 0, len(users))
	for i := range users {
		out = append(out, userTeamsView{userView: toUserView(users[i]), Teams: toTeamRefViews(teams[users[i].ID])})
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
	return a.writeUser(w, r, http.StatusCreated, u)
}

func (a *UsersAPI) get(w http.ResponseWriter, r *http.Request) error {
	u, err := a.Users.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	return a.writeUser(w, r, http.StatusOK, u)
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
	return a.writeUser(w, r, http.StatusOK, u)
}

type userTeamsRequest struct {
	TeamIDs []string `json:"teamIds"`
}

func (a *UsersAPI) setTeams(w http.ResponseWriter, r *http.Request) error {
	var in userTeamsRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := a.Teams.SetUserTeams(r.Context(), actor(r, a.ClientIP), id, in.TeamIDs); err != nil {
		return err
	}
	u, err := a.Users.Get(r.Context(), id)
	if err != nil {
		return err
	}
	return a.writeUser(w, r, http.StatusOK, u)
}

func (a *UsersAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Users.Delete(r.Context(), actor(r, a.ClientIP), principal(r).UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
