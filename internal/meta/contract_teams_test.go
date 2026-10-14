// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
)

// teamsEnv is a database with three users for the teams contract.
type teamsEnv struct {
	db              *DB
	teams           *Teams
	alice, bob, eve *User
}

func newTeamsEnv(t *testing.T, open opener) *teamsEnv {
	t.Helper()
	ctx := context.Background()
	e := &teamsEnv{db: open(t)}
	e.teams = NewTeams(e.db)
	users := NewUsers(e.db)
	for _, u := range []**User{&e.alice, &e.bob, &e.eve} {
		*u = &User{PasswordHash: "h", Role: RoleViewer}
	}
	e.alice.Username, e.bob.Username, e.eve.Username = "alice", "bob", "eve"
	for _, u := range []*User{e.alice, e.bob, e.eve} {
		if err := users.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *teamsEnv) team(t *testing.T, name string) *Team {
	t.Helper()
	tm := &Team{Name: name, Description: name + " team"}
	if err := e.teams.Create(context.Background(), tm); err != nil {
		t.Fatal(err)
	}
	return tm
}

func contractTeams(t *testing.T, open opener) {
	t.Run("crud", func(t *testing.T) { contractTeamsCRUD(t, newTeamsEnv(t, open)) })
	t.Run("members and streams", func(t *testing.T) { contractTeamsSets(t, newTeamsEnv(t, open)) })
	t.Run("cascades and revoke", func(t *testing.T) { contractTeamsCascades(t, newTeamsEnv(t, open)) })
	t.Run("export import", func(t *testing.T) { contractTeamsExport(t, open, newTeamsEnv(t, open)) })
}

func contractTeamsCRUD(t *testing.T, e *teamsEnv) {
	ctx := context.Background()
	pay := e.team(t, "Payments")
	if err := e.teams.Create(ctx, &Team{Name: "PAYMENTS"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate name (case-insensitive): %v", err)
	}
	got, err := e.teams.Get(ctx, pay.ID)
	if err != nil || got.Name != "Payments" || got.Description != "Payments team" || !got.CreatedAt.Equal(t0) {
		t.Fatalf("get %+v %v", got, err)
	}
	ops := e.team(t, "ops")
	ops.Name = "payments"
	if err := e.teams.Update(ctx, ops); !errors.Is(err, ErrConflict) {
		t.Errorf("rename onto another team: %v", err)
	}
	ops.Name, ops.Description = "Operations", ""
	if err := e.teams.Update(ctx, ops); err != nil {
		t.Fatal(err)
	}
	list, err := e.teams.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Operations" || list[1].Name != "Payments" ||
		list[0].MemberCount != 0 || list[0].Streams == nil {
		t.Errorf("list %+v %v", list, err)
	}
	if n, err := e.teams.Count(ctx); err != nil || n != 2 {
		t.Errorf("count %d %v", n, err)
	}
	if err := e.teams.Delete(ctx, ops.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.teams.Delete(ctx, ops.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v", err)
	}
	if err := e.teams.Update(ctx, &Team{ID: "nope", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v", err)
	}
	if _, err := e.teams.Get(ctx, ops.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get deleted: %v", err)
	}
}

func contractTeamsSets(t *testing.T, e *teamsEnv) {
	ctx := context.Background()
	pay, ops := e.team(t, "payments"), e.team(t, "ops")
	ch, err := e.teams.SetMembers(ctx, pay.ID, []string{e.bob.ID, e.alice.ID, e.alice.ID})
	if err != nil || len(ch.Added) != 2 || len(ch.Removed) != 0 {
		t.Fatalf("set members %+v %v", ch, err)
	}
	ch, err = e.teams.SetMembers(ctx, pay.ID, []string{e.alice.ID})
	if err != nil || len(ch.Added) != 0 || !slices.Equal(ch.Removed, []string{e.bob.ID}) {
		t.Errorf("shrink members %+v %v", ch, err)
	}
	if _, err := e.teams.SetMembers(ctx, pay.ID, []string{"no-such-user"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if m, _ := e.teams.Members(ctx, pay.ID); len(m) != 1 || m[0].Username != "alice" || m[0].Role != RoleViewer {
		t.Errorf("members unchanged after failed set: %+v", m)
	}
	if _, err := e.teams.SetMembers(ctx, "no-such-team", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown team: %v", err)
	}
	if ch, err := e.teams.SetStreams(ctx, pay.ID, []string{"payments", "checkout"}); err != nil || len(ch.Added) != 2 {
		t.Fatalf("set streams %+v %v", ch, err)
	}
	if _, err := e.teams.SetStreams(ctx, ops.ID, []string{"infra", "checkout"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.teams.Streams(ctx, pay.ID); !slices.Equal(s, []string{"checkout", "payments"}) {
		t.Errorf("streams %v", s)
	}
	ch, err = e.teams.SetUserTeams(ctx, e.alice.ID, []string{ops.ID, pay.ID})
	if err != nil || !slices.Equal(ch.Added, []string{ops.ID}) || len(ch.Removed) != 0 {
		t.Errorf("set user teams %+v %v", ch, err)
	}
	if _, err := e.teams.SetUserTeams(ctx, "no-such-user", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := e.teams.SetUserTeams(ctx, e.bob.ID, []string{"no-such-team"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown team: %v", err)
	}
	n, streams, err := e.teams.StreamsForUser(ctx, e.alice.ID)
	if err != nil || n != 2 || !slices.Equal(streams, []string{"checkout", "infra", "payments"}) {
		t.Errorf("streams for alice %d %v %v", n, streams, err)
	}
	if n, streams, err := e.teams.StreamsForUser(ctx, e.eve.ID); err != nil || n != 0 || streams == nil || len(streams) != 0 {
		t.Errorf("streams for eve %d %v %v", n, streams, err)
	}
	if refs, _ := e.teams.UserTeams(ctx, e.alice.ID); len(refs) != 2 || refs[0].Name != "ops" || refs[1].Name != "payments" {
		t.Errorf("user teams %+v", refs)
	}
	if all, _ := e.teams.Memberships(ctx); len(all) != 1 || len(all[e.alice.ID]) != 2 {
		t.Errorf("memberships %+v", all)
	}
	if n, _ := e.teams.CountWithoutTeam(ctx); n != 2 {
		t.Errorf("users without team %d", n)
	}
	if list, _ := e.teams.List(ctx); len(list) != 2 || list[1].MemberCount != 1 || !slices.Equal(list[1].Streams, []string{"checkout", "payments"}) {
		t.Errorf("summaries %+v", list)
	}
}

func contractTeamsCascades(t *testing.T, e *teamsEnv) {
	ctx := context.Background()
	pay, ops := e.team(t, "payments"), e.team(t, "ops")
	_, _ = e.teams.SetMembers(ctx, pay.ID, []string{e.alice.ID, e.bob.ID})
	_, _ = e.teams.SetMembers(ctx, ops.ID, []string{e.bob.ID})
	_, _ = e.teams.SetStreams(ctx, pay.ID, []string{"payments", "shared"})
	_, _ = e.teams.SetStreams(ctx, ops.ID, []string{"shared"})
	if _, err := NewUsers(e.db).Delete(ctx, e.bob.ID, "heir"); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.teams.Members(ctx, ops.ID); len(m) != 0 {
		t.Errorf("deleted user still a member: %+v", m)
	}
	names, err := e.teams.RevokeStream(ctx, "shared")
	if err != nil || !slices.Equal(names, []string{"ops", "payments"}) {
		t.Errorf("revoke %v %v", names, err)
	}
	if s, _ := e.teams.Streams(ctx, pay.ID); !slices.Equal(s, []string{"payments"}) {
		t.Errorf("streams after revoke %v", s)
	}
	if names, err := e.teams.RevokeStream(ctx, "unknown"); err != nil || len(names) != 0 {
		t.Errorf("revoke unknown %v %v", names, err)
	}
	if err := e.teams.Delete(ctx, pay.ID); err != nil {
		t.Fatal(err)
	}
	if n, streams, _ := e.teams.StreamsForUser(ctx, e.alice.ID); n != 0 || len(streams) != 0 {
		t.Errorf("team delete did not cascade: %d %v", n, streams)
	}
}

func contractTeamsExport(t *testing.T, open opener, e *teamsEnv) {
	ctx := context.Background()
	pay := e.team(t, "payments")
	_, _ = e.teams.SetMembers(ctx, pay.ID, []string{e.alice.ID})
	_, _ = e.teams.SetStreams(ctx, pay.ID, []string{"payments"})
	var buf bytes.Buffer
	if err := e.db.ExportJSON(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	dst := open(t)
	if err := dst.ImportJSON(ctx, bytes.NewReader(buf.Bytes())); err != nil {
		t.Fatalf("import: %v", err)
	}
	n, streams, err := NewTeams(dst).StreamsForUser(ctx, e.alice.ID)
	if err != nil || n != 1 || !slices.Equal(streams, []string{"payments"}) {
		t.Errorf("imported grants %d %v %v", n, streams, err)
	}
}
