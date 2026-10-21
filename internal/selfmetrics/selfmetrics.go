// SPDX-License-Identifier: Apache-2.0

// Package selfmetrics exposes Prometheus metrics about OpenTidemark itself (SPEC §11.6). It uses
// its own registry (no global state) and implements the ingest and pipeline observer ports.
package selfmetrics

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Metrics holds the collectors.
type Metrics struct {
	reg          *prometheus.Registry
	ingested     *prometheus.CounterVec
	ingestDrops  *prometheus.CounterVec
	ingestReject *prometheus.CounterVec
	rerouted     *prometheus.CounterVec
	enqueued     *prometheus.CounterVec
	written      *prometheus.CounterVec
	pipeDrops    *prometheus.CounterVec
	deadLettered *prometheus.CounterVec
	writeErrors  *prometheus.CounterVec
	queueDepth   *prometheus.GaugeVec

	mu     sync.Mutex
	totals map[string]float64 // snapshot counters for /system
}

// New registers all collectors (plus Go runtime and process collectors).
func New() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry(), totals: map[string]float64{}}
	m.ingested = m.counter("tidemark_ingest_items_total", "Items accepted by ingestion.", "protocol", "source")
	m.ingestDrops = m.counter("tidemark_ingest_dropped_total", "Items dropped by connection policy.", "protocol", "reason")
	m.ingestReject = m.counter("tidemark_ingest_rejected_requests_total", "Rejected ingestion requests.", "protocol", "reason")
	m.rerouted = m.counter("tidemark_ingest_rerouted_total", "Items written to the connection's default stream instead of the requested one.",
		"protocol", "reason")
	m.enqueued = m.counter("tidemark_pipeline_enqueued_total", "Rows enqueued.", "signal")
	m.written = m.counter("tidemark_pipeline_written_total", "Rows written to ClickHouse.", "signal")
	m.pipeDrops = m.counter("tidemark_pipeline_dropped_total", "Rows dropped by the pipeline.", "signal", "reason")
	m.deadLettered = m.counter("tidemark_pipeline_deadlettered_total", "Rows written to dead-letter files.", "signal")
	m.writeErrors = m.counter("tidemark_pipeline_write_errors_total", "Failed insert attempts.", "signal")
	m.queueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "tidemark_pipeline_queue_depth", Help: "Rows queued or in flight."}, []string{"signal"})
	m.reg.MustRegister(m.queueDepth, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	m.reg.MustRegister(c)
	return c
}

func (m *Metrics) add(key string, n float64) {
	m.mu.Lock()
	m.totals[key] += n
	m.mu.Unlock()
}

// Accepted implements ingest.Observer.
func (m *Metrics) Accepted(protocol string, source model.Source, n int) {
	m.ingested.WithLabelValues(protocol, string(source)).Add(float64(n))
	m.add("ingested."+string(source), float64(n))
}

// Dropped implements ingest.Observer.
func (m *Metrics) Dropped(protocol, reason string, n int) {
	m.ingestDrops.WithLabelValues(protocol, reason).Add(float64(n))
	m.add("ingest_dropped."+reason, float64(n))
}

// Rejected implements ingest.Observer.
func (m *Metrics) Rejected(protocol, reason string) {
	m.ingestReject.WithLabelValues(protocol, reason).Inc()
	m.add("ingest_rejected."+reason, 1)
}

// Rerouted implements ingest.Observer.
func (m *Metrics) Rerouted(protocol, reason string, n int) {
	m.rerouted.WithLabelValues(protocol, reason).Add(float64(n))
	m.add("ingest_rerouted."+reason, float64(n))
}

// PipelineMetrics adapts Metrics to the pipeline.Metrics port (its Dropped has another shape).
type PipelineMetrics struct{ m *Metrics }

// Pipeline returns the pipeline adapter.
func (m *Metrics) Pipeline() PipelineMetrics { return PipelineMetrics{m: m} }

// Enqueued implements pipeline.Metrics.
func (p PipelineMetrics) Enqueued(signal string, n int) {
	p.m.enqueued.WithLabelValues(signal).Add(float64(n))
}

// Written implements pipeline.Metrics.
func (p PipelineMetrics) Written(signal string, n int) {
	p.m.written.WithLabelValues(signal).Add(float64(n))
	p.m.add("written."+signal, float64(n))
}

// Dropped implements pipeline.Metrics.
func (p PipelineMetrics) Dropped(signal, reason string, n int) {
	p.m.pipeDrops.WithLabelValues(signal, reason).Add(float64(n))
	p.m.add("pipeline_dropped."+reason, float64(n))
}

// DeadLettered implements pipeline.Metrics.
func (p PipelineMetrics) DeadLettered(signal string, n int) {
	p.m.deadLettered.WithLabelValues(signal).Add(float64(n))
	p.m.add("deadlettered."+signal, float64(n))
}

// WriteFailed implements pipeline.Metrics.
func (p PipelineMetrics) WriteFailed(signal string) { p.m.writeErrors.WithLabelValues(signal).Inc() }

// QueueDepth implements pipeline.Metrics.
func (p PipelineMetrics) QueueDepth(signal string, n int) {
	p.m.queueDepth.WithLabelValues(signal).Set(float64(n))
}

// WALBytes exposes the on-disk size of a signal's write-ahead log, read at scrape time, as
// tidemark_wal_bytes{signal}: data acknowledged to clients but possibly not yet in ClickHouse.
func (m *Metrics) WALBytes(signal string, size func() int64) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "tidemark_wal_bytes",
		Help: "Bytes of the write-ahead log segments on disk.", ConstLabels: prometheus.Labels{"signal": signal}},
		func() float64 { return float64(size()) }))
}

// UpdateAvailable exposes tidemark_update_available, 1 while the update check (SPEC §17.7) knows
// a newer release, read at scrape time.
func (m *Metrics) UpdateAvailable(available func() bool) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "tidemark_update_available",
		Help: "1 when a newer OpenTidemark release is available, else 0."},
		func() float64 {
			if available() {
				return 1
			}
			return 0
		}))
}

// Totals returns a copy of the cumulative counters (for GET /api/v1/system).
func (m *Metrics) Totals() map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]float64, len(m.totals))
	for k, v := range m.totals {
		out[k] = v
	}
	return out
}

// Handler serves /metrics; when token is non-empty it requires "Authorization: Bearer <token>".
func (m *Metrics) Handler(token string) http.Handler {
	h := promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
	if token == "" {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}
