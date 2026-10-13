// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bufio"
	_ "embed" // go:embed of the common password list
	"errors"
	"strings"
	"unicode/utf8"
)

// commonPasswordsText is the SecLists "10k-most-common.txt" list (MIT licence,
// github.com/danielmiessler/SecLists, Passwords/Common-Credentials), one password per line.
//
//go:embed common_passwords.txt
var commonPasswordsText string

// Password length limits (SPEC §15.2).
const (
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

// Password policy violations.
var (
	ErrPasswordTooShort = errors.New("password must be at least 12 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 256 characters")
	ErrPasswordUsername = errors.New("password must not equal the username")
	ErrPasswordCommon   = errors.New("password is too common")
)

// PasswordPolicy validates new passwords.
type PasswordPolicy struct {
	common map[string]struct{}
}

// NewPasswordPolicy builds the policy with the embedded common-password list.
func NewPasswordPolicy() *PasswordPolicy {
	common := make(map[string]struct{}, 10000)
	sc := bufio.NewScanner(strings.NewReader(commonPasswordsText))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			common[strings.ToLower(line)] = struct{}{}
		}
	}
	return &PasswordPolicy{common: common}
}

// CommonCount returns the number of entries in the embedded list.
func (p *PasswordPolicy) CommonCount() int { return len(p.common) }

// Validate checks a new password for username.
func (p *PasswordPolicy) Validate(username, password string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLen:
		return ErrPasswordTooShort
	case n > MaxPasswordLen:
		return ErrPasswordTooLong
	case strings.EqualFold(password, username):
		return ErrPasswordUsername
	}
	if _, ok := p.common[strings.ToLower(password)]; ok {
		return ErrPasswordCommon
	}
	return nil
}
