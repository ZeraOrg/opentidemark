// SPDX-License-Identifier: Apache-2.0

// Package service contains the application services behind the HTTP API: users, API tokens,
// connections, settings, audit, system information, stream registry and first-start
// seeding. Services hold the business rules; handlers only decode and encode.
package service

import (
	"errors"
	"fmt"
)

// Service errors mapped by the API (SPEC §11.1).
var (
	ErrForbidden       = errors.New("forbidden")
	ErrLastAdmin       = errors.New("the last admin cannot be removed, disabled or demoted")
	ErrInvalidPassword = errors.New("current password is incorrect")
)

// ValidationError reports an invalid input field.
type ValidationError struct {
	Field string
	Msg   string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Msg) }

func invalid(field, msg string) error { return &ValidationError{Field: field, Msg: msg} }
