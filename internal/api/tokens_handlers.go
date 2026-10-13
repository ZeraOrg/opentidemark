// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/tidemark/internal/service"
)

// TokensAPI serves /tokens (own tokens of any authenticated user).
type TokensAPI struct {
	Tokens   *service.Tokens
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *TokensAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/tokens", Authenticated, a.list},
		{http.MethodPost, "/tokens", Authenticated, a.create},
		{http.MethodDelete, "/tokens/{id}", Authenticated, a.delete},
	}
}

func (a *TokensAPI) list(w http.ResponseWriter, r *http.Request) error {
	tokens, err := a.Tokens.List(r.Context(), principal(r))
	if err != nil {
		return err
	}
	out := make([]tokenView, 0, len(tokens))
	for i := range tokens {
		out = append(out, toTokenView(tokens[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type createTokenRequest struct {
	Name      string     `json:"name"`
	Scope     string     `json:"scope"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

func (a *TokensAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in createTokenRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	t, raw, err := a.Tokens.Create(r.Context(), actor(r, a.ClientIP), principal(r), service.NewToken(in))
	if err != nil {
		return err
	}
	v := toTokenView(t)
	v.Token = raw
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func (a *TokensAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Tokens.Delete(r.Context(), actor(r, a.ClientIP), principal(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
