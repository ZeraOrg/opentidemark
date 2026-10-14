// SPDX-License-Identifier: Apache-2.0

package meta_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// fixtureManifest describes testdata/upgrade/meta-<version>.db (SPEC §8.5 item 6).
type fixtureManifest struct {
	AdminUsername string           `json:"adminUsername"`
	AdminPassword string           `json:"adminPassword"`
	ConnectionKey string           `json:"connectionKey"`
	APIToken      string           `json:"apiToken"`
	RowCounts     map[string]int64 `json:"rowCounts"`
}

// TestUpgradeFixtures migrates every release fixture to head and checks that nothing a user
// relies on was lost.
func TestUpgradeFixtures(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("testdata", "upgrade", "meta-*.db"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no upgrade fixtures: %v", err)
	}
	for _, f := range fixtures {
		t.Run(filepath.Base(f), func(t *testing.T) { checkUpgrade(t, f) })
	}
}

func checkUpgrade(t *testing.T, fixture string) {
	ctx := context.Background()
	var m fixtureManifest
	raw, err := os.ReadFile(strings.TrimSuffix(fixture, ".db") + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), meta.DBFileName)
	src, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	backups := t.TempDir()
	db, err := meta.Open(ctx, path, meta.WithBinaryVersion(releaseVersion(t)), meta.WithBackupDir(backups))
	if err != nil {
		t.Fatalf("migrate fixture: %v", err)
	}
	defer db.Close()
	// The fixture of the newest release may already be at head; any pending migration must be
	// preceded by a backup.
	if rep := db.Migrations(); len(rep.Applied) > 0 && rep.BackupPath == "" {
		t.Errorf("migrations applied without a backup: %+v", rep)
	}
	assertCounts(ctx, t, db, m.RowCounts)
	assertLogin(ctx, t, db, m)
	assertKeys(ctx, t, db, m)
	assertContent(ctx, t, db)
	if m.RowCounts["team_members"] == 0 {
		assertNoTeams(ctx, t, db)
	}
	assertCounts(ctx, t, db, m.RowCounts)
}

// releaseVersion is the current release (VERSION), the binary version that opens every fixture.
func releaseVersion(t *testing.T) string {
	t.Helper()
	v, err := os.ReadFile(filepath.Join("..", "..", "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(v))
}

// assertNoTeams checks that upgraded users belong to no team, so they keep seeing every
// stream unless deny-by-default is switched on (SPEC §15.10).
func assertNoTeams(ctx context.Context, t *testing.T, db *meta.DB) {
	t.Helper()
	teams := meta.NewTeams(db)
	if list, err := teams.List(ctx); err != nil || len(list) != 0 {
		t.Errorf("teams after upgrade: %+v %v", list, err)
	}
	users, err := meta.NewUsers(db).List(ctx)
	if err != nil || len(users) == 0 {
		t.Fatalf("users: %v", err)
	}
	access := service.NewAccess(teams, meta.NewSettings(db))
	for _, u := range users {
		if n, streams, err := teams.StreamsForUser(ctx, u.ID); err != nil || n != 0 || len(streams) != 0 {
			t.Errorf("user %s after upgrade: %d teams %v %v", u.Username, n, streams, err)
		}
		p := auth.Principal{UserID: u.ID, Role: auth.Role(u.Role), UserRole: auth.Role(u.Role)}
		if r, err := access.Resolve(ctx, p); err != nil || !r.Scope.All() {
			t.Errorf("user %s after upgrade must see every stream: %+v %v", u.Username, r, err)
		}
	}
}

func assertCounts(ctx context.Context, t *testing.T, db *meta.DB, want map[string]int64) {
	t.Helper()
	var buf strings.Builder
	if err := db.ExportJSON(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	var exp meta.Export
	if err := json.Unmarshal([]byte(buf.String()), &exp); err != nil {
		t.Fatal(err)
	}
	for table, n := range want {
		got := int64(len(exp.Tables[table]))
		if table == "sessions" || table == "audit_log" {
			if got < n {
				t.Errorf("%s: %d rows, want at least %d", table, got, n)
			}
			continue
		}
		if got != n {
			t.Errorf("%s: %d rows, want %d", table, got, n)
		}
	}
}

func assertLogin(ctx context.Context, t *testing.T, db *meta.DB, m fixtureManifest) {
	t.Helper()
	users := meta.NewUsers(db)
	before, err := users.GetByUsername(ctx, m.AdminUsername)
	if err != nil {
		t.Fatal(err)
	}
	clock := auth.SystemClock{}
	hasher := auth.NewPasswordHasher(auth.DefaultArgon2Params())
	sessions := auth.NewSessionManager(meta.NewSessions(db), users, clock, time.Hour, 24*time.Hour)
	login, err := auth.NewLogin(users, hasher, sessions, clock)
	if err != nil {
		t.Fatal(err)
	}
	res, err := login.Login(ctx, m.AdminUsername, m.AdminPassword, "192.0.2.1", "upgrade-test")
	if err != nil || res.User.Role != meta.RoleAdmin {
		t.Fatalf("admin login after upgrade: %+v %v", res.User, err)
	}
	after, _ := users.Get(ctx, before.ID)
	if after.PasswordHash == before.PasswordHash || hasher.NeedsRehash(after.PasswordHash) {
		t.Error("outdated password hash was not re-hashed on login")
	}
	if _, err := sessions.Validate(ctx, res.Session.Token); err != nil {
		t.Errorf("new session invalid: %v", err)
	}
}

func assertKeys(ctx context.Context, t *testing.T, db *meta.DB, m fixtureManifest) {
	t.Helper()
	clock := auth.SystemClock{}
	conn, err := auth.NewConnectionAuthenticator(meta.NewConnections(db), clock).
		AuthenticateKey(ctx, m.ConnectionKey, auth.ProtocolSeq, netip.MustParseAddr("192.0.2.1"))
	if err != nil || conn.DefaultStream != "checkout" {
		t.Errorf("connection key after upgrade: %+v %v", conn, err)
	}
	p, err := auth.NewTokenAuthenticator(meta.NewAPITokens(db), meta.NewUsers(db), clock).Authenticate(ctx, m.APIToken)
	if err != nil || p.Role != auth.RoleViewer && p.Role != auth.RoleAdmin {
		t.Errorf("api token after upgrade: %+v %v", p, err)
	}
}

func assertContent(ctx context.Context, t *testing.T, db *meta.DB) {
	t.Helper()
	admin, _ := meta.NewUsers(db).GetByUsername(ctx, "admin")
	if d, err := meta.NewDashboards(db).ListVisible(ctx, admin.ID); err != nil || len(d) != 1 || !json.Valid(d[0].Doc) {
		t.Errorf("dashboards: %+v %v", d, err)
	}
	if s, err := meta.NewSignals(db).ListVisible(ctx, admin.ID); err != nil || len(s) != 1 || s[0].TQL == "" {
		t.Errorf("signals: %+v %v", s, err)
	}
	rules, err := meta.NewAlertRules(db).List(ctx)
	if err != nil || len(rules) != 1 || !rules[0].Enabled {
		t.Fatalf("alerts: %+v %v", rules, err)
	}
	if h, err := meta.NewAlertRules(db).History(ctx, rules[0].ID, 10); err != nil || len(h) != 1 {
		t.Errorf("alert history: %+v %v", h, err)
	}
}
