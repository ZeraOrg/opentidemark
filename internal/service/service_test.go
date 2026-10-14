// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/retention"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fastHasher() *auth.PasswordHasher {
	return auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
}

func openDB(t *testing.T) *meta.DB {
	t.Helper()
	db, err := meta.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type noopRevoker struct{}

func (noopRevoker) RevokeUser(context.Context, string, string) error { return nil }

func newUsersSvc(db *meta.DB) *Users {
	return NewUsers(UsersDeps{Store: meta.NewUsers(db), Sessions: noopRevoker{}, Tokens: meta.NewAPITokens(db),
		Settings: meta.NewSettings(db), Hasher: fastHasher(), Policy: auth.NewPasswordPolicy(),
		Audit: NewAuditor(meta.NewAuditLog(db), quiet(), time.Now), Now: time.Now})
}

func newSeeder(db *meta.DB) *Seeder {
	return NewSeeder(SeedStores{Users: meta.NewUsers(db), Connections: meta.NewConnections(db), Settings: meta.NewSettings(db)},
		fastHasher(), auth.NewPasswordPolicy(), quiet())
}

func TestSeedDefaultAdmin(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	s := newSeeder(db)
	res, err := s.Seed(ctx, SeedConfig{AdminUsername: "admin", AdminPassword: "admin", AdminPasswordIsDefault: true,
		BootstrapIngestKey: "tmk_bootstrapbootstrapbootstrap1"})
	if err != nil || !res.CreatedAdmin || !res.CreatedBootstrap || !res.DefaultCredentials {
		t.Fatalf("%+v %v", res, err)
	}
	u, _ := meta.NewUsers(db).GetByUsername(ctx, "admin")
	if !u.MustChangePassword || u.Role != meta.RoleAdmin {
		t.Errorf("admin %+v", u)
	}
	c, err := meta.NewConnections(db).GetByKeyHash(ctx, auth.HashKey("tmk_bootstrapbootstrapbootstrap1"), time.Now())
	if err != nil || c.Name != BootstrapConnectionName || len(c.Protocols) != 4 || c.DefaultStream != "default" {
		t.Errorf("bootstrap %+v %v", c, err)
	}
	res, err = s.Seed(ctx, SeedConfig{AdminUsername: "admin", AdminPassword: "admin", AdminPasswordIsDefault: true,
		BootstrapIngestKey: "tmk_bootstrapbootstrapbootstrap1"})
	if err != nil || res.CreatedAdmin || res.CreatedBootstrap || !res.DefaultCredentials {
		t.Errorf("second seed %+v %v", res, err)
	}
	users := newUsersSvc(db)
	if !users.DefaultCredentials(ctx) {
		t.Error("default credentials flag")
	}
	if err := users.ResetPassword(ctx, "admin", "Recovered-Pass-2026"); err != nil {
		t.Fatal(err)
	}
	if users.DefaultCredentials(ctx) {
		t.Error("flag not cleared by reset")
	}
	if err := users.ResetPassword(ctx, "admin", "short"); err == nil {
		t.Error("weak reset accepted")
	}
	if err := users.ResetPassword(ctx, "ghost", "Recovered-Pass-2026"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
}

func TestSeedCustomPassword(t *testing.T) {
	ctx := context.Background()
	if _, err := newSeeder(openDB(t)).Seed(ctx, SeedConfig{AdminUsername: "root", AdminPassword: "weak"}); err == nil {
		t.Error("weak configured password accepted")
	}
	db := openDB(t)
	res, err := newSeeder(db).Seed(ctx, SeedConfig{AdminUsername: "root", AdminPassword: "A-Strong-Configured-1"})
	if err != nil || !res.CreatedAdmin || res.DefaultCredentials {
		t.Errorf("%+v %v", res, err)
	}
	if u, _ := meta.NewUsers(db).GetByUsername(ctx, "root"); u.MustChangePassword {
		t.Error("configured password must not force a change")
	}
}

// TestCreateUnlimited guards the removal of the editions (SPEC §19): the user service accepts any
// number of enabled accounts and re-enabling an account is never refused for capacity.
func TestCreateUnlimited(t *testing.T) {
	ctx := context.Background()
	s := newUsersSvc(openDB(t))
	const n = 25
	var last meta.User
	for i := range n {
		u, err := s.Create(ctx, SystemActor, NewUser{Username: fmt.Sprintf("user%02d", i), Password: "Correct-Horse-Battery-9", Role: meta.RoleViewer})
		if err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
		last = u
	}
	disabled, enabled := true, false
	if _, err := s.Update(ctx, SystemActor, last.ID, UserPatch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, SystemActor, last.ID, UserPatch{Disabled: &enabled}); err != nil {
		t.Errorf("re-enable: %v", err)
	}
	if all, err := s.List(ctx); err != nil || len(all) != n {
		t.Errorf("list: %d %v", len(all), err)
	}
}

func TestStreamRegistry(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	repo := meta.NewStreams(db)
	_ = repo.Create(ctx, &meta.Stream{Name: "paused-one", Paused: true})
	r := NewStreamRegistry(repo, 4, quiet())
	if err := r.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Count() != 2 {
		t.Fatalf("count %d (default must be created)", r.Count())
	}
	tests := []struct {
		in, out string
		ok      bool
	}{
		{"paused-one", "paused-one", false}, {"app", "app", true}, {"app", "app", true}, {"web", "web", true},
		{"overflow", "default", true},
	}
	for _, tt := range tests {
		got, ok := r.Resolve(ctx, tt.in)
		if got != tt.out || ok != tt.ok {
			t.Errorf("Resolve(%s) = %s,%v", tt.in, got, ok)
		}
	}
	if list, _ := repo.List(ctx); len(list) != 4 {
		t.Errorf("persisted streams %d", len(list))
	}
}

type recordingApplier struct{ rules []retention.Rule }

func (r *recordingApplier) ApplyRetention(_ context.Context, rules []retention.Rule) error {
	r.rules = rules
	return nil
}

func TestRetentionSyncPolicy(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	settings, streams := meta.NewSettings(db), meta.NewStreams(db)
	days, spanDays := 90, 2
	_ = streams.Create(ctx, &meta.Stream{Name: "api", RetentionDays: &days, LevelRules: []meta.LevelRule{{Levels: []string{"Verbose", "debug", "bogus"}, Days: 3}}})
	_ = streams.Create(ctx, &meta.Stream{Name: "plain"})
	_ = streams.Create(ctx, &meta.Stream{Name: "traces", SpansRetentionDays: &spanDays})
	_ = settings.Set(ctx, SettingRetention, json.RawMessage(`{"logsDays":10,"spansDays":5,"metricsDays":20}`))
	app := &recordingApplier{}
	rs := NewRetentionSync(settings, streams, app, quiet())
	p, err := rs.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Defaults.LogsDays != 10 || len(p.Streams) != 2 || len(p.Streams[0].LevelRules[0].Levels) != 2 {
		t.Errorf("policy %+v", p)
	}
	if err := rs.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(app.rules) != 3 || !strings.Contains(app.rules[0].TTL, "level_num IN (0,1)") || !strings.Contains(app.rules[1].TTL, "toIntervalDay(2)") {
		t.Errorf("rules %+v", app.rules)
	}
	_ = settings.Set(ctx, SettingRetention, json.RawMessage(`{"logsDays":0,"spansDays":5,"metricsDays":20}`))
	if err := rs.Sync(ctx); err == nil {
		t.Error("invalid stored retention applied")
	}
	_ = settings.Set(ctx, SettingRetention, json.RawMessage(`"oops"`))
	if _, err := LoadRetention(ctx, settings); err == nil {
		t.Error("corrupt retention setting accepted")
	}
}

type failingStorage struct{}

func (failingStorage) Info(context.Context) (store.ServerInfo, error) {
	return store.ServerInfo{}, errors.New("down")
}

func TestSystemReport(t *testing.T) {
	start := time.Now().Add(-time.Minute)
	s := NewSystem(SystemDeps{Version: "v1", Started: start, Now: time.Now, Depths: func() map[string]int { return map[string]int{"events": 2} },
		Counters: func() map[string]float64 { return map[string]float64{"x": 1} }, Streams: func() int { return 3 }, Storage: failingStorage{}})
	r := s.Report(context.Background())
	if r.Version != "v1" || r.UptimeSeconds < 59 || r.QueueDepth["events"] != 2 || r.Streams != 3 || r.ClickHouse != nil || r.ClickHouseErr != "unavailable" {
		t.Errorf("%+v", r)
	}
}

func TestSnippetsHTTPS(t *testing.T) {
	pub, _ := url.Parse("https://logs.example.com")
	s := Snippets(meta.Connection{DefaultStream: "app"}, pub)
	joined := ""
	for _, sn := range s {
		joined += sn.Content
	}
	for _, want := range []string{"https://logs.example.com/otlp", "Port               443", "tls                On", KeyPlaceholder} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestSnippetLogstashUsesHTTPOutput: Logstash's elasticsearch output refuses servers that do not
// identify as Elasticsearch, so the snippet uses the http output to the native endpoint (SPEC §9.3).
func TestSnippetLogstashUsesHTTPOutput(t *testing.T) {
	pub, _ := url.Parse("https://logs.example.com")
	var logstash string
	for _, sn := range Snippets(meta.Connection{DefaultStream: "app"}, pub) {
		if sn.ID == "logstash" {
			logstash = sn.Content
		}
	}
	for _, want := range []string{"http {", `url => "https://logs.example.com/api/v1/ingest/logs"`,
		`format => "json_batch"`, `"X-Tidemark-Key" => "` + KeyPlaceholder + `"`} {
		if !strings.Contains(logstash, want) {
			t.Errorf("missing %q in %s", want, logstash)
		}
	}
	if strings.Contains(logstash, "elasticsearch") {
		t.Errorf("elasticsearch output in %s", logstash)
	}
}

func TestTokensLimit(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	users := meta.NewUsers(db)
	u := meta.User{Username: "u", PasswordHash: "x", Role: meta.RoleViewer}
	_ = users.Create(ctx, &u)
	ts := NewTokens(meta.NewAPITokens(db), NewAuditor(meta.NewAuditLog(db), quiet(), time.Now), time.Now)
	p := auth.Principal{UserID: u.ID, Role: auth.RoleViewer}
	for i := range maxTokensPerUser {
		if _, _, err := ts.Create(ctx, SystemActor, p, NewToken{Name: "t" + string(rune('a'+i%26)), Scope: "read"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := ts.Create(ctx, SystemActor, p, NewToken{Name: "one-too-many", Scope: "read"}); err == nil {
		t.Error("token limit not enforced")
	}
}

// TestPasswordResetRevokesTokens: a password set by an admin or the CLI recovery revokes the
// user's API tokens as well as the sessions (SPEC §15.2); a role change keeps them.
func TestPasswordResetRevokesTokens(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	u := meta.User{Username: "dana", PasswordHash: "x", Role: meta.RoleEditor}
	if err := meta.NewUsers(db).Create(ctx, &u); err != nil {
		t.Fatal(err)
	}
	svc := newUsersSvc(db)
	tokens := meta.NewAPITokens(db)
	ts := NewTokens(tokens, NewAuditor(meta.NewAuditLog(db), quiet(), time.Now), time.Now)
	p := auth.Principal{UserID: u.ID, Role: auth.RoleEditor}
	newToken := func(name string) string {
		tok, _, err := ts.Create(ctx, SystemActor, p, NewToken{Name: name, Scope: "read"})
		if err != nil {
			t.Fatal(err)
		}
		return tok.ID
	}
	revoked := func(id string) bool {
		tok, err := tokens.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return tok.RevokedAt != nil
	}
	viewer := string(auth.RoleViewer)
	kept := newToken("kept")
	if _, err := svc.Update(ctx, SystemActor, u.ID, UserPatch{Role: &viewer}); err != nil {
		t.Fatal(err)
	}
	if revoked(kept) {
		t.Error("role change revoked the token")
	}
	pw := "Admin-Set-Password-26"
	if _, err := svc.Update(ctx, SystemActor, u.ID, UserPatch{Password: &pw}); err != nil {
		t.Fatal(err)
	}
	if !revoked(kept) {
		t.Error("admin password reset kept the token")
	}
	cli := newToken("cli")
	if err := svc.ResetPassword(ctx, "dana", "Recovered-Pass-2026"); err != nil {
		t.Fatal(err)
	}
	if !revoked(cli) {
		t.Error("CLI password reset kept the token")
	}
}

// TestUserDeleteTransfersObjects: the deleting admin inherits the user's private dashboards and
// signals and can open them (SPEC §11.2).
func TestUserDeleteTransfersObjects(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	store := meta.NewUsers(db)
	admin := meta.User{Username: "boss", PasswordHash: "x", Role: meta.RoleAdmin}
	leaver := meta.User{Username: "leaver", PasswordHash: "x", Role: meta.RoleEditor}
	for _, u := range []*meta.User{&admin, &leaver} {
		if err := store.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	d := meta.Dashboard{Name: "private", OwnerID: leaver.ID, Doc: json.RawMessage(`{"version":1}`)}
	if err := meta.NewDashboards(db).Create(ctx, &d); err != nil {
		t.Fatal(err)
	}
	s := meta.Signal{Name: "mine", TQL: "*", OwnerID: leaver.ID, Source: "logs"}
	if err := meta.NewSignals(db).Create(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := newUsersSvc(db).Delete(ctx, SystemActor, admin.ID, leaver.ID); err != nil {
		t.Fatal(err)
	}
	audit := NewAuditor(meta.NewAuditLog(db), quiet(), time.Now)
	dashboards, err := NewDashboards(meta.NewDashboards(db), audit)
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{UserID: admin.ID, Role: auth.RoleAdmin}
	if got, err := dashboards.Get(ctx, p, d.ID); err != nil || got.OwnerID != admin.ID {
		t.Errorf("transferred dashboard: %+v %v", got, err)
	}
	if got, _ := meta.NewSignals(db).Get(ctx, s.ID); got.OwnerID != admin.ID {
		t.Errorf("transferred signal owner %q", got.OwnerID)
	}
}

func TestValidationError(t *testing.T) {
	err := invalid("field", "msg")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Error() != "field: msg" {
		t.Error(err)
	}
	if model.DefaultStream != "default" {
		t.Error("default stream")
	}
}
