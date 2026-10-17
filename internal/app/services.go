// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"path/filepath"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/api"
	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/cluster"
	"github.com/ZeraOrg/opentidemark/internal/httpx"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/pipeline"
	"github.com/ZeraOrg/opentidemark/internal/selfmetrics"
	"github.com/ZeraOrg/opentidemark/internal/service"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
)

// deadLetterDir is the dead-letter folder inside the data directory.
const deadLetterDir = "deadletter"

// components holds what wire builds and the HTTP layer needs.
type components struct {
	router  *api.Router
	ingest  *ingest.Deps
	realIP  *httpx.RealIP
	health  *api.Health
	metrics *selfmetrics.Metrics
}

// repos groups the metadata repositories.
type repos struct {
	users       *meta.Users
	sessions    *meta.Sessions
	connections *meta.Connections
	tokens      *meta.APITokens
	streams     *meta.Streams
	settings    *meta.Settings
	audit       *meta.AuditLog
	teams       *meta.Teams
}

func newRepos(db *meta.DB) repos {
	return repos{users: meta.NewUsers(db), sessions: meta.NewSessions(db), connections: meta.NewConnections(db),
		tokens: meta.NewAPITokens(db), streams: meta.NewStreams(db), settings: meta.NewSettings(db), audit: meta.NewAuditLog(db),
		teams: meta.NewTeams(db)}
}

// wire builds every service, the pipeline and the HTTP/gRPC servers.
func (a *App) wire(ctx context.Context) error {
	r := newRepos(a.meta)
	a.sessions = r.sessions
	clock := auth.SystemClock{}
	hasher := auth.NewPasswordHasher(auth.DefaultArgon2Params())
	policy := auth.NewPasswordPolicy()
	audit := service.NewAuditor(r.audit, a.logger, time.Now)
	access := service.NewAccess(r.teams, r.settings)

	sessions := auth.NewSessionManager(r.sessions, r.users, clock, a.cfg.SessionIdleTimeout, a.cfg.SessionAbsoluteTimeout)
	users := service.NewUsers(service.UsersDeps{Store: r.users, Sessions: sessions, Tokens: r.tokens, Settings: r.settings,
		Hasher: hasher, Policy: policy, Teams: r.teams, Audit: audit, Now: time.Now})
	teams := service.NewTeams(service.TeamsDeps{Repo: r.teams, Users: r.users, Settings: r.settings, Audit: audit})
	login, err := auth.NewLogin(r.users, hasher, sessions, clock)
	if err != nil {
		return err
	}
	if err := a.seed(ctx, r, hasher, policy); err != nil {
		return err
	}

	registry := service.NewStreamRegistry(r.streams, a.cfg.MaxStreams, a.logger)
	if err := registry.Load(ctx); err != nil {
		return err
	}
	retentionMgr, err := ch.NewRetentionManager(a.chWriter, a.cfg.ClickHouse.Database)
	if err != nil {
		return err
	}
	retSync := service.NewRetentionSync(r.settings, r.streams, retentionMgr, a.logger)
	if err := retSync.Sync(ctx); err != nil {
		a.logger.Error("applying retention failed", "err", err)
	}

	if err := a.buildPipeline(registry); err != nil {
		return err
	}
	a.buildCluster()
	connAuth := auth.NewConnectionAuthenticator(r.connections, clock, a.keyCacheOptions()...)
	a.bus.Subscribe(cluster.TopicConnections, connAuth.Invalidate)
	keyCache := cluster.BroadcastCache{Bus: a.bus, Topic: cluster.TopicConnections, Logger: a.logger}
	a.usage = auth.NewUsageTracker(r.connections, clock)
	realIP := httpx.NewRealIP(a.cfg.TrustedProxies)

	c := &components{realIP: realIP, metrics: a.metrics}
	c.ingest = a.ingestDeps(connAuth, realIP, clock)
	c.health = &api.Health{Checks: []api.Pinger{a.meta, ch.Pinger{Conn: a.chWriter}}, Draining: a.drainer.Draining,
		Failure: a.walFailure}
	c.router = api.NewRouter(api.RouterDeps{Sessions: sessions, Tokens: auth.NewTokenAuthenticator(r.tokens, r.users, clock),
		Limiter: auth.NewKeyedLimiter(clock, api.APIRateBurst, api.APIRateWindow), PublicURL: a.cfg.PublicURL,
		ClientIP: realIP.ClientIP, Logger: a.logger})
	ip := realIP.ClientIP
	searchRegs, streams, err := a.searchAPIs(audit, access, ip)
	if err != nil {
		return err
	}
	c.router.Register(searchRegs...)
	c.router.Register(
		&api.AuthAPI{Login: login, Sessions: sessions, Users: users, Access: access, Teams: teams, Audit: audit,
			SecureCookies: a.cfg.SecureCookies(), ClientIP: ip},
		&api.UsersAPI{Users: users, Teams: teams, ClientIP: ip},
		&api.TeamsAPI{Teams: teams, ClientIP: ip},
		&api.TokensAPI{Tokens: service.NewTokens(r.tokens, audit, time.Now), ClientIP: ip},
		&api.ConnectionsAPI{Connections: service.NewConnections(r.connections, keyCache, audit, time.Now), PublicURL: a.cfg.PublicURL, ClientIP: ip},
		&api.SettingsAPI{Settings: service.NewSettings(r.settings, retSync, audit), ClientIP: ip},
		&api.AuditAPI{Audit: audit, Now: time.Now},
		&api.SystemAPI{System: service.NewSystem(service.SystemDeps{Version: a.version, Started: a.started, Now: time.Now,
			Depths: a.pipeline.Depths, Counters: a.metrics.Totals, Streams: registry.Count, Storage: ch.Info{Conn: a.chWriter}})},
	)
	ops, jobs, err := a.wireOps(opsDeps{registry: registry, retention: retSync, audit: audit, leader: a.elector,
		streams: r.streams, teams: r.teams, access: access, clientIP: ip, querier: a.querier})
	if err != nil {
		return err
	}
	c.router.Register(ops...)
	a.jobs = jobs
	if err := a.buildServers(c); err != nil {
		return err
	}
	a.onShutdown(streams.Close)
	return nil
}

func (a *App) seed(ctx context.Context, r repos, hasher *auth.PasswordHasher, policy *auth.PasswordPolicy) error {
	seeder := service.NewSeeder(service.SeedStores{Users: r.users, Connections: r.connections, Settings: r.settings},
		hasher, policy, a.logger)
	_, err := seeder.Seed(ctx, service.SeedConfig{AdminUsername: a.cfg.AdminUsername, AdminPassword: a.cfg.AdminPassword,
		AdminPasswordIsDefault: a.cfg.AdminPasswordIsDefault, BootstrapIngestKey: a.cfg.BootstrapIngestKey})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}

func (a *App) buildPipeline(gate pipeline.StreamGate) error {
	writer, err := ch.NewWriter(a.chWriter, a.cfg.ClickHouse.Database, a.writerOptions()...)
	if err != nil {
		return err
	}
	a.metrics = selfmetrics.New()
	journals, err := a.openWAL()
	if err != nil {
		return err
	}
	a.pipeline = pipeline.New(pipeline.Config{MaxRows: a.cfg.BatchMaxRows, MaxWait: a.cfg.BatchMaxWait, QueueRows: a.cfg.QueueMaxRows},
		pipeline.Deps{Events: writer, Spans: writer, Metrics: writer, Gate: gate,
			Dead: pipeline.NewFileDeadLetter(filepath.Join(a.cfg.DataDir, deadLetterDir), a.cfg.DeadLetterMaxBytes,
				time.Now, a.logger),
			Stats: a.metrics.Pipeline(), Logger: a.logger, Now: time.Now, WAL: journals})
	a.handOffJournals()
	return nil
}

func (a *App) ingestDeps(keys ingest.KeyAuthenticator, realIP *httpx.RealIP, clock auth.Clock) *ingest.Deps {
	return &ingest.Deps{
		Auth: keys, Events: a.pipeline, Spans: a.pipeline, Metrics: a.pipeline,
		Limiter: auth.NewKeyedLimiter(clock, 1, time.Second), Usage: a.usage, Observer: a.metrics,
		Limits:   ingest.Limits{MaxBodyBytes: a.cfg.MaxBodyBytes, MaxDecompressedBytes: a.cfg.MaxDecompressedBytes},
		Now:      time.Now,
		ClientIP: func(r *http.Request) netip.Addr { return realIP.ClientIP(r) },
		Logger:   a.logger, Version: a.version,
	}
}
