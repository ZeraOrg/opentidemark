// SPDX-License-Identifier: Apache-2.0

package elastic_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/ingest/elastic"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/testutil"
)

type env struct {
	mux  *http.ServeMux
	sink *testutil.Sink
}

func newEnv(t *testing.T, prefix string) *env {
	t.Helper()
	sink := &testutil.Sink{}
	d := testutil.NewDeps(&testutil.FakeAuth{Conn: testutil.AllProtocolsConn()}, sink, testutil.NewObserver(), nil)
	mux := http.NewServeMux()
	elastic.New(d, prefix).Register(mux)
	return &env{mux: mux, sink: sink}
}

func basicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("elastic:"+testutil.TestKey))
}

func (e *env) do(method, path string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Authorization", basicAuth())
	r.Header.Set("Content-Type", "application/x-ndjson")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type bulkResp struct {
	Took   int64                       `json:"took"`
	Errors bool                        `json:"errors"`
	Items  []map[string]map[string]any `json:"items"`
}

func decodeBulk(t *testing.T, w *httptest.ResponseRecorder) bulkResp {
	t.Helper()
	var r bulkResp
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return r
}

func TestInfoAndProductHeader(t *testing.T) {
	for _, prefix := range []string{"", "/es"} {
		e := newEnv(t, prefix)
		w := e.do("GET", prefix+"/", nil)
		if w.Code != 200 || w.Header().Get("X-Elastic-Product") != "Elasticsearch" {
			t.Fatalf("%q: %d %v", prefix, w.Code, w.Header())
		}
		var info struct {
			Name    string            `json:"name"`
			Version map[string]string `json:"version"`
			Tagline string            `json:"tagline"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &info)
		if info.Name != "tidemark" || info.Version["number"] != "8.11.0" || info.Version["build_flavor"] != "default" ||
			info.Version["minimum_wire_compatibility_version"] != "7.17.0" || info.Tagline != "You Know, for Search" {
			t.Errorf("info %+v", info)
		}
		if w := e.do("HEAD", prefix+"/", nil); w.Code != 200 {
			t.Errorf("HEAD: %d", w.Code)
		}
	}
	e := newEnv(t, "/es")
	if w := e.do("GET", "/es", nil); w.Code != 200 {
		t.Errorf("bare prefix: %d", w.Code)
	}
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t, "")
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "Basic") ||
		!strings.Contains(w.Body.String(), `"type":"security_exception"`) {
		t.Errorf("%d %v %s", w.Code, w.Header(), w.Body.String())
	}
}

func TestFilebeatBulk(t *testing.T) {
	e := newEnv(t, "")
	w := e.do("POST", "/_bulk", fixture(t, "filebeat_bulk.ndjson"))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	resp := decodeBulk(t, w)
	if resp.Errors || len(resp.Items) != 2 {
		t.Fatalf("%+v", resp)
	}
	first := resp.Items[0]["create"]
	if first["_index"] != "filebeat-8.11.0" || first["status"] != float64(201) || first["result"] != "created" ||
		first["_version"] != float64(1) || first["_id"] == "" {
		t.Errorf("item 0 %v", first)
	}
	if resp.Items[1]["index"]["_id"] != "fb-doc-2" {
		t.Errorf("item 1 %v", resp.Items[1])
	}
	if first["_id"] != e.sink.Events[0].ID.String() {
		t.Error("generated _id does not match event id")
	}
	ev := e.sink.Events[0]
	if ev.Stream != "filebeat-8.11.0" || ev.Service != "nginx" || ev.Host != "web-01" || ev.Level != model.Information ||
		ev.Message != "GET /index.html HTTP/1.1 200 612" || ev.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || ev.Protocol != "elastic" {
		t.Errorf("event 0 %+v", ev)
	}
	ev = e.sink.Events[1]
	if ev.Stream != "filebeat-8.11.0" || ev.Level != model.Error || ev.Exception != "NginxError: upstream timed out" ||
		ev.Properties["_id"] != "fb-doc-2" || ev.Service != "filebeat-8.11.0" {
		t.Errorf("event 1 %+v", ev)
	}
}

func TestShipperFixtures(t *testing.T) {
	tests := []struct {
		file, stream, service, host, message string
		level                                model.Level
	}{
		{"fluentbit_bulk.ndjson", "fluent-bit", "nginx", "", "2026/10/13 11:58:01 [error] 7#7: *1 open()", model.Information},
		{"vector_bulk.ndjson", "vector", "vector", "vector-01", `{"level":"warn","msg":"disk 91% full"}`, model.Information},
		{"logstash_bulk.ndjson", "logstash", "logstash", "db-01", "Oct 13 11:58:01 db-01 sshd[1234]", model.Information},
	}
	for _, tt := range tests {
		e := newEnv(t, "/es")
		w := e.do("POST", "/es/_bulk", fixture(t, tt.file))
		resp := decodeBulk(t, w)
		if w.Code != 200 || resp.Errors || len(e.sink.Events) != 2 {
			t.Fatalf("%s: %d %s", tt.file, w.Code, w.Body.String())
		}
		ev := e.sink.Events[0]
		if ev.Stream != tt.stream || ev.Service != tt.service || ev.Host != tt.host ||
			!strings.HasPrefix(ev.Message, tt.message) || ev.Level != tt.level {
			t.Errorf("%s: %+v", tt.file, ev)
		}
	}
	// Second documents: vector's flat level and logstash's syslog severity.
	e := newEnv(t, "")
	e.do("POST", "/_bulk", fixture(t, "vector_bulk.ndjson"))
	if ev := e.sink.Events[1]; ev.Level != model.Warning || ev.Service != "disk-monitor" || ev.Message != "disk 91% full" {
		t.Errorf("vector 2: %+v", ev)
	}
	e = newEnv(t, "")
	e.do("POST", "/_bulk", fixture(t, "logstash_bulk.ndjson"))
	if ev := e.sink.Events[1]; ev.Level != model.Fatal || ev.Stream != "logstash" {
		t.Errorf("logstash 2: %+v", ev)
	}
}

func TestMixedActionsAndURLIndex(t *testing.T) {
	e := newEnv(t, "")
	w := e.do("POST", "/My-Index-2026.10/_bulk", fixture(t, "mixed_actions.ndjson"))
	resp := decodeBulk(t, w)
	if w.Code != 200 || !resp.Errors || len(resp.Items) != 5 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if resp.Items[1]["update"]["error"].(map[string]any)["type"] != "action_not_supported" ||
		resp.Items[2]["delete"]["status"] != float64(400) ||
		resp.Items[3]["create"]["error"].(map[string]any)["type"] != "mapper_parsing_exception" ||
		resp.Items[4]["create"]["_index"] != "My-Index-2026.10" {
		t.Errorf("items %+v", resp.Items)
	}
	if len(e.sink.Events) != 2 || e.sink.Events[1].Stream != "my-index" || e.sink.Events[0].Stream != "app-logs" {
		t.Errorf("events %+v", e.sink.Events)
	}
}

func TestBulkRequestErrors(t *testing.T) {
	tests := map[string]string{
		"bad action":     "not json\n",
		"unknown action": `{"upsert":{}}` + "\n{}\n",
		"two keys":       `{"index":{},"create":{}}` + "\n{}\n",
		"missing source": `{"index":{}}` + "\n",
	}
	for name, body := range tests {
		e := newEnv(t, "")
		w := e.do("POST", "/_bulk", []byte(body))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "illegal_argument_exception") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	e := newEnv(t, "")
	e.sink.Err = ingest.ErrBackpressure
	w := e.do("POST", "/_bulk", fixture(t, "filebeat_bulk.ndjson"))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "es_rejected_execution_exception") || w.Header().Get("Retry-After") != "1" {
		t.Errorf("backpressure: %d %s", w.Code, w.Body.String())
	}
}

func TestSingleDocument(t *testing.T) {
	e := newEnv(t, "")
	tests := []struct {
		method, path string
		code         int
		id           string
	}{
		{"POST", "/orders-2026.10.13/_doc", 201, ""},
		{"PUT", "/orders/_doc/abc", 201, "abc"},
		{"POST", "/orders/_create/xyz", 201, "xyz"},
		{"PUT", "/orders/_create/xyz2", 201, "xyz2"},
	}
	for _, tt := range tests {
		w := e.do(tt.method, tt.path, []byte(`{"message":"m","level":"warn"}`))
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != tt.code || resp["result"] != "created" || (tt.id != "" && resp["_id"] != tt.id) || resp["_id"] == "" {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body.String())
		}
	}
	if len(e.sink.Events) != 4 || e.sink.Events[0].Stream != "orders" || e.sink.Events[0].Level != model.Warning {
		t.Errorf("events %+v", e.sink.Events)
	}
	if w := e.do("POST", "/orders/_doc", []byte(`{bad`)); w.Code != 400 {
		t.Errorf("bad doc: %d", w.Code)
	}
	if w := e.do("POST", "/orders/_create", []byte(`{}`)); w.Code != 404 {
		t.Errorf("create without id: %d", w.Code)
	}
}

func TestCompatEndpoints(t *testing.T) {
	e := newEnv(t, "")
	tests := []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/_license", 200, `{"license":{"status":"active","type":"basic"}}`},
		{"GET", "/_xpack", 200, `{"features":{}}`},
		{"GET", "/_cluster/health", 200, `{"status":"green"}`},
		{"PUT", "/_index_template/filebeat-8.11.0", 200, `{"acknowledged":true}`},
		{"GET", "/_index_template/filebeat-8.11.0", 200, `{}`},
		{"HEAD", "/_template/logstash", 200, ``},
		{"DELETE", "/_ilm/policy/x", 200, `{"acknowledged":true}`},
		{"PUT", "/_ingest/pipeline/p1", 200, `{"acknowledged":true}`},
		{"GET", "/_data_stream/logs-x", 200, `{}`},
		{"PUT", "/_component_template/c", 200, `{"acknowledged":true}`},
		{"PUT", "/my-index", 200, `{"acknowledged":true}`},
		{"HEAD", "/my-index", 200, ``},
		{"PATCH", "/_ilm/policy/x", 405, ``},
		{"GET", "/_nodes", 404, ``},
		{"GET", "/my-index/_search", 404, ``},
		{"DELETE", "/my-index", 404, ``},
	}
	for _, tt := range tests {
		w := e.do(tt.method, tt.path, nil)
		if w.Code != tt.code || (tt.body != "" && strings.TrimSpace(w.Body.String()) != tt.body) {
			t.Errorf("%s %s: %d %s", tt.method, tt.path, w.Code, w.Body.String())
		}
	}
	w := e.do("GET", "/_nodes", nil)
	if !strings.Contains(w.Body.String(), `"type":"unsupported_operation"`) || !strings.Contains(w.Body.String(), `"status":404`) {
		t.Errorf("404 shape %s", w.Body.String())
	}
}

func TestStreamFromIndex(t *testing.T) {
	tests := map[string]string{
		"filebeat-8.11.0-2026.10.13": "filebeat-8.11.0",
		"logstash-2026.10":           "logstash",
		"app-20261013":               "app",
		"logs-nginx-default":         "logs-nginx-default",
		".ds-logs-app-2026.10.13":    ".ds-logs-app",
		"MyIndex":                    "myindex",
		"plain":                      "plain",
		"metrics-2026":               "metrics-2026",
	}
	for in, want := range tests {
		if got := elastic.StreamFromIndex(in); got != want {
			t.Errorf("StreamFromIndex(%q) = %q want %q", in, got, want)
		}
	}
	if model.NormalizeStream(elastic.StreamFromIndex(".ds-logs-app-2026.10.13")) != "ds-logs-app" {
		t.Error("data-stream backing index normalization")
	}
}

func TestBulkError(t *testing.T) {
	if (&elastic.BulkError{Line: 2, Reason: "x"}).Error() != "line [2]: x" {
		t.Error("format")
	}
}

func TestBodyTooLarge(t *testing.T) {
	e := newEnv(t, "")
	big := bytes.Repeat([]byte("x"), 2<<20)
	if w := e.do("POST", "/_bulk", big); w.Code != 413 || !strings.Contains(w.Body.String(), "content_too_long_exception") {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/x/_doc", big); w.Code != 413 {
		t.Errorf("single: %d", w.Code)
	}
}
