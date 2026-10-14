// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// TeamsAPI serves /teams (admin, SPEC §11.5, §15.10).
type TeamsAPI struct {
	Teams    *service.Teams
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *TeamsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/teams", Admin, a.list},
		{http.MethodPost, "/teams", Admin, a.create},
		{http.MethodGet, "/teams/access", Admin, a.access},
		{http.MethodGet, "/teams/{id}", Admin, a.get},
		{http.MethodPatch, "/teams/{id}", Admin, a.update},
		{http.MethodDelete, "/teams/{id}", Admin, a.delete},
		{http.MethodPut, "/teams/{id}/members", Admin, a.setMembers},
		{http.MethodPut, "/teams/{id}/streams", Admin, a.setStreams},
	}
}

type teamRefView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toTeamRefViews(refs []meta.TeamRef) []teamRefView {
	out := make([]teamRefView, 0, len(refs))
	for _, r := range refs {
		out = append(out, teamRefView{ID: r.ID, Name: r.Name})
	}
	return out
}

type teamSummaryView struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int       `json:"memberCount"`
	Streams     []string  `json:"streams"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type teamMemberView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
}

type teamView struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Members     []teamMemberView `json:"members"`
	Streams     []string         `json:"streams"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

func toTeamView(t service.TeamDetail) teamView {
	members := make([]teamMemberView, 0, len(t.Members))
	for _, m := range t.Members {
		members = append(members, teamMemberView{ID: m.UserID, Username: m.Username, DisplayName: m.DisplayName, Role: m.Role})
	}
	return teamView{ID: t.ID, Name: t.Name, Description: t.Description, Members: members, Streams: nonNil(t.Streams),
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func (a *TeamsAPI) list(w http.ResponseWriter, r *http.Request) error {
	teams, err := a.Teams.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]teamSummaryView, 0, len(teams))
	for _, t := range teams {
		out = append(out, teamSummaryView{ID: t.ID, Name: t.Name, Description: t.Description, MemberCount: t.MemberCount,
			Streams: nonNil(t.Streams), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *TeamsAPI) access(w http.ResponseWriter, r *http.Request) error {
	acc, err := a.Teams.Access(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"denyByDefault": acc.DenyByDefault, "usersWithoutTeam": acc.UsersWithoutTeam})
	return nil
}

type createTeamRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (a *TeamsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in createTeamRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	t, err := a.Teams.Create(r.Context(), actor(r, a.ClientIP), service.TeamInput(in))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toTeamView(service.TeamDetail{Team: t}))
	return nil
}

func (a *TeamsAPI) get(w http.ResponseWriter, r *http.Request) error {
	t, err := a.Teams.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTeamView(t))
	return nil
}

type updateTeamRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

func (a *TeamsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in updateTeamRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := a.Teams.Update(r.Context(), actor(r, a.ClientIP), id, service.TeamPatch(in)); err != nil {
		return err
	}
	return a.get(w, r)
}

func (a *TeamsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Teams.Delete(r.Context(), actor(r, a.ClientIP), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type teamMembersRequest struct {
	UserIDs []string `json:"userIds"`
}

func (a *TeamsAPI) setMembers(w http.ResponseWriter, r *http.Request) error {
	var in teamMembersRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	t, err := a.Teams.SetMembers(r.Context(), actor(r, a.ClientIP), r.PathValue("id"), in.UserIDs)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTeamView(t))
	return nil
}

type teamStreamsRequest struct {
	Streams []string `json:"streams"`
}

func (a *TeamsAPI) setStreams(w http.ResponseWriter, r *http.Request) error {
	var in teamStreamsRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	t, err := a.Teams.SetStreams(r.Context(), actor(r, a.ClientIP), r.PathValue("id"), in.Streams)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toTeamView(t))
	return nil
}
