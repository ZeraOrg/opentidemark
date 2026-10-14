// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

type teamEnv struct {
	teams *Teams
	users *meta.Users
	repo  *meta.Teams
	audit *meta.AuditLog
	db    *meta.DB
}

func newTeamEnv(t *testing.T) *teamEnv {
	t.Helper()
	db := openDB(t)
	e := &teamEnv{users: meta.NewUsers(db), repo: meta.NewTeams(db), audit: meta.NewAuditLog(db), db: db}
	e.teams = NewTeams(TeamsDeps{Repo: e.repo, Users: e.users, Settings: meta.NewSettings(db),
		Audit: NewAuditor(e.audit, quiet(), time.Now)})
	return e
}

func (e *teamEnv) user(t *testing.T, name string) string {
	t.Helper()
	u := &meta.User{Username: name, PasswordHash: "h", Role: meta.RoleViewer}
	if err := e.users.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// lastAudit returns the details of the newest audit entry of action.
func (e *teamEnv) lastAudit(t *testing.T, action string) map[string]any {
	t.Helper()
	entries, err := e.audit.List(context.Background(), meta.AuditFilter{Action: action})
	if err != nil || len(entries) == 0 {
		t.Fatalf("no %s audit entry: %v", action, err)
	}
	var d map[string]any
	if err := json.Unmarshal(entries[0].Details, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTeamValidation(t *testing.T) {
	e := newTeamEnv(t)
	ctx := context.Background()
	var ve *ValidationError
	for _, in := range []TeamInput{{Name: " "}, {Name: strings.Repeat("é", MaxTeamNameLen+1)},
		{Name: "ok", Description: strings.Repeat("x", MaxTeamDescription+1)}} {
		if _, err := e.teams.Create(ctx, SystemActor, in); !errors.As(err, &ve) {
			t.Errorf("%q accepted: %v", in.Name, err)
		}
	}
	if _, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: strings.Repeat("é", MaxTeamNameLen)}); err != nil {
		t.Errorf("64 characters: %v", err)
	}
	if _, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: " Payments "}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: "PAYMENTS"}); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	for i := 2; i < MaxTeams; i++ {
		if _, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: fmt.Sprintf("t%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: "one too many"}); !errors.As(err, &ve) {
		t.Errorf("team limit: %v", err)
	}
}

func TestTeamLifecycle(t *testing.T) {
	e := newTeamEnv(t)
	ctx := context.Background()
	alice, bob := e.user(t, "alice"), e.user(t, "bob")
	team, err := e.teams.Create(ctx, SystemActor, TeamInput{Name: "Payments", Description: "pay"})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.lastAudit(t, ActionTeamCreate); d["name"] != "Payments" {
		t.Errorf("create audit %v", d)
	}
	name := "Billing"
	if _, err := e.teams.Update(ctx, SystemActor, team.ID, TeamPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if d := e.lastAudit(t, ActionTeamUpdate); d["name"] != "Billing" || d["previousName"] != "Payments" {
		t.Errorf("update audit %v", d)
	}
	long := strings.Repeat("x", MaxTeamDescription+1)
	if _, err := e.teams.Update(ctx, SystemActor, team.ID, TeamPatch{Description: &long}); err == nil {
		t.Error("long description accepted")
	}
	var ve *ValidationError
	if _, err := e.teams.SetMembers(ctx, SystemActor, team.ID, []string{alice, "nope"}); !errors.As(err, &ve) || ve.Field != "userIds" {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := e.teams.SetMembers(ctx, SystemActor, "nope", nil); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("unknown team: %v", err)
	}
	d, err := e.teams.SetMembers(ctx, SystemActor, team.ID, []string{bob, alice, alice})
	if err != nil || len(d.Members) != 2 || d.Members[0].Username != "alice" {
		t.Fatalf("members %+v %v", d.Members, err)
	}
	if a := e.lastAudit(t, ActionTeamMembers); len(a["added"].([]any)) != 2 || len(a["removed"].([]any)) != 0 {
		t.Errorf("members audit %v", a)
	}
	for _, bad := range [][]string{{"Bad Name"}, make([]string, MaxTeamStreams+1)} {
		if _, err := e.teams.SetStreams(ctx, SystemActor, team.ID, bad); !errors.As(err, &ve) || ve.Field != "streams" {
			t.Errorf("streams %d accepted: %v", len(bad), err)
		}
	}
	if d, err = e.teams.SetStreams(ctx, SystemActor, team.ID, []string{"payments", "billing"}); err != nil ||
		!slices.Equal(d.Streams, []string{"billing", "payments"}) {
		t.Fatalf("streams %v %v", d.Streams, err)
	}
	if _, err := e.teams.SetStreams(ctx, SystemActor, "nope", []string{"a"}); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("streams of unknown team: %v", err)
	}
	acc, err := e.teams.Access(ctx)
	if err != nil || acc.DenyByDefault || acc.UsersWithoutTeam != 0 {
		t.Errorf("access %+v %v", acc, err)
	}
	if err := e.teams.Delete(ctx, SystemActor, team.ID); err != nil {
		t.Fatal(err)
	}
	if a := e.lastAudit(t, ActionTeamDelete); a["name"] != "Billing" || len(a["members"].([]any)) != 2 || len(a["streams"].([]any)) != 2 {
		t.Errorf("delete audit %v", a)
	}
	if acc, _ := e.teams.Access(ctx); acc.UsersWithoutTeam != 2 {
		t.Errorf("users without team after delete: %d", acc.UsersWithoutTeam)
	}
}

func TestUserTeams(t *testing.T) {
	e := newTeamEnv(t)
	ctx := context.Background()
	alice := e.user(t, "alice")
	a, _ := e.teams.Create(ctx, SystemActor, TeamInput{Name: "A"})
	b, _ := e.teams.Create(ctx, SystemActor, TeamInput{Name: "B"})
	if _, err := e.teams.SetUserTeams(ctx, SystemActor, "nope", nil); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	var ve *ValidationError
	if _, err := e.teams.SetUserTeams(ctx, SystemActor, alice, []string{a.ID, "nope"}); !errors.As(err, &ve) {
		t.Errorf("unknown team: %v", err)
	}
	refs, err := e.teams.SetUserTeams(ctx, SystemActor, alice, []string{b.ID, a.ID})
	if err != nil || len(refs) != 2 || refs[0].Name != "A" {
		t.Fatalf("teams %+v %v", refs, err)
	}
	if refs, _ = e.teams.SetUserTeams(ctx, SystemActor, alice, nil); len(refs) != 0 || refs == nil {
		t.Errorf("cleared teams %#v", refs)
	}
	if d := e.lastAudit(t, ActionUserTeams); len(d["removed"].([]any)) != 2 {
		t.Errorf("user.teams audit %v", d)
	}
}

func TestUserDeleteRecordsTeams(t *testing.T) {
	e := newTeamEnv(t)
	ctx := context.Background()
	alice := e.user(t, "alice")
	team, _ := e.teams.Create(ctx, SystemActor, TeamInput{Name: "Ops"})
	if _, err := e.teams.SetMembers(ctx, SystemActor, team.ID, []string{alice}); err != nil {
		t.Fatal(err)
	}
	users := NewUsers(UsersDeps{Store: e.users, Settings: meta.NewSettings(e.db), Teams: e.repo,
		Audit: NewAuditor(e.audit, quiet(), time.Now), Now: time.Now, Sessions: noopRevoker{}, Tokens: meta.NewAPITokens(e.db)})
	if err := users.Delete(ctx, SystemActor, "someone-else", alice); err != nil {
		t.Fatal(err)
	}
	if d := e.lastAudit(t, ActionUserDelete); d["username"] != "alice" || len(d["teams"].([]any)) != 1 {
		t.Errorf("user.delete audit %v", d)
	}
}
