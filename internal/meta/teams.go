// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// Team is a named group of users that is granted access to streams (SPEC §15.10).
type Team struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TeamSummary is a team with its member count and granted streams (sorted).
type TeamSummary struct {
	Team
	MemberCount int
	Streams     []string
}

// TeamMember is a user of a team.
type TeamMember struct {
	UserID      string
	Username    string
	DisplayName string
	Role        string
}

// TeamRef names a team (the teams of a user).
type TeamRef struct {
	ID   string
	Name string
}

// Teams is the teams, team_members and team_streams repository.
type Teams struct{ db *DB }

// NewTeams returns the teams repository.
func NewTeams(db *DB) *Teams { return &Teams{db: db} }

const teamCols = `id, name, description, created_at, updated_at`

func scanTeam(r rowScanner, extra ...any) (Team, error) {
	var t Team
	var created, updated string
	if err := r.Scan(append([]any{&t.ID, &t.Name, &t.Description, &created, &updated}, extra...)...); err != nil {
		return Team{}, mapErr(err)
	}
	t.CreatedAt, t.UpdatedAt = decodeTime(created), decodeTime(updated)
	return t, nil
}

// Create inserts a team; a name used by another team (case-insensitive) is ErrConflict.
func (r *Teams) Create(ctx context.Context, t *Team) error {
	if t.ID == "" {
		t.ID = NewID()
	}
	now := r.db.now()
	t.CreatedAt, t.UpdatedAt = now, now
	_, err := r.db.sql.ExecContext(ctx, `INSERT INTO teams (`+teamCols+`) VALUES (?,?,?,?,?)`,
		t.ID, t.Name, t.Description, encodeTime(now), encodeTime(now))
	return mapErr(err)
}

// Get returns a team by id.
func (r *Teams) Get(ctx context.Context, id string) (Team, error) {
	return scanTeam(r.db.sql.QueryRowContext(ctx, `SELECT `+teamCols+` FROM teams WHERE id = ?`, id))
}

// Count returns the number of teams.
func (r *Teams) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM teams`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count teams: %w", err)
	}
	return n, nil
}

// List returns every team with its member count and streams, ordered by name.
func (r *Teams) List(ctx context.Context) ([]TeamSummary, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT `+teamCols+`,
		(SELECT count(*) FROM team_members m WHERE m.team_id = teams.id) FROM teams ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	out := []TeamSummary{}
	for rows.Next() {
		var s TeamSummary
		if s.Team, err = scanTeam(rows, &s.MemberCount); err != nil {
			return nil, err
		}
		s.Streams = []string{}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	grants, err := r.allStreams(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if g, ok := grants[out[i].ID]; ok {
			out[i].Streams = g
		}
	}
	return out, nil
}

// allStreams maps team ids to their granted streams (sorted).
func (r *Teams) allStreams(ctx context.Context) (map[string][]string, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT team_id, stream FROM team_streams ORDER BY team_id, stream`)
	if err != nil {
		return nil, fmt.Errorf("list team streams: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var team, stream string
		if err := rows.Scan(&team, &stream); err != nil {
			return nil, fmt.Errorf("scan team stream: %w", err)
		}
		out[team] = append(out[team], stream)
	}
	return out, rows.Err()
}

// Update writes name and description.
func (r *Teams) Update(ctx context.Context, t *Team) error {
	t.UpdatedAt = r.db.now()
	return expectOne(r.db.sql.ExecContext(ctx, `UPDATE teams SET name = ?, description = ?, updated_at = ? WHERE id = ?`,
		t.Name, t.Description, encodeTime(t.UpdatedAt), t.ID))
}

// Delete removes a team; its memberships and grants cascade.
func (r *Teams) Delete(ctx context.Context, id string) error {
	return expectOne(r.db.sql.ExecContext(ctx, `DELETE FROM teams WHERE id = ?`, id))
}

// Members lists the users of a team ordered by username.
func (r *Teams) Members(ctx context.Context, teamID string) ([]TeamMember, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT u.id, u.username, u.display_name, u.role FROM team_members m
		JOIN users u ON u.id = m.user_id WHERE m.team_id = ? ORDER BY lower(u.username), u.id`, teamID)
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}
	defer rows.Close()
	out := []TeamMember{}
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.UserID, &m.Username, &m.DisplayName, &m.Role); err != nil {
			return nil, fmt.Errorf("scan team member: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Streams lists the streams granted to a team (sorted).
func (r *Teams) Streams(ctx context.Context, teamID string) ([]string, error) {
	return r.strings(ctx, `SELECT stream FROM team_streams WHERE team_id = ? ORDER BY stream`, teamID)
}

// UserTeams lists the teams of a user ordered by name.
func (r *Teams) UserTeams(ctx context.Context, userID string) ([]TeamRef, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT t.id, t.name FROM team_members m JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = ? ORDER BY lower(t.name), t.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user teams: %w", err)
	}
	defer rows.Close()
	return scanRefs(rows)
}

// Memberships maps every user id that has teams to its teams (ordered by name).
func (r *Teams) Memberships(ctx context.Context) (map[string][]TeamRef, error) {
	rows, err := r.db.sql.QueryContext(ctx, `SELECT m.user_id, t.id, t.name FROM team_members m
		JOIN teams t ON t.id = m.team_id ORDER BY m.user_id, lower(t.name), t.id`)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	out := map[string][]TeamRef{}
	for rows.Next() {
		var user string
		var ref TeamRef
		if err := rows.Scan(&user, &ref.ID, &ref.Name); err != nil {
			return nil, fmt.Errorf("scan membership: %w", err)
		}
		out[user] = append(out[user], ref)
	}
	return out, rows.Err()
}

// StreamsForUser returns the number of teams of a user and the union of their granted streams
// (sorted, unique). It is read on every request that reads data (SPEC §15.10).
func (r *Teams) StreamsForUser(ctx context.Context, userID string) (int, []string, error) {
	var teams int
	if err := r.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM team_members WHERE user_id = ?`, userID).Scan(&teams); err != nil {
		return 0, nil, fmt.Errorf("count user teams: %w", err)
	}
	if teams == 0 {
		return 0, []string{}, nil
	}
	streams, err := r.strings(ctx, `SELECT DISTINCT s.stream FROM team_streams s JOIN team_members m ON m.team_id = s.team_id
		WHERE m.user_id = ? ORDER BY s.stream`, userID)
	return teams, streams, err
}

// CountWithoutTeam returns the number of users that belong to no team.
func (r *Teams) CountWithoutTeam(ctx context.Context) (int, error) {
	var n int
	err := r.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM users u
		WHERE NOT EXISTS (SELECT 1 FROM team_members m WHERE m.user_id = u.id)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users without team: %w", err)
	}
	return n, nil
}

// SetChange is the outcome of a Set* call: the values added and removed (sorted).
type SetChange struct {
	Added   []string
	Removed []string
}

// SetMembers replaces the members of a team. ErrNotFound when the team or a user does not
// exist.
func (r *Teams) SetMembers(ctx context.Context, teamID string, userIDs []string) (SetChange, error) {
	return r.replace(ctx, setSpec{owner: "teams", ownerID: teamID, table: "team_members", key: "team_id", value: "user_id"}, userIDs)
}

// SetStreams replaces the streams granted to a team. ErrNotFound when the team does not exist.
func (r *Teams) SetStreams(ctx context.Context, teamID string, streams []string) (SetChange, error) {
	return r.replace(ctx, setSpec{owner: "teams", ownerID: teamID, table: "team_streams", key: "team_id", value: "stream"}, streams)
}

// SetUserTeams replaces the teams of a user. ErrNotFound when the user or a team does not
// exist.
func (r *Teams) SetUserTeams(ctx context.Context, userID string, teamIDs []string) (SetChange, error) {
	return r.replace(ctx, setSpec{owner: "users", ownerID: userID, table: "team_members", key: "user_id", value: "team_id"}, teamIDs)
}

// RevokeStream removes every grant of a stream (the stream was deleted) and returns the names
// of the teams that had it.
func (r *Teams) RevokeStream(ctx context.Context, stream string) ([]string, error) {
	var names []string
	err := r.db.inTx(ctx, func(tx *Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.name FROM team_streams s JOIN teams t ON t.id = s.team_id
			WHERE s.stream = ? ORDER BY lower(t.name)`, stream)
		if err != nil {
			return fmt.Errorf("list stream grants: %w", err)
		}
		names, err = scanStrings(rows)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM team_streams WHERE stream = ?`, stream); err != nil {
			return fmt.Errorf("revoke stream: %w", err)
		}
		return nil
	})
	return names, err
}

// setSpec describes a set-valued relation: rows of table with key = ownerID hold value.
// Every identifier comes from the constant call sites above, never from input.
type setSpec struct {
	owner, ownerID, table, key, value string
}

// replace makes the values of ownerID equal to want in one transaction.
func (r *Teams) replace(ctx context.Context, s setSpec, want []string) (SetChange, error) {
	want = sortedUnique(want)
	var ch SetChange
	err := r.db.inTx(ctx, func(tx *Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+s.owner+` WHERE id = ?`, s.ownerID).Scan(&n); err != nil {
			return fmt.Errorf("check %s: %w", s.owner, err)
		}
		if n == 0 {
			return ErrNotFound
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+s.value+` FROM `+s.table+` WHERE `+s.key+` = ? ORDER BY `+s.value, s.ownerID)
		if err != nil {
			return fmt.Errorf("read %s: %w", s.table, err)
		}
		have, err := scanStrings(rows)
		if err != nil {
			return err
		}
		ch = diff(have, want)
		return r.applyChange(ctx, tx, s, ch)
	})
	if err != nil {
		return SetChange{}, err
	}
	return ch, nil
}

func (r *Teams) applyChange(ctx context.Context, tx *Tx, s setSpec, ch SetChange) error {
	for _, v := range ch.Removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+s.table+` WHERE `+s.key+` = ? AND `+s.value+` = ?`, s.ownerID, v); err != nil {
			return fmt.Errorf("delete from %s: %w", s.table, err)
		}
	}
	now := encodeTime(r.db.now())
	for _, v := range ch.Added {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+s.table+` (`+s.key+`, `+s.value+`, created_at, updated_at) VALUES (?,?,?,?)`,
			s.ownerID, v, now, now); err != nil {
			return refErr(err)
		}
	}
	return nil
}

// diff returns the values of want missing from have (Added) and of have missing from want
// (Removed); both inputs are sorted and unique.
func diff(have, want []string) SetChange {
	ch := SetChange{Added: []string{}, Removed: []string{}}
	for _, v := range want {
		if _, ok := slices.BinarySearch(have, v); !ok {
			ch.Added = append(ch.Added, v)
		}
	}
	for _, v := range have {
		if _, ok := slices.BinarySearch(want, v); !ok {
			ch.Removed = append(ch.Removed, v)
		}
	}
	return ch
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func (r *Teams) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := r.db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	return scanStrings(rows)
}

// scanStrings reads one string column and closes rows.
func scanStrings(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}) ([]string, error) {
	defer func() { _ = rows.Close() }() // read-only cursor; Err below reports read failures
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanRefs(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]TeamRef, error) {
	out := []TeamRef{}
	for rows.Next() {
		var ref TeamRef
		if err := rows.Scan(&ref.ID, &ref.Name); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
