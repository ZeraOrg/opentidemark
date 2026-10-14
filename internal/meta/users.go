// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Roles.
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// User is an OpenTidemark user account.
type User struct {
	ID                 string
	Username           string
	DisplayName        string
	PasswordHash       string
	Role               string
	MustChangePassword bool
	FailedLogins       int
	LockedUntil        *time.Time
	LastLoginAt        *time.Time
	Disabled           bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Users is the users repository.
type Users struct{ db *DB }

// NewUsers returns the users repository.
func NewUsers(db *DB) *Users { return &Users{db: db} }

const userCols = `id, username, display_name, password_hash, role, must_change_password, failed_logins,
	locked_until, last_login_at, disabled, created_at, updated_at`

func scanUser(r rowScanner) (User, error) {
	var u User
	var locked, lastLogin sql.NullString
	var created, updated string
	err := r.Scan(&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &u.MustChangePassword,
		&u.FailedLogins, &locked, &lastLogin, &u.Disabled, &created, &updated)
	if err != nil {
		return User{}, mapErr(err)
	}
	u.LockedUntil, u.LastLoginAt = decodeTimePtr(locked), decodeTimePtr(lastLogin)
	u.CreatedAt, u.UpdatedAt = decodeTime(created), decodeTime(updated)
	return u, nil
}

// Create inserts a user; ID and timestamps are assigned when empty.
func (r *Users) Create(ctx context.Context, u *User) error {
	if u.ID == "" {
		u.ID = NewID()
	}
	now := r.db.now()
	u.CreatedAt, u.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, u.Role, boolInt(u.MustChangePassword), u.FailedLogins,
		encodeTimePtr(u.LockedUntil), encodeTimePtr(u.LastLoginAt), boolInt(u.Disabled),
		encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns the user with the given id.
func (r *Users) Get(ctx context.Context, id string) (User, error) {
	return scanUser(r.db.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetByUsername returns the user with the given username (case-insensitive).
func (r *Users) GetByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(r.db.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE lower(username) = lower(?)`, username))
}

// List returns all users ordered by creation time.
func (r *Users) List(ctx context.Context) ([]User, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Count returns the number of users; enabledOnly excludes disabled accounts.
func (r *Users) Count(ctx context.Context, enabledOnly bool) (int, error) {
	q := `SELECT count(*) FROM users`
	if enabledOnly {
		q += ` WHERE disabled = 0`
	}
	var n int
	if err := r.db.sql.QueryRowContext(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// CountAdmins returns the number of enabled admins.
func (r *Users) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// Update writes the mutable profile fields (display name, role, disabled, must-change flag).
func (r *Users) Update(ctx context.Context, u *User) error {
	u.UpdatedAt = r.db.now()
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE users SET display_name = ?, role = ?, disabled = ?,
		must_change_password = ?, updated_at = ? WHERE id = ?`,
		u.DisplayName, u.Role, boolInt(u.Disabled), boolInt(u.MustChangePassword), encodeTime(u.UpdatedAt), u.ID))
}

// SetPassword stores a new password hash and the must-change flag, clearing lockout state.
func (r *Users) SetPassword(ctx context.Context, id, hash string, mustChange bool) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE users SET password_hash = ?, must_change_password = ?,
		failed_logins = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
		hash, boolInt(mustChange), encodeTime(r.db.now()), id))
}

// UpdatePasswordHash replaces the hash only while it still equals oldHash (transparent re-hash on
// login, SPEC §8.5 item 5); it does not touch lockout state or the must-change flag.
func (r *Users) UpdatePasswordHash(ctx context.Context, id, oldHash, newHash string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ? AND password_hash = ?`,
		newHash, encodeTime(r.db.now()), id, oldHash))
}

// RecordLoginFailure stores the failure counter and lockout deadline.
func (r *Users) RecordLoginFailure(ctx context.Context, id string, failures int, lockedUntil *time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE users SET failed_logins = ?, locked_until = ?, updated_at = ? WHERE id = ?`,
		failures, encodeTimePtr(lockedUntil), encodeTime(r.db.now()), id))
}

// RecordLoginSuccess resets the failure counter and sets last_login_at.
func (r *Users) RecordLoginSuccess(ctx context.Context, id string, at time.Time) error {
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE users SET failed_logins = 0, locked_until = NULL,
		last_login_at = ?, updated_at = ? WHERE id = ?`, encodeTime(at), encodeTime(r.db.now()), id))
}

// Delete removes a user (sessions and tokens cascade).
func (r *Users) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id))
}
