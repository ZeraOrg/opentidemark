// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
)

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// esDoc is one document of the fake cluster.
type esDoc struct {
	index string
	id    string
	ts    time.Time
	src   map[string]any
}

// fakeES serves the subset of the Elasticsearch API the importer uses: _pit, _search with
// pit/search_after, scroll, and their deletion. With noPIT it answers _pit with 404 (OpenSearch).
type fakeES struct {
	mu       sync.Mutex
	docs     []esDoc
	noPIT    bool
	pits     map[string]bool
	scrolls  map[string]int // scroll id ⇒ next offset
	scrollSz map[string]int
	scrollQ  map[string][]esDoc
	nextID   int
	requests []string
	apiKey   string
}

func newFakeES(t *testing.T, n int, noPIT bool) (*fakeES, *httptest.Server) {
	t.Helper()
	f := &fakeES{noPIT: noPIT, pits: map[string]bool{}, scrolls: map[string]int{}, scrollSz: map[string]int{},
		scrollQ: map[string][]esDoc{},
		apiKey:  "es-secret"}
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		ts := base.Add(time.Duration(i/2) * time.Second) // pairs share a timestamp (tie-breaking)
		idx := "app-logs-2026.09.01"
		if i%3 == 0 {
			idx = "web-2026.09.01"
		}
		f.docs = append(f.docs, esDoc{index: idx, id: fmt.Sprintf("doc-%03d", i), ts: ts, src: map[string]any{
			"@timestamp": ts.Format(time.RFC3339Nano), "message": fmt.Sprintf("message %d", i),
			"log.level": "warn", "service.name": "checkout", "n": i}})
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeES) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "ApiKey "+f.apiKey {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_pit"):
		f.openPIT(w)
	case r.Method == http.MethodDelete && r.URL.Path == "/_pit":
		delete(f.pits, fmt.Sprint(body["id"]))
		writeJSONResp(w, map[string]any{"succeeded": true})
	case r.Method == http.MethodPost && r.URL.Path == "/_search":
		f.searchPIT(w, body)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/_search") && r.URL.Query().Get("scroll") != "":
		f.openScroll(w, body)
	case r.Method == http.MethodPost && r.URL.Path == "/_search/scroll":
		f.continueScroll(w, body)
	case r.Method == http.MethodDelete && r.URL.Path == "/_search/scroll":
		writeJSONResp(w, map[string]any{"succeeded": true})
	default:
		http.Error(w, `{"error":"unsupported"}`, http.StatusNotFound)
	}
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeES) openPIT(w http.ResponseWriter) {
	if f.noPIT {
		http.Error(w, `{"error":"no handler found"}`, http.StatusNotFound)
		return
	}
	f.nextID++
	id := fmt.Sprintf("pit-%d", f.nextID)
	f.pits[id] = true
	writeJSONResp(w, map[string]any{"id": id})
}

// expirePIT drops every point in time and scroll (keep-alive elapsed).
func (f *fakeES) expireContexts() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pits, f.scrolls, f.scrollQ = map[string]bool{}, map[string]int{}, map[string][]esDoc{}
}

// matching returns the documents >= the query's epoch_millis resume range, oldest first, with
// the stable tie-breaker (document order).
func (f *fakeES) matching(body map[string]any) []esDoc {
	minMillis := int64(-1)
	raw, _ := json.Marshal(body["query"])
	if i := strings.Index(string(raw), `"format":"epoch_millis","gte":"`); i >= 0 {
		rest := string(raw)[i+len(`"format":"epoch_millis","gte":"`):]
		minMillis, _ = strconv.ParseInt(rest[:strings.Index(rest, `"`)], 10, 64)
	}
	out := []esDoc{}
	for i, d := range f.docs {
		if d.ts.UnixMilli() >= minMillis {
			out = append(out, f.docs[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ts.Before(out[j].ts) })
	return out
}

func hitsResponse(docs []esDoc, total int, withShardDoc bool, offset int) map[string]any {
	hits := []any{}
	for i, d := range docs {
		sortVals := []any{d.ts.UnixMilli()}
		if withShardDoc {
			sortVals = append(sortVals, offset+i)
		}
		hits = append(hits, map[string]any{"_index": d.index, "_id": d.id, "_source": d.src, "sort": sortVals})
	}
	return map[string]any{"hits": map[string]any{"total": map[string]any{"value": total}, "hits": hits}}
}

func (f *fakeES) searchPIT(w http.ResponseWriter, body map[string]any) {
	pit, _ := body["pit"].(map[string]any)
	if pit == nil || !f.pits[fmt.Sprint(pit["id"])] {
		http.Error(w, `{"error":{"type":"search_context_missing_exception"}}`, http.StatusNotFound)
		return
	}
	docs := f.matching(body)
	start := 0
	if sa, ok := body["search_after"].([]any); ok && len(sa) == 2 {
		shard, _ := strconv.Atoi(fmt.Sprint(sa[1]))
		start = shard + 1
	}
	size, _ := strconv.Atoi(fmt.Sprint(body["size"]))
	end := min(start+size, len(docs))
	start = min(start, end)
	resp := hitsResponse(docs[start:end], len(docs), true, start)
	resp["pit_id"] = pit["id"]
	writeJSONResp(w, resp)
}

func (f *fakeES) openScroll(w http.ResponseWriter, body map[string]any) {
	docs := f.matching(body)
	f.nextID++
	id := fmt.Sprintf("scroll-%d", f.nextID)
	f.scrollQ[id] = docs
	size, _ := strconv.Atoi(fmt.Sprint(body["size"]))
	end := min(size, len(docs))
	f.scrolls[id], f.scrollSz[id] = end, size
	resp := hitsResponse(docs[:end], len(docs), false, 0)
	resp["_scroll_id"] = id
	writeJSONResp(w, resp)
}

func (f *fakeES) continueScroll(w http.ResponseWriter, body map[string]any) {
	id := fmt.Sprint(body["scroll_id"])
	docs, ok := f.scrollQ[id]
	if !ok {
		http.Error(w, `{"error":{"type":"search_context_missing_exception"}}`, http.StatusNotFound)
		return
	}
	start := f.scrolls[id]
	end := min(start+f.scrollSz[id], len(docs))
	f.scrolls[id] = end
	resp := hitsResponse(docs[start:end], len(docs), false, 0)
	resp["_scroll_id"] = id
	writeJSONResp(w, resp)
}

// fakeSeq serves GET /api/events (newest first, afterId paging) and GET /api/signals.
type fakeSeq struct {
	events  []map[string]any // newest first
	signals []map[string]any
	key     string
}

func newFakeSeq(t *testing.T, n int) (*fakeSeq, *httptest.Server) {
	t.Helper()
	f := &fakeSeq{key: "seq-key-123"}
	base := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	for i := n - 1; i >= 0; i-- {
		f.events = append(f.events, map[string]any{
			"Id": fmt.Sprintf("event-%04d", i), "Timestamp": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			"Level": "Error", "EventType": "$A1B2C3D4", "Exception": "System.Exception: boom",
			"RenderedMessage": fmt.Sprintf("Order %d failed", i),
			"MessageTemplateTokens": []any{map[string]any{"Text": "Order "},
				map[string]any{"PropertyName": "OrderId", "RawText": "{OrderId}"}, map[string]any{"Text": " failed {x}"}},
			"Properties": []any{map[string]any{"Name": "OrderId", "Value": i}, map[string]any{"Name": "Application", "Value": "shop"},
				map[string]any{"Name": "@weird", "Value": "kept"}},
			"TraceId": "4bf92f3577b34da6a3ce929d0e0e4736", "SpanId": "00f067aa0ba902b7"})
	}
	f.signals = []map[string]any{
		{"Id": "signal-1", "Title": "Errors", "Filters": []any{map[string]any{"Filter": "@Level = 'Error'"}}},
		{"Id": "signal-2", "Title": "Shop orders", "Filters": []any{map[string]any{"Filter": "Application = 'shop'"},
			map[string]any{"Filter": "OrderId > 10"}}},
		{"Id": "signal-3", "Title": "Unsupported", "Filters": []any{map[string]any{"Filter": "@Properties['x'] is not null"}}},
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSeq) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Seq-ApiKey") != f.key {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/api/signals":
		writeJSONResp(w, f.signals)
	case "/api/events":
		q := r.URL.Query()
		count, _ := strconv.Atoi(q.Get("count"))
		start := 0
		if after := q.Get("afterId"); after != "" {
			for i, e := range f.events {
				if e["Id"] == after {
					start = i + 1
				}
			}
		}
		end := min(start+count, len(f.events))
		writeJSONResp(w, f.events[start:end])
	default:
		http.NotFound(w, r)
	}
}

// fakeWriter emulates ClickHouse block deduplication by insert token.
type fakeWriter struct {
	mu        sync.Mutex
	seen      map[string]bool
	rows      []model.Event
	inserts   int
	transient int
}

func newFakeWriter() *fakeWriter { return &fakeWriter{seen: map[string]bool{}} }

func (w *fakeWriter) WriteEvents(ctx context.Context, ev []model.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.transient > 0 {
		w.transient--
		return errors.New("connection reset")
	}
	w.inserts++
	tok := store.DedupToken(ctx)
	if tok == "" {
		return errors.New("missing dedup token")
	}
	if w.seen[tok] {
		return nil
	}
	w.seen[tok] = true
	w.rows = append(w.rows, ev...)
	return nil
}

func (w *fakeWriter) snapshot() []model.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]model.Event(nil), w.rows...)
}

// openGate accepts every stream; paused names are dropped.
type openGate struct{ paused map[string]bool }

func (g openGate) Resolve(_ context.Context, name string) (string, bool) {
	return name, !g.paused[name]
}

// soloElector always leads.
type soloElector struct{}

func (soloElector) Register(...string)                                           {}
func (soloElector) IsLeader(string) bool                                         { return true }
func (soloElector) Lead(ctx context.Context, _ string, fn func(context.Context)) { fn(ctx) }
func (soloElector) Release(context.Context, string)                              {}

// failingJobs makes SaveProgress fail once after failAfter successful saves (a crash between
// the insert and the checkpoint).
type failingJobs struct {
	*meta.ImportJobs
	mu        sync.Mutex
	saves     int
	failAfter int
}

func (f *failingJobs) SaveProgress(ctx context.Context, id string, p meta.ImportProgress) error {
	f.mu.Lock()
	f.saves++
	fail := f.failAfter > 0 && f.saves == f.failAfter+1
	f.mu.Unlock()
	if fail {
		return errors.New("node crashed")
	}
	return f.ImportJobs.SaveProgress(ctx, id, p)
}

// testEnv is a runner and service over a temporary SQLite database.
type testEnv struct {
	db      *meta.DB
	jobs    *meta.ImportJobs
	writer  *fakeWriter
	runner  *Runner
	svc     *Service
	box     *auth.SecretBox
	signals *meta.Signals
	adminID string
	actor   service.Actor
}

// newTestEnv builds the environment; wrap (optional) decorates the job store.
func newTestEnv(t *testing.T, wrap func(*meta.ImportJobs) JobStore) *testEnv {
	t.Helper()
	ctx := context.Background()
	db, err := meta.Open(ctx, filepath.Join(t.TempDir(), meta.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	box, err := auth.NewSecretBox([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{db: db, jobs: meta.NewImportJobs(db), writer: newFakeWriter(), box: box, signals: meta.NewSignals(db)}
	var jobs JobStore = e.jobs
	if wrap != nil {
		jobs = wrap(e.jobs)
	}
	u := &meta.User{Username: "admin", PasswordHash: "x", Role: meta.RoleAdmin}
	if err := meta.NewUsers(db).Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	e.adminID, e.actor = u.ID, service.Actor{ID: "user:" + u.ID}
	audit := service.NewAuditor(meta.NewAuditLog(db), discard(), time.Now)
	sink := Signals{Store: e.signals}
	e.runner = NewRunner(RunnerDeps{Jobs: jobs, Writer: e.writer, Streams: openGate{}, Signals: sink, Box: box,
		Elector: soloElector{}, NodeID: "node-a", IsPermanent: func(error) bool { return false }, Audit: audit,
		Logger: discard(), Now: time.Now, Sleep: func(context.Context, time.Duration) error { return nil }})
	e.svc = NewService(ServiceDeps{Jobs: jobs, Box: box, Signals: sink, Streams: openGate{}, Audit: audit,
		UploadDir: filepath.Join(t.TempDir(), "imports"), NodeID: "node-a", Now: time.Now})
	return e
}
