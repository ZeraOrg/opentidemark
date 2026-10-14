// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"bytes"
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/snappy"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	grpcgzip "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

func readJSON(t *testing.T, name string, msg proto.Message) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(b, true, msg); err != nil {
		t.Fatal(err)
	}
}

func TestLogsJSONMapping(t *testing.T) {
	req := &collogspb.ExportLogsServiceRequest{}
	readJSON(t, "logs.json", req)
	evs := LogsToEvents(req)
	if len(evs) != 3 {
		t.Fatalf("events %d", len(evs))
	}
	ev := evs[0]
	if !ev.Timestamp.Equal(time.Unix(0, 1791892681123456789).UTC()) || ev.Level != model.Information ||
		ev.Service != "cart-service" || ev.Environment != "staging" || ev.Host != "cart-7d9f" || ev.Stream != "Cart" ||
		ev.Message != "Added item 42 to cart 7" || ev.MessageTemplate != "Added item {ItemId} to cart {CartId}" ||
		ev.TraceID != "5b8efff798038103d269b633813fc60c" || ev.SpanID != "eee19b7ec3c1b174" {
		t.Errorf("event 0 %+v", ev)
	}
	if ev.Properties["ItemId"] != int64(42) || ev.Properties[attrScopeName] != "Cart.Api.CartController" ||
		ev.Properties["telemetry.sdk.language"] != "dotnet" {
		t.Errorf("props %v", ev.Properties)
	}
	if _, ok := ev.Properties[attrStream]; ok {
		t.Error("tidemark.stream kept")
	}
	ev = evs[1]
	if !ev.Timestamp.Equal(time.Unix(0, 1791892682000000000).UTC()) || ev.Level != model.Error || ev.Message != "checkout failed" ||
		ev.Exception != "InvalidOperationException: Cart is empty\nat Cart.Api.CartController.Checkout()" ||
		ev.Properties["orderId"] != "o-9" || ev.Properties["retry"] != true || ev.Properties["ratio"] != 0.25 {
		t.Errorf("event 1 %+v", ev)
	}
	if tags, _ := ev.Properties["tags"].([]any); len(tags) != 2 {
		t.Errorf("tags %v", ev.Properties["tags"])
	}
	if evs[2].Level != model.Debug || evs[2].MessageTemplate != "" {
		t.Errorf("event 2 %+v", evs[2])
	}
}

func TestTracesJSONMapping(t *testing.T) {
	req := &coltracepb.ExportTraceServiceRequest{}
	readJSON(t, "traces.json", req)
	spans := TracesToSpans(req)
	if len(spans) != 2 {
		t.Fatalf("spans %d", len(spans))
	}
	s := spans[0]
	if s.TraceID != "5b8efff798038103d269b633813fc60c" || s.Kind != model.SpanKindServer || s.Duration != 250*time.Millisecond ||
		s.StatusCode != model.StatusError || s.StatusMessage != "internal error" || s.Service != "frontend" ||
		s.Attributes["http.response.status_code"] != int64(500) || len(s.Events) != 1 || s.Events[0].Name != "exception" ||
		s.ResourceAttributes["service.version"] != "2.3.1" || s.ParentSpanID != "" {
		t.Errorf("span 0 %+v", s)
	}
	s = spans[1]
	if s.Kind != model.SpanKindClient || s.ParentSpanID != "eee19b7ec3c1b174" || s.StatusCode != model.StatusOK ||
		len(s.Links) != 1 || s.Links[0].TraceID != "0af7651916cd43dd8448eb211c80319c" || s.Links[0].TraceState != "rojo=00f067aa0ba902b7" {
		t.Errorf("span 1 %+v", s)
	}
}

func TestMetricsJSONMapping(t *testing.T) {
	req := &colmetricspb.ExportMetricsServiceRequest{}
	readJSON(t, "metrics.json", req)
	pts := MetricsToPoints(req)
	if len(pts) != 5 {
		t.Fatalf("points %d: %+v", len(pts), pts)
	}
	byName := map[string]model.MetricPoint{}
	for _, p := range pts {
		byName[p.Name] = p
	}
	g := byName["process.runtime.memory"]
	if g.Kind != model.MetricGauge || g.Value != 52428800 || g.Unit != "By" || g.Attributes["pool"] != "heap" ||
		g.Attributes["host.name"] != "api-1" || g.Service != "api" || g.Description != "Heap memory" {
		t.Errorf("gauge %+v", g)
	}
	if _, ok := g.Attributes["service.name"]; ok {
		t.Error("service.name in attrs")
	}
	s := byName["http.server.requests"]
	if s.Kind != model.MetricSum || s.Temporality != model.TemporalityCumulative || !s.IsMonotonic || s.Value != 1234 {
		t.Errorf("sum %+v", s)
	}
	h := byName["http.server.request.duration"]
	if h.Kind != model.MetricHistogram || h.Temporality != model.TemporalityDelta || h.Count != 4 || h.Sum != 2 ||
		h.Value != 0.5 || h.Min != 0.1 || h.Max != 1.2 || len(h.BucketBounds) != 3 || len(h.BucketCounts) != 4 {
		t.Errorf("histogram %+v", h)
	}
	e := byName["rpc.latency"]
	wantBounds := []float64{0, 4, 8}
	wantCounts := []uint64{1, 2, 3, 0}
	if e.Kind != model.MetricExpHistogram || e.Value != 5 || !equalF(e.BucketBounds, wantBounds) || !equalU(e.BucketCounts, wantCounts) {
		t.Errorf("exp histogram %+v", e)
	}
	sm := byName["gc.pause"]
	if sm.Kind != model.MetricSummary || sm.Value != 5 || sm.Attributes["quantile.0.99"] != 12.0 || sm.Attributes["quantile.0.5"] != 4.0 {
		t.Errorf("summary %+v", sm)
	}
}

func TestExpToExplicit(t *testing.T) {
	neg := &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: 0, BucketCounts: []uint64{5, 6}}
	pos := &metricspb.ExponentialHistogramDataPoint_Buckets{Offset: -1, BucketCounts: []uint64{7}}
	bounds, counts := ExpToExplicit(1, 0, pos, neg)
	sqrt2 := math.Sqrt2
	want := []float64{-sqrt2, -1, 0, 1}
	if !equalF(bounds, want) || !equalU(counts, []uint64{6, 5, 0, 7, 0}) {
		t.Errorf("bounds %v counts %v", bounds, counts)
	}
	b, c := ExpToExplicit(0, 0, nil, nil)
	if len(b) != 0 || !equalU(c, []uint64{0}) {
		t.Errorf("empty %v %v", b, c)
	}
}

func equalF(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-9 {
			return false
		}
	}
	return true
}

func equalU(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestProtobufFixtureMapping(t *testing.T) {
	evs := LogsToEvents(buildLogs())
	if len(evs) != 2 || evs[0].Level != model.Warning || evs[0].Environment != "prod" || evs[0].Host != "node-7" ||
		evs[0].Properties["amount_cents"] != int64(1999) || evs[1].Level != model.Fatal || evs[1].Message != "AQID" {
		t.Errorf("logs %+v", evs)
	}
	spans := TracesToSpans(buildTraces())
	if spans[0].Kind != model.SpanKindProducer || spans[0].Duration != 5*time.Millisecond || spans[1].Duration != 0 ||
		spans[1].Kind != model.SpanKindConsumer || spans[1].StatusCode != model.StatusUnset {
		t.Errorf("spans %+v", spans)
	}
	pts := MetricsToPoints(buildMetrics())
	if len(pts) != 2 || pts[0].Value != 17 || pts[1].Temporality != model.TemporalityDelta || pts[1].Value != 3.5 {
		t.Errorf("points %+v", pts)
	}
}

// --- HTTP transport ---

type httpEnv struct {
	mux  *http.ServeMux
	sink *testutil.Sink
}

func newHTTPEnv(t *testing.T, conn auth.Connection) *httpEnv {
	t.Helper()
	sink := &testutil.Sink{}
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: conn}, sink, testutil.NewObserver(), nil)
	mux := http.NewServeMux()
	NewHTTPHandler(d, NewExporter(d), "/otlp").Register(mux)
	return &httpEnv{mux: mux, sink: sink}
}

func (e *httpEnv) post(path, ct, enc string, body []byte, key bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	if enc != "" {
		r.Header.Set("Content-Encoding", enc)
	}
	if key {
		r.Header.Set("Authorization", "Bearer "+testutil.TestKey)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func TestHTTPProtobufAndJSON(t *testing.T) {
	e := newHTTPEnv(t, testutil.AllProtocolsConn())
	logsPB, _ := os.ReadFile("testdata/logs.pb")
	w := e.post("/otlp/v1/logs", "application/x-protobuf", "", logsPB, true)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	resp := &collogspb.ExportLogsServiceResponse{}
	if err := proto.Unmarshal(w.Body.Bytes(), resp); err != nil || resp.GetPartialSuccess() != nil {
		t.Errorf("resp %v %v", resp, err)
	}
	tracesJSON, _ := os.ReadFile("testdata/traces.json")
	if w := e.post("/otlp/v1/traces", "application/json", "", tracesJSON, true); w.Code != 200 || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("traces json %d %s", w.Code, w.Body.String())
	}
	metricsPB, _ := os.ReadFile("testdata/metrics.pb")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(metricsPB)
	_ = zw.Close()
	if w := e.post("/otlp/v1/metrics", "application/x-protobuf", "gzip", gz.Bytes(), true); w.Code != 200 {
		t.Errorf("metrics gzip %d %s", w.Code, w.Body.String())
	}
	if w := e.post("/otlp/v1/metrics", "application/protobuf", "snappy", snappy.Encode(nil, metricsPB), true); w.Code != 200 {
		t.Errorf("metrics snappy %d %s", w.Code, w.Body.String())
	}
	if len(e.sink.Events) != 2 || len(e.sink.Spans) != 2 || len(e.sink.Metrics) != 4 {
		t.Errorf("sink %d %d %d", len(e.sink.Events), len(e.sink.Spans), len(e.sink.Metrics))
	}
	if e.sink.Spans[0].Stream != "app" || e.sink.Spans[0].ConnectionID != "conn-1" || e.sink.Metrics[0].Stream != "app" {
		t.Errorf("prepared %+v", e.sink.Spans[0])
	}
}

func TestHTTPErrors(t *testing.T) {
	e := newHTTPEnv(t, testutil.AllProtocolsConn())
	tests := []struct {
		name, ct, enc string
		body          []byte
		key           bool
		code          int
		grpc          codes.Code
	}{
		{"no key", "application/x-protobuf", "", nil, false, 401, codes.Unauthenticated},
		{"bad content type", "text/plain", "", nil, true, 415, codes.InvalidArgument},
		{"garbage content type", ";;", "", nil, true, 415, codes.InvalidArgument},
		{"bad protobuf", "application/x-protobuf", "", []byte{0xff, 0xff, 0xff}, true, 400, codes.InvalidArgument},
		{"bad json", "application/json", "", []byte(`{"resourceLogs":7}`), true, 400, codes.InvalidArgument},
		{"syntax json", "application/json", "", []byte(`{`), true, 400, codes.InvalidArgument},
		{"bad encoding", "application/json", "br", []byte(`{}`), true, 415, codes.InvalidArgument},
	}
	for _, tt := range tests {
		w := e.post("/otlp/v1/logs", tt.ct, tt.enc, tt.body, tt.key)
		if w.Code != tt.code {
			t.Errorf("%s: %d %s", tt.name, w.Code, w.Body.String())
			continue
		}
		st := &statusProto{}
		if !st.decode(w) || st.code != tt.grpc {
			t.Errorf("%s: status body %q", tt.name, w.Body.String())
		}
	}
	e.sink.Err = ingest.ErrBackpressure
	w := e.post("/otlp/v1/logs", "application/json", "", []byte(`{}`), true)
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Errorf("backpressure %d", w.Code)
	}
}

type statusProto struct{ code codes.Code }

func (s *statusProto) decode(w *httptest.ResponseRecorder) bool {
	st := status.New(codes.OK, "").Proto()
	var err error
	if strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		err = protojson.Unmarshal(w.Body.Bytes(), st)
	} else {
		err = proto.Unmarshal(w.Body.Bytes(), st)
	}
	s.code = codes.Code(st.GetCode())
	return err == nil
}

func TestPartialSuccessForMinimumLevel(t *testing.T) {
	warn := model.Warning
	conn := testutil.AllProtocolsConn()
	conn.MinimumLevel = &warn
	e := newHTTPEnv(t, conn)
	logsJSON, _ := os.ReadFile("testdata/logs.json")
	w := e.post("/otlp/v1/logs", "application/json", "", logsJSON, true)
	resp := &collogspb.ExportLogsServiceResponse{}
	if err := protojson.Unmarshal(w.Body.Bytes(), resp); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || resp.GetPartialSuccess().GetRejectedLogRecords() != 2 || len(e.sink.Events) != 1 {
		t.Errorf("%d %v %d", w.Code, resp, len(e.sink.Events))
	}
}

// --- gRPC transport ---

func startGRPC(t *testing.T, sink *testutil.Sink) *grpc.ClientConn {
	t.Helper()
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, sink, testutil.NewObserver(), nil)
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	RegisterGRPC(srv, d, NewExporter(d))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return cc
}

func TestGRPCExport(t *testing.T) {
	sink := &testutil.Sink{}
	cc := startGRPC(t, sink)
	ctx := metadata.AppendToOutgoingContext(t.Context(), "x-tidemark-key", testutil.TestKey)
	if _, err := collogspb.NewLogsServiceClient(cc).Export(ctx, buildLogs(), grpc.UseCompressor(grpcgzip.Name)); err != nil {
		t.Fatalf("logs: %v", err)
	}
	bearer := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+testutil.TestKey)
	if _, err := coltracepb.NewTraceServiceClient(cc).Export(bearer, buildTraces()); err != nil {
		t.Fatalf("traces: %v", err)
	}
	if _, err := colmetricspb.NewMetricsServiceClient(cc).Export(ctx, buildMetrics()); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if len(sink.Events) != 2 || len(sink.Spans) != 2 || len(sink.Metrics) != 2 {
		t.Errorf("sink %d %d %d", len(sink.Events), len(sink.Spans), len(sink.Metrics))
	}
	_, err := collogspb.NewLogsServiceClient(cc).Export(t.Context(), buildLogs())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("no key: %v", err)
	}
	_, err = coltracepb.NewTraceServiceClient(cc).Export(t.Context(), buildTraces())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("traces no key: %v", err)
	}
	_, err = colmetricspb.NewMetricsServiceClient(cc).Export(t.Context(), buildMetrics())
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("metrics no key: %v", err)
	}
	sink.Err = ingest.ErrBackpressure
	if _, err := collogspb.NewLogsServiceClient(cc).Export(ctx, buildLogs()); status.Code(err) != codes.Unavailable {
		t.Errorf("backpressure: %v", err)
	}
}

func TestGRPCCodes(t *testing.T) {
	tests := map[error]codes.Code{
		auth.ErrForbidden:          codes.PermissionDenied,
		&ingest.RateLimitedError{}: codes.ResourceExhausted,
		ingest.ErrBodyTooLarge:     codes.InvalidArgument,
		errors.New("x"):            codes.Internal,
		ingest.ErrBackpressure:     codes.Unavailable,
		auth.ErrUnauthorized:       codes.Unauthenticated,
	}
	for err, want := range tests {
		if got := grpcCode(err); got != want {
			t.Errorf("%v: %v want %v", err, got, want)
		}
	}
	if toGRPCError(nil) != nil {
		t.Error("nil")
	}
	if peerAddr(context.Background()).IsValid() {
		t.Error("peer without context")
	}
	if keyFromMetadata(context.Background()) != "" {
		t.Error("key without metadata")
	}
}

func TestExceptionText(t *testing.T) {
	tests := []struct {
		attrs map[string]any
		want  string
	}{
		{map[string]any{attrExcType: "E", attrExcMessage: "m", attrExcStacktrace: "s"}, "E: m\ns"},
		{map[string]any{attrExcMessage: "m"}, "m"},
		{map[string]any{attrExcType: "E"}, "E"},
		{map[string]any{attrExcStacktrace: "s"}, "s"},
		{map[string]any{}, ""},
	}
	for _, tt := range tests {
		if got := exceptionText(tt.attrs); got != tt.want {
			t.Errorf("%v: %q want %q", tt.attrs, got, tt.want)
		}
	}
}
