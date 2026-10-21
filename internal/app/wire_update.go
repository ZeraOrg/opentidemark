// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/api"
	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/httpx"
	"github.com/ZeraOrg/opentidemark/internal/service"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/opentidemark/internal/update"
)

// updateHosts are the only hosts the update client may reach, including redirects (SPEC §15.6):
// the GitHub API and the hosts GitHub serves release assets from.
var updateHosts = []string{"api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"}

// Timeouts and modes of the update wiring.
const (
	updateRequestTimeout  = 30 * time.Second // one request of the release check
	updateDownloadTimeout = 15 * time.Minute // one release download, the archive included
	updateReadyTimeout    = 5 * time.Second  // one readiness probe of the update watcher
	helperWorkDirPerm     = 0o755            // state.json is read by the service
)

// ErrRestartForUpdate is returned by Run when the server stops so that its service manager
// starts the binary the update helper installed or restored (cmd/tidemark exits with status 3).
var ErrRestartForUpdate = update.ErrRestartForUpdate

// wireUpdate builds the updates of SPEC §17.7: the allow-listed update client, the GitHub
// source, the checker job and the tidemark_update_available gauge; on a service install the
// watcher of the update helper; and where one-click apply is supported, the applier.
func (a *App) wireUpdate(audit *service.Auditor, clientIP func(*http.Request) netip.Addr) (*api.UpdateAPI, []background, error) {
	build := updateBuild(a.build)
	install := updateInstall(a.cfg, build)
	checker, err := newUpdateChecker(a.cfg, build, install, a.logger)
	if err != nil {
		return nil, nil, err
	}
	a.metrics.UpdateAvailable(checker.Available)
	apply := install.Apply()
	a.logger.Info("update check", "enabled", install.Enabled, "install_kind", install.Kind, "apply_supported", apply.Supported,
		"apply_reason", apply.Reason, "proxy", httpx.ProxyDisplay(a.cfg.Update.Proxy)) // never the proxy credentials
	jobs := []background{checker.Run}
	var applier *update.Applier
	paths, perr := update.DefaultPaths()
	if perr == nil && (install.Kind == update.KindSystemd || install.Kind == update.KindWindowsService) {
		events := updateEvents{audit: audit}
		reports := update.NewReporter(paths.Staging, events, a.logger)
		// systemd restarts the service on exit status 3; the Windows helper restarts it itself.
		jobs = append(jobs, a.updateWatcher(paths, install.Kind == update.KindSystemd, reports).Run)
		if apply.Supported {
			if applier, err = a.newApplier(paths, reports, events); err != nil {
				return nil, nil, err
			}
			jobs = append(jobs, applier.Run)
		}
	}
	updates := update.NewManager(checker, applier, a.updateBusy)
	return &api.UpdateAPI{Updates: updates, Audit: audit, ClientIP: clientIP}, jobs, nil
}

func updateBuild(b BuildInfo) update.Build {
	return update.Build{Version: b.Version, Distribution: b.Distribution, WindowsService: b.WindowsService}
}

func updateInstall(cfg config.Config, build update.Build) update.Install {
	kind := update.DetectInstallKind(build)
	return update.Install{Enabled: cfg.Update.Check, Kind: kind, HA: cfg.HA(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Helper: update.HelperInstalled(kind)}
}

// newUpdateChecker builds the checker with the allow-listed update client and the GitHub source.
func newUpdateChecker(cfg config.Config, build update.Build, install update.Install, logger *slog.Logger) (*update.Checker, error) {
	client, err := httpx.UpdateClient(httpx.UpdateClientOptions{AllowedHosts: updateHosts, Proxy: cfg.Update.Proxy,
		Timeout: updateRequestTimeout})
	if err != nil {
		return nil, err
	}
	return update.NewChecker(update.CheckerDeps{Source: update.NewGitHubSource(client, update.GitHubAPIBase, time.Now),
		Build: build, Install: install, NodeID: cfg.NodeID, Now: time.Now, After: time.After, Rand: jitter, Logger: logger}), nil
}

// newApplier builds the one-click apply: downloads through the allow-listed update client with
// the download timeout, staged into paths.Staging.
func (a *App) newApplier(paths update.HelperPaths, reports *update.Reporter, events update.Events) (*update.Applier, error) {
	client, err := httpx.UpdateClient(httpx.UpdateClientOptions{AllowedHosts: updateHosts, Proxy: a.cfg.Update.Proxy,
		Timeout: updateDownloadTimeout})
	if err != nil {
		return nil, err
	}
	platform := update.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	return update.NewApplier(update.ApplierDeps{
		Stager: update.NewStager(client, paths.Staging, update.TrustedSigningKeys(), platform), Paths: paths,
		Current: a.build.Version, Trigger: update.StartHelper, FreeSpace: freeSpace, Reports: reports, Events: events,
		NewID: uuid.NewString, Now: time.Now, After: time.After, Logger: a.logger,
	}), nil
}

// freeSpace returns the free bytes of the file system holding dir.
func freeSpace(dir string) (uint64, error) {
	free, _, err := diskSpace(dir)
	return free, err
}

// updateBusy says why this node must not update right now: it is draining or a WAL failed.
func (a *App) updateBusy() string {
	switch {
	case a.drainer.Draining():
		return update.ReasonDraining
	case a.walFailure() != nil:
		return update.ReasonWALFailed
	default:
		return ""
	}
}

// updateEvents writes the outcomes of updates to the audit log as the system (SPEC §15.5).
type updateEvents struct{ audit *service.Auditor }

func (e updateEvents) Installed(ctx context.Context, from, to string) {
	e.audit.Record(ctx, service.SystemActor, service.ActionUpdateInstalled, to, map[string]any{"from": from, "to": to})
}

func (e updateEvents) RolledBack(ctx context.Context, from, to, reason string) {
	e.audit.Record(ctx, service.SystemActor, service.ActionUpdateRolledBack, to,
		map[string]any{"from": from, "to": to, "reason": reason})
}

func (e updateEvents) Failed(ctx context.Context, to, stage, reason string) {
	e.audit.Record(ctx, service.SystemActor, service.ActionUpdateFailed, to,
		map[string]any{"to": to, "stage": stage, "reason": reason})
}

// updateWatcher confirms an installed update once this server is ready, audits the helper's
// outcomes and, with restart, stops the server when the helper changed the binary (SPEC §17.7).
func (a *App) updateWatcher(paths update.HelperPaths, restart bool, reports *update.Reporter) *update.Watcher {
	return update.NewWatcher(update.WatcherConfig{Paths: paths, Version: a.build.Version, Restart: restart,
		Ready: a.updateReady, Fatal: a.fatal, Reports: reports, Now: time.Now, After: time.After, Logger: a.logger})
}

// updateReady reports what /readyz reports, plus a working WAL: metadata and ClickHouse answer,
// no drain has started and no write-ahead log failed.
func (a *App) updateReady(ctx context.Context) bool {
	if a.drainer.Draining() || a.walFailure() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, updateReadyTimeout)
	defer cancel()
	return a.meta.Ping(ctx) == nil && ch.Pinger{Conn: a.chWriter}.Ping(ctx) == nil
}

// fatal hands err to Run, which drains and returns it; a later error is dropped while Run is
// already stopping.
func (a *App) fatal(err error) {
	select {
	case a.errs <- err:
	default: // Run is already stopping on an earlier fatal error
	}
}

// CheckForUpdate runs one release check for `tidemark update check` (SPEC §17.7) and returns the
// status. It fails with update.UnavailableError when the check is disabled or this is a
// development build.
func CheckForUpdate(ctx context.Context, cfg config.Config, build BuildInfo) (update.Status, error) {
	b := updateBuild(build)
	checker, err := newUpdateChecker(cfg, b, updateInstall(cfg, b), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return update.Status{}, err
	}
	return checker.CheckNow(ctx)
}

// RunUpdateHelper is the privileged update helper of `tidemark update install` (Linux) and the
// TidemarkUpdate service (Windows): it installs the staged release over this binary, has the
// service restart (restart, nil on Linux), waits for the confirmation and rolls back without it.
func RunUpdateHelper(ctx context.Context, version string, restart func(context.Context) error, logger *slog.Logger) error {
	paths, err := update.DefaultPaths()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.Work, helperWorkDirPerm); err != nil {
		return fmt.Errorf("update work directory: %w", err)
	}
	inst, err := update.NewInstaller(update.InstallerConfig{StagingDir: paths.Staging, WorkDir: paths.Work,
		BinaryPath: paths.Binary, Current: version, Platform: update.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Keys: update.TrustedSigningKeys(), Now: time.Now})
	if err != nil {
		return err
	}
	return update.RunHelper(ctx, update.HelperDeps{Installer: inst, Restart: restart, Timeout: update.ConfirmTimeout,
		Logger: logger})
}

// jitter spreads the update checks of many installations over time.
func jitter(n int64) int64 {
	return rand.Int64N(n) //nolint:gosec // schedule jitter, not a secret
}
