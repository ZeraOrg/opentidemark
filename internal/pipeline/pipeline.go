// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// Signal names (used in metrics and dead-letter file names).
const (
	SignalEvents  = "events"
	SignalSpans   = "spans"
	SignalMetrics = "metrics"
)

// EventWriter, SpanWriter and MetricWriter are the storage ports used by the pipeline.
type (
	EventWriter interface {
		WriteEvents(ctx context.Context, ev []model.Event) error
	}
	SpanWriter interface {
		WriteSpans(ctx context.Context, sp []model.Span) error
	}
	MetricWriter interface {
		WriteMetrics(ctx context.Context, mp []model.MetricPoint) error
	}
)

// Deps are the collaborators of a Pipeline.
type Deps struct {
	Events  EventWriter
	Spans   SpanWriter
	Metrics MetricWriter
	Gate    StreamGate
	Dead    DeadLetter
	Stats   Metrics
	Logger  *slog.Logger
	Now     func() time.Time
	Sleep   func(ctx context.Context, d time.Duration) error
	// WAL, when set, makes every signal durable in its write-ahead log before acknowledging
	// (SPEC §9.8); without it the in-memory queues of §9.1 are used.
	WAL *WALJournals
}

// WALJournals are the per-signal write-ahead logs and the insert error classifier.
type WALJournals struct {
	Events      Journal
	Spans       Journal
	Metrics     Journal
	IsPermanent func(error) bool
}

// signalQueue is implemented by the in-memory Queue and the WALQueue.
type signalQueue[T any] interface {
	Enqueue(ctx context.Context, rows []T) error
	Depth() int
	Start()
	Close(ctx context.Context) error
}

// Pipeline owns one queue per signal and implements the ingest sinks.
type Pipeline struct {
	events  signalQueue[model.Event]
	spans   signalQueue[model.Span]
	metrics signalQueue[model.MetricPoint]
}

// New builds a pipeline; call Start, and Shutdown to drain.
func New(cfg Config, d Deps) *Pipeline {
	ev := QueueDeps[model.Event]{Write: d.Events.WriteEvents, Stream: func(e *model.Event) *string { return &e.Stream },
		Gate: d.Gate, Dead: d.Dead, Metrics: d.Stats, Logger: d.Logger, Now: d.Now, Sleep: d.Sleep}
	sp := QueueDeps[model.Span]{Write: d.Spans.WriteSpans, Stream: func(s *model.Span) *string { return &s.Stream },
		Gate: d.Gate, Dead: d.Dead, Metrics: d.Stats, Logger: d.Logger, Now: d.Now, Sleep: d.Sleep}
	mp := QueueDeps[model.MetricPoint]{Write: d.Metrics.WriteMetrics, Stream: func(m *model.MetricPoint) *string { return &m.Stream },
		Gate: d.Gate, Dead: d.Dead, Metrics: d.Stats, Logger: d.Logger, Now: d.Now, Sleep: d.Sleep}
	if w := d.WAL; w != nil {
		return &Pipeline{
			events: NewWALQueue(SignalEvents, cfg, WALQueueDeps[model.Event]{QueueDeps: ev, Journal: w.Events,
				Codec: EventCodec(), IsPermanent: w.IsPermanent}),
			spans: NewWALQueue(SignalSpans, cfg, WALQueueDeps[model.Span]{QueueDeps: sp, Journal: w.Spans,
				Codec: SpanCodec(), IsPermanent: w.IsPermanent}),
			metrics: NewWALQueue(SignalMetrics, cfg, WALQueueDeps[model.MetricPoint]{QueueDeps: mp, Journal: w.Metrics,
				Codec: MetricCodec(), IsPermanent: w.IsPermanent}),
		}
	}
	return &Pipeline{events: NewQueue(SignalEvents, cfg, ev), spans: NewQueue(SignalSpans, cfg, sp),
		metrics: NewQueue(SignalMetrics, cfg, mp)}
}

// Start launches the batch loops.
func (p *Pipeline) Start() {
	p.events.Start()
	p.spans.Start()
	p.metrics.Start()
}

// EnqueueEvents implements ingest.EventSink.
func (p *Pipeline) EnqueueEvents(ctx context.Context, ev []model.Event) error {
	return p.events.Enqueue(ctx, ev)
}

// EnqueueSpans implements ingest.SpanSink.
func (p *Pipeline) EnqueueSpans(ctx context.Context, sp []model.Span) error {
	return p.spans.Enqueue(ctx, sp)
}

// EnqueueMetrics implements ingest.MetricSink.
func (p *Pipeline) EnqueueMetrics(ctx context.Context, mp []model.MetricPoint) error {
	return p.metrics.Enqueue(ctx, mp)
}

// Depths returns the current depth per signal (for /system).
func (p *Pipeline) Depths() map[string]int {
	return map[string]int{SignalEvents: p.events.Depth(), SignalSpans: p.spans.Depth(), SignalMetrics: p.metrics.Depth()}
}

// Shutdown drains all queues concurrently within ctx.
func (p *Pipeline) Shutdown(ctx context.Context) error {
	errs := make(chan error, 3)
	go func() { errs <- p.events.Close(ctx) }()
	go func() { errs <- p.spans.Close(ctx) }()
	go func() { errs <- p.metrics.Close(ctx) }()
	return errors.Join(<-errs, <-errs, <-errs)
}
