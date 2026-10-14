// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/tidemark/internal/alerting"
	"github.com/ZeraOrg/tidemark/internal/api"
	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/cluster"
	"github.com/ZeraOrg/tidemark/internal/httpx"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
)

// opsDeps are the shared collaborators of the stream, dashboard and alerting modules.
type opsDeps struct {
	registry  *service.StreamRegistry
	retention *service.RetentionSync
	audit     *service.Auditor
	leader    service.Leader
	streams   *meta.Streams
	teams     *meta.Teams
	access    service.ScopeResolver
	clientIP  func(*http.Request) netip.Addr
	querier   alerting.Querier // store.Querier of the query module; nil ⇒ evaluations fail with alerting.ErrNoQuerier
}

// background is a job started with the listeners and stopped on shutdown.
type background func(ctx context.Context)

// wireOps builds the stream management (SPEC §12), dashboard (§13) and alerting (§14) modules
// and returns their API registrars and background jobs.
func (a *App) wireOps(d opsDeps) ([]api.Registrar, []background, error) {
	admin, err := ch.NewAdmin(a.chWriter, a.cfg.ClickHouse.Database, time.Now)
	if err != nil {
		return nil, nil, err
	}
	indexed := service.NewIndexedFields(meta.NewIndexedFields(a.meta), admin, d.audit)
	streams := service.NewStreamAdmin(service.StreamAdminDeps{Repo: d.streams, Registry: d.registry, Storage: admin,
		Grants: d.teams, Access: d.access, Retention: d.retention, Indexed: indexed, Audit: d.audit, Leader: d.leader, Now: time.Now, Logger: a.logger})
	purges := service.NewPurges(meta.NewPurgeJobs(a.meta), admin, d.streams, d.audit, a.logger)
	dashboards, err := service.NewDashboards(meta.NewDashboards(a.meta), d.audit)
	if err != nil {
		return nil, nil, err
	}
	alertsAPI, alertJobs, err := a.wireAlerting(d, indexed)
	if err != nil {
		return nil, nil, err
	}
	imports, importRunner, err := buildImporter(importDeps{meta: a.meta, conn: a.chWriter, database: a.cfg.ClickHouse.Database,
		writer: a.writerOptions(), secret: a.secret, streams: d.registry, audit: d.audit, elector: a.elector,
		nodeID: a.cfg.NodeID, dataDir: a.cfg.DataDir, logger: a.logger})
	if err != nil {
		return nil, nil, err
	}
	regs := []api.Registrar{
		&api.StreamsAPI{Streams: streams, Indexed: indexed, Purges: purges, ClientIP: d.clientIP},
		&api.DashboardsAPI{Dashboards: dashboards, ClientIP: d.clientIP},
		&api.ImportsAPI{Imports: imports, ClientIP: d.clientIP},
		alertsAPI,
	}
	jobs := append([]background{
		func(ctx context.Context) { streams.RunStatsCollector(ctx, service.StatsInterval) },
		func(ctx context.Context) { a.elector.Lead(ctx, cluster.LeasePurge, purges.Run) },
		importRunner.Run,
	}, alertJobs...)
	return regs, jobs, nil
}

// wireAlerting builds channels (secrets sealed with the secret key), the notifiers on the
// SSRF-safe client, the dispatcher, the evaluator and the alert services (SPEC §14).
func (a *App) wireAlerting(d opsDeps, indexed alerting.IndexedColumns) (*api.AlertsAPI, []background, error) {
	box, err := auth.NewSecretBox(a.secret)
	if err != nil {
		return nil, nil, err
	}
	smtp := alerting.SMTPConfig{Host: a.cfg.SMTP.Host, Port: a.cfg.SMTP.Port, Username: a.cfg.SMTP.Username,
		Password: a.cfg.SMTP.Password, From: a.cfg.SMTP.From, StartTLS: a.cfg.SMTP.StartTLS}
	rules := meta.NewAlertRules(a.meta)
	publicURL := a.cfg.PublicURL.String()
	channels := service.NewChannels(service.ChannelsDeps{Repo: meta.NewChannels(a.meta), Rules: rules, Box: box,
		Policy: alerting.ChannelPolicy{AllowPrivate: a.cfg.AllowPrivateWebhooks, SMTPConfigured: smtp.Configured()},
		Audit:  d.audit, PublicURL: publicURL})
	client := httpx.SafeClient(a.cfg.AllowPrivateWebhooks, alerting.SendTimeout)
	dispatcher := alerting.NewDispatcher(channels, a.logger, time.Now, alerting.Webhook{Client: client},
		alerting.Slack{Client: client}, alerting.Teams{Client: client}, alerting.Email{SMTP: smtp})
	channels.SetTester(dispatcher)
	evaluator := alerting.NewEvaluator(alerting.EvaluatorDeps{Rules: rules, Querier: d.querier, Notify: dispatcher,
		Clock: auth.SystemClock{}, Leader: d.leader, Indexed: indexed, Database: a.cfg.ClickHouse.Database,
		PublicURL: publicURL, Logger: a.logger})
	alerts := service.NewAlerts(service.AlertsDeps{Repo: rules, Channels: channels, Evaluator: evaluator, Access: d.access,
		Audit: d.audit, Now: time.Now})
	jobs := []background{
		dispatcher.Run,
		func(ctx context.Context) { evaluator.Run(ctx, alerting.TickInterval) },
	}
	return &api.AlertsAPI{Alerts: alerts, Channels: channels, ClientIP: d.clientIP}, jobs, nil
}
