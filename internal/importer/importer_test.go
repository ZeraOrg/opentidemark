// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/cluster"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

func elasticInput(url string, pageSize int, extra map[string]any) CreateInput {
	cfg := map[string]any{"url": url, "index": "app-*,web-*", "pageSize": pageSize,
		"auth": map[string]any{"type": "apiKey", "apiKey": "es-secret"}}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	return CreateInput{Kind: KindElastic, Name: "es", Config: raw}
}

// uniqueDocs checks that every source document is stored exactly once.
func uniqueDocs(t *testing.T, rows []model.Event, want int) {
	t.Helper()
	seen := map[string]bool{}
	for _, ev := range rows {
		id, _ := ev.Properties["_id"].(string)
		if seen[id] {
			t.Errorf("document %s stored twice", id)
		}
		seen[id] = true
	}
	if len(seen) != want || len(rows) != want {
		t.Fatalf("stored %d rows (%d distinct), want %d", len(rows), len(seen), want)
	}
}

func runToEnd(t *testing.T, e *testEnv, id string) meta.ImportJob {
	t.Helper()
	if err := e.runner.RunJob(context.Background(), id); err != nil {
		t.Fatalf("run job: %v", err)
	}
	j, err := e.jobs.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestElasticPointInTimeImport(t *testing.T) {
	es, srv := newFakeES(t, 23, false)
	e := newTestEnv(t, nil)
	e.writer.transient = 2 // retried with backoff
	job, err := e.svc.Create(context.Background(), e.actor, elasticInput(srv.URL, 5, nil))
	if err != nil {
		t.Fatal(err)
	}
	var stored ElasticConfig
	_ = json.Unmarshal(job.Config, &stored)
	if strings.Contains(string(job.Config), "es-secret") || !auth.IsSealed(stored.Auth.APIKey) {
		t.Fatalf("secret stored in clear: %s", job.Config)
	}
	got := runToEnd(t, e, job.ID)
	if got.Status != meta.ImportCompleted || got.RowsImported != 23 || got.RowsTotal != 23 || got.StartedAt == nil {
		t.Fatalf("job %+v", got)
	}
	rows := e.writer.snapshot()
	uniqueDocs(t, rows, 23)
	streams := map[string]int{}
	for _, ev := range rows {
		streams[ev.Stream]++
		if ev.Level != model.Warning || ev.Service != "checkout" || ev.ID.Version() != 7 {
			t.Fatalf("mapping: %+v", ev)
		}
	}
	if streams["web"] != 8 || streams["app-logs"] != 15 {
		t.Fatalf("streams %v", streams)
	}
	if len(es.pits) != 0 {
		t.Fatalf("point in time not closed: %v", es.pits)
	}
}

func TestElasticResumeAfterCrashDoesNotDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noPIT   bool
		expired bool
	}{
		{"pit", false, false},
		{"pit expired", false, true},
		{"scroll", true, false},
		{"scroll expired", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			es, srv := newFakeES(t, 23, tc.noPIT)
			e := newTestEnv(t, func(j *meta.ImportJobs) JobStore { return &failingJobs{ImportJobs: j, failAfter: 2} })
			job, err := e.svc.Create(context.Background(), e.actor, elasticInput(srv.URL, 5, nil))
			if err != nil {
				t.Fatal(err)
			}
			if err := e.runner.RunJob(context.Background(), job.ID); err == nil {
				t.Fatal("expected the simulated crash")
			}
			mid, _ := e.jobs.Get(context.Background(), job.ID)
			if mid.Status != meta.ImportRunning || mid.RowsImported != 10 {
				t.Fatalf("after crash: %+v", mid)
			}
			if tc.expired {
				es.expireContexts()
			}
			if got := runToEnd(t, e, job.ID); got.Status != meta.ImportCompleted {
				t.Fatalf("resume: %+v", got)
			}
			uniqueDocs(t, e.writer.snapshot(), 23)
		})
	}
}

func TestElasticPermanentErrorFailsJob(t *testing.T) {
	_, srv := newFakeES(t, 3, false)
	e := newTestEnv(t, nil)
	in := elasticInput(srv.URL, 5, map[string]any{"auth": map[string]any{"type": "apiKey", "apiKey": "wrong"}})
	job, err := e.svc.Create(context.Background(), e.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runner.RunJob(context.Background(), job.ID); err == nil {
		t.Fatal("expected failure")
	}
	j, _ := e.jobs.Get(context.Background(), job.ID)
	if j.Status != meta.ImportFailed || !strings.Contains(j.LastError, "401") || j.FinishedAt == nil {
		t.Fatalf("job %+v", j)
	}
}

func TestSeqImportEventsAndSignals(t *testing.T) {
	_, srv := newFakeSeq(t, 25)
	e := newTestEnv(t, nil)
	raw, _ := json.Marshal(map[string]any{"url": srv.URL, "apiKey": "seq-key-123", "pageSize": 10,
		"importSignals": true, "stream": "legacy"})
	job, err := e.svc.Create(context.Background(), e.actor, CreateInput{Kind: KindSeq, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	got := runToEnd(t, e, job.ID)
	if got.Status != meta.ImportCompleted || got.RowsImported != 25 || len(got.Warnings) != 1 ||
		!strings.Contains(got.Warnings[0], "Unsupported") || got.Name != "Import seq" {
		t.Fatalf("job %+v", got)
	}
	rows := e.writer.snapshot()
	ev := rows[0]
	if ev.Stream != "legacy" || ev.Level != model.Error || ev.MessageTemplate != "Order {OrderId} failed {{x}}" ||
		ev.Message != "Order 24 failed" || ev.Exception == "" || ev.TraceID == "" || ev.EventType != "a1b2c3d4" ||
		ev.Properties["@weird"] != "kept" || ev.Service != "shop" {
		t.Fatalf("mapped event %+v", ev)
	}
	sigs, _ := e.signals.ListVisible(context.Background(), e.adminID)
	if len(sigs) != 3 {
		t.Fatalf("signals %+v", sigs)
	}
	for _, s := range sigs {
		disabled, _ := e.signals.IsDisabled(context.Background(), s.ID)
		if disabled != (s.Name == "Unsupported") {
			t.Errorf("signal %q disabled=%v tql=%q", s.Name, disabled, s.TQL)
		}
	}
	// Re-importing signals (a resumed job) does not duplicate them.
	if err := (Signals{Store: e.signals}).ImportSignal(context.Background(), e.adminID, "Errors", "*", true); err != nil {
		t.Fatal(err)
	}
	if again, _ := e.signals.ListVisible(context.Background(), e.adminID); len(again) != 3 {
		t.Fatalf("duplicate signal: %d", len(again))
	}
}

func writeFile(t *testing.T, path string, data []byte, gz bool) {
	t.Helper()
	if gz {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		_, _ = zw.Write(data)
		_ = zw.Close()
		data = b.Bytes()
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func clefLines(from, n int) []byte {
	var b strings.Builder
	for i := from; i < from+n; i++ {
		ts := time.Date(2026, 9, 3, 0, 0, i, 0, time.UTC).Format(time.RFC3339)
		b.WriteString(`{"@t":"` + ts + `","@mt":"Line {N}","N":` + jsonNum(i) + `,"@stream":"Files"}` + "\n")
	}
	return []byte(b.String())
}

func jsonNum(i int) string { raw, _ := json.Marshal(i); return string(raw) }

func TestCLEFFilesResumeAndGzip(t *testing.T) {
	dir := t.TempDir()
	plain, gz := filepath.Join(dir, "a.clef"), filepath.Join(dir, "b.clef.gz")
	writeFile(t, plain, append(clefLines(0, 7000), []byte("not json\n\n")...), false)
	writeFile(t, gz, clefLines(7000, 3000), true)
	e := newTestEnv(t, func(j *meta.ImportJobs) JobStore { return &failingJobs{ImportJobs: j, failAfter: 1} })
	job, err := e.svc.CreateLocalFiles(context.Background(), e.actor, "files", "", []string{plain, gz})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runner.RunJob(context.Background(), job.ID); err == nil {
		t.Fatal("expected the simulated crash")
	}
	got := runToEnd(t, e, job.ID)
	if got.Status != meta.ImportCompleted || got.RowsImported != 10000 || got.Errors != 1 ||
		!strings.Contains(got.LastError, "a.clef line 7001") {
		t.Fatalf("job %+v", got)
	}
	rows := e.writer.snapshot()
	seen := map[string]bool{}
	for _, ev := range rows {
		seen[ev.Message] = true
		if ev.Stream != "files" {
			t.Fatalf("stream %q", ev.Stream)
		}
	}
	if len(rows) != 10000 || len(seen) != 10000 {
		t.Fatalf("rows %d distinct %d", len(rows), len(seen))
	}
}

func TestCancelAndPausedStreams(t *testing.T) {
	_, srv := newFakeES(t, 12, false)
	e := newTestEnv(t, nil)
	ctx := context.Background()
	job, err := e.svc.Create(ctx, e.actor, elasticInput(srv.URL, 5, nil))
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := e.svc.Cancel(ctx, e.actor, job.ID)
	if err != nil || cancelled.Status != meta.ImportCancelled {
		t.Fatalf("cancel queued: %+v %v", cancelled, err)
	}
	if _, err := e.svc.Cancel(ctx, e.actor, job.ID); !errors.Is(err, ErrJobFinished) {
		t.Fatalf("second cancel: %v", err)
	}
	if err := e.runner.RunJob(ctx, job.ID); err != nil || len(e.writer.snapshot()) != 0 {
		t.Fatalf("cancelled job ran: %v", err)
	}
	// A running job stops at its next page.
	job2, _ := e.svc.Create(ctx, e.actor, elasticInput(srv.URL, 5, nil))
	_ = e.jobs.MarkRunning(ctx, job2.ID, time.Now())
	if _, err := e.svc.Cancel(ctx, e.actor, job2.ID); err != nil {
		t.Fatal(err)
	}
	if got := runToEnd(t, e, job2.ID); got.Status != meta.ImportCancelled {
		t.Fatalf("running job: %+v", got)
	}
	// Rows for paused streams are dropped like live ingestion.
	e.runner.d.Streams = openGate{paused: map[string]bool{"web": true}}
	job3, _ := e.svc.Create(ctx, e.actor, elasticInput(srv.URL, 5, nil))
	if got := runToEnd(t, e, job3.ID); got.RowsImported != 8 {
		t.Fatalf("paused stream rows: %+v", got)
	}
}

func TestCreateValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	cases := map[string]CreateInput{
		"kind":      {Kind: "splunk", Config: json.RawMessage(`{}`)},
		"file kind": {Kind: KindCLEFFile, Config: json.RawMessage(`{}`)},
		"unknown":   {Kind: KindSeq, Config: json.RawMessage(`{"url":"https://seq.local","bogus":1}`)},
		"url":       {Kind: KindSeq, Config: json.RawMessage(`{"url":"ftp://seq.local"}`)},
		"index":     {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"a b"}`)},
		"auth":      {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"x","auth":{"type":"basic"}}`)},
		"range": {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"x",
			"from":"2026-01-02T00:00:00Z","to":"2026-01-01T00:00:00Z"}`)},
		"query":    {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"x","query":[1]}`)},
		"pageSize": {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"x","pageSize":20000}`)},
		"stream":   {Kind: KindSeq, Config: json.RawMessage(`{"url":"https://seq.local","stream":"Bad Name"}`)},
		"caCert":   {Kind: KindElastic, Config: json.RawMessage(`{"url":"https://es.local","index":"x","caCert":"nope"}`)},
		"nothing":  {Kind: KindSeq, Config: json.RawMessage(`{"url":"https://seq.local","importEvents":false}`)},
		"name":     {Kind: KindSeq, Name: strings.Repeat("n", 129), Config: json.RawMessage(`{"url":"https://seq.local"}`)},
	}
	for name, in := range cases {
		var ve *service.ValidationError
		if _, err := e.svc.Create(context.Background(), e.actor, in); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPublicConfigHidesSecrets(t *testing.T) {
	e := newTestEnv(t, nil)
	raw := json.RawMessage(`{"url":"https://es.local","index":"logs-*","auth":{"type":"basic","username":"u","password":"p4ss"}}`)
	job, err := e.svc.Create(context.Background(), e.actor, CreateInput{Kind: KindElastic, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	pub := string(PublicConfig(&job))
	if strings.Contains(pub, "password") || !strings.Contains(pub, `"username":"u"`) || !strings.Contains(pub, `"to":`) {
		t.Fatalf("public config %s", pub)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.clef")
	writeFile(t, path, clefLines(0, 1), false)
	fj, err := e.svc.CreateLocalFiles(context.Background(), e.actor, "", "", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if pub := string(PublicConfig(&fj)); strings.Contains(pub, dir) || !strings.Contains(pub, `"name":"x.clef"`) {
		t.Fatalf("file paths exposed: %s", pub)
	}
}

func TestUploadsAndKibana(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	files := []Upload{{Name: `C:\logs\app.clef`, Body: bytes.NewReader(clefLines(0, 3))}}
	next := func() (Upload, bool, error) {
		if len(files) == 0 {
			return Upload{}, false, nil
		}
		u := files[0]
		files = files[1:]
		return u, true, nil
	}
	job, err := e.svc.CreateWithUploads(ctx, e.actor, CreateInput{Kind: KindCLEFFile, Config: json.RawMessage(`{"stream":"up"}`)}, next)
	if err != nil {
		t.Fatal(err)
	}
	var cfg CLEFConfig
	_ = json.Unmarshal(job.Config, &cfg)
	if cfg.Node != "node-a" || len(cfg.Files) != 1 || cfg.Files[0].Name != "app.clef" || cfg.Files[0].Size == 0 {
		t.Fatalf("config %+v", cfg)
	}
	if got := runToEnd(t, e, job.ID); got.RowsImported != 3 {
		t.Fatalf("upload job %+v", got)
	}
	export := strings.Join([]string{
		`{"type":"index-pattern","id":"ip1","attributes":{"title":"filebeat-*,logs-app-*"}}`,
		`{"type":"search","id":"s1","attributes":{"title":"Checkout errors","kibanaSavedObjectMeta":{"searchSourceJSON":"{\"query\":{\"query\":\"service.name:checkout and log.level:error\",\"language\":\"kuery\"},\"filter\":[]}"}}}`,
		`{"type":"search","id":"s2","attributes":{"title":"Range","kibanaSavedObjectMeta":{"searchSourceJSON":"{\"query\":{\"query\":\"status:[500 TO 599]\",\"language\":\"lucene\"},\"filter\":[]}"}}}`,
		`{"type":"search","id":"s3","attributes":{"title":"Pills","kibanaSavedObjectMeta":{"searchSourceJSON":"{\"query\":{\"query\":\"\",\"language\":\"kuery\"},\"filter\":[{\"meta\":{\"disabled\":false}}]}"}}}`,
		`{"type":"dashboard","id":"d1","attributes":{"title":"Ops"}}`,
		`{"exportedCount":5,"missingRefCount":0}`,
	}, "\n")
	files = []Upload{{Name: "export.ndjson", Body: strings.NewReader(export)}}
	kj, err := e.svc.CreateWithUploads(ctx, e.actor, CreateInput{Kind: KindKibana}, next)
	if err != nil {
		t.Fatal(err)
	}
	if kj.Status != meta.ImportCompleted || kj.RowsImported != 5 || kj.Errors != 2 || len(kj.Warnings) != 3 {
		t.Fatalf("kibana job %+v", kj)
	}
	sigs, _ := e.signals.ListVisible(ctx, e.adminID)
	if len(sigs) != 3 {
		t.Fatalf("signals %+v", sigs)
	}
}

func TestTranslate(t *testing.T) {
	seqCases := []struct{ in, want string }{
		{"@Level = 'Error'", "@level = 'Error'"},
		{"Application = 'it''s' and @MessageTemplate like '%x%'", `Application = 'it\'s' and @template like '%x%'`},
		{"@Exception is not null and (Code is null)", "@exception:* and (not Code:*)"},
		{`Path = 'C:\temp'`, `Path = 'C:\\temp'`},
		{"@Properties['x'] = 1", ""}, // indexer syntax: untranslatable
	}
	for _, c := range seqCases {
		got, err := TranslateSeq(c.in)
		if (err == nil) != (c.want != "") || (c.want != "" && got != c.want) {
			t.Errorf("TranslateSeq(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for in, ok := range map[string]bool{
		"service:checkout and not level:debug": true,
		"_exists_:user.id":                     true,
		"":                                     true,
		"status:[500 TO 599]":                  false,
		"a | stats count()":                    false,
	} {
		if _, err := TranslateKQL(in); (err == nil) != ok {
			t.Errorf("TranslateKQL(%q) err=%v", in, err)
		}
	}
}

func TestTokensAndIDsAreDeterministic(t *testing.T) {
	cp := json.RawMessage(`{"afterId":"x"}`)
	first, again := PageToken("j", cp, "a"), PageToken("j", json.RawMessage(`{"afterId":"x"}`), "a")
	if first != again || first == PageToken("k", cp, "a") ||
		PageToken("j", cp, "a") == PageToken("j", cp, "b") || PageToken("j", nil, "") != PageToken("j", json.RawMessage("{}"), "") {
		t.Fatal("page tokens")
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a, b := EventID("t", 1, ts, time.Now()), EventID("t", 1, ts, time.Now())
	if a != b || a.Version() != 7 || EventID("t", 2, ts, time.Now()) == a {
		t.Fatal("event ids")
	}
	ms := int64(a[0])<<40 | int64(a[1])<<32 | int64(a[2])<<24 | int64(a[3])<<16 | int64(a[4])<<8 | int64(a[5])
	if ms != ts.UnixMilli() {
		t.Fatalf("event id time %d", ms)
	}
}

// TestTwoNodesRunEachJobOnce: both nodes poll the same job table; the import:<id> lease makes
// one node run each job, and a clef-file job runs only on the node holding its files.
func TestTwoNodesRunEachJobOnce(t *testing.T) {
	_, seqSrv := newFakeSeq(t, 25)
	e := newTestEnv(t, nil)
	leases := meta.NewLeases(e.db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writers := map[string]*fakeWriter{}
	runners := map[string]*Runner{}
	for _, node := range []string{"node-a", "node-b"} {
		el := cluster.NewElector(leases, auth.SystemClock{}, cluster.ElectorConfig{NodeID: node, TTL: 3 * time.Second,
			RenewEvery: time.Second}, discard())
		go el.Run(ctx)
		d := e.runner.d
		d.Elector, d.NodeID, d.Sleep = el, node, nil
		writers[node] = newFakeWriter()
		d.Writer = writers[node]
		runners[node] = NewRunner(d)
	}
	raw, _ := json.Marshal(map[string]any{"url": seqSrv.URL, "apiKey": "seq-key-123", "pageSize": 5})
	seqJob, err := e.svc.Create(ctx, e.actor, CreateInput{Kind: KindSeq, Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "b.clef")
	writeFile(t, path, clefLines(0, 4), false)
	svcB := NewService(ServiceDeps{Jobs: e.jobs, Box: e.box, Signals: Signals{Store: e.signals}, Streams: openGate{},
		Audit: e.svc.d.Audit, NodeID: "node-b", Now: time.Now})
	fileJob, err := svcB.CreateLocalFiles(ctx, e.actor, "b-files", "", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runners {
		go r.Run(ctx)
	}
	deadline := time.Now().Add(30 * time.Second)
	for _, id := range []string{seqJob.ID, fileJob.ID} {
		for {
			j, _ := e.jobs.Get(ctx, id)
			if j.Status == meta.ImportCompleted {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s still %s (%s)", id, j.Status, j.LastError)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	a, b := writers["node-a"].snapshot(), writers["node-b"].snapshot()
	if len(a)+len(b) != 29 {
		t.Fatalf("rows a=%d b=%d, want 29 in total", len(a), len(b))
	}
	fileRows := 0
	for _, ev := range a {
		if ev.Stream == "files" {
			t.Fatal("node-a ran the file job of node-b")
		}
	}
	for _, ev := range b {
		if ev.Stream == "files" {
			fileRows++
		}
	}
	if fileRows != 4 {
		t.Fatalf("file rows on node-b: %d", fileRows)
	}
}
