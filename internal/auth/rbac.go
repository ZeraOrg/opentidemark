// SPDX-License-Identifier: Apache-2.0

package auth

import "github.com/ZeraOrg/opentidemark/internal/meta"

// Role is a user role (SPEC §11.1).
type Role string

// Roles in ascending order of privilege.
const (
	RoleViewer Role = meta.RoleViewer
	RoleEditor Role = meta.RoleEditor
	RoleAdmin  Role = meta.RoleAdmin
)

// rank orders roles; unknown roles rank 0 (no access).
func (r Role) rank() int {
	switch r {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	default:
		return 0
	}
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r.rank() > 0 }

// Allows reports whether r has at least the privileges of required.
func (r Role) Allows(required Role) bool { return r.rank() > 0 && r.rank() >= required.rank() }

// MinRole returns the less privileged of two roles.
func MinRole(a, b Role) Role {
	if a.rank() <= b.rank() {
		return a
	}
	return b
}

// ScopeRole maps an API token scope to the maximum role it grants.
func ScopeRole(scope string) Role {
	switch scope {
	case meta.ScopeAdmin:
		return RoleAdmin
	case meta.ScopeWrite:
		return RoleEditor
	case meta.ScopeRead:
		return RoleViewer
	default:
		return ""
	}
}
