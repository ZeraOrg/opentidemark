// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// SettingAccess is the settings key of the access settings (SPEC §15.10).
const SettingAccess = "access"

// How a principal's stream scope was decided (GET /auth/me streamAccess, SPEC §15.10).
const (
	StreamAccessAdmin       = "admin"        // admins see every stream
	StreamAccessTeams       = "teams"        // the union of the user's team grants
	StreamAccessDefaultAll  = "default-all"  // no team, deny-by-default off: every stream
	StreamAccessDefaultNone = "default-none" // no team, deny-by-default on: no stream
)

// ErrNoPrincipal is returned when a scope is requested without an authenticated user.
var ErrNoPrincipal = errors.New("stream scope needs an authenticated user")

// AccessSettings is the "access" setting.
type AccessSettings struct {
	DenyByDefault bool `json:"denyByDefault"` // users without a team see no stream
}

// TeamStreams reads the teams of a user and the union of their stream grants.
type TeamStreams interface {
	StreamsForUser(ctx context.Context, userID string) (teams int, streams []string, err error)
}

// SettingsReader reads one setting (meta.ErrNotFound when unset).
type SettingsReader interface {
	Get(ctx context.Context, key string) (json.RawMessage, error)
}

// ScopeResolver decides the stream scope of a principal; consumers (search, alerts, streams)
// depend on this interface.
type ScopeResolver interface {
	Scope(ctx context.Context, p auth.Principal) (model.StreamScope, error)
}

// StreamAccess is the resolved access of a principal: its scope and how it was decided.
type StreamAccess struct {
	Scope model.StreamScope
	Mode  string // StreamAccess* constant
}

// Access resolves stream scopes from the user's role, teams and the access setting. It reads
// the metadata on every call, so changes apply to the next request on every node (SPEC §15.10).
type Access struct {
	teams    TeamStreams
	settings SettingsReader
}

var _ ScopeResolver = (*Access)(nil)

// NewAccess returns the access resolver.
func NewAccess(teams TeamStreams, settings SettingsReader) *Access {
	return &Access{teams: teams, settings: settings}
}

// Scope returns the stream scope of p.
func (a *Access) Scope(ctx context.Context, p auth.Principal) (model.StreamScope, error) {
	r, err := a.Resolve(ctx, p)
	return r.Scope, err
}

// Resolve returns the stream scope of p and how it was decided: the user's own role admin
// (also through a token of lower scope) ⇒ every stream; member of at least one team ⇒ the
// union of the teams' grants (a team without grants adds nothing); no team ⇒ every stream,
// or none when deny-by-default is on.
func (a *Access) Resolve(ctx context.Context, p auth.Principal) (StreamAccess, error) {
	if p.UserID == "" {
		return StreamAccess{}, ErrNoPrincipal
	}
	if p.UserRole == auth.RoleAdmin {
		return StreamAccess{Scope: model.AllStreams(), Mode: StreamAccessAdmin}, nil
	}
	teams, streams, err := a.teams.StreamsForUser(ctx, p.UserID)
	if err != nil {
		return StreamAccess{}, fmt.Errorf("load team streams: %w", err)
	}
	if teams > 0 {
		return StreamAccess{Scope: model.OnlyStreams(streams...), Mode: StreamAccessTeams}, nil
	}
	s, err := LoadAccess(ctx, a.settings)
	if err != nil {
		return StreamAccess{}, err
	}
	if s.DenyByDefault {
		return StreamAccess{Scope: model.NoStreams(), Mode: StreamAccessDefaultNone}, nil
	}
	return StreamAccess{Scope: model.AllStreams(), Mode: StreamAccessDefaultAll}, nil
}

// LoadAccess reads the access setting (deny-by-default off when unset).
func LoadAccess(ctx context.Context, st SettingsReader) (AccessSettings, error) {
	var s AccessSettings
	v, err := st.Get(ctx, SettingAccess)
	if errors.Is(err, meta.ErrNotFound) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("load access setting: %w", err)
	}
	if err := json.Unmarshal(v, &s); err != nil {
		return s, fmt.Errorf("stored access setting: %w", err)
	}
	return s, nil
}

// validateAccess checks a PATCH /settings value of "access" and returns it decoded.
func validateAccess(v json.RawMessage) (AccessSettings, error) {
	var raw struct {
		DenyByDefault *bool `json:"denyByDefault"`
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil || raw.DenyByDefault == nil {
		return AccessSettings{}, invalid(SettingAccess, "expected {denyByDefault: true|false}")
	}
	return AccessSettings{DenyByDefault: *raw.DenyByDefault}, nil
}
