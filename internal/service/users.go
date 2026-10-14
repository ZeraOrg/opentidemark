// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// UserStore is the user persistence used by Users.
type UserStore interface {
	Create(ctx context.Context, u *meta.User) error
	Get(ctx context.Context, id string) (meta.User, error)
	GetByUsername(ctx context.Context, username string) (meta.User, error)
	List(ctx context.Context) ([]meta.User, error)
	Count(ctx context.Context, enabledOnly bool) (int, error)
	CountAdmins(ctx context.Context) (int, error)
	Update(ctx context.Context, u *meta.User) error
	SetPassword(ctx context.Context, id, hash string, mustChange bool) error
	Delete(ctx context.Context, id, heirID string) (meta.Transferred, error)
}

// SessionRevoker revokes sessions.
type SessionRevoker interface {
	RevokeUser(ctx context.Context, userID, keepSessionID string) error
}

// TokenRevoker revokes API tokens.
type TokenRevoker interface {
	RevokeForUser(ctx context.Context, userID string, at time.Time) error
}

// SettingsStore is the key/value settings persistence.
type SettingsStore interface {
	Get(ctx context.Context, key string) (json.RawMessage, error)
	Set(ctx context.Context, key string, value json.RawMessage) error
	Delete(ctx context.Context, key string) error
}

// SettingDefaultAdmin marks the seeded admin while it still uses the default password.
const SettingDefaultAdmin = "security.default_admin_user_id"

// Limits of user fields.
const (
	maxDisplayName = 128
)

// Users manages accounts (SPEC §11.2, §15.2). The number of accounts is not limited.
type Users struct {
	store    UserStore
	sessions SessionRevoker
	tokens   TokenRevoker
	settings SettingsStore
	hasher   *auth.PasswordHasher
	policy   *auth.PasswordPolicy
	teams    UserTeamLister
	audit    *Auditor
	now      func() time.Time
}

// UserTeamLister lists the teams of a user (recorded when the user is deleted).
type UserTeamLister interface {
	UserTeams(ctx context.Context, userID string) ([]meta.TeamRef, error)
}

// UsersDeps bundles the collaborators of Users.
type UsersDeps struct {
	Store    UserStore
	Sessions SessionRevoker
	Tokens   TokenRevoker
	Settings SettingsStore
	Hasher   *auth.PasswordHasher
	Policy   *auth.PasswordPolicy
	Teams    UserTeamLister // optional
	Audit    *Auditor
	Now      func() time.Time
}

// NewUsers returns the user service.
func NewUsers(d UsersDeps) *Users {
	return &Users{store: d.Store, sessions: d.Sessions, tokens: d.Tokens, settings: d.Settings, hasher: d.Hasher,
		policy: d.Policy, teams: d.Teams, audit: d.Audit, now: d.Now}
}

// NewUser is the input of Create.
type NewUser struct {
	Username    string
	DisplayName string
	Password    string
	Role        string
}

// List returns all users.
func (s *Users) List(ctx context.Context) ([]meta.User, error) { return s.store.List(ctx) }

// Get returns one user.
func (s *Users) Get(ctx context.Context, id string) (meta.User, error) { return s.store.Get(ctx, id) }

// Create adds a user after role and password checks.
func (s *Users) Create(ctx context.Context, actor Actor, in NewUser) (meta.User, error) {
	in.Username = strings.TrimSpace(in.Username)
	if err := validateUsername(in.Username); err != nil {
		return meta.User{}, err
	}
	if !auth.Role(in.Role).Valid() {
		return meta.User{}, invalid("role", "must be admin, editor or viewer")
	}
	if utf8.RuneCountInString(in.DisplayName) > maxDisplayName {
		return meta.User{}, invalid("displayName", "too long")
	}
	if err := s.policy.Validate(in.Username, in.Password); err != nil {
		return meta.User{}, invalid("password", err.Error())
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return meta.User{}, err
	}
	u := meta.User{Username: in.Username, DisplayName: in.DisplayName, PasswordHash: hash, Role: in.Role}
	if err := s.store.Create(ctx, &u); err != nil {
		if errors.Is(err, meta.ErrConflict) {
			return meta.User{}, invalid("username", "already exists")
		}
		return meta.User{}, err
	}
	s.audit.Record(ctx, actor, ActionUserCreate, u.ID, map[string]any{"username": u.Username, "role": u.Role})
	return u, nil
}

func validateUsername(name string) error {
	if name == "" || len(name) > 64 {
		return invalid("username", "must be 1-64 characters")
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("._@-", r) {
			return invalid("username", "may contain letters, digits and . _ @ - only")
		}
	}
	return nil
}

// UserPatch holds optional changes.
type UserPatch struct {
	DisplayName *string
	Role        *string
	Disabled    *bool
	Password    *string // admin-set password; the user must change it at next login
}

// Update applies a patch; demoting/disabling the last admin is refused.
func (s *Users) Update(ctx context.Context, actor Actor, id string, p UserPatch) (meta.User, error) {
	u, err := s.store.Get(ctx, id)
	if err != nil {
		return meta.User{}, err
	}
	roleChanged, disabling, err := s.applyPatch(&u, p)
	if err != nil {
		return meta.User{}, err
	}
	if err := s.guardAdmins(ctx, u, roleChanged || disabling); err != nil {
		return meta.User{}, err
	}
	if err := s.store.Update(ctx, &u); err != nil {
		return meta.User{}, err
	}
	if p.Password != nil {
		if err := s.setPassword(ctx, u, *p.Password, true); err != nil {
			return meta.User{}, err
		}
	}
	if roleChanged || disabling || p.Password != nil {
		// Tokens are capped at the owner's current role, so a role change keeps them; disabling
		// or an admin-set password (account recovery) revokes them too.
		if err := s.revokeAll(ctx, u.ID, disabling || p.Password != nil); err != nil {
			return meta.User{}, err
		}
	}
	s.audit.Record(ctx, actor, ActionUserUpdate, u.ID, map[string]any{"role": u.Role, "disabled": u.Disabled,
		"passwordReset": p.Password != nil})
	return s.store.Get(ctx, id)
}

func (s *Users) applyPatch(u *meta.User, p UserPatch) (roleChanged, disabling bool, err error) {
	if p.DisplayName != nil {
		if utf8.RuneCountInString(*p.DisplayName) > maxDisplayName {
			return false, false, invalid("displayName", "too long")
		}
		u.DisplayName = *p.DisplayName
	}
	if p.Role != nil && *p.Role != u.Role {
		if !auth.Role(*p.Role).Valid() {
			return false, false, invalid("role", "must be admin, editor or viewer")
		}
		u.Role = *p.Role
		roleChanged = true
	}
	if p.Disabled != nil && *p.Disabled != u.Disabled {
		disabling = *p.Disabled
		u.Disabled = *p.Disabled
	}
	if p.Password != nil {
		if err := s.policy.Validate(u.Username, *p.Password); err != nil {
			return false, false, invalid("password", err.Error())
		}
	}
	return roleChanged, disabling, nil
}

// guardAdmins refuses changes that would leave no enabled admin. u is the user after the change.
func (s *Users) guardAdmins(ctx context.Context, u meta.User, removesPrivilege bool) error {
	if !removesPrivilege {
		return nil
	}
	before, err := s.store.Get(ctx, u.ID)
	if err != nil {
		return err
	}
	wasActiveAdmin := before.Role == meta.RoleAdmin && !before.Disabled
	stillActiveAdmin := u.Role == meta.RoleAdmin && !u.Disabled
	if !wasActiveAdmin || stillActiveAdmin {
		return nil
	}
	n, err := s.store.CountAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

// Delete removes a user; the last admin and the caller's own account cannot be deleted. The
// user's dashboards and signals pass to the caller, so private ones stay reachable.
func (s *Users) Delete(ctx context.Context, actor Actor, callerID, id string) error {
	if callerID == id {
		return invalid("id", "you cannot delete your own account")
	}
	u, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	u.Disabled = true // deleting removes the account's privileges like disabling it
	if err := s.guardAdmins(ctx, u, true); err != nil {
		return err
	}
	details := map[string]any{"username": u.Username}
	if s.teams != nil {
		refs, err := s.teams.UserTeams(ctx, id)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, r.Name)
		}
		details["teams"] = names
	}
	moved, err := s.store.Delete(ctx, id, callerID)
	if err != nil {
		return err
	}
	details["transferredTo"] = callerID
	details["dashboards"] = moved.Dashboards
	details["signals"] = moved.Signals
	s.audit.Record(ctx, actor, ActionUserDelete, id, details)
	return nil
}

// ChangePassword changes the caller's password (current password required) and revokes the
// caller's other sessions.
func (s *Users) ChangePassword(ctx context.Context, actor Actor, p auth.Principal, current, next string) error {
	u, err := s.store.Get(ctx, p.UserID)
	if err != nil {
		return err
	}
	ok, err := s.hasher.Verify(current, u.PasswordHash)
	if err != nil || !ok {
		return ErrInvalidPassword
	}
	if current == next {
		return invalid("newPassword", "must differ from the current password")
	}
	if err := s.policy.Validate(u.Username, next); err != nil {
		return invalid("newPassword", err.Error())
	}
	if err := s.setPassword(ctx, u, next, false); err != nil {
		return err
	}
	if err := s.sessions.RevokeUser(ctx, u.ID, p.SessionID); err != nil {
		return err
	}
	s.audit.Record(ctx, actor, ActionPasswordChange, u.ID, nil)
	return nil
}

// ResetPassword sets a password without the current one (CLI recovery); the user must change it
// at next login, the lockout is cleared and every session and token is revoked.
func (s *Users) ResetPassword(ctx context.Context, username, password string) error {
	u, err := s.store.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	if err := s.policy.Validate(u.Username, password); err != nil {
		return invalid("password", err.Error())
	}
	if err := s.setPassword(ctx, u, password, true); err != nil {
		return err
	}
	if err := s.revokeAll(ctx, u.ID, true); err != nil {
		return err
	}
	s.audit.Record(ctx, SystemActor, ActionPasswordReset, u.ID, nil)
	return nil
}

func (s *Users) setPassword(ctx context.Context, u meta.User, password string, mustChange bool) error {
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return err
	}
	if err := s.store.SetPassword(ctx, u.ID, hash, mustChange); err != nil {
		return err
	}
	return s.clearDefaultMarker(ctx, u.ID)
}

func (s *Users) revokeAll(ctx context.Context, userID string, tokensToo bool) error {
	if err := s.sessions.RevokeUser(ctx, userID, ""); err != nil {
		return err
	}
	if tokensToo {
		return s.tokens.RevokeForUser(ctx, userID, s.now())
	}
	return nil
}

func (s *Users) clearDefaultMarker(ctx context.Context, userID string) error {
	v, err := s.settings.Get(ctx, SettingDefaultAdmin)
	if errors.Is(err, meta.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.Trim(string(v), `"`) == userID {
		return s.settings.Delete(ctx, SettingDefaultAdmin)
	}
	return nil
}

// DefaultCredentials reports whether the seeded admin still uses the default password.
func (s *Users) DefaultCredentials(ctx context.Context) bool {
	_, err := s.settings.Get(ctx, SettingDefaultAdmin)
	return err == nil
}
