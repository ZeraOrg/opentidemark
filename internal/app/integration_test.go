// SPDX-License-Identifier: Apache-2.0

//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"github.com/ZeraOrg/tidemark/internal/config"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
	"github.com/ZeraOrg/tidemark/internal/testutil"
)

const bootstrapKey = "tmk_integrationintegrationintegration"

func startApp(t *testing.T) (*App, testutil.ClickHouseEnv) {
	t.Helper()
	env, ok := testutil.ClickHouseFromEnv()
	if !ok {
		t.Skip("TIDEMARK_TEST_CLICKHOUSE_ADDR not set")
	}
	ctx := context.Background()
	conn, err := ch.Open(ctx, ch.Options{Addrs: env.Addrs, Username: env.WriterUser, Password: env.WriterPassword})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+env.Database+" SYNC"); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	vars := config.MapEnv{
		config.VarDataDir: t.TempDir(), config.VarHTTPAddr: "127.0.0.1:0", config.VarSeqAddr: "127.0.0.1:0",
		config.VarESAddr: "127.0.0.1:0", config.VarOTLPHTTPAddr: "127.0.0.1:0", config.VarOTLPGRPCAddr: "127.0.0.1:0",
		config.VarCHAddr: strings.Join(env.Addrs, ","), config.VarCHDatabase: env.Database,
		config.VarCHWriterUser: env.WriterUser, config.VarCHWriterPassword: env.WriterPassword,
		config.VarCHReaderUser: env.ReaderUser, config.VarCHReaderPassword: env.ReaderPassword,
		config.VarBootstrapIngestKey: bootstrapKey, config.VarBatchMaxWait: "50ms", config.VarMetricsEnabled: "true",
	}
	cfg, err := config.Load(nil, vars)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(ctx, cfg, "it", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return a, env
}

func fixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "ingest", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(t *testing.T, url, contentType string, body []byte, headers map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestIntegrationEndToEnd(t *testing.T) {
	a, env := startApp(t)
	addrs := a.Addrs()
	base := "http://" + addrs[ListenerHTTP]
	seqKey := map[string]string{"X-Seq-ApiKey": bootstrapKey}
	bearer := map[string]string{"Authorization": "Bearer " + bootstrapKey}
	basic := map[string]string{"Authorization": "ApiKey " + bootstrapKey}

	steps := []struct {
		name, url, ct, file string
		headers             map[string]string
		want                int
	}{
		{"serilog clef", "http://" + addrs[ListenerSeq] + "/api/events/raw", "application/vnd.serilog.clef", "seq/testdata/serilog_clef.ndjson", seqKey, 201},
		{"nlog legacy", base + "/api/events/raw", "application/json", "seq/testdata/nlog_legacy.json", seqKey, 201},
		{"filebeat", "http://" + addrs[ListenerES] + "/_bulk", "application/x-ndjson", "elastic/testdata/filebeat_bulk.ndjson", basic, 200},
		{"fluentbit", "http://" + addrs[ListenerES] + "/_bulk", "application/x-ndjson", "elastic/testdata/fluentbit_bulk.ndjson", basic, 200},
		{"vector", base + "/es/_bulk", "application/x-ndjson", "elastic/testdata/vector_bulk.ndjson", basic, 200},
		{"logstash", base + "/es/_bulk", "application/x-ndjson", "elastic/testdata/logstash_bulk.ndjson", basic, 200},
		{"native array", base + "/api/v1/ingest/logs", "application/json", "native/testdata/array.json", bearer, 202},
		{"native ndjson", base + "/api/v1/ingest/logs", "application/x-ndjson", "native/testdata/lines.ndjson", bearer, 202},
		{"otlp logs json", base + "/otlp/v1/logs", "application/json", "otlp/testdata/logs.json", bearer, 200},
		{"otlp traces json", "http://" + addrs[ListenerOTLPHTTP] + "/v1/traces", "application/json", "otlp/testdata/traces.json", bearer, 200},
		{"otlp traces pb", "http://" + addrs[ListenerOTLPHTTP] + "/v1/traces", "application/x-protobuf", "otlp/testdata/traces.pb", bearer, 200},
		{"otlp metrics json", base + "/otlp/v1/metrics", "application/json", "otlp/testdata/metrics.json", bearer, 200},
		{"otlp metrics pb", "http://" + addrs[ListenerOTLPHTTP] + "/v1/metrics", "application/x-protobuf", "otlp/testdata/metrics.pb", bearer, 200},
	}
	for _, s := range steps {
		if got := post(t, s.url, s.ct, fixture(t, s.file), s.headers); got != s.want {
			t.Errorf("%s: status %d, want %d", s.name, got, s.want)
		}
	}
	if got := post(t, base+"/ingest/clef", "", []byte(`{"@m":"x"}`), map[string]string{"X-Seq-ApiKey": "tmk_wrong"}); got != 401 {
		t.Errorf("wrong key: %d", got)
	}
	sendGRPCLogs(t, addrs["otlp-grpc"])
	checkAPI(t, base)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	verifyRows(t, env)
}

func sendGRPCLogs(t *testing.T, addr string) {
	t.Helper()
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	req := &collogspb.ExportLogsServiceRequest{}
	if err := proto.Unmarshal(fixture(t, "otlp/testdata/logs.pb"), req); err != nil {
		t.Fatal(err)
	}
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-tidemark-key", bootstrapKey)
	if _, err := collogspb.NewLogsServiceClient(cc).Export(ctx, req); err != nil {
		t.Fatalf("grpc export: %v", err)
	}
}

func checkAPI(t *testing.T, base string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.Post(base+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"admin"}`))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	resp, err = c.Get(base + "/api/v1/system")
	if err != nil {
		t.Fatal(err)
	}
	var sys map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&sys)
	resp.Body.Close()
	chInfo, _ := sys["clickhouse"].(map[string]any)
	if chInfo == nil || !strings.HasPrefix(chInfo["version"].(string), "25.") {
		t.Errorf("system report %v", sys)
	}
	resp, err = http.Get(base + "/metrics")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("metrics: %v", err)
	} else {
		resp.Body.Close()
	}
}

func verifyRows(t *testing.T, env testutil.ClickHouseEnv) {
	t.Helper()
	ctx := context.Background()
	conn, err := ch.Open(ctx, ch.Options{Addrs: env.Addrs, Database: env.Database, Username: env.ReaderUser, Password: env.ReaderPassword})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	counts := map[string]uint64{}
	rows, err := conn.Query(ctx, "SELECT protocol, count() FROM events GROUP BY protocol")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		var n uint64
		_ = rows.Scan(&p, &n)
		counts[p] = n
	}
	rows.Close()
	want := map[string]uint64{"seq": 7, "elastic": 8, "native": 5, "otlp": 5}
	for p, n := range want {
		if counts[p] != n {
			t.Errorf("events via %s: %d, want %d (all: %v)", p, counts[p], n, counts)
		}
	}
	checks := []struct {
		q    string
		want uint64
	}{
		{"SELECT count() FROM spans", 4},
		{"SELECT count() FROM metric_points", 7},
		{"SELECT count() FROM events WHERE stream = 'health' AND level = 'Verbose'", 1},
		{"SELECT count() FROM events WHERE stream = 'filebeat-8.11.0'", 2},
		{"SELECT count() FROM events WHERE stream = 'cart' AND service = 'cart-service'", 3},
		{"SELECT count() FROM events WHERE message = 'Order 1042 shipped to Amsterdam'", 1},
		{"SELECT count() FROM events WHERE props['otel.scope.name'] = 'payments.worker'", 2},
		{"SELECT count() FROM spans WHERE status_code = 'error' AND duration_ns = 250000000", 1},
		{"SELECT count() FROM metric_points WHERE kind = 'exp_histogram' AND length(bucket_counts) = 4", 1},
		{"SELECT count() FROM field_catalog WHERE key = 'RequestPath'", 1},
	}
	for _, c := range checks {
		var n uint64
		if err := conn.QueryRow(ctx, c.q).Scan(&n); err != nil || n != c.want {
			t.Errorf("%s = %d (%v), want %d", c.q, n, err, c.want)
		}
	}
	var ddl string
	if err := conn.QueryRow(ctx, "SELECT create_table_query FROM system.tables WHERE database = ? AND name = 'events'", env.Database).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "TTL toDateTime(timestamp) + toIntervalDay(30)") {
		t.Errorf("startup retention not applied: %s", ddl)
	}
}
