// SPDX-License-Identifier: Apache-2.0

package auth

import "context"

// PrincipalKind tells how a request was authenticated.
type PrincipalKind string

// Principal kinds.
const (
	KindSession PrincipalKind = "session"
	KindToken   PrincipalKind = "token"
)

// Principal is the authenticated identity of an API request.
type Principal struct {
	Kind               PrincipalKind
	UserID             string
	Username           string
	Role               Role // effective role (token scope applied)
	UserRole           Role // the user's own role (decides the stream scope, SPEC §15.10)
	SessionID          string
	TokenID            string
	MustChangePassword bool
}

// Actor returns the audit actor string ("user:<id>" or "token:<id>").
func (p Principal) Actor() string {
	if p.Kind == KindToken {
		return "token:" + p.TokenID
	}
	return "user:" + p.UserID
}

type principalKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal stored in ctx.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
