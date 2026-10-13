// SPDX-License-Identifier: Apache-2.0

package model

// Source names one of the three telemetry datasets.
type Source string

// Telemetry sources.
const (
	SourceLogs    Source = "logs"
	SourceSpans   Source = "spans"
	SourceMetrics Source = "metrics"
)

// Valid reports whether s is a known source.
func (s Source) Valid() bool {
	return s == SourceLogs || s == SourceSpans || s == SourceMetrics
}

// Protocol names used in Event.Protocol.
const (
	ProtocolSeq     = "seq"
	ProtocolElastic = "elastic"
	ProtocolOTLP    = "otlp"
	ProtocolNative  = "native"
)
