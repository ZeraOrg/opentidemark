// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Team audit actions and limits (SPEC §11.5, §15.5, §15.10).
const (
	ActionTeamCreate   = "team.create"
	ActionTeamUpdate   = "team.update"
	ActionTeamDelete   = "team.delete"
	ActionTeamMembers  = "team.members"
	ActionTeamStreams  = "team.streams"
	ActionUserTeams    = "user.teams"
	MaxTeams           = 500
	MaxTeamNameLen     = 64
	MaxTeamDescription = 1024
	MaxTeamStreams     = 1000
	MaxTeamMembers     = 10000
)

// TeamRepo is the team persistence (meta.Teams).
type TeamRepo interface {
	Create(ctx context.Context, t *meta.Team) error
	Get(ctx context.Context, id string) (meta.Team, error)
	Count(ctx context.Context) (int, error)
	List(ctx context.Context) ([]meta.TeamSummary, error)
	Update(ctx context.Context, t *meta.Team) error
	Delete(ctx context.Context, id string) error
	Members(ctx context.Context, teamID string) ([]meta.TeamMember, error)
	Streams(ctx context.Context, teamID string) ([]string, error)
	UserTeams(ctx context.Context, userID string) ([]meta.TeamRef, error)
	Memberships(ctx context.Context) (map[string][]meta.TeamRef, error)
	CountWithoutTeam(ctx context.Context) (int, error)
	SetMembers(ctx context.Context, teamID string, userIDs []string) (meta.SetChange, error)
	SetStreams(ctx context.Context, teamID string, streams []string) (meta.SetChange, error)
	SetUserTeams(ctx context.Context, userID string, teamIDs []string) (meta.SetChange, error)
}

// UserGetter loads a user.
type UserGetter interface {
	Get(ctx context.Context, id string) (meta.User, error)
}

// TeamsDeps are the collaborators of Teams.
type TeamsDeps struct {
	Repo     TeamRepo
	Users    UserGetter
	Settings SettingsReader
	Audit    *Auditor
}

// Teams manages teams, their members and their stream grants (SPEC §15.10). Changes apply to
// the next request of every member, because scopes are resolved per request.
type Teams struct{ d TeamsDeps }

// NewTeams returns the team service.
func NewTeams(d TeamsDeps) *Teams { return &Teams{d: d} }

// TeamDetail is a team with its members and granted streams.
type TeamDetail struct {
	meta.Team
	Members []meta.TeamMember
	Streams []string
}

// TeamInput creates a team.
type TeamInput struct {
	Name        string
	Description string
}

// TeamPatch changes a team; nil fields are kept.
type TeamPatch struct {
	Name        *string
	Description *string
}

// TeamAccess is the overview of the access settings: deny-by-default and how many users it
// affects.
type TeamAccess struct {
	DenyByDefault    bool
	UsersWithoutTeam int
}

// List returns every team with its member count and streams, ordered by name.
func (s *Teams) List(ctx context.Context) ([]meta.TeamSummary, error) { return s.d.Repo.List(ctx) }

// Get returns a team with members and streams.
func (s *Teams) Get(ctx context.Context, id string) (TeamDetail, error) {
	t, err := s.d.Repo.Get(ctx, id)
	if err != nil {
		return TeamDetail{}, err
	}
	members, err := s.d.Repo.Members(ctx, id)
	if err != nil {
		return TeamDetail{}, err
	}
	streams, err := s.d.Repo.Streams(ctx, id)
	if err != nil {
		return TeamDetail{}, err
	}
	return TeamDetail{Team: t, Members: members, Streams: streams}, nil
}

// Access returns the deny-by-default setting and the number of users in no team.
func (s *Teams) Access(ctx context.Context) (TeamAccess, error) {
	st, err := LoadAccess(ctx, s.d.Settings)
	if err != nil {
		return TeamAccess{}, err
	}
	n, err := s.d.Repo.CountWithoutTeam(ctx)
	if err != nil {
		return TeamAccess{}, err
	}
	return TeamAccess{DenyByDefault: st.DenyByDefault, UsersWithoutTeam: n}, nil
}

func validTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxTeamNameLen {
		return "", invalid("name", fmt.Sprintf("is required (at most %d characters)", MaxTeamNameLen))
	}
	return name, nil
}

func validTeamDescription(d string) (string, error) {
	if utf8.RuneCountInString(d) > MaxTeamDescription {
		return "", invalid("description", fmt.Sprintf("at most %d characters", MaxTeamDescription))
	}
	return strings.TrimSpace(d), nil
}

// Create adds a team (names are unique ignoring case: meta.ErrConflict).
func (s *Teams) Create(ctx context.Context, actor Actor, in TeamInput) (meta.Team, error) {
	name, err := validTeamName(in.Name)
	if err != nil {
		return meta.Team{}, err
	}
	desc, err := validTeamDescription(in.Description)
	if err != nil {
		return meta.Team{}, err
	}
	n, err := s.d.Repo.Count(ctx)
	if err != nil {
		return meta.Team{}, err
	}
	if n >= MaxTeams {
		return meta.Team{}, invalid("name", fmt.Sprintf("at most %d teams", MaxTeams))
	}
	t := meta.Team{Name: name, Description: desc}
	if err := s.d.Repo.Create(ctx, &t); err != nil {
		return meta.Team{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionTeamCreate, t.ID, map[string]any{"name": t.Name})
	return t, nil
}

// Update renames a team or changes its description.
func (s *Teams) Update(ctx context.Context, actor Actor, id string, p TeamPatch) (meta.Team, error) {
	t, err := s.d.Repo.Get(ctx, id)
	if err != nil {
		return meta.Team{}, err
	}
	details := map[string]any{}
	if p.Name != nil {
		name, err := validTeamName(*p.Name)
		if err != nil {
			return meta.Team{}, err
		}
		if name != t.Name {
			details["previousName"] = t.Name
		}
		t.Name = name
	}
	if p.Description != nil {
		if t.Description, err = validTeamDescription(*p.Description); err != nil {
			return meta.Team{}, err
		}
	}
	if err := s.d.Repo.Update(ctx, &t); err != nil {
		return meta.Team{}, err
	}
	details["name"] = t.Name
	s.d.Audit.Record(ctx, actor, ActionTeamUpdate, id, details)
	return t, nil
}

// Delete removes a team; its memberships and grants go with it.
func (s *Teams) Delete(ctx context.Context, actor Actor, id string) error {
	t, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.d.Repo.Delete(ctx, id); err != nil {
		return err
	}
	members := make([]string, 0, len(t.Members))
	for _, m := range t.Members {
		members = append(members, m.Username)
	}
	s.d.Audit.Record(ctx, actor, ActionTeamDelete, id, map[string]any{"name": t.Name, "members": members, "streams": t.Streams})
	return nil
}

// SetMembers replaces the members of a team (unknown users ⇒ 400).
func (s *Teams) SetMembers(ctx context.Context, actor Actor, id string, userIDs []string) (TeamDetail, error) {
	if len(userIDs) > MaxTeamMembers {
		return TeamDetail{}, invalid("userIds", fmt.Sprintf("at most %d users", MaxTeamMembers))
	}
	if _, err := s.d.Repo.Get(ctx, id); err != nil {
		return TeamDetail{}, err
	}
	ch, err := s.d.Repo.SetMembers(ctx, id, nonNil(userIDs))
	if errors.Is(err, meta.ErrNotFound) {
		return TeamDetail{}, invalid("userIds", "unknown user")
	}
	if err != nil {
		return TeamDetail{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionTeamMembers, id, map[string]any{"added": ch.Added, "removed": ch.Removed})
	return s.Get(ctx, id)
}

// SetStreams replaces the streams granted to a team. Grants are by name and may precede the
// stream's data.
func (s *Teams) SetStreams(ctx context.Context, actor Actor, id string, streams []string) (TeamDetail, error) {
	if len(streams) > MaxTeamStreams {
		return TeamDetail{}, invalid("streams", fmt.Sprintf("at most %d streams", MaxTeamStreams))
	}
	for _, n := range streams {
		if !model.ValidStreamName(n) {
			return TeamDetail{}, invalid("streams", fmt.Sprintf("%q is not a valid stream name", n))
		}
	}
	ch, err := s.d.Repo.SetStreams(ctx, id, nonNil(streams))
	if err != nil {
		return TeamDetail{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionTeamStreams, id, map[string]any{"added": ch.Added, "removed": ch.Removed})
	return s.Get(ctx, id)
}

// UserTeams returns the teams of a user ordered by name.
func (s *Teams) UserTeams(ctx context.Context, userID string) ([]meta.TeamRef, error) {
	refs, err := s.d.Repo.UserTeams(ctx, userID)
	return nonNilRefs(refs), err
}

// Memberships maps user ids to their teams (users without a team are absent).
func (s *Teams) Memberships(ctx context.Context) (map[string][]meta.TeamRef, error) {
	return s.d.Repo.Memberships(ctx)
}

// SetUserTeams replaces the teams of a user (unknown teams ⇒ 400) and returns them.
func (s *Teams) SetUserTeams(ctx context.Context, actor Actor, userID string, teamIDs []string) ([]meta.TeamRef, error) {
	if len(teamIDs) > MaxTeams {
		return nil, invalid("teamIds", fmt.Sprintf("at most %d teams", MaxTeams))
	}
	if _, err := s.d.Users.Get(ctx, userID); err != nil {
		return nil, err
	}
	ch, err := s.d.Repo.SetUserTeams(ctx, userID, nonNil(teamIDs))
	if errors.Is(err, meta.ErrNotFound) {
		return nil, invalid("teamIds", "unknown team")
	}
	if err != nil {
		return nil, err
	}
	s.d.Audit.Record(ctx, actor, ActionUserTeams, userID, map[string]any{"added": ch.Added, "removed": ch.Removed})
	return s.UserTeams(ctx, userID)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilRefs(r []meta.TeamRef) []meta.TeamRef {
	if r == nil {
		return []meta.TeamRef{}
	}
	return r
}
