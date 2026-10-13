// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/cluster"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/normalize"
	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// Runner timing and limits (SPEC §9.9).
const (
	// PollInterval is how often every node looks for queued or orphaned jobs.
	PollInterval      = 5 * time.Second
	maxConcurrentJobs = 2
	maxSourceAttempts = 6
	maxWriteAttempts  = 10
	retryBase         = time.Second
	retryMax          = 30 * time.Second
	maxWarnings       = 100
	progressTimeout   = 10 * time.Second
	leaseCheck        = time.Second
	leaseWait         = 2*cluster.DefaultRenewEvery + leaseCheck
)

// Audit actions of import jobs.
const (
	ActionImportCreate = "import.create"
	ActionImportCancel = "import.cancel"
	ActionImportDone   = "import.done"
	ActionImportFailed = "import.failed"
)

// JobStore is the import_jobs persistence (meta.ImportJobs).
type JobStore interface {
	Create(ctx context.Context, j *meta.ImportJob) error
	Get(ctx context.Context, id string) (meta.ImportJob, error)
	List(ctx context.Context, limit int) ([]meta.ImportJob, error)
	ListRunnable(ctx context.Context) ([]meta.ImportJob, error)
	MarkRunning(ctx context.Context, id string, at time.Time) error
	SaveProgress(ctx context.Context, id string, p meta.ImportProgress) error
	SetWarnings(ctx context.Context, id string, warnings []string) error
	Finish(ctx context.Context, id, status, lastError string, at time.Time) error
	RequestCancel(ctx context.Context, id string, at time.Time) error
}

// EventWriter stores events (the ClickHouse writer; it honours store.WithDedupToken).
type EventWriter interface {
	WriteEvents(ctx context.Context, ev []model.Event) error
}

// Elector hands out the per-job leases (cluster.Elector).
type Elector interface {
	Register(names ...string)
	IsLeader(name string) bool
	Lead(ctx context.Context, name string, fn func(ctx context.Context))
	Release(ctx context.Context, name string)
}

// RunnerDeps are the collaborators of a Runner.
type RunnerDeps struct {
	Jobs        JobStore
	Writer      EventWriter
	Streams     StreamResolver
	Signals     SignalSink
	Box         Sealer
	Elector     Elector
	NodeID      string
	IsPermanent func(error) bool // permanent storage errors (clickhouse.IsPermanent)
	Audit       *service.Auditor
	Logger      *slog.Logger
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error // nil = real sleep
}

// Runner executes import jobs. Every node runs one; the import:<id> lease makes sure a job runs
// on one node at a time, and a job whose node died is resumed from its checkpoint by whichever
// node takes the lease over (SPEC §9.9, §17.5).
type Runner struct {
	d    RunnerDeps
	wake chan struct{}

	mu     sync.Mutex
	active map[string]bool // job id ⇒ registered for its lease on this node
}

// NewRunner returns a runner.
func NewRunner(d RunnerDeps) *Runner {
	if d.Sleep == nil {
		d.Sleep = sleepCtx
	}
	return &Runner{d: d, wake: make(chan struct{}, 1), active: map[string]bool{}}
}

// Wake makes the runner look for jobs now (after a job was created on this node).
func (r *Runner) Wake() {
	select {
	case r.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Run polls for runnable jobs until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		r.schedule(ctx, &wg)
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-t.C:
		}
	}
}

// schedule registers leases for runnable jobs this node may run and starts the ones it leads.
func (r *Runner) schedule(ctx context.Context, wg *sync.WaitGroup) {
	jobs, err := r.d.Jobs.ListRunnable(ctx)
	if err != nil {
		r.d.Logger.Error("listing import jobs failed", "err", err)
		return
	}
	for i := range jobs {
		j := &jobs[i]
		if !r.eligible(j) {
			continue
		}
		name := cluster.LeaseImportPrefix + j.ID
		r.mu.Lock()
		registered, room := r.active[j.ID], len(r.active) < maxConcurrentJobs
		if !registered && room {
			r.active[j.ID] = true
			r.d.Elector.Register(name)
			wg.Add(1)
			go r.lead(ctx, wg, j.ID, name)
		}
		r.mu.Unlock()
	}
}

// eligible: kibana jobs run synchronously when uploaded; file jobs only where the files are.
func (r *Runner) eligible(j *meta.ImportJob) bool {
	switch j.Kind {
	case KindKibana:
		return false
	case KindCLEFFile:
		var c CLEFConfig
		return json.Unmarshal(j.Config, &c) == nil && c.Node == r.d.NodeID
	default:
		return true
	}
}

// lead competes for the job's lease, runs the job while holding it and releases it at the end.
func (r *Runner) lead(ctx context.Context, wg *sync.WaitGroup, id, name string) {
	defer wg.Done()
	defer func() {
		r.d.Elector.Release(context.WithoutCancel(ctx), name)
		r.mu.Lock()
		delete(r.active, id)
		r.mu.Unlock()
	}()
	if !r.awaitLease(ctx, name) {
		return // another node runs the job; the slot is freed for other jobs
	}
	r.d.Elector.Lead(ctx, name, func(lctx context.Context) {
		if err := r.RunJob(lctx, id); err != nil && lctx.Err() == nil {
			r.d.Logger.Error("import job failed", "id", id, "err", err)
		}
	})
}

// awaitLease waits up to two lease renewal rounds for this node to acquire name.
func (r *Runner) awaitLease(ctx context.Context, name string) bool {
	for waited := time.Duration(0); waited <= leaseWait; waited += leaseCheck {
		if r.d.Elector.IsLeader(name) {
			return true
		}
		if r.d.Sleep(ctx, leaseCheck) != nil {
			return false
		}
	}
	return false
}

// jobRun is the mutable state of one execution.
type jobRun struct {
	job      meta.ImportJob
	progress meta.ImportProgress
	warnings []string
	dirty    bool // warnings changed
	final    bool // the job reached a terminal status
}

func (j *jobRun) warn(msg string) {
	switch {
	case len(j.warnings) < maxWarnings:
		j.warnings = append(j.warnings, msg)
	case len(j.warnings) == maxWarnings:
		j.warnings = append(j.warnings, "further warnings omitted")
	default:
		return
	}
	j.dirty = true
}

// RunJob executes (or resumes) one job until it finishes, is cancelled or ctx ends. The caller
// must hold the job's lease. A job whose context ends stays running and is resumed later.
func (r *Runner) RunJob(ctx context.Context, id string) error {
	job, err := r.d.Jobs.Get(ctx, id)
	if err != nil {
		return err
	}
	if job.Status != meta.ImportQueued && job.Status != meta.ImportRunning {
		return nil
	}
	run := &jobRun{job: job, warnings: append([]string(nil), job.Warnings...), progress: meta.ImportProgress{
		Checkpoint: job.Checkpoint, RowsImported: job.RowsImported, RowsTotal: job.RowsTotal, Errors: job.Errors,
		LastError: job.LastError}}
	src, err := r.source(&job, run.warn)
	if err != nil {
		return r.finish(ctx, run, err)
	}
	defer func() { _ = src.Close(context.WithoutCancel(ctx), run.final) }() // best effort; contexts expire server side
	if err := r.d.Jobs.MarkRunning(ctx, id, r.d.Now()); err != nil {
		return err
	}
	return r.loop(ctx, run, src)
}

func (r *Runner) loop(ctx context.Context, run *jobRun, src Source) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cancelled, err := r.cancelRequested(ctx, run.job.ID); err != nil || cancelled {
			if err != nil {
				return err
			}
			return r.finishStatus(ctx, run, meta.ImportCancelled, "")
		}
		page, err := r.nextPage(ctx, src, run.progress.Checkpoint)
		if err == nil && len(page.Events) > 0 {
			var n int
			n, err = r.write(ctx, PageToken(run.job.ID, run.progress.Checkpoint, page.Key), page.Events)
			run.progress.RowsImported += int64(n)
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return r.finish(ctx, run, err)
		}
		if err := r.commit(ctx, run, &page); err != nil {
			return err
		}
		if page.Done {
			return r.finishStatus(ctx, run, meta.ImportCompleted, "")
		}
	}
}

func (r *Runner) cancelRequested(ctx context.Context, id string) (bool, error) {
	j, err := r.d.Jobs.Get(ctx, id)
	if err != nil {
		return false, err
	}
	return j.CancelRequested, nil
}

// commit stores the checkpoint after a written page.
func (r *Runner) commit(ctx context.Context, run *jobRun, page *Page) error {
	run.progress.Checkpoint = page.Checkpoint
	run.progress.Errors += page.Skipped
	if page.LastError != "" {
		run.progress.LastError = page.LastError
	}
	if page.Total > 0 {
		run.progress.RowsTotal = page.Total
	}
	if run.dirty {
		if err := r.d.Jobs.SetWarnings(ctx, run.job.ID, run.warnings); err != nil {
			return err
		}
		run.dirty = false
	}
	return r.d.Jobs.SaveProgress(ctx, run.job.ID, run.progress)
}

// nextPage reads a page, retrying transient source errors with backoff.
func (r *Runner) nextPage(ctx context.Context, src Source, cp json.RawMessage) (Page, error) {
	var err error
	for attempt := 1; attempt <= maxSourceAttempts; attempt++ {
		var p Page
		if p, err = src.Next(ctx, cp); err == nil {
			return p, nil
		}
		if errors.Is(err, ErrPermanent) || ctx.Err() != nil {
			return Page{}, err
		}
		r.d.Logger.Warn("import source request failed; retrying", "attempt", attempt, "err", err)
		if serr := r.d.Sleep(ctx, backoff(attempt)); serr != nil {
			return Page{}, serr
		}
	}
	return Page{}, err
}

// write finalizes the page's events and inserts them with the page's deduplication token,
// retrying transient storage errors. It returns the number of rows stored (paused streams drop).
func (r *Runner) write(ctx context.Context, token string, events []model.Event) (int, error) {
	now := r.d.Now()
	kept := make([]model.Event, 0, len(events))
	for i := range events {
		ev := events[i]
		ev.ID = EventID(token, i, ev.Timestamp, now)
		normalize.Finalize(&ev, now)
		if ev.Stream == "" {
			ev.Stream = model.DefaultStream
		}
		name, ok := r.d.Streams.Resolve(ctx, ev.Stream)
		if !ok {
			continue
		}
		ev.Stream = name
		kept = append(kept, ev)
	}
	if len(kept) == 0 {
		return 0, nil
	}
	wctx := store.WithDedupToken(ctx, token)
	for attempt := 1; ; attempt++ {
		err := r.d.Writer.WriteEvents(wctx, kept)
		if err == nil {
			return len(kept), nil
		}
		if r.d.IsPermanent(err) || attempt >= maxWriteAttempts || ctx.Err() != nil {
			return 0, fmt.Errorf("write imported events: %w", err)
		}
		if serr := r.d.Sleep(ctx, backoff(attempt)); serr != nil {
			return 0, serr
		}
	}
}

func (r *Runner) finish(ctx context.Context, run *jobRun, cause error) error {
	if err := r.finishStatus(ctx, run, meta.ImportFailed, cause.Error()); err != nil {
		return err
	}
	return cause
}

// finishStatus records a terminal status and audits it.
func (r *Runner) finishStatus(ctx context.Context, run *jobRun, status, lastError string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), progressTimeout)
	defer cancel()
	if run.dirty {
		if err := r.d.Jobs.SetWarnings(ctx, run.job.ID, run.warnings); err != nil {
			return err
		}
	}
	if lastError == "" {
		lastError = run.progress.LastError
	}
	if err := r.d.Jobs.Finish(ctx, run.job.ID, status, lastError, r.d.Now()); err != nil {
		return err
	}
	run.final = true
	action := ActionImportDone
	if status == meta.ImportFailed {
		action = ActionImportFailed
	}
	r.d.Audit.Record(ctx, service.SystemActor, action, run.job.ID, map[string]any{"status": status,
		"rowsImported": run.progress.RowsImported, "errors": run.progress.Errors})
	return nil
}

// source builds the reader of a job from its stored configuration (secrets opened).
func (r *Runner) source(job *meta.ImportJob, warn func(string)) (Source, error) {
	switch job.Kind {
	case KindElastic:
		var c ElasticConfig
		if err := json.Unmarshal(job.Config, &c); err != nil {
			return nil, fmt.Errorf("%w: config: %w", ErrPermanent, err)
		}
		if err := c.Auth.open(r.d.Box); err != nil {
			return nil, err
		}
		return newElasticSource(c)
	case KindSeq:
		var c SeqConfig
		if err := json.Unmarshal(job.Config, &c); err != nil {
			return nil, fmt.Errorf("%w: config: %w", ErrPermanent, err)
		}
		if err := openSecret(r.d.Box, &c.APIKey); err != nil {
			return nil, err
		}
		return newSeqSource(c, r.d.Signals, jobOwner(job), warn)
	case KindCLEFFile:
		var c CLEFConfig
		if err := json.Unmarshal(job.Config, &c); err != nil {
			return nil, fmt.Errorf("%w: config: %w", ErrPermanent, err)
		}
		return newCLEFSource(c), nil
	default:
		return nil, fmt.Errorf("%w: kind %q cannot run in the background", ErrPermanent, job.Kind)
	}
}

// jobOwner is the user owning signals created by the job (requested_by = "user:<id>").
func jobOwner(job *meta.ImportJob) string {
	const prefix = "user:"
	if len(job.RequestedBy) > len(prefix) && job.RequestedBy[:len(prefix)] == prefix {
		return job.RequestedBy[len(prefix):]
	}
	return job.RequestedBy
}

// PageToken is the deterministic insert deduplication token of the page with content key key
// that follows checkpoint cp of job jobID (SPEC §8.5 item 7, §9.9): a resumed job re-reads the
// same page after the same checkpoint and ClickHouse drops the repeated insert, while a page
// whose content differs never reuses a token (which would silently drop it).
func PageToken(jobID string, cp json.RawMessage, key string) string {
	if len(cp) == 0 {
		cp = json.RawMessage("{}")
	}
	h := sha256.New()
	h.Write([]byte("tidemark-import\x00" + jobID + "\x00"))
	h.Write(cp)
	h.Write([]byte("\x00" + key))
	sum := h.Sum(nil)
	var id uuid.UUID
	copy(id[:], sum[:16])
	return "import-" + id.String()
}

// EventID derives a stable UUIDv7-shaped id for the i-th event of a page: the event time (or
// now when it has none) in the timestamp bits, the rest from the page token.
func EventID(token string, i int, ts, now time.Time) uuid.UUID {
	if ts.IsZero() {
		ts = now
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", token, i)))
	var id uuid.UUID
	copy(id[:], sum[:16])
	ms := ts.UnixMilli()
	if ms < 0 {
		ms = 0
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(ms)) //nolint:gosec // ms is non-negative
	copy(id[0:6], b[2:8])
	id[6] = (id[6] & 0x0f) | 0x70 // version 7
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant
	return id
}

func backoff(attempt int) time.Duration {
	d := retryBase << (attempt - 1)
	if d > retryMax || d <= 0 {
		return retryMax
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
