// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

// fakeTeamStreams answers StreamsForUser from a map of user id to team grants.
type fakeTeamStreams struct {
	teams map[string][][]string // user ⇒ the stream grants of each of their teams
	err   error
}

func (f fakeTeamStreams) StreamsForUser(_ context.Context, userID string) (int, []string, error) {
	if f.err != nil {
		return 0, nil, f.err
	}
	var all []string
	for _, g := range f.teams[userID] {
		all = append(all, g...)
	}
	return len(f.teams[userID]), all, nil
}

// fakeSettings is an in-memory SettingsReader.
type fakeSettings map[string]string

func (f fakeSettings) Get(_ context.Context, key string) (json.RawMessage, error) {
	v, ok := f[key]
	if !ok {
		return nil, meta.ErrNotFound
	}
	return json.RawMessage(v), nil
}

func TestAccessResolve(t *testing.T) {
	teams := fakeTeamStreams{teams: map[string][][]string{
		"u-union":   {{"payments", "checkout"}, {"infra", "checkout"}},
		"u-nogrant": {{}},
		"u-admin":   {{"payments"}},
	}}
	deny, allow := fakeSettings{SettingAccess: `{"denyByDefault":true}`}, fakeSettings{}
	session := func(id string, role auth.Role) auth.Principal {
		return auth.Principal{Kind: auth.KindSession, UserID: id, Role: role, UserRole: role}
	}
	readToken := auth.Principal{Kind: auth.KindToken, UserID: "u-admin", Role: auth.RoleViewer, UserRole: auth.RoleAdmin}
	tests := []struct {
		name     string
		p        auth.Principal
		settings fakeSettings
		scope    string
		mode     string
	}{
		{"admin", session("u-admin", auth.RoleAdmin), deny, "all", StreamAccessAdmin},
		{"admin read token", readToken, deny, "all", StreamAccessAdmin},
		{"union of teams", session("u-union", auth.RoleViewer), allow, "[checkout infra payments]", StreamAccessTeams},
		{"team without grants", session("u-nogrant", auth.RoleEditor), allow, "[]", StreamAccessTeams},
		{"no team, deny off", session("u-none", auth.RoleViewer), allow, "all", StreamAccessDefaultAll},
		{"no team, explicit off", session("u-none", auth.RoleViewer), fakeSettings{SettingAccess: `{"denyByDefault":false}`},
			"all", StreamAccessDefaultAll},
		{"no team, deny on", session("u-none", auth.RoleEditor), deny, "[]", StreamAccessDefaultNone},
		{"token of a team member", auth.Principal{Kind: auth.KindToken, UserID: "u-union", Role: auth.RoleViewer,
			UserRole: auth.RoleEditor}, deny, "[checkout infra payments]", StreamAccessTeams},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewAccess(teams, tc.settings).Resolve(context.Background(), tc.p)
			if err != nil || r.Scope.String() != tc.scope || r.Mode != tc.mode {
				t.Errorf("Resolve = %s %s %v, want %s %s", r.Scope, r.Mode, err, tc.scope, tc.mode)
			}
			s, err := NewAccess(teams, tc.settings).Scope(context.Background(), tc.p)
			if err != nil || s.String() != tc.scope {
				t.Errorf("Scope = %s %v", s, err)
			}
		})
	}
}

func TestAccessResolveErrors(t *testing.T) {
	ctx := context.Background()
	viewer := auth.Principal{UserID: "u", Role: auth.RoleViewer, UserRole: auth.RoleViewer}
	if s, err := NewAccess(fakeTeamStreams{}, fakeSettings{}).Scope(ctx, auth.Principal{}); !errors.Is(err, ErrNoPrincipal) || s.IsSet() {
		t.Errorf("no principal: %s %v", s, err)
	}
	boom := errors.New("boom")
	if s, err := NewAccess(fakeTeamStreams{err: boom}, fakeSettings{}).Scope(ctx, viewer); !errors.Is(err, boom) || s.IsSet() {
		t.Errorf("repository error: %s %v", s, err)
	}
	if s, err := NewAccess(fakeTeamStreams{}, fakeSettings{SettingAccess: `"x"`}).Scope(ctx, viewer); err == nil || s.IsSet() {
		t.Errorf("corrupt setting: %s %v", s, err)
	}
}

func TestValidateAccess(t *testing.T) {
	for v, ok := range map[string]bool{
		`{"denyByDefault":true}`: true, `{"denyByDefault":false}`: true, `{}`: false, `{"denyByDefault":1}`: false,
		`{"denyByDefault":true,"x":1}`: false, `null`: false, `[]`: false,
	} {
		if _, err := validateAccess(json.RawMessage(v)); (err == nil) != ok {
			t.Errorf("validateAccess(%s) = %v, want ok=%v", v, err, ok)
		}
	}
}
