// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// StreamStore is the stream persistence used by the registry.
type StreamStore interface {
	Create(ctx context.Context, s *meta.Stream) error
	List(ctx context.Context) ([]meta.Stream, error)
}

// StreamRegistry auto-creates streams on first write (up to the configured maximum) and
// reports paused streams; it implements pipeline.StreamGate (SPEC §12.1, §12.6). Data for new
// streams beyond the limit is stored in the "default" stream.
type StreamRegistry struct {
	store  StreamStore
	max    int
	logger *slog.Logger

	mu     sync.RWMutex
	paused map[string]bool // known stream ⇒ paused
}

// NewStreamRegistry returns a registry allowing up to maxStreams streams.
func NewStreamRegistry(store StreamStore, maxStreams int, logger *slog.Logger) *StreamRegistry {
	return &StreamRegistry{store: store, max: maxStreams, logger: logger, paused: map[string]bool{}}
}

// Load reads all streams and makes sure the default stream exists.
func (r *StreamRegistry) Load(ctx context.Context) error {
	streams, err := r.store.List(ctx)
	if err != nil {
		return fmt.Errorf("load streams: %w", err)
	}
	r.mu.Lock()
	r.paused = make(map[string]bool, len(streams))
	for i := range streams {
		r.paused[streams[i].Name] = streams[i].Paused
	}
	_, hasDefault := r.paused[model.DefaultStream]
	r.mu.Unlock()
	if !hasDefault {
		return r.create(ctx, model.DefaultStream)
	}
	return nil
}

// Resolve implements pipeline.StreamGate.
func (r *StreamRegistry) Resolve(ctx context.Context, name string) (string, bool) {
	r.mu.RLock()
	paused, known := r.paused[name]
	count := len(r.paused)
	r.mu.RUnlock()
	if known {
		return name, !paused
	}
	if count >= r.max {
		r.mu.RLock()
		defPaused := r.paused[model.DefaultStream]
		r.mu.RUnlock()
		return model.DefaultStream, !defPaused
	}
	if err := r.create(ctx, name); err != nil {
		r.logger.Warn("auto-create stream failed; storing in default", "stream", name, "err", err)
		return model.DefaultStream, true
	}
	return name, true
}

func (r *StreamRegistry) create(ctx context.Context, name string) error {
	err := r.store.Create(ctx, &meta.Stream{Name: name})
	if err != nil && !errors.Is(err, meta.ErrConflict) {
		return err
	}
	r.mu.Lock()
	if _, ok := r.paused[name]; !ok {
		r.paused[name] = false
	}
	r.mu.Unlock()
	return nil
}

// Count returns the number of known streams.
func (r *StreamRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.paused)
}

// Full reports whether the stream limit is reached.
func (r *StreamRegistry) Full() bool { return r.Count() >= r.max }

// Track records a stream created or changed through the API; paused streams drop their rows
// in the pipeline (SPEC §12.6).
func (r *StreamRegistry) Track(name string, paused bool) {
	r.mu.Lock()
	r.paused[name] = paused
	r.mu.Unlock()
}

// Forget removes a deleted stream; new data for it auto-creates it again.
func (r *StreamRegistry) Forget(name string) {
	r.mu.Lock()
	delete(r.paused, name)
	r.mu.Unlock()
}
