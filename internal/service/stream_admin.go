// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/retention"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// Stream audit actions (SPEC §15.5).
const (
	ActionStreamCreate = "stream.create"
	ActionStreamUpdate = "stream.update"
	ActionStreamDelete = "stream.delete"
)

// Stream management limits (SPEC §12).
const (
	StatsInterval     = 5 * time.Minute // collector period and cache lifetime (§12.2)
	maxLevelRules     = 6
	LeaderStats       = "stats"
	streamDescription = maxDescription
)

// ErrConfirmMismatch is returned when a destructive request lacks the typed confirmation.
var ErrConfirmMismatch = errors.New("confirmation does not match the stream name")

// ErrProtectedStream is returned when deleting the default stream.
var ErrProtectedStream = errors.New("the default stream cannot be deleted")

// Leader reports whether this node runs a singleton job (SPEC §17.5). Single-node
// installations use an implementation that always returns true.
type Leader interface {
	IsLeader(name string) bool
}

// StreamRepo is the stream persistence used by StreamAdmin.
type StreamRepo interface {
	Create(ctx context.Context, s *meta.Stream) error
	GetByName(ctx context.Context, name string) (meta.Stream, error)
	List(ctx context.Context) ([]meta.Stream, error)
	Update(ctx context.Context, s *meta.Stream) error
	Delete(ctx context.Context, name string) error
}

// StreamStorage is the subset of store.StreamAdmin used for streams.
type StreamStorage interface {
	Stats(ctx context.Context) ([]store.StreamStats, error)
	Mappings(ctx context.Context, scope model.StreamScope, stream string) ([]store.FieldMapping, error)
	DeleteStream(ctx context.Context, name string) error
}

// StreamGrants removes the team grants of a deleted stream (SPEC §12.7, §15.10).
type StreamGrants interface {
	RevokeStream(ctx context.Context, stream string) ([]string, error)
}

// RetentionSyncer applies the retention policy after stream changes.
type RetentionSyncer interface {
	Sync(ctx context.Context) error
}

// StreamAdminDeps bundles the collaborators of StreamAdmin.
type StreamAdminDeps struct {
	Repo      StreamRepo
	Registry  *StreamRegistry
	Storage   StreamStorage
	Grants    StreamGrants
	Access    ScopeResolver
	Retention RetentionSyncer
	Indexed   IndexedFieldLister
	Audit     *Auditor
	Leader    Leader
	Now       func() time.Time
	Logger    *slog.Logger
}

// StreamAdmin manages streams: CRUD with retention and pause, statistics and mappings
// (SPEC §12).
type StreamAdmin struct {
	d StreamAdminDeps

	mu      sync.Mutex
	stats   []store.StreamStats
	statsAt time.Time
}

// NewStreamAdmin returns the stream admin service.
func NewStreamAdmin(d StreamAdminDeps) *StreamAdmin { return &StreamAdmin{d: d} }

// StreamInput holds editable stream fields; nil = unchanged (or default on create). A
// retention value of nil inside a non-nil pointer clears the override.
type StreamInput struct {
	Name                 string
	Description          *string
	RetentionDays        **int
	SpansRetentionDays   **int
	MetricsRetentionDays **int
	LevelRules           *[]meta.LevelRule
	Paused               *bool
}

// StreamWithStats is a stream with its per-source statistics.
type StreamWithStats struct {
	meta.Stream
	Stats map[model.Source]store.StreamStats
}

// List returns the streams p may see with cached statistics (refreshed when older than
// StatsInterval).
func (s *StreamAdmin) List(ctx context.Context, p auth.Principal) ([]StreamWithStats, time.Time, error) {
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return nil, time.Time{}, err
	}
	streams, err := s.d.Repo.List(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}
	stats, at := s.Stats(ctx)
	out := make([]StreamWithStats, 0, len(streams))
	for i := range streams {
		if !scope.Allows(streams[i].Name) {
			continue
		}
		ws := StreamWithStats{Stream: streams[i], Stats: map[model.Source]store.StreamStats{}}
		for j := range stats {
			if stats[j].Stream == streams[i].Name {
				ws.Stats[stats[j].Source] = stats[j]
			}
		}
		out = append(out, ws)
	}
	return out, at, nil
}

// Stats returns the cached statistics, refreshing them on demand when stale. Collection
// errors are logged and the previous values are kept.
func (s *StreamAdmin) Stats(ctx context.Context) ([]store.StreamStats, time.Time) {
	s.mu.Lock()
	fresh := !s.statsAt.IsZero() && s.d.Now().Sub(s.statsAt) < StatsInterval
	stats, at := s.stats, s.statsAt
	s.mu.Unlock()
	if fresh {
		return stats, at
	}
	if err := s.RefreshStats(ctx); err != nil {
		s.d.Logger.Warn("stream stats collection failed", "err", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats, s.statsAt
}

// RefreshStats collects statistics now.
func (s *StreamAdmin) RefreshStats(ctx context.Context) error {
	stats, err := s.d.Storage.Stats(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats, s.statsAt = stats, s.d.Now()
	return nil
}

// RunStatsCollector refreshes statistics every interval while this node leads "stats".
func (s *StreamAdmin) RunStatsCollector(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if s.d.Leader.IsLeader(LeaderStats) {
			if err := s.RefreshStats(ctx); err != nil && ctx.Err() == nil {
				s.d.Logger.Warn("stream stats collection failed", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Get returns one stream.
func (s *StreamAdmin) Get(ctx context.Context, name string) (meta.Stream, error) {
	return s.d.Repo.GetByName(ctx, name)
}

// Create adds a stream explicitly.
func (s *StreamAdmin) Create(ctx context.Context, actor Actor, in StreamInput) (meta.Stream, error) {
	if !model.ValidStreamName(in.Name) {
		return meta.Stream{}, invalid("name", "must match ^[a-z0-9][a-z0-9._-]{0,63}$")
	}
	if s.d.Registry.Full() {
		return meta.Stream{}, invalid("name", "the maximum number of streams is reached")
	}
	st := meta.Stream{Name: in.Name}
	if err := applyStreamInput(&st, in); err != nil {
		return meta.Stream{}, err
	}
	if err := s.d.Repo.Create(ctx, &st); err != nil {
		return meta.Stream{}, err
	}
	s.d.Registry.Track(st.Name, st.Paused)
	if hasRetentionOverride(st) {
		if err := s.d.Retention.Sync(ctx); err != nil {
			return st, fmt.Errorf("apply retention: %w", err)
		}
	}
	s.d.Audit.Record(ctx, actor, ActionStreamCreate, st.Name, nil)
	return st, nil
}

// Update changes a stream; retention is re-applied when it changed and the pipeline gate
// follows pause/resume immediately.
func (s *StreamAdmin) Update(ctx context.Context, actor Actor, name string, in StreamInput) (meta.Stream, error) {
	st, err := s.d.Repo.GetByName(ctx, name)
	if err != nil {
		return meta.Stream{}, err
	}
	before := st
	if err := applyStreamInput(&st, in); err != nil {
		return meta.Stream{}, err
	}
	if err := s.d.Repo.Update(ctx, &st); err != nil {
		return meta.Stream{}, err
	}
	s.d.Registry.Track(st.Name, st.Paused)
	if retentionChanged(before, st) {
		if err := s.d.Retention.Sync(ctx); err != nil {
			return st, fmt.Errorf("apply retention: %w", err)
		}
	}
	s.d.Audit.Record(ctx, actor, ActionStreamUpdate, st.Name, map[string]any{"paused": st.Paused})
	return st, nil
}

// Delete drops a stream and all of its data; confirm must equal the name.
func (s *StreamAdmin) Delete(ctx context.Context, actor Actor, name, confirm string) error {
	if name == model.DefaultStream {
		return ErrProtectedStream
	}
	if confirm != name {
		return ErrConfirmMismatch
	}
	st, err := s.d.Repo.GetByName(ctx, name)
	if err != nil {
		return err
	}
	s.d.Registry.Track(name, true) // stop new rows while the data is deleted
	if err := s.d.Storage.DeleteStream(ctx, name); err != nil {
		s.d.Registry.Track(name, st.Paused)
		return err
	}
	if err := s.d.Repo.Delete(ctx, name); err != nil {
		return err
	}
	revoked, err := s.d.Grants.RevokeStream(ctx, name)
	if err != nil {
		return fmt.Errorf("revoke team grants: %w", err)
	}
	s.d.Registry.Forget(name)
	if hasRetentionOverride(st) {
		if err := s.d.Retention.Sync(ctx); err != nil {
			return fmt.Errorf("apply retention: %w", err)
		}
	}
	s.d.Audit.Record(ctx, actor, ActionStreamDelete, name, map[string]any{"revokedFromTeams": revoked})
	return nil
}

// FieldMappingView is a property key of a stream with its indexed state.
type FieldMappingView struct {
	store.FieldMapping
	Indexed bool
}

// Fields lists the property keys of a stream (SPEC §12.4); a stream p may not see is not
// found.
func (s *StreamAdmin) Fields(ctx context.Context, p auth.Principal, name string) ([]FieldMappingView, error) {
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return nil, err
	}
	if !scope.Allows(name) {
		return nil, meta.ErrNotFound
	}
	if _, err := s.d.Repo.GetByName(ctx, name); err != nil {
		return nil, err
	}
	maps, err := s.d.Storage.Mappings(ctx, scope, name)
	if err != nil {
		return nil, err
	}
	cols, err := s.d.Indexed.Columns(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]FieldMappingView, 0, len(maps))
	for _, m := range maps {
		_, ok := cols[m.Source][m.Key]
		out = append(out, FieldMappingView{FieldMapping: m, Indexed: ok})
	}
	return out, nil
}

func applyStreamInput(st *meta.Stream, in StreamInput) error {
	if in.Description != nil {
		if len(*in.Description) > streamDescription {
			return invalid("description", "is too long")
		}
		st.Description = *in.Description
	}
	for _, f := range []struct {
		field string
		in    **int
		dst   **int
	}{{"retentionDays", in.RetentionDays, &st.RetentionDays}, {"spansRetentionDays", in.SpansRetentionDays, &st.SpansRetentionDays},
		{"metricsRetentionDays", in.MetricsRetentionDays, &st.MetricsRetentionDays}} {
		if f.in == nil {
			continue
		}
		if d := *f.in; d != nil && (*d < retention.MinDays || *d > retention.MaxDays) {
			return invalid(f.field, fmt.Sprintf("must be between %d and %d", retention.MinDays, retention.MaxDays))
		}
		*f.dst = *f.in
	}
	if in.LevelRules != nil {
		rules, err := normalizeLevelRules(*in.LevelRules)
		if err != nil {
			return err
		}
		st.LevelRules = rules
	}
	if in.Paused != nil {
		st.Paused = *in.Paused
	}
	return nil
}

// normalizeLevelRules validates level rules and stores canonical level names.
func normalizeLevelRules(rules []meta.LevelRule) ([]meta.LevelRule, error) {
	if len(rules) > maxLevelRules {
		return nil, invalid("levelRules", fmt.Sprintf("at most %d rules", maxLevelRules))
	}
	out := make([]meta.LevelRule, 0, len(rules))
	for _, r := range rules {
		if r.Days < retention.MinDays || r.Days > retention.MaxDays {
			return nil, invalid("levelRules", fmt.Sprintf("days must be between %d and %d", retention.MinDays, retention.MaxDays))
		}
		if len(r.Levels) == 0 {
			return nil, invalid("levelRules", "each rule needs at least one level")
		}
		var levels []string
		for _, name := range r.Levels {
			l, ok := model.ParseLevel(name)
			if !ok {
				return nil, invalid("levelRules", fmt.Sprintf("unknown level %q", name))
			}
			if !slices.Contains(levels, l.String()) {
				levels = append(levels, l.String())
			}
		}
		out = append(out, meta.LevelRule{Levels: levels, Days: r.Days})
	}
	return out, nil
}

func hasRetentionOverride(s meta.Stream) bool {
	return s.RetentionDays != nil || s.SpansRetentionDays != nil || s.MetricsRetentionDays != nil || len(s.LevelRules) > 0
}

func retentionChanged(a, b meta.Stream) bool {
	eq := func(x, y *int) bool { return (x == nil && y == nil) || (x != nil && y != nil && *x == *y) }
	if !eq(a.RetentionDays, b.RetentionDays) || !eq(a.SpansRetentionDays, b.SpansRetentionDays) ||
		!eq(a.MetricsRetentionDays, b.MetricsRetentionDays) || len(a.LevelRules) != len(b.LevelRules) {
		return true
	}
	for i := range a.LevelRules {
		if a.LevelRules[i].Days != b.LevelRules[i].Days || !slices.Equal(a.LevelRules[i].Levels, b.LevelRules[i].Levels) {
			return true
		}
	}
	return false
}
