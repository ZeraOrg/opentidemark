// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"context"
	"fmt"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// Exporter turns decoded OTLP requests into enqueued telemetry; it is shared by the gRPC
// and HTTP transports.
type Exporter struct {
	d *ingest.Deps
}

// NewExporter returns an exporter.
func NewExporter(d *ingest.Deps) *Exporter { return &Exporter{d: d} }

// ExportLogs maps and enqueues logs. Records below the connection's minimum level are
// reported as rejected through partial success.
func (e *Exporter) ExportLogs(ctx context.Context, req *ingest.Request, in *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	events := LogsToEvents(in)
	if err := e.d.CheckRate(req, len(events)); err != nil {
		return nil, err
	}
	kept, dropped := e.d.PrepareEvents(req, events)
	if err := e.d.Events.EnqueueEvents(ctx, kept); err != nil {
		return nil, err
	}
	e.d.Accepted(req, model.SourceLogs, len(kept))
	resp := &collogspb.ExportLogsServiceResponse{}
	if dropped > 0 {
		resp.PartialSuccess = &collogspb.ExportLogsPartialSuccess{
			RejectedLogRecords: int64(dropped),
			ErrorMessage:       fmt.Sprintf("%d log records below the connection's minimum level were dropped", dropped),
		}
	}
	return resp, nil
}

// ExportTraces maps and enqueues spans.
func (e *Exporter) ExportTraces(ctx context.Context, req *ingest.Request, in *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	spans := TracesToSpans(in)
	if err := e.d.CheckRate(req, len(spans)); err != nil {
		return nil, err
	}
	req.PrepareSpans(spans)
	if err := e.d.Spans.EnqueueSpans(ctx, spans); err != nil {
		return nil, err
	}
	e.d.Accepted(req, model.SourceSpans, len(spans))
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// ExportMetrics maps and enqueues metric points.
func (e *Exporter) ExportMetrics(ctx context.Context, req *ingest.Request, in *colmetricspb.ExportMetricsServiceRequest) (*colmetricspb.ExportMetricsServiceResponse, error) {
	points := MetricsToPoints(in)
	if err := e.d.CheckRate(req, len(points)); err != nil {
		return nil, err
	}
	req.PrepareMetrics(points)
	if err := e.d.Metrics.EnqueueMetrics(ctx, points); err != nil {
		return nil, err
	}
	e.d.Accepted(req, model.SourceMetrics, len(points))
	return &colmetricspb.ExportMetricsServiceResponse{}, nil
}
