// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/alerting"
	"github.com/ZeraOrg/tidemark/internal/meta"
)

// Alert audit actions and limits (SPEC §14).
const (
	ActionAlertCreate   = "alert.create"
	ActionAlertUpdate   = "alert.update"
	ActionAlertDelete   = "alert.delete"
	ActionAlertMute     = "alert.mute"
	DefaultHistoryLimit = 100
	MaxHistoryLimit     = 1000
	maxMuteDuration     = 366 * 24 * time.Hour
	muteRetries         = 3
)

// AlertRepo is the alert rule persistence.
type AlertRepo interface {
	Create(ctx context.Context, a *meta.AlertRule) error
	Get(ctx context.Context, id string) (meta.AlertRule, error)
	List(ctx context.Context) ([]meta.AlertRule, error)
	Update(ctx context.Context, a *meta.AlertRule) error
	Delete(ctx context.Context, id string) error
	History(ctx context.Context, ruleID string, limit int) ([]meta.AlertHistory, error)
}

// ChannelGetter checks that channels exist.
type ChannelGetter interface {
	Get(ctx context.Context, id string) (meta.Channel, error)
}

// RuleEvaluator evaluates a rule without side effects (POST /alerts/{id}/test).
type RuleEvaluator interface {
	Evaluate(ctx context.Context, r meta.AlertRule, now time.Time) alerting.Outcome
}

// Alerts manages alert rules (SPEC §14.1).
type Alerts struct {
	repo      AlertRepo
	channels  ChannelGetter
	evaluator RuleEvaluator
	audit     *Auditor
	now       func() time.Time
}

// NewAlerts returns the alert rule service.
func NewAlerts(repo AlertRepo, channels ChannelGetter, evaluator RuleEvaluator, audit *Auditor, now func() time.Time) *Alerts {
	return &Alerts{repo: repo, channels: channels, evaluator: evaluator, audit: audit, now: now}
}

// AlertInput is a create/update request.
type AlertInput struct {
	Name    string
	Enabled *bool
	Doc     json.RawMessage
	Version int
}

// List returns all rules.
func (s *Alerts) List(ctx context.Context) ([]meta.AlertRule, error) { return s.repo.List(ctx) }

// Get returns one rule.
func (s *Alerts) Get(ctx context.Context, id string) (meta.AlertRule, error) {
	return s.repo.Get(ctx, id)
}

// Create validates and stores a rule (enabled unless stated otherwise).
func (s *Alerts) Create(ctx context.Context, actor Actor, in AlertInput) (meta.AlertRule, error) {
	doc, err := s.validate(ctx, in)
	if err != nil {
		return meta.AlertRule{}, err
	}
	r := meta.AlertRule{Name: strings.TrimSpace(in.Name), Enabled: in.Enabled == nil || *in.Enabled, Doc: doc,
		State: string(alerting.StateOK)}
	if err := s.repo.Create(ctx, &r); err != nil {
		return meta.AlertRule{}, err
	}
	s.audit.Record(ctx, actor, ActionAlertCreate, r.ID, map[string]any{"name": r.Name})
	return r, nil
}

// Update replaces name, enabled flag and document (optimistic concurrency on Version).
func (s *Alerts) Update(ctx context.Context, actor Actor, id string, in AlertInput) (meta.AlertRule, error) {
	if in.Version < 1 {
		return meta.AlertRule{}, invalid("version", "is required")
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return meta.AlertRule{}, err
	}
	doc, err := s.validate(ctx, in)
	if err != nil {
		return meta.AlertRule{}, err
	}
	r.Name, r.Doc, r.Version = strings.TrimSpace(in.Name), doc, in.Version
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if err := s.repo.Update(ctx, &r); err != nil {
		return meta.AlertRule{}, err
	}
	s.audit.Record(ctx, actor, ActionAlertUpdate, id, map[string]any{"version": r.Version, "enabled": r.Enabled})
	return r, nil
}

// Delete removes a rule and its history.
func (s *Alerts) Delete(ctx context.Context, actor Actor, id string) error {
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.audit.Record(ctx, actor, ActionAlertDelete, id, nil)
	return nil
}

// Test evaluates a rule now without persisting state or notifying.
func (s *Alerts) Test(ctx context.Context, id string) (alerting.Outcome, error) {
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return alerting.Outcome{}, err
	}
	return s.evaluator.Evaluate(ctx, r, s.now().UTC()), nil
}

// Mute suppresses notifications until the given time (nil unmutes). The rule keeps being
// evaluated and its transitions recorded.
func (s *Alerts) Mute(ctx context.Context, actor Actor, id string, until *time.Time) (meta.AlertRule, error) {
	now := s.now()
	if until != nil && (!until.After(now) || until.Sub(now) > maxMuteDuration) {
		return meta.AlertRule{}, invalid("until", "must be in the future and within one year")
	}
	for attempt := 0; ; attempt++ {
		r, err := s.repo.Get(ctx, id)
		if err != nil {
			return meta.AlertRule{}, err
		}
		doc, err := alerting.ParseRule(r.Doc)
		if err != nil {
			return meta.AlertRule{}, err
		}
		doc.MuteUntil = nil
		if until != nil {
			u := until.UTC()
			doc.MuteUntil = &u
		}
		if r.Doc, err = json.Marshal(doc); err != nil {
			return meta.AlertRule{}, err
		}
		err = s.repo.Update(ctx, &r)
		if err == nil {
			s.audit.Record(ctx, actor, ActionAlertMute, id, map[string]any{"until": until})
			return r, nil
		}
		if !errors.Is(err, meta.ErrConflict) || attempt+1 >= muteRetries {
			return meta.AlertRule{}, err
		}
	}
}

// History returns the newest transitions (limit 0 ⇒ 100, at most 1000).
func (s *Alerts) History(ctx context.Context, id string, limit int) ([]meta.AlertHistory, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	return s.repo.History(ctx, id, min(limit, MaxHistoryLimit))
}

// validate checks name, document and channel references and returns the canonical doc.
func (s *Alerts) validate(ctx context.Context, in AlertInput) (json.RawMessage, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxNameLen {
		return nil, invalid("name", "is required (at most 128 characters)")
	}
	doc, err := alerting.ParseRule(in.Doc)
	if err != nil {
		return nil, err
	}
	for _, id := range doc.Channels {
		if _, err := s.channels.Get(ctx, id); err != nil {
			if errors.Is(err, meta.ErrNotFound) {
				return nil, invalid("doc.channels", fmt.Sprintf("unknown channel %q", id))
			}
			return nil, err
		}
	}
	return json.Marshal(doc)
}
