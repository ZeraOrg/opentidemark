// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// fakeStreamStorage implements StreamStorage, IndexStorage and PurgeStorage.
type fakeStreamStorage struct {
	mu         sync.Mutex
	stats      []store.StreamStats
	statsErr   error
	statsCalls int
	mappings   []store.FieldMapping
	deleted    []string
	deleteErr  error
	added      []store.IndexedField
	dropped    []store.IndexedField
	addErr     error
	purges     []store.PurgeRequest
	purgeErr   error
	estimate   uint64
	purged     chan store.PurgeRequest
}

func (f *fakeStreamStorage) Stats(context.Context) ([]store.StreamStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statsCalls++
	return f.stats, f.statsErr
}

func (f *fakeStreamStorage) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsCalls
}

func (f *fakeStreamStorage) Mappings(context.Context, model.StreamScope, string) ([]store.FieldMapping, error) {
	return f.mappings, nil
}

func (f *fakeStreamStorage) DeleteStream(_ context.Context, name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeStreamStorage) AddIndexedField(_ context.Context, x store.IndexedField) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.added = append(f.added, x)
	return nil
}

func (f *fakeStreamStorage) DropIndexedField(_ context.Context, x store.IndexedField) error {
	f.dropped = append(f.dropped, x)
	return nil
}

func (f *fakeStreamStorage) Purge(_ context.Context, p store.PurgeRequest) (store.PurgeJob, error) {
	if p.DryRun {
		return store.PurgeJob{RowsEstimate: f.estimate}, nil
	}
	f.mu.Lock()
	f.purges = append(f.purges, p)
	f.mu.Unlock()
	if f.purged != nil {
		f.purged <- p
	}
	if f.purgeErr != nil {
		return store.PurgeJob{}, f.purgeErr
	}
	return store.PurgeJob{DroppedPartitions: []string{"20261001"}, LightweightDeleted: true}, nil
}

type countingSyncer struct{ n int }

func (c *countingSyncer) Sync(context.Context) error { c.n++; return nil }

type fakeLeader struct{ leader bool }

func (f fakeLeader) IsLeader(string) bool { return f.leader }

type streamEnv struct {
	svc      *StreamAdmin
	storage  *fakeStreamStorage
	sync     *countingSyncer
	registry *StreamRegistry
	repo     *meta.Streams
	teams    *meta.Teams
	indexed  *IndexedFields
	now      *time.Time
}

func newStreamEnv(t *testing.T, maxStreams int) *streamEnv {
	t.Helper()
	db := openDB(t)
	repo := meta.NewStreams(db)
	reg := NewStreamRegistry(repo, maxStreams, quiet())
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	e := &streamEnv{storage: &fakeStreamStorage{}, sync: &countingSyncer{}, registry: reg, repo: repo, teams: meta.NewTeams(db),
		now: &now}
	audit := NewAuditor(meta.NewAuditLog(db), quiet(), time.Now)
	e.indexed = NewIndexedFields(meta.NewIndexedFields(db), e.storage, audit)
	e.svc = NewStreamAdmin(StreamAdminDeps{Repo: repo, Registry: reg, Storage: e.storage, Grants: e.teams, Access: NewAccess(e.teams, meta.NewSettings(db)), Retention: e.sync, Indexed: e.indexed,
		Audit: audit, Leader: fakeLeader{true}, Now: func() time.Time { return *e.now }, Logger: quiet()})
	return e
}

func intp(n int) *int { return &n }

func TestStreamCreateValidation(t *testing.T) {
	e := newStreamEnv(t, 3)
	ctx := context.Background()
	bad := []StreamInput{
		{Name: "Bad"},
		{Name: ""},
		{Name: "ok", RetentionDays: ptrTo(intp(0))},
		{Name: "ok", SpansRetentionDays: ptrTo(intp(40000))},
		{Name: "ok", LevelRules: &[]meta.LevelRule{{Levels: []string{"nope"}, Days: 3}}},
		{Name: "ok", LevelRules: &[]meta.LevelRule{{Levels: nil, Days: 3}}},
		{Name: "ok", LevelRules: &[]meta.LevelRule{{Levels: []string{"debug"}, Days: 0}}},
		{Name: "ok", LevelRules: &[]meta.LevelRule{{Levels: []string{"debug"}, Days: 1}, {Levels: []string{"debug"}, Days: 1},
			{Levels: []string{"debug"}, Days: 1}, {Levels: []string{"debug"}, Days: 1}, {Levels: []string{"debug"}, Days: 1},
			{Levels: []string{"debug"}, Days: 1}, {Levels: []string{"debug"}, Days: 1}}},
		{Name: "ok", Description: ptrTo(strings.Repeat("d", 2000))},
	}
	for i, in := range bad {
		var ve *ValidationError
		if _, err := e.svc.Create(ctx, SystemActor, in); !errors.As(err, &ve) {
			t.Errorf("case %d: %v", i, err)
		}
	}
	s, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "api", RetentionDays: ptrTo(intp(90)),
		LevelRules: &[]meta.LevelRule{{Levels: []string{"debug", "verbose", "Debug"}, Days: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if e.sync.n != 1 || *s.RetentionDays != 90 || strings.Join(s.LevelRules[0].Levels, ",") != "Debug,Verbose" {
		t.Errorf("created %+v sync %d", s, e.sync.n)
	}
	if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "api"}); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "plain"}); err != nil || e.sync.n != 1 {
		t.Errorf("plain: %v sync %d", err, e.sync.n)
	}
	if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "fourth"}); err == nil {
		t.Error("stream limit not enforced")
	}
}

func ptrTo[T any](v T) *T { return &v }

func TestStreamUpdatePauseAndRetention(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Update(ctx, SystemActor, "web", StreamInput{Paused: ptrTo(true)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.registry.Resolve(ctx, "web"); ok || e.sync.n != 0 {
		t.Errorf("paused stream must drop rows; sync %d", e.sync.n)
	}
	s, err := e.svc.Update(ctx, SystemActor, "web", StreamInput{Paused: ptrTo(false), MetricsRetentionDays: ptrTo(intp(5))})
	if err != nil || e.sync.n != 1 || *s.MetricsRetentionDays != 5 {
		t.Fatalf("%+v %v sync %d", s, err, e.sync.n)
	}
	if _, ok := e.registry.Resolve(ctx, "web"); !ok {
		t.Error("resumed stream must accept rows")
	}
	var cleared *int
	if s, err = e.svc.Update(ctx, SystemActor, "web", StreamInput{MetricsRetentionDays: &cleared}); err != nil || s.MetricsRetentionDays != nil || e.sync.n != 2 {
		t.Errorf("clear: %+v %v %d", s, err, e.sync.n)
	}
	if _, err := e.svc.Update(ctx, SystemActor, "missing", StreamInput{}); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := e.svc.Update(ctx, SystemActor, "web", StreamInput{RetentionDays: ptrTo(intp(-1))}); err == nil {
		t.Error("invalid days accepted")
	}
}

func TestStreamDelete(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: "old", RetentionDays: ptrTo(intp(3))}); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, SystemActor, model.DefaultStream, model.DefaultStream); !errors.Is(err, ErrProtectedStream) {
		t.Errorf("default: %v", err)
	}
	if err := e.svc.Delete(ctx, SystemActor, "old", "OLD"); !errors.Is(err, ErrConfirmMismatch) {
		t.Errorf("confirm: %v", err)
	}
	if err := e.svc.Delete(ctx, SystemActor, "nope", "nope"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	e.storage.deleteErr = errors.New("boom")
	if err := e.svc.Delete(ctx, SystemActor, "old", "old"); err == nil {
		t.Error("storage error swallowed")
	}
	if _, ok := e.registry.Resolve(ctx, "old"); !ok {
		t.Error("gate must be restored after a failed delete")
	}
	e.storage.deleteErr = nil
	team := &meta.Team{Name: "payments"}
	if err := e.teams.Create(ctx, team); err != nil {
		t.Fatal(err)
	}
	if _, err := e.teams.SetStreams(ctx, team.ID, []string{"old", "keep"}); err != nil {
		t.Fatal(err)
	}
	syncs := e.sync.n
	if err := e.svc.Delete(ctx, SystemActor, "old", "old"); err != nil {
		t.Fatal(err)
	}
	if grants, _ := e.teams.Streams(ctx, team.ID); len(grants) != 1 || grants[0] != "keep" {
		t.Errorf("grants of the deleted stream must be revoked: %v", grants)
	}
	if len(e.storage.deleted) != 1 || e.sync.n != syncs+1 {
		t.Errorf("deleted %v sync %d", e.storage.deleted, e.sync.n)
	}
	if _, err := e.repo.GetByName(ctx, "old"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("repo: %v", err)
	}
}

func TestStreamStatsCache(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	e.storage.stats = []store.StreamStats{{Stream: "default", Source: model.SourceLogs, Rows: 5}, {Stream: "gone", Source: model.SourceLogs}}
	list, at, err := e.svc.List(ctx, viewerUser)
	if err != nil || len(list) != 1 || list[0].Stats[model.SourceLogs].Rows != 5 || !at.Equal(*e.now) {
		t.Fatalf("%+v %v %v", list, at, err)
	}
	_, _, _ = e.svc.List(ctx, viewerUser)
	if e.storage.calls() != 1 {
		t.Errorf("cache miss: %d calls", e.storage.calls())
	}
	*e.now = e.now.Add(StatsInterval)
	e.storage.statsErr = errors.New("down")
	list, at, _ = e.svc.List(ctx, viewerUser)
	if e.storage.calls() != 2 || list[0].Stats[model.SourceLogs].Rows != 5 || at.Equal(*e.now) {
		t.Errorf("stale cache must be kept on error: calls %d at %v", e.storage.calls(), at)
	}
}

func TestStatsCollectorFollowsLeader(t *testing.T) {
	for _, leader := range []bool{false, true} {
		e := newStreamEnv(t, 10)
		e.svc.d.Leader = fakeLeader{leader}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { e.svc.RunStatsCollector(ctx, time.Millisecond); close(done) }()
		deadline := time.Now().Add(time.Second)
		for leader && e.storage.calls() < 2 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !leader {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
		<-done
		if got := e.storage.calls(); (got >= 2) != leader {
			t.Errorf("leader=%v: %d collections", leader, got)
		}
	}
}

func TestStreamFields(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	e.storage.mappings = []store.FieldMapping{{Source: model.SourceLogs, Key: "user.id", Type: "number"}, {Source: model.SourceLogs, Key: "path"}}
	if _, err := e.indexed.Create(ctx, SystemActor, model.SourceLogs, "user.id"); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Fields(ctx, viewerUser, model.DefaultStream)
	if err != nil || len(got) != 2 || !got[0].Indexed || got[1].Indexed {
		t.Errorf("%+v %v", got, err)
	}
	if _, err := e.svc.Fields(ctx, viewerUser, "missing"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

func TestColumnFor(t *testing.T) {
	tests := []struct {
		key  string
		used map[string]bool
		want string
	}{
		{"user.id", nil, "f_user_id"},
		{"Request/Path", nil, "f_request_path"},
		{"@@x--y", nil, "f_x_y"},
		{"...", nil, "f_field"},
		{"user.id", map[string]bool{"f_user_id": true}, "f_user_id_2"},
		{"user.id", map[string]bool{"f_user_id": true, "f_user_id_2": true}, "f_user_id_3"},
		{strings.Repeat("a", 100), nil, "f_" + strings.Repeat("a", 64)},
		{strings.Repeat("a", 100), map[string]bool{"f_" + strings.Repeat("a", 64): true}, "f_" + strings.Repeat("a", 62) + "_2"},
	}
	for _, tc := range tests {
		if got, err := ColumnFor(tc.key, tc.used); err != nil || got != tc.want {
			t.Errorf("ColumnFor(%q) = %q, %v; want %q", tc.key, got, err, tc.want)
		}
	}
	used := map[string]bool{"f_x": true}
	for i := 2; i <= maxSlugAttempts+1; i++ {
		used["f_x_"+strconv.Itoa(i)] = true
	}
	if _, err := ColumnFor("x", used); err == nil {
		t.Error("exhausted suffixes accepted")
	}
}

func TestIndexedFieldsLifecycle(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	for _, tc := range []struct {
		src model.Source
		key string
	}{{"bogus", "k"}, {model.SourceLogs, "a'b"}, {model.SourceLogs, ""}, {model.SourceLogs, "a b"}} {
		if _, err := e.indexed.Create(ctx, SystemActor, tc.src, tc.key); err == nil {
			t.Errorf("%v accepted", tc)
		}
	}
	f, err := e.indexed.Create(ctx, SystemActor, model.SourceSpans, "http.route")
	if err != nil || f.ColumnName != "f_http_route" || len(e.storage.added) != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := e.indexed.Create(ctx, SystemActor, model.SourceSpans, "http.route"); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	g, err := e.indexed.Create(ctx, SystemActor, model.SourceLogs, "http_route")
	if err != nil || g.ColumnName != "f_http_route_2" {
		t.Errorf("collision: %+v %v", g, err)
	}
	cols, _ := e.indexed.Columns(ctx)
	if cols[model.SourceSpans]["http.route"] != "f_http_route" || cols[model.SourceLogs]["http_route"] != "f_http_route_2" {
		t.Errorf("columns %v", cols)
	}
	e.storage.addErr = errors.New("ch down")
	if _, err := e.indexed.Create(ctx, SystemActor, model.SourceLogs, "z"); err == nil {
		t.Error("storage error swallowed")
	}
	e.storage.addErr = nil
	if err := e.indexed.Delete(ctx, SystemActor, f.ID); err != nil || len(e.storage.dropped) != 1 {
		t.Errorf("delete: %v %v", err, e.storage.dropped)
	}
	if err := e.indexed.Delete(ctx, SystemActor, f.ID); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestIndexedFieldsLimit(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	for i := range MaxIndexedFields {
		if _, err := e.indexed.Create(ctx, SystemActor, model.SourceLogs, "k"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	var ve *ValidationError
	if _, err := e.indexed.Create(ctx, SystemActor, model.SourceLogs, "one.more"); !errors.As(err, &ve) {
		t.Errorf("limit: %v", err)
	}
}

func newPurges(e *streamEnv, t *testing.T) (*Purges, *meta.PurgeJobs) {
	t.Helper()
	db := openDB(t)
	repo := meta.NewPurgeJobs(db)
	return NewPurges(repo, e.storage, e.repo, NewAuditor(meta.NewAuditLog(db), quiet(), time.Now), quiet()), repo
}

func TestPurgeValidation(t *testing.T) {
	e := newStreamEnv(t, 10)
	p, _ := newPurges(e, t)
	ctx := context.Background()
	from, to := time.Unix(1000, 0), time.Unix(2000, 0)
	bad := []PurgeInput{
		{Source: "x", From: from, To: to},
		{Source: model.SourceLogs, From: to, To: from},
		{Source: model.SourceLogs},
		{Source: model.SourceLogs, From: from, To: to, Query: "level:error | stats count()"},
		{Source: model.SourceLogs, From: from, To: to, Query: "source spans | name:x"},
		{Source: model.SourceLogs, From: from, To: to, Query: strings.Repeat("a", maxPurgeQueryLen+1)},
	}
	for i, in := range bad {
		if _, err := p.Request(ctx, SystemActor, in); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := p.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: from, To: to, Stream: "nope"}); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("unknown stream: %v", err)
	}
	var qe *tql.Error
	if _, err := p.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: from, To: to, Query: "level >= erorr"}); !errors.As(err, &qe) {
		t.Errorf("bad level: %v", err)
	}
}

func TestPurgeDryRunAndJob(t *testing.T) {
	e := newStreamEnv(t, 10)
	e.storage.estimate = 42
	e.storage.purged = make(chan store.PurgeRequest, 1)
	p, repo := newPurges(e, t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	from, to := time.Unix(1000, 0), time.Unix(2000, 0)
	out, err := p.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: from, To: to, DryRun: true, Query: "level:error"})
	if err != nil || out.RowsEstimate != 42 || out.Job != nil {
		t.Fatalf("dry run %+v %v", out, err)
	}
	out, err = p.Request(ctx, SystemActor, PurgeInput{Stream: model.DefaultStream, Source: model.SourceSpans, From: from, To: to, Query: "status:error"})
	if err != nil || out.Job == nil || out.Job.Status != meta.PurgePending || out.Job.RowsEstimate != 42 {
		t.Fatalf("job %+v %v", out, err)
	}
	go p.Run(ctx)
	req := <-e.storage.purged
	if req.Stream != model.DefaultStream || req.Source != model.SourceSpans || !strings.Contains(req.Where, "status_code") {
		t.Errorf("request %+v", req)
	}
	waitStatus(t, repo, out.Job.ID, meta.PurgeDone)
	j, _ := p.Get(ctx, out.Job.ID)
	if j.MutationID != "20261001" {
		t.Errorf("partitions %q", j.MutationID)
	}
}

func waitStatus(t *testing.T, repo *meta.PurgeJobs, id, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if j, _ := repo.Get(context.Background(), id); j.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _ := repo.Get(context.Background(), id)
	t.Fatalf("job %s status %s, want %s", id, j.Status, want)
}

func TestPurgeFailureBusyAndRecovery(t *testing.T) {
	e := newStreamEnv(t, 10)
	p, repo := newPurges(e, t)
	ctx := context.Background()
	from, to := time.Unix(1000, 0), time.Unix(2000, 0)
	var ids []string
	for range purgeQueueSize {
		out, err := p.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: from, To: to})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, out.Job.ID)
	}
	if _, err := p.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: from, To: to}); !errors.Is(err, ErrPurgeBusy) {
		t.Fatalf("busy: %v", err)
	}
	stale := &meta.PurgeJob{Source: "logs", From: from, To: to, Status: meta.PurgeRunning, RequestedBy: "x"}
	if err := repo.Create(ctx, stale); err != nil {
		t.Fatal(err)
	}
	e.storage.purgeErr = errors.New("ch down")
	// A fresh service sees the persisted pending jobs (the old queue is gone after a restart).
	p2 := NewPurges(repo, e.storage, e.repo, NewAuditor(meta.NewAuditLog(openDB(t)), quiet(), time.Now), quiet())
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p2.Run(rctx)
	waitStatus(t, repo, stale.ID, meta.PurgeFailed)
	waitStatus(t, repo, ids[0], meta.PurgeFailed)
}

// Regression: purge jobs were queued in the requesting node's memory, so in HA mode a job
// requested on a node without the purge lease never ran (SPEC §12.5, §17.5).
func TestPurgeRequestedOnOtherNodeRuns(t *testing.T) {
	e := newStreamEnv(t, 10)
	requester, repo := newPurges(e, t)
	ctx := context.Background()
	out, err := requester.Request(ctx, SystemActor, PurgeInput{Source: model.SourceLogs, From: time.Unix(1000, 0), To: time.Unix(2000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	leader := NewPurges(repo, e.storage, e.repo, NewAuditor(meta.NewAuditLog(openDB(t)), quiet(), time.Now), quiet())
	leader.poll = 10 * time.Millisecond
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go leader.Run(rctx)
	waitStatus(t, repo, out.Job.ID, meta.PurgeDone)
}

func TestStreamListAndFieldsFollowScope(t *testing.T) {
	e := newStreamEnv(t, 10)
	ctx := context.Background()
	for _, n := range []string{"a", "b"} {
		if _, err := e.svc.Create(ctx, SystemActor, StreamInput{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	scope := &fixedScope{scope: model.OnlyStreams("a")}
	e.svc.d.Access = scope
	list, _, err := e.svc.List(ctx, viewerUser)
	if err != nil || len(list) != 1 || list[0].Name != "a" {
		t.Errorf("list %+v %v", list, err)
	}
	if _, err := e.svc.Fields(ctx, viewerUser, "b"); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("fields of a hidden stream: %v", err)
	}
	if _, err := e.svc.Fields(ctx, viewerUser, "a"); err != nil {
		t.Errorf("fields of a visible stream: %v", err)
	}
	scope.err = errors.New("meta down")
	if _, _, err := e.svc.List(ctx, viewerUser); err == nil {
		t.Error("list without a scope")
	}
}
