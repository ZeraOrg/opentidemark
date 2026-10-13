// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"errors"
	"time"
)

// Sentinel errors (mapped to HTTP codes by the API and ingest layers).
var (
	ErrUnauthorized       = errors.New("unauthorized")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many requests")
)

// Clock supplies the current time.
type Clock interface{ Now() time.Time }

// SystemClock is the wall clock.
type SystemClock struct{}

// Now returns time.Now().
func (SystemClock) Now() time.Time { return time.Now() }
