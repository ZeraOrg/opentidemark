// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/retention"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// Setting keys exposed through GET/PATCH /settings.
const (
	SettingRetention = "retention"
	SettingUI        = "ui"
	maxUISettingSize = 16 * 1024
)

// RetentionSettings is the "retention" setting.
type RetentionSettings struct {
	LogsDays    int `json:"logsDays"`
	SpansDays   int `json:"spansDays"`
	MetricsDays int `json:"metricsDays"`
}

// StreamLister lists streams for retention.
type StreamLister interface {
	List(ctx context.Context) ([]meta.Stream, error)
}

// Settings manages admin settings.
type Settings struct {
	store     SettingsStore
	retention *RetentionSync
	audit     *Auditor
}

// NewSettings returns the settings service.
func NewSettings(store SettingsStore, rs *RetentionSync, audit *Auditor) *Settings {
	return &Settings{store: store, retention: rs, audit: audit}
}

// All returns the public settings with defaults applied.
func (s *Settings) All(ctx context.Context) (map[string]json.RawMessage, error) {
	ret, err := LoadRetention(ctx, s.store)
	if err != nil {
		return nil, err
	}
	rb, err := json.Marshal(ret)
	if err != nil {
		return nil, err
	}
	acc, err := LoadAccess(ctx, s.store)
	if err != nil {
		return nil, err
	}
	ab, err := json.Marshal(acc)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{SettingRetention: rb, SettingAccess: ab, SettingUI: json.RawMessage("{}")}
	if ui, err := s.store.Get(ctx, SettingUI); err == nil {
		out[SettingUI] = ui
	} else if !errors.Is(err, meta.ErrNotFound) {
		return nil, err
	}
	return out, nil
}

// Patch validates and stores the given keys; retention changes are applied immediately, access
// changes with the next request (SPEC §15.10).
func (s *Settings) Patch(ctx context.Context, actor Actor, patch map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	details := map[string]any{}
	for k, v := range patch {
		switch k {
		case SettingRetention:
			if err := validateRetention(v); err != nil {
				return nil, err
			}
		case SettingAccess:
			acc, err := validateAccess(v)
			if err != nil {
				return nil, err
			}
			details["access.denyByDefault"] = acc.DenyByDefault
		case SettingUI:
			if len(v) > maxUISettingSize || !isJSONObject(v) {
				return nil, invalid(SettingUI, "must be a JSON object of at most 16 KiB")
			}
		default:
			return nil, invalid(k, "unknown setting")
		}
	}
	for k, v := range patch {
		if err := s.store.Set(ctx, k, v); err != nil {
			return nil, err
		}
	}
	if _, ok := patch[SettingRetention]; ok {
		if err := s.retention.Sync(ctx); err != nil {
			return nil, fmt.Errorf("apply retention: %w", err)
		}
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	details["keys"] = keys
	s.audit.Record(ctx, actor, ActionSettingsUpdate, "settings", details)
	return s.All(ctx)
}

func isJSONObject(v json.RawMessage) bool {
	var m map[string]any
	return json.Unmarshal(v, &m) == nil && m != nil
}

func validateRetention(v json.RawMessage) error {
	var r RetentionSettings
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return invalid(SettingRetention, "expected {logsDays, spansDays, metricsDays}")
	}
	err := retention.Validate(retention.Policy{Defaults: retention.Defaults{LogsDays: r.LogsDays, SpansDays: r.SpansDays, MetricsDays: r.MetricsDays}})
	if err != nil {
		return invalid(SettingRetention, err.Error())
	}
	return nil
}

// LoadRetention reads the retention defaults (built-in defaults when unset).
func LoadRetention(ctx context.Context, st SettingsStore) (RetentionSettings, error) {
	d := retention.DefaultDefaults()
	r := RetentionSettings{LogsDays: d.LogsDays, SpansDays: d.SpansDays, MetricsDays: d.MetricsDays}
	v, err := st.Get(ctx, SettingRetention)
	if errors.Is(err, meta.ErrNotFound) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(v, &r); err != nil {
		return r, fmt.Errorf("stored retention setting: %w", err)
	}
	return r, nil
}

// RetentionSync builds TTL rules from settings and streams and applies them (SPEC §12.3).
type RetentionSync struct {
	settings SettingsStore
	streams  StreamLister
	applier  store.RetentionApplier
	logger   *slog.Logger
}

// NewRetentionSync returns a retention synchronizer.
func NewRetentionSync(settings SettingsStore, streams StreamLister, applier store.RetentionApplier, logger *slog.Logger) *RetentionSync {
	return &RetentionSync{settings: settings, streams: streams, applier: applier, logger: logger}
}

// Sync applies the current policy (idempotent).
func (r *RetentionSync) Sync(ctx context.Context) error {
	p, err := r.Policy(ctx)
	if err != nil {
		return err
	}
	rules, err := retention.Build(p)
	if err != nil {
		return err
	}
	return r.applier.ApplyRetention(ctx, rules)
}

// Policy assembles the retention policy.
func (r *RetentionSync) Policy(ctx context.Context) (retention.Policy, error) {
	rs, err := LoadRetention(ctx, r.settings)
	if err != nil {
		return retention.Policy{}, err
	}
	streams, err := r.streams.List(ctx)
	if err != nil {
		return retention.Policy{}, err
	}
	p := retention.Policy{Defaults: retention.Defaults{LogsDays: rs.LogsDays, SpansDays: rs.SpansDays, MetricsDays: rs.MetricsDays}}
	for i := range streams {
		s := &streams[i]
		sp := retention.StreamPolicy{Name: s.Name, LogsDays: s.RetentionDays, SpansDays: s.SpansRetentionDays, MetricsDays: s.MetricsRetentionDays}
		for _, lr := range s.LevelRules {
			rule := retention.LevelRule{Days: lr.Days}
			for _, name := range lr.Levels {
				if l, ok := model.ParseLevel(name); ok {
					rule.Levels = append(rule.Levels, l)
				}
			}
			sp.LevelRules = append(sp.LevelRules, rule)
		}
		if sp.LogsDays != nil || sp.SpansDays != nil || sp.MetricsDays != nil || len(sp.LevelRules) > 0 {
			p.Streams = append(p.Streams, sp)
		}
	}
	return p, nil
}
