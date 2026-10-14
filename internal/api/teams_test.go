// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

type meView struct {
	Role         string        `json:"role"`
	AllStreams   bool          `json:"allStreams"`
	Streams      []string      `json:"streams"`
	Teams        []teamRefView `json:"teams"`
	StreamAccess string        `json:"streamAccess"`
}

// userID returns the id of a harness user by username.
func (e *env) userID(username string) string {
	e.t.Helper()
	for _, u := range decodeBody[[]userTeamsView](e.t, e.do(e.sessions["admin"], "GET", "/users", nil)) {
		if u.Username == username {
			return u.ID
		}
	}
	e.t.Fatalf("no user %s", username)
	return ""
}

func TestTeamsFlow(t *testing.T) {
	e := newEnv(t)
	admin, viewer := e.sessions["admin"], e.sessions["viewer"]
	if me := decodeBody[meView](t, e.do(viewer, "GET", "/auth/me", nil)); !me.AllStreams || me.StreamAccess != "default-all" ||
		me.Streams == nil || me.Teams == nil {
		t.Errorf("team-less viewer %+v", me)
	}
	if me := decodeBody[meView](t, e.do(admin, "GET", "/auth/me", nil)); !me.AllStreams || me.StreamAccess != "admin" {
		t.Errorf("admin %+v", me)
	}
	w := e.do(admin, "POST", "/teams", map[string]string{"name": "Payments", "description": "Payment services"})
	team := decodeBody[teamView](t, w)
	if w.Code != http.StatusCreated || team.ID == "" || team.Members == nil || team.Streams == nil {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "POST", "/teams", map[string]string{"name": "payments"}); w.Code != http.StatusConflict {
		t.Errorf("duplicate %d", w.Code)
	}
	if w := e.do(admin, "POST", "/teams", map[string]any{"name": "x", "bogus": 1}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown field %d", w.Code)
	}
	viewerID := e.userID("viewer1")
	w = e.do(admin, "PUT", "/teams/"+team.ID+"/members", map[string]any{"userIds": []string{viewerID}})
	if got := decodeBody[teamView](t, w); w.Code != 200 || len(got.Members) != 1 || got.Members[0].Username != "viewer1" ||
		got.Members[0].Role != "viewer" {
		t.Errorf("members %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "PUT", "/teams/"+team.ID+"/members", map[string]any{"userIds": []string{"nope"}}); w.Code != http.StatusBadRequest {
		t.Errorf("unknown member %d", w.Code)
	}
	if me := decodeBody[meView](t, e.do(viewer, "GET", "/auth/me", nil)); me.AllStreams || me.StreamAccess != "teams" ||
		len(me.Streams) != 0 || len(me.Teams) != 1 || me.Teams[0].Name != "Payments" {
		t.Errorf("member of a team without grants %+v", me)
	}
	w = e.do(admin, "PUT", "/teams/"+team.ID+"/streams", map[string]any{"streams": []string{"payments", "app"}})
	if got := decodeBody[teamView](t, w); w.Code != 200 || !slices.Equal(got.Streams, []string{"app", "payments"}) {
		t.Errorf("streams %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "PUT", "/teams/"+team.ID+"/streams", map[string]any{"streams": []string{"Bad Name"}}); w.Code != http.StatusBadRequest {
		t.Errorf("invalid stream %d", w.Code)
	}
	if me := decodeBody[meView](t, e.do(viewer, "GET", "/auth/me", nil)); !slices.Equal(me.Streams, []string{"app", "payments"}) {
		t.Errorf("granted streams %+v", me)
	}
	w = e.do(admin, "PATCH", "/teams/"+team.ID, map[string]string{"name": "Billing"})
	if got := decodeBody[teamView](t, w); w.Code != 200 || got.Name != "Billing" || got.Description != "Payment services" ||
		len(got.Members) != 1 {
		t.Errorf("patch %d %s", w.Code, w.Body.String())
	}
	list := decodeBody[[]teamSummaryView](t, e.do(admin, "GET", "/teams", nil))
	if len(list) != 1 || list[0].MemberCount != 1 || len(list[0].Streams) != 2 {
		t.Errorf("list %+v", list)
	}
	acc := decodeBody[map[string]any](t, e.do(admin, "GET", "/teams/access", nil))
	if acc["denyByDefault"] != false || acc["usersWithoutTeam"] != float64(2) {
		t.Errorf("access %v", acc)
	}
	users := decodeBody[[]userTeamsView](t, e.do(admin, "GET", "/users", nil))
	for _, u := range users {
		if want := u.Username == "viewer1"; (len(u.Teams) == 1) != want || u.Teams == nil {
			t.Errorf("user %s teams %+v", u.Username, u.Teams)
		}
	}
	if w := e.do(admin, "GET", "/teams/nope", nil); w.Code != http.StatusNotFound {
		t.Errorf("missing team %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/teams/"+team.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete %d", w.Code)
	}
	if me := decodeBody[meView](t, e.do(viewer, "GET", "/auth/me", nil)); !me.AllStreams || len(me.Teams) != 0 {
		t.Errorf("after delete %+v", me)
	}
	entries := decodeBody[[]auditView](t, e.do(admin, "GET", "/audit", nil))
	var actions []string
	for _, a := range entries {
		if a.Target == team.ID {
			actions = append(actions, a.Action)
		}
	}
	for _, want := range []string{"team.create", "team.members", "team.streams", "team.update", "team.delete"} {
		if !slices.Contains(actions, want) {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
}

func TestUserTeamsRoute(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	a := decodeBody[teamView](t, e.do(admin, "POST", "/teams", map[string]string{"name": "A"}))
	b := decodeBody[teamView](t, e.do(admin, "POST", "/teams", map[string]string{"name": "B"}))
	editorID := e.userID("editor1")
	w := e.do(admin, "PUT", "/users/"+editorID+"/teams", map[string]any{"teamIds": []string{b.ID, a.ID}})
	u := decodeBody[userTeamsView](t, w)
	if w.Code != 200 || u.Username != "editor1" || len(u.Teams) != 2 || u.Teams[0].Name != "A" {
		t.Errorf("set teams %d %s", w.Code, w.Body.String())
	}
	if got := decodeBody[userTeamsView](t, e.do(admin, "GET", "/users/"+editorID, nil)); len(got.Teams) != 2 {
		t.Errorf("get user %+v", got)
	}
	if w := e.do(admin, "PUT", "/users/"+editorID+"/teams", map[string]any{"teamIds": []string{"nope"}}); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "teamIds") {
		t.Errorf("unknown team %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "PUT", "/users/nope/teams", map[string]any{"teamIds": []string{}}); w.Code != http.StatusNotFound {
		t.Errorf("unknown user %d", w.Code)
	}
	entries := decodeBody[[]auditView](t, e.do(admin, "GET", "/audit?action=user.teams", nil))
	if len(entries) != 1 || entries[0].Target != editorID || !strings.Contains(string(entries[0].Details), b.ID) {
		t.Errorf("audit %+v", entries)
	}
}
