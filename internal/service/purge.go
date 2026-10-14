// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// Purge audit actions and limits (SPEC §12.5).
const (
	ActionPurgeRequest = "purge.request"
	ActionPurgeDone    = "purge.done"
	ActionPurgeFailed  = "purge.failed"
	purgeQueueSize     = 16
	maxPurgeQueryLen   = 8192
	// PurgePollInterval is how often the purge leader looks for jobs queued on other nodes.
	PurgePollInterval = 5 * time.Second
)

// ErrPurgeBusy is returned when too many purge jobs are queued.
var ErrPurgeBusy = errors.New("too many purge jobs are queued")

var errPurgeInterrupted = errors.New("interrupted by a restart")

// PurgeRepo is the purge job persistence.
type PurgeRepo interface {
	Create(ctx context.Context, j *meta.PurgeJob) error
	Get(ctx context.Context, id string) (meta.PurgeJob, error)
	ListByStatus(ctx context.Context, status string) ([]meta.PurgeJob, error)
	SetStatus(ctx context.Context, id, status, mutationID string) error
}

// PurgeStorage executes purges.
type PurgeStorage interface {
	Purge(ctx context.Context, p store.PurgeRequest) (store.PurgeJob, error)
}

// StreamGetter looks up a stream by name.
type StreamGetter interface {
	GetByName(ctx context.Context, name string) (meta.Stream, error)
}

// PurgeInput is a purge request (SPEC §11.5).
type PurgeInput struct {
	Stream string
	Source model.Source
	Query  string
	From   time.Time
	To     time.Time
	DryRun bool
}

// PurgeOutcome is the estimate of a dry run or the queued job.
type PurgeOutcome struct {
	RowsEstimate uint64
	Job          *meta.PurgeJob
}

// Purges validates purge requests and runs the jobs one at a time in the background. The queue
// is the set of persisted pending jobs, so a job requested on any node is run by the node that
// holds the purge lease (SPEC §12.5, §17.5).
type Purges struct {
	repo    PurgeRepo
	storage PurgeStorage
	streams StreamGetter
	audit   *Auditor
	logger  *slog.Logger
	wake    chan struct{}
	poll    time.Duration
}

// NewPurges returns the purge service.
func NewPurges(repo PurgeRepo, storage PurgeStorage, streams StreamGetter, audit *Auditor, logger *slog.Logger) *Purges {
	return &Purges{repo: repo, storage: storage, streams: streams, audit: audit, logger: logger,
		wake: make(chan struct{}, 1), poll: PurgePollInterval}
}

// Get returns a job.
func (s *Purges) Get(ctx context.Context, id string) (meta.PurgeJob, error) {
	return s.repo.Get(ctx, id)
}

// Request validates in and returns the estimate (dry run) or queues a job.
func (s *Purges) Request(ctx context.Context, actor Actor, in PurgeInput) (PurgeOutcome, error) {
	req, err := s.build(ctx, in.Stream, in.Source, in.Query, in.From, in.To)
	if err != nil {
		return PurgeOutcome{}, err
	}
	req.DryRun = true
	est, err := s.storage.Purge(ctx, req)
	if err != nil {
		return PurgeOutcome{}, err
	}
	if in.DryRun {
		return PurgeOutcome{RowsEstimate: est.RowsEstimate}, nil
	}
	job := &meta.PurgeJob{Stream: in.Stream, Source: string(in.Source), TQL: in.Query, From: in.From.UTC(), To: in.To.UTC(),
		Status: meta.PurgePending, RowsEstimate: clampInt64(est.RowsEstimate), RequestedBy: actor.ID}
	if err := s.repo.Create(ctx, job); err != nil {
		return PurgeOutcome{}, err
	}
	if pending, err := s.repo.ListByStatus(ctx, meta.PurgePending); err != nil || len(pending) > purgeQueueSize {
		_ = s.repo.SetStatus(ctx, job.ID, meta.PurgeFailed, "") // best effort: the job never ran
		if err != nil {
			return PurgeOutcome{}, err
		}
		return PurgeOutcome{}, ErrPurgeBusy
	}
	select {
	case s.wake <- struct{}{}:
	default: // a wake-up is already pending
	}
	s.audit.Record(ctx, actor, ActionPurgeRequest, job.ID, map[string]any{"stream": in.Stream, "source": in.Source,
		"q": in.Query, "from": job.From, "to": job.To, "rowsEstimate": est.RowsEstimate})
	return PurgeOutcome{RowsEstimate: est.RowsEstimate, Job: job}, nil
}

// build validates the request and compiles the optional filter.
func (s *Purges) build(ctx context.Context, stream string, src model.Source, q string, from, to time.Time) (store.PurgeRequest, error) {
	if !src.Valid() {
		return store.PurgeRequest{}, invalid("source", "must be logs, spans or metrics")
	}
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return store.PurgeRequest{}, invalid("from", "from and to are required and from must be before to")
	}
	if stream != "" {
		if _, err := s.streams.GetByName(ctx, stream); err != nil {
			return store.PurgeRequest{}, err
		}
	}
	req := store.PurgeRequest{Stream: stream, Source: src, From: from.UTC(), To: to.UTC()}
	if strings.TrimSpace(q) == "" {
		return req, nil
	}
	where, args, err := purgeFilter(src, q, req.From, req.To)
	if err != nil {
		return store.PurgeRequest{}, err
	}
	req.Where, req.Args = where, args
	return req, nil
}

// purgeFilter compiles a filter-only query for src.
func purgeFilter(src model.Source, q string, from, to time.Time) (string, []any, error) {
	if len(q) > maxPurgeQueryLen {
		return "", nil, invalid("q", "query is too long")
	}
	parsed, err := tql.Parse(q)
	if err != nil {
		return "", nil, err
	}
	if parsed.Source != "" && parsed.Source != model.SourceLogs && parsed.Source != src {
		return "", nil, invalid("q", "the query source differs from the purge source")
	}
	for _, st := range parsed.Stages {
		if _, ok := st.(*tql.Where); !ok {
			return "", nil, invalid("q", "only filters and where stages are allowed")
		}
	}
	scoped := *parsed
	scoped.Source = src
	return tql.CompileWhere(&scoped, tql.Options{From: from, To: to, Streams: model.AllStreams()}) // admin only (SPEC §15.10)
}

// Run executes pending jobs, oldest first, until ctx ends; it must run on one node only (the
// purge lease holder). Jobs left running by a previous leader are marked failed first.
func (s *Purges) Run(ctx context.Context) {
	if running, err := s.repo.ListByStatus(ctx, meta.PurgeRunning); err == nil {
		for i := range running {
			s.finish(ctx, running[i], errPurgeInterrupted)
		}
	}
	t := time.NewTicker(s.poll)
	defer t.Stop()
	for {
		s.runPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		}
	}
}

func (s *Purges) runPending(ctx context.Context) {
	pending, err := s.repo.ListByStatus(ctx, meta.PurgePending)
	if err != nil {
		s.logger.Error("listing purge jobs failed", "err", err)
		return
	}
	for i := range pending {
		if ctx.Err() != nil {
			return
		}
		s.execute(ctx, pending[i].ID)
	}
}

func (s *Purges) execute(ctx context.Context, id string) {
	j, err := s.repo.Get(ctx, id)
	if err != nil {
		s.logger.Error("purge job lookup failed", "id", id, "err", err)
		return
	}
	if j.Status != meta.PurgePending {
		return
	}
	if err := s.repo.SetStatus(ctx, id, meta.PurgeRunning, ""); err != nil {
		s.logger.Error("purge job status failed", "id", id, "err", err)
		return
	}
	req, err := s.build(ctx, j.Stream, model.Source(j.Source), j.TQL, j.From, j.To)
	if err == nil {
		var res store.PurgeJob
		res, err = s.storage.Purge(ctx, req)
		j.MutationID = strings.Join(res.DroppedPartitions, ",")
	}
	s.finish(ctx, j, err)
}

// finish records the outcome; MutationID lists the dropped partitions.
func (s *Purges) finish(ctx context.Context, j meta.PurgeJob, runErr error) {
	status, action, details := meta.PurgeDone, ActionPurgeDone, map[string]any{"partitions": j.MutationID}
	if runErr != nil {
		status, action, details = meta.PurgeFailed, ActionPurgeFailed, map[string]any{"error": runErr.Error()}
		s.logger.Error("purge failed", "id", j.ID, "err", runErr)
	}
	if err := s.repo.SetStatus(ctx, j.ID, status, j.MutationID); err != nil {
		s.logger.Error("purge job status failed", "id", j.ID, "err", err)
	}
	s.audit.Record(ctx, SystemActor, action, j.ID, details)
}

func clampInt64(n uint64) int64 {
	if n > 1<<63-1 {
		return 1<<63 - 1
	}
	return int64(n)
}
