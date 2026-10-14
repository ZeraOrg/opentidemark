// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// userView is the JSON form of a user.
type userView struct {
	ID                 string     `json:"id"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"displayName"`
	Role               string     `json:"role"`
	MustChangePassword bool       `json:"mustChangePassword"`
	Disabled           bool       `json:"disabled"`
	LastLoginAt        *time.Time `json:"lastLoginAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

func toUserView(u meta.User) userView {
	return userView{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role,
		MustChangePassword: u.MustChangePassword, Disabled: u.Disabled, LastLoginAt: u.LastLoginAt,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

// tokenView is the JSON form of an API token (never the secret).
type tokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"keyPrefix"`
	Scope      string     `json:"scope"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	Token      string     `json:"token,omitempty"` // only in the create response
}

func toTokenView(t meta.APIToken) tokenView {
	return tokenView{ID: t.ID, Name: t.Name, KeyPrefix: t.KeyPrefix, Scope: t.Scope, ExpiresAt: t.ExpiresAt,
		RevokedAt: t.RevokedAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt}
}

// connectionView is the JSON form of a connection (never the key hash).
type connectionView struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	KeyPrefix          string         `json:"keyPrefix"`
	Protocols          []string       `json:"protocols"`
	DefaultStream      string         `json:"defaultStream"`
	AllowedCIDRs       []string       `json:"allowedCidrs"`
	AllowedStreams     []string       `json:"allowedStreams"`
	MinimumLevel       *string        `json:"minimumLevel"`
	AppliedProperties  map[string]any `json:"appliedProperties"`
	RateLimitEPS       int            `json:"rateLimitEps"`
	ExpiresAt          *time.Time     `json:"expiresAt"`
	RevokedAt          *time.Time     `json:"revokedAt"`
	RotationGraceUntil *time.Time     `json:"rotationGraceUntil"`
	LastUsedAt         *time.Time     `json:"lastUsedAt"`
	LastUsedIP         string         `json:"lastUsedIp"`
	EventsReceived     int64          `json:"eventsReceived"`
	CreatedAt          time.Time      `json:"createdAt"`
	UpdatedAt          time.Time      `json:"updatedAt"`
	Key                string         `json:"key,omitempty"` // only in create/rotate responses
}

func toConnectionView(c meta.Connection) connectionView {
	v := connectionView{ID: c.ID, Name: c.Name, Description: c.Description, KeyPrefix: c.KeyPrefix,
		Protocols: nonNil(c.Protocols), DefaultStream: c.DefaultStream, AllowedCIDRs: nonNil(c.AllowedCIDRs), AllowedStreams: nonNil(c.AllowedStreams),
		AppliedProperties: c.AppliedProperties, RateLimitEPS: c.RateLimitEPS, ExpiresAt: c.ExpiresAt,
		RevokedAt: c.RevokedAt, RotationGraceUntil: c.PrevKeyExpiresAt, LastUsedAt: c.LastUsedAt,
		LastUsedIP: c.LastUsedIP, EventsReceived: c.EventsReceived, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
	if c.MinimumLevel != "" {
		lvl := c.MinimumLevel
		v.MinimumLevel = &lvl
	}
	if v.AppliedProperties == nil {
		v.AppliedProperties = map[string]any{}
	}
	return v
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// auditView is the JSON form of an audit entry.
type auditView struct {
	ID      string          `json:"id"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor"`
	IP      string          `json:"ip"`
	Action  string          `json:"action"`
	Target  string          `json:"target"`
	Details json.RawMessage `json:"details"`
}
