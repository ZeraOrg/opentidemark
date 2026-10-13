// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"time"
)

// Server timeouts (SPEC §15.4). Streaming handlers extend their write deadline with
// http.ResponseController.
const (
	ReadHeaderTimeout = 10 * time.Second
	ReadTimeout       = 60 * time.Second
	WriteTimeout      = 120 * time.Second
	IdleTimeout       = 120 * time.Second
	MaxHeaderBytes    = 64 << 10
)

// NewServer returns an http.Server with the mandated timeouts.
func NewServer(addr string, h http.Handler, tlsCfg *tls.Config, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    MaxHeaderBytes,
		ErrorLog:          log.New(slogWriter{logger}, "", 0),
	}
}

// slogWriter forwards net/http's internal error log to slog at debug level (TLS handshake noise).
type slogWriter struct{ l *slog.Logger }

func (w slogWriter) Write(p []byte) (int, error) {
	w.l.Debug("http server", "msg", string(p))
	return len(p), nil
}

// LoadTLS loads a certificate pair into a TLS 1.2+ configuration with Go's default suites.
func LoadTLS(certFile, keyFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate: %w", err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}, nil
}
