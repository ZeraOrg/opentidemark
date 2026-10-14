// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/importer"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/retention"
	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

const testPassword = "Correct-Horse-Battery-9"

type fakeApplier struct{ calls int }

func (f *fakeApplier) ApplyRetention(context.Context, []retention.Rule) error { f.calls++; return nil }

type fakeStorage struct{}

func (fakeStorage) Info(context.Context) (store.ServerInfo, error) {
	return store.ServerInfo{Version: "25.8.0", Disks: []store.DiskInfo{{Name: "default", FreeBytes: 1, TotalBytes: 2}}}, nil
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }

// env is a fully wired API on a temporary SQLite database.
type env struct {
	t        *testing.T
	handler  http.Handler
	router   *Router
	db       *meta.DB
	users    *service.Users
	applier  *fakeApplier
	sessions map[string]*client
	querier  *testutil.FakeQuerier
	admin    *fakeAdmin
}

// client carries one principal's cookies.
type client struct {
	cookies []*http.Cookie
	csrf    string
	bearer  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := meta.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := auth.SystemClock{}
	// The API limiter uses a frozen clock so slow (-race) runs cannot refill buckets mid-test.
	frozen := &testClock{now: time.Now()}
	usersRepo, sessRepo, tokRepo, connRepo := meta.NewUsers(db), meta.NewSessions(db), meta.NewAPITokens(db), meta.NewConnections(db)
	settings, streams := meta.NewSettings(db), meta.NewStreams(db)
	hasher := auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	policy := auth.NewPasswordPolicy()
	audit := service.NewAuditor(meta.NewAuditLog(db), logger, time.Now)
	sm := auth.NewSessionManager(sessRepo, usersRepo, clock, time.Hour, 24*time.Hour)
	users := service.NewUsers(service.UsersDeps{Store: usersRepo, Sessions: sm, Tokens: tokRepo, Settings: settings,
		Hasher: hasher, Policy: policy, Teams: meta.NewTeams(db), Audit: audit, Now: time.Now})
	teams := service.NewTeams(service.TeamsDeps{Repo: meta.NewTeams(db), Users: usersRepo, Settings: settings, Audit: audit})
	login, err := auth.NewLogin(usersRepo, hasher, sm, clock)
	if err != nil {
		t.Fatal(err)
	}
	applier := &fakeApplier{}
	rs := service.NewRetentionSync(settings, streams, applier, logger)
	pub, _ := url.Parse("http://tidemark.test")
	ip := func(*http.Request) netip.Addr { return netip.MustParseAddr("192.0.2.10") }
	rt := NewRouter(RouterDeps{Sessions: sm, Tokens: auth.NewTokenAuthenticator(tokRepo, usersRepo, clock),
		Limiter: auth.NewKeyedLimiter(frozen, APIRateBurst, APIRateWindow), PublicURL: pub, ClientIP: ip, Logger: logger})
	admin := &fakeAdmin{}
	registry := service.NewStreamRegistry(streams, 50, logger)
	if err := registry.Load(ctx); err != nil {
		t.Fatal(err)
	}
	indexed := service.NewIndexedFields(meta.NewIndexedFields(db), admin, audit)
	streamAdmin := service.NewStreamAdmin(service.StreamAdminDeps{Repo: streams, Registry: registry, Storage: admin,
		Grants: meta.NewTeams(db), Access: service.NewAccess(meta.NewTeams(db), meta.NewSettings(db)), Retention: rs, Indexed: indexed, Audit: audit, Leader: alwaysLeader{}, Now: time.Now, Logger: logger})
	purges := service.NewPurges(meta.NewPurgeJobs(db), admin, streams, audit, logger)
	dashboards, err := service.NewDashboards(meta.NewDashboards(db), audit)
	if err != nil {
		t.Fatal(err)
	}
	box, err := auth.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	ruleRepo := meta.NewAlertRules(db)
	channels := service.NewChannels(service.ChannelsDeps{Repo: meta.NewChannels(db), Rules: ruleRepo, Box: box,
		Policy: alerting.ChannelPolicy{AllowPrivate: true}, Audit: audit, PublicURL: "http://tidemark.test"})
	channels.SetTester(alerting.NewDispatcher(channels, logger, time.Now, alerting.Webhook{Client: http.DefaultClient}))
	evaluator := alerting.NewEvaluator(alerting.EvaluatorDeps{Rules: ruleRepo, Querier: &fakeQuerier{value: 42},
		Notify: alerting.NewDispatcher(channels, logger, time.Now), Clock: wallClock{}, Leader: alwaysLeader{},
		Database: "tidemark", Logger: logger})
	alerts := service.NewAlerts(service.AlertsDeps{Repo: ruleRepo, Channels: channels, Evaluator: evaluator,
		Access: service.NewAccess(meta.NewTeams(db), meta.NewSettings(db)), Audit: audit, Now: time.Now})
	rt.Register(
		&AlertsAPI{Alerts: alerts, Channels: channels, ClientIP: ip},
		&DashboardsAPI{Dashboards: dashboards, ClientIP: ip},
		&StreamsAPI{Streams: streamAdmin, Indexed: indexed, Purges: purges, ClientIP: ip},
		&AuthAPI{Login: login, Sessions: sm, Users: users, Access: service.NewAccess(meta.NewTeams(db), settings), Teams: teams,
			Audit: audit, ClientIP: ip},
		&UsersAPI{Users: users, Teams: teams, ClientIP: ip},
		&TeamsAPI{Teams: teams, ClientIP: ip},
		&TokensAPI{Tokens: service.NewTokens(tokRepo, audit, time.Now), ClientIP: ip},
		&ConnectionsAPI{Connections: service.NewConnections(connRepo, auth.NewConnectionAuthenticator(connRepo, clock), audit, time.Now), PublicURL: pub, ClientIP: ip},
		&SettingsAPI{Settings: service.NewSettings(settings, rs, audit), ClientIP: ip},
		&AuditAPI{Audit: audit, Now: time.Now},
		&SystemAPI{System: service.NewSystem(service.SystemDeps{Version: "test", Started: time.Now(), Now: time.Now,
			Depths: func() map[string]int { return map[string]int{} }, Counters: func() map[string]float64 { return nil },
			Streams: func() int { return 1 }, Storage: fakeStorage{}})},
	)
	imports := importer.NewService(importer.ServiceDeps{Jobs: meta.NewImportJobs(db), Box: box,
		Signals: importer.Signals{Store: meta.NewSignals(db)}, Streams: registry, Audit: audit,
		UploadDir: filepath.Join(t.TempDir(), "imports"), NodeID: "node-test", Now: time.Now})
	rt.Register(&ImportsAPI{Imports: imports, ClientIP: ip})
	querier := &testutil.FakeQuerier{}
	rt.Register(searchModules(db, querier, audit, ip, logger)...)
	e := &env{t: t, handler: rt, router: rt, db: db, users: users, applier: applier,
		sessions: map[string]*client{}, querier: querier, admin: admin}
	for _, role := range []string{meta.RoleAdmin, meta.RoleEditor, meta.RoleViewer} {
		if _, err := users.Create(ctx, service.SystemActor, service.NewUser{Username: role + "1", Password: testPassword, Role: role}); err != nil {
			t.Fatal(err)
		}
		e.sessions[role] = e.login(role+"1", testPassword)
	}
	return e
}

func (e *env) login(username, password string) *client {
	e.t.Helper()
	w := e.do(nil, "POST", "/auth/login", map[string]string{"username": username, "password": password})
	if w.Code != http.StatusOK {
		e.t.Fatalf("login %s: %d %s", username, w.Code, w.Body.String())
	}
	c := &client{cookies: w.Result().Cookies()}
	for _, ck := range c.cookies {
		if ck.Name == auth.CSRFCookie {
			c.csrf = ck.Value
		}
	}
	return c
}

// do sends a request as c (nil = anonymous); body is JSON-encoded unless it is a string.
func (e *env) do(c *client, method, path string, body any) *httptest.ResponseRecorder {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		r = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, Prefix+path, r)
	if c != nil {
		for _, ck := range c.cookies {
			req.AddCookie(ck)
		}
		if c.csrf != "" {
			req.Header.Set(auth.CSRFHeader, c.csrf)
		}
		if c.bearer != "" {
			req.Header.Set("Authorization", "Bearer "+c.bearer)
		}
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}
