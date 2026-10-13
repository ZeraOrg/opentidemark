// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// update regenerates testdata/*.pb from the builders below: go test ./internal/ingest/otlp -run Fixtures -update
var update = flag.Bool("update", false, "regenerate protobuf fixtures")

const baseNanos = uint64(1791892681000000000)

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

func intAttr(k string, v int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}}
}

func resource(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{strAttr("service.name", service),
		strAttr("deployment.environment", "prod"), strAttr("host.name", "node-7")}}
}

func traceID() []byte {
	return []byte{0x5b, 0x8e, 0xff, 0xf7, 0x98, 0x03, 0x81, 0x03, 0xd2, 0x69, 0xb6, 0x33, 0x81, 0x3f, 0xc6, 0x0c}
}
func spanID() []byte { return []byte{0xee, 0xe1, 0x9b, 0x7e, 0xc3, 0xc1, 0xb1, 0x74} }

func buildLogs() *collogspb.ExportLogsServiceRequest {
	return &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource: resource("payments"),
		ScopeLogs: []*logspb.ScopeLogs{{
			Scope: &commonpb.InstrumentationScope{Name: "payments.worker"},
			LogRecords: []*logspb.LogRecord{
				{TimeUnixNano: baseNanos, SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_WARN, SeverityText: "WARN",
					Body:       &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "card declined"}},
					Attributes: []*commonpb.KeyValue{strAttr("card.brand", "visa"), intAttr("amount_cents", 1999)},
					TraceId:    traceID(), SpanId: spanID()},
				{TimeUnixNano: baseNanos + 1, SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_FATAL,
					Body: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{1, 2, 3}}}},
			},
		}},
	}}}
}

func buildTraces() *coltracepb.ExportTraceServiceRequest {
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: resource("payments"),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{
			{TraceId: traceID(), SpanId: spanID(), Name: "charge", Kind: tracepb.Span_SPAN_KIND_PRODUCER,
				StartTimeUnixNano: baseNanos, EndTimeUnixNano: baseNanos + 5_000_000,
				Status: &tracepb.Status{Code: tracepb.Status_STATUS_CODE_OK}},
			{TraceId: traceID(), SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, ParentSpanId: spanID(), Name: "consume",
				Kind: tracepb.Span_SPAN_KIND_CONSUMER, StartTimeUnixNano: baseNanos + 10, EndTimeUnixNano: baseNanos}, // end < start
		}}},
	}}}
}

func buildMetrics() *colmetricspb.ExportMetricsServiceRequest {
	return &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: resource("payments"),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
			{Name: "queue.depth", Unit: "{message}", Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{
				DataPoints: []*metricspb.NumberDataPoint{{TimeUnixNano: baseNanos, Value: &metricspb.NumberDataPoint_AsInt{AsInt: 17},
					Attributes: []*commonpb.KeyValue{strAttr("queue", "charges")}}}}}},
			{Name: "charges.total", Data: &metricspb.Metric_Sum{Sum: &metricspb.Sum{IsMonotonic: true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
				DataPoints:             []*metricspb.NumberDataPoint{{TimeUnixNano: baseNanos, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 3.5}}}}}},
		}}},
	}}}
}

func TestFixturesProtobuf(t *testing.T) {
	cases := map[string]proto.Message{"logs.pb": buildLogs(), "traces.pb": buildTraces(), "metrics.pb": buildMetrics()}
	for name, msg := range cases {
		path := filepath.Join("testdata", name)
		if *update {
			b, err := proto.Marshal(msg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", name, err)
		}
		got := msg.ProtoReflect().New().Interface()
		if err := Unmarshal(b, false, got); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(got, msg) {
			t.Errorf("%s does not match its builder; regenerate with -update", name)
		}
	}
}
