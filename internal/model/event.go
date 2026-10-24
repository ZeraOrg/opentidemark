// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
)

// Event is one structured log record.
type Event struct {
	ID              uuid.UUID // UUIDv7 generated at ingest
	Timestamp       time.Time // event time (UTC, ns)
	IngestedAt      time.Time
	Stream          string
	Level           Level
	Service         string
	Environment     string
	Host            string
	MessageTemplate string // may be empty
	Message         string // rendered
	Exception       string
	EventType       string // 8 hex chars: FNV-1a 32 of MessageTemplate (or Message if no template)
	TraceID         string // lowercase hex, "" if none
	SpanID          string
	Protocol        string // seq | elastic | otlp | native
	ConnectionID    string
	Properties      map[string]any // typed, may be nested
}

// ComputeEventType returns the 8-hex-character FNV-1a 32 hash of the template, or of the
// message when the template is empty.
func ComputeEventType(template, message string) string {
	src := template
	if src == "" {
		src = message
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(src)) // hash.Hash.Write never returns an error
	return fmt.Sprintf("%08x", h.Sum32())
}

// Span is one unit of work in a distributed trace.
type Span struct {
	TraceID            string
	SpanID             string
	ParentSpanID       string
	Stream             string
	Service            string
	Name               string
	Kind               string // internal|server|client|producer|consumer
	Start              time.Time
	Duration           time.Duration
	StatusCode         string // unset|ok|error
	StatusMessage      string
	Attributes         map[string]any
	ResourceAttributes map[string]any
	Events             []SpanEvent
	Links              []SpanLink
	ConnectionID       string
}

// SpanEvent is a timestamped annotation on a span.
type SpanEvent struct {
	Time       time.Time      `json:"time"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// SpanLink references another span.
type SpanLink struct {
	TraceID    string         `json:"traceId"`
	SpanID     string         `json:"spanId"`
	TraceState string         `json:"traceState,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// Span kinds and status codes.
const (
	SpanKindInternal = "internal"
	SpanKindServer   = "server"
	SpanKindClient   = "client"
	SpanKindProducer = "producer"
	SpanKindConsumer = "consumer"

	StatusUnset = "unset"
	StatusOK    = "ok"
	StatusError = "error"
)

// MetricPoint is one sample of a metric time series.
type MetricPoint struct {
	Timestamp    time.Time
	StartTime    time.Time // start of the aggregation period of cumulative points (zero = unknown)
	Stream       string
	Service      string
	Name         string
	Description  string
	Unit         string
	Kind         string // gauge|sum|histogram|exp_histogram|summary
	Temporality  string // cumulative|delta|""
	IsMonotonic  bool
	Value        float64 // gauge/sum value; for histograms = sum/count (mean)
	Count        uint64
	Sum          float64
	Min          float64
	Max          float64
	BucketBounds []float64
	BucketCounts []uint64
	Attributes   map[string]any
	ConnectionID string
}

// Metric kinds and temporalities.
const (
	MetricGauge        = "gauge"
	MetricSum          = "sum"
	MetricHistogram    = "histogram"
	MetricExpHistogram = "exp_histogram"
	MetricSummary      = "summary"

	TemporalityCumulative = "cumulative"
	TemporalityDelta      = "delta"
)

// SummaryQuantilePrefix starts the attribute keys that hold the quantile values of a summary
// point ("quantile.0.99"). They are values, not part of the series identity (SPEC §8.2).
const SummaryQuantilePrefix = "quantile."
