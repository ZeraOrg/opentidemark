// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
)

// LoginService authenticates credentials.
type LoginService interface {
	Login(ctx context.Context, username, password, ip, userAgent string) (auth.LoginResult, error)
}

// SessionRevoker ends sessions.
type SessionRevoker interface {
	Revoke(ctx context.Context, sessionID string) error
}

// AuthAPI serves /auth/*.
type AuthAPI struct {
	Login         LoginService
	Sessions      SessionRevoker
	Users         *service.Users
	Access        StreamAccessResolver
	Teams         *service.Teams
	Audit         *service.Auditor
	SecureCookies bool
	ClientIP      func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *AuthAPI) Routes() []Route {
	return []Route{
		{http.MethodPost, "/auth/login", Public, a.login},
		{http.MethodPost, "/auth/logout", Authenticated, a.logout},
		{http.MethodGet, "/auth/me", Authenticated, a.me},
		{http.MethodPost, "/auth/password", Authenticated, a.password},
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const maxCredentialLen = 1024

func (a *AuthAPI) login(w http.ResponseWriter, r *http.Request) error {
	var in loginRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Username == "" || in.Password == "" || len(in.Username) > maxCredentialLen || len(in.Password) > maxCredentialLen {
		return errBadRequest("username and password are required")
	}
	act := actor(r, a.ClientIP)
	res, err := a.Login.Login(r.Context(), in.Username, in.Password, act.IP, r.UserAgent())
	if err != nil {
		var lf *auth.LoginFailure
		if errors.As(err, &lf) {
			a.Audit.Record(r.Context(), act, service.ActionLoginFailure, truncate(in.Username, 64), map[string]any{"reason": lf.Reason})
		}
		return err
	}
	a.setCookies(w, res.Session.Token, res.Session.CSRF, res.Session.ExpiresAt)
	a.Audit.Record(r.Context(), service.Actor{ID: "user:" + res.User.ID, IP: act.IP}, service.ActionLoginSuccess, res.User.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": toUserView(res.User), "mustChangePassword": res.User.MustChangePassword})
	return nil
}

// setCookies sets the session and CSRF cookies (SPEC §15.2). Secure follows the https decision of
// §15.1 (a.SecureCookies), and the CSRF cookie is readable by scripts for the double-submit check.
func (a *AuthAPI) setCookies(w http.ResponseWriter, token, csrf string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: auth.SessionCookie, Value: token, Path: "/", Expires: expires, //nolint:gosec // Secure per §15.1
		HttpOnly: true, Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	http.SetCookie(w, &http.Cookie{Name: auth.CSRFCookie, Value: csrf, Path: "/", Expires: expires, //nolint:gosec // double-submit cookie, §15.2
		HttpOnly: false, Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
}

func (a *AuthAPI) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{auth.SessionCookie, auth.CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: name == auth.SessionCookie, //nolint:gosec // same attributes as setCookies
			Secure: a.SecureCookies, SameSite: http.SameSiteStrictMode})
	}
}

func (a *AuthAPI) logout(w http.ResponseWriter, r *http.Request) error {
	p := principal(r)
	if p.Kind == auth.KindSession {
		if err := a.Sessions.Revoke(r.Context(), p.SessionID); err != nil {
			return err
		}
	}
	a.clearCookies(w)
	a.Audit.Record(r.Context(), actor(r, a.ClientIP), service.ActionLogout, p.UserID, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *AuthAPI) me(w http.ResponseWriter, r *http.Request) error {
	p := principal(r)
	u, err := a.Users.Get(r.Context(), p.UserID)
	if errors.Is(err, meta.ErrNotFound) {
		return auth.ErrUnauthorized
	}
	if err != nil {
		return err
	}
	acc, err := a.Access.Resolve(r.Context(), p)
	if err != nil {
		return err
	}
	teams, err := a.Teams.UserTeams(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "username": u.Username, "displayName": u.DisplayName,
		"role": string(p.Role), "mustChangePassword": u.MustChangePassword,
		"defaultCredentials": a.Users.DefaultCredentials(r.Context()),
		"allStreams":         acc.Scope.All(),
		"streams":            nonNil(acc.Scope.Names()),
		"teams":              toTeamRefViews(teams),
		"streamAccess":       acc.Mode})
	return nil
}

// StreamAccessResolver resolves the stream access of a principal (service.Access).
type StreamAccessResolver interface {
	Resolve(ctx context.Context, p auth.Principal) (service.StreamAccess, error)
}

type passwordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (a *AuthAPI) password(w http.ResponseWriter, r *http.Request) error {
	var in passwordRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	if len(in.CurrentPassword) > maxCredentialLen || len(in.NewPassword) > maxCredentialLen {
		return errBadRequest("password too long")
	}
	if err := a.Users.ChangePassword(r.Context(), actor(r, a.ClientIP), principal(r), in.CurrentPassword, in.NewPassword); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
