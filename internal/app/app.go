// SPDX-License-Identifier: Apache-2.0

// Package app is the composition root (SPEC §6.2): it is the only package that constructs
// concrete implementations. It wires storage, services, ingestion, the API and the web UI,
// starts every listener of SPEC §7 and shuts them down gracefully.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"google.golang.org/grpc"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/cluster"
	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/pipeline"
	"github.com/ZeraOrg/opentidemark/internal/selfmetrics"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/wal"
)

// Timeouts of the lifecycle.
const (
	clickhouseStartupWait = 2 * time.Minute
	shutdownTimeout       = 30 * time.Second
	usageFlushInterval    = 10 * time.Second
	sessionSweepInterval  = time.Hour
)

// App is a fully wired OpenTidemark instance.
type App struct {
	cfg     config.Config
	logger  *slog.Logger
	version string
	started time.Time

	meta     *meta.DB
	chWriter driver.Conn
	chReader driver.Conn
	pipeline *pipeline.Pipeline
	usage    *auth.UsageTracker
	metrics  *selfmetrics.Metrics
	sessions *meta.Sessions
	tlsCfg   *tls.Config
	journals []*wal.Log
	elector  *cluster.Elector
	bus      cluster.Bus
	drainer  cluster.Drainer
	secret   []byte
	jobs     []background
	querier  store.Querier // query engine on the reader connection (SPEC §11.3)

	listeners []listener
	grpc      *grpc.Server
	grpcLn    net.Listener
	grpcAddr  string

	bg     sync.WaitGroup
	cancel context.CancelFunc
	errs   chan error
	closed bool
}

// listener is one HTTP listener.
type listener struct {
	name   string
	addr   string
	server *http.Server
	ln     net.Listener
}

// New builds the application: opens storage, applies migrations, seeds and wires everything.
func New(ctx context.Context, cfg config.Config, version string, logger *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, logger: logger, version: version, started: time.Now(), errs: make(chan error, 8)}
	for _, w := range cfg.Warnings {
		logger.Warn("configuration", "warning", w)
	}
	if err := a.openStorage(ctx); err != nil {
		a.closeStorage()
		return nil, err
	}
	if err := a.wire(ctx); err != nil {
		a.closeJournals()
		if a.pipeline != nil {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			_ = a.pipeline.Shutdown(sctx) // closes the WAL; the wiring error is what matters
			cancel()
		}
		a.closeStorage()
		return nil, err
	}
	return a, nil
}

func (a *App) openStorage(ctx context.Context) error {
	if err := os.MkdirAll(a.cfg.DataDir, dataDirPerm); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	key, err := ensureSecretKey(a.cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	a.secret = key
	db, err := a.openMeta(ctx)
	if err != nil {
		return err
	}
	a.meta = db
	return a.openClickHouse(ctx)
}

func (a *App) closeStorage() {
	var closers []func() error
	if a.chReader != nil {
		closers = append(closers, a.chReader.Close)
	}
	if a.chWriter != nil {
		closers = append(closers, a.chWriter.Close)
	}
	if a.meta != nil {
		closers = append(closers, a.meta.Close)
	}
	for _, c := range closers {
		if err := c(); err != nil {
			a.logger.Warn("close storage", "err", err)
		}
	}
	a.chReader, a.chWriter, a.meta = nil, nil, nil
}

// Start opens every listener and serves in the background.
func (a *App) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	if err := a.listen(); err != nil {
		cancel()
		return err
	}
	a.pipeline.Start() //nolint:contextcheck // queues run until Shutdown drains them under its own deadline
	a.startBackground(ctx)
	for _, l := range a.listeners {
		a.serveHTTP(l)
	}
	if a.grpc != nil {
		a.serveGRPC()
	}
	a.logger.Info("tidemark started", "version", a.version, "listeners", a.Addrs())
	return nil
}

// Run starts the app and blocks until ctx ends or a listener fails, then shuts down.
func (a *App) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-a.errs:
		a.logger.Error("fatal error, shutting down", "err", runErr)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), a.cfg.Drain+shutdownTimeout)
	defer cancel()
	return errors.Join(runErr, a.Shutdown(sctx))
}

// Addrs returns the bound address of each listener by name.
func (a *App) Addrs() map[string]string {
	out := map[string]string{}
	for _, l := range a.listeners {
		out[l.name] = l.ln.Addr().String()
	}
	if a.grpcAddr != "" {
		out["otlp-grpc"] = a.grpcAddr
	}
	return out
}

func (a *App) startBackground(ctx context.Context) {
	for _, job := range a.jobs {
		a.bg.Add(1)
		go func() {
			defer a.bg.Done()
			job(ctx)
		}()
	}
	for _, l := range a.journals {
		a.bg.Add(1)
		go func() {
			defer a.bg.Done()
			a.watchJournal(ctx, l)
		}()
	}
	a.bg.Add(4)
	go func() {
		defer a.bg.Done()
		a.elector.Run(ctx)
	}()
	go func() {
		defer a.bg.Done()
		a.bus.Run(ctx)
	}()
	go func() {
		defer a.bg.Done()
		a.usage.Run(ctx, usageFlushInterval, func(err error) { a.logger.Warn("connection usage flush failed", "err", err) })
	}()
	go func() {
		defer a.bg.Done()
		a.sweepSessions(ctx)
	}()
}

// watchJournal turns a failed WAL (a failed fsync, SPEC §9.8) into a fatal error: Run drains
// and returns it, the process exits non-zero and its supervisor restarts it; the startup
// recovery then rereads the segments. With TIDEMARK_WAL_EXIT_ON_FAILURE=false the node stays
// up, refuses ingestion with backpressure and reports /healthz as failed.
func (a *App) watchJournal(ctx context.Context, l failable) {
	select {
	case <-ctx.Done():
		return
	case <-l.Failed():
	}
	err := l.Err()
	if !a.cfg.WAL.ExitOnFailure {
		a.logger.Error("write-ahead log failed; ingestion is refused until the process restarts", "err", err)
		return
	}
	a.logger.Error("write-ahead log failed; stopping so the supervisor restarts the process", "err", err)
	select {
	case a.errs <- fmt.Errorf("wal: %w", err):
	default: // Run is already stopping on an earlier fatal error
	}
}

// failable is a WAL as watchJournal sees it (implemented by *wal.Log).
type failable interface {
	Failed() <-chan struct{}
	Err() error
}

// walFailure returns the error of the first failed WAL, or nil (/healthz).
func (a *App) walFailure() error {
	for _, l := range a.journals {
		if err := l.Err(); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) sweepSessions(ctx context.Context) {
	t := time.NewTicker(sessionSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := a.sessions.DeleteExpired(ctx, time.Now(), a.cfg.SessionIdleTimeout); err != nil {
				a.logger.Warn("session cleanup failed", "err", err)
			}
		}
	}
}

// Shutdown drains (readiness 503 for TIDEMARK_DRAIN_SECONDS), stops listeners, flushes the
// pipeline/WAL and closes storage (SPEC §17.5).
func (a *App) Shutdown(ctx context.Context) error {
	if a.closed {
		return nil
	}
	a.closed = true
	a.logger.Info("draining", "seconds", a.cfg.Drain.Seconds())
	a.drainer.Drain(ctx, a.cfg.Drain)
	var errs []error
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, l := range a.listeners {
		wg.Add(1)
		go func(l listener) {
			defer wg.Done()
			if err := l.server.Shutdown(ctx); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("shutdown %s: %w", l.name, err))
				mu.Unlock()
			}
		}(l)
	}
	if a.grpc != nil {
		stopGRPC(ctx, a.grpc)
	}
	wg.Wait()
	if err := a.pipeline.Shutdown(ctx); err != nil {
		errs = append(errs, err)
	}
	if a.cancel != nil {
		a.cancel()
	}
	a.bg.Wait()
	a.closeStorage()
	a.logger.Info("tidemark stopped")
	return errors.Join(errs...)
}

// stopGRPC drains in-flight RPCs, forcing a stop when ctx ends.
func stopGRPC(ctx context.Context, s *grpc.Server) {
	done := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		s.Stop()
		<-done
	}
}
