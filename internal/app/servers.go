// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
	"net"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/ZeraOrg/tidemark/internal/api"
	"github.com/ZeraOrg/tidemark/internal/httpx"
	"github.com/ZeraOrg/tidemark/internal/ingest/elastic"
	"github.com/ZeraOrg/tidemark/internal/ingest/native"
	"github.com/ZeraOrg/tidemark/internal/ingest/otlp"
	"github.com/ZeraOrg/tidemark/internal/ingest/seq"
	"github.com/ZeraOrg/tidemark/internal/web"
)

// Listener names.
const (
	ListenerHTTP     = "http"
	ListenerSeq      = "seq"
	ListenerES       = "elastic"
	ListenerOTLPHTTP = "otlp-http"
)

// buildServers creates the HTTP servers and the gRPC server (not yet listening).
func (a *App) buildServers(c *components) error {
	if a.cfg.TLSEnabled() {
		cfg, err := httpx.LoadTLS(a.cfg.TLSCertFile, a.cfg.TLSKeyFile)
		if err != nil {
			return err
		}
		a.tlsCfg = cfg
	}
	exporter := otlp.NewExporter(c.ingest)
	main, err := a.mainMux(c, exporter)
	if err != nil {
		return err
	}
	a.addListener(ListenerHTTP, a.cfg.HTTPAddr, main, c)
	if a.cfg.SeqAddr != "" {
		mux := http.NewServeMux()
		seq.New(c.ingest).Register(mux)
		c.health.Register(mux)
		a.addListener(ListenerSeq, a.cfg.SeqAddr, mux, c)
	}
	if a.cfg.ESAddr != "" {
		mux := http.NewServeMux()
		elastic.New(c.ingest, "").Register(mux)
		a.addListener(ListenerES, a.cfg.ESAddr, mux, c)
	}
	if a.cfg.OTLPHTTPAddr != "" {
		mux := http.NewServeMux()
		otlp.NewHTTPHandler(c.ingest, exporter, "").Register(mux)
		a.addListener(ListenerOTLPHTTP, a.cfg.OTLPHTTPAddr, mux, c)
	}
	if a.cfg.OTLPGRPCAddr != "" {
		opts := []grpc.ServerOption{grpc.MaxRecvMsgSize(int(a.cfg.MaxDecompressedBytes))}
		if a.tlsCfg != nil {
			opts = append(opts, grpc.Creds(credentials.NewTLS(a.tlsCfg)))
		}
		a.grpc = grpc.NewServer(opts...)
		otlp.RegisterGRPC(a.grpc, c.ingest, exporter)
	}
	return nil
}

// mainMux serves UI, API, native, Seq, ES (/es) and OTLP (/otlp) on TIDEMARK_HTTP_ADDR.
func (a *App) mainMux(c *components, exporter *otlp.Exporter) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux.Handle(api.Prefix+"/", c.router)
	native.New(c.ingest).Register(mux)
	seq.New(c.ingest).Register(mux)
	elastic.New(c.ingest, "/es").Register(mux)
	otlp.NewHTTPHandler(c.ingest, exporter, "/otlp").Register(mux)
	c.health.Register(mux)
	if a.cfg.MetricsEnabled {
		mux.Handle("GET /metrics", c.metrics.Handler(a.cfg.MetricsToken))
	}
	ui, err := web.New()
	if err != nil {
		return nil, err
	}
	if ui.IsPlaceholder() {
		a.logger.Warn("web UI not embedded in this build; serving a placeholder page")
	}
	mux.Handle("/", ui)
	return mux, nil
}

func (a *App) addListener(name, addr string, h http.Handler, c *components) {
	chain := httpx.Chain(h,
		httpx.Recover(a.logger), httpx.RequestID, httpx.SecurityHeaders(a.cfg.SecureCookies()),
		httpx.AccessLog(a.logger, c.realIP.ClientIPString))
	a.listeners = append(a.listeners, listener{name: name, addr: addr, server: httpx.NewServer(addr, chain, a.tlsCfg, a.logger)})
}

// listen binds every listener so port conflicts fail startup immediately.
func (a *App) listen() error {
	for i := range a.listeners {
		ln, err := net.Listen("tcp", a.listeners[i].addr)
		if err != nil {
			a.closeListeners()
			return fmt.Errorf("listen %s on %s: %w", a.listeners[i].name, a.listeners[i].addr, err)
		}
		a.listeners[i].ln = ln
	}
	if a.grpc != nil {
		ln, err := net.Listen("tcp", a.cfg.OTLPGRPCAddr)
		if err != nil {
			a.closeListeners()
			return fmt.Errorf("listen otlp-grpc on %s: %w", a.cfg.OTLPGRPCAddr, err)
		}
		a.grpcLn = ln
		a.grpcAddr = ln.Addr().String()
	}
	return nil
}

func (a *App) closeListeners() {
	for _, l := range a.listeners {
		if l.ln != nil {
			_ = l.ln.Close() // startup is failing; the listen error is what matters
		}
	}
}

func (a *App) serveHTTP(l listener) {
	go func() {
		var err error
		if a.tlsCfg != nil {
			err = l.server.ServeTLS(l.ln, "", "")
		} else {
			err = l.server.Serve(l.ln)
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.errs <- fmt.Errorf("%s: %w", l.name, err)
		}
	}()
}

func (a *App) serveGRPC() {
	go func() {
		if err := a.grpc.Serve(a.grpcLn); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			a.errs <- fmt.Errorf("otlp-grpc: %w", err)
		}
	}()
}
