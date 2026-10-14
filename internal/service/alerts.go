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
	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
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

// AlertsDeps are the collaborators of Alerts.
type AlertsDeps struct {
	Repo      AlertRepo
	Channels  ChannelGetter
	Evaluator RuleEvaluator
	Access    ScopeResolver // the caller's stream scope (SPEC §14.1, §15.10)
	Audit     *Auditor
	Now       func() time.Time
}

// Alerts manages alert rules (SPEC §14.1). A rule reads its stored streams. A caller sees
// and manages only the rules whose streams all lie within the caller's scope (a rule over
// every stream only when the caller sees every stream); other rules are not found.
type Alerts struct{ d AlertsDeps }

// NewAlerts returns the alert rule service.
func NewAlerts(d AlertsDeps) *Alerts { return &Alerts{d: d} }

// AlertInput is a create/update request. Streams nil means every stream, which only callers
// who see every stream may choose; otherwise it must be a non-empty subset of their scope.
type AlertInput struct {
	Name    string
	Enabled *bool
	Doc     json.RawMessage
	Streams []string
	Version int
}

// List returns the rules visible to p.
func (s *Alerts) List(ctx context.Context, p auth.Principal) ([]meta.AlertRule, error) {
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return nil, err
	}
	rules, err := s.d.Repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]meta.AlertRule, 0, len(rules))
	for _, r := range rules {
		if alerting.RuleScope(r.Streams).Subset(scope) {
			out = append(out, r)
		}
	}
	return out, nil
}

// load returns a rule visible to p (meta.ErrNotFound otherwise) together with p's scope.
func (s *Alerts) load(ctx context.Context, p auth.Principal, id string) (meta.AlertRule, model.StreamScope, error) {
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return meta.AlertRule{}, model.StreamScope{}, err
	}
	r, err := s.d.Repo.Get(ctx, id)
	if err != nil {
		return meta.AlertRule{}, model.StreamScope{}, err
	}
	if !alerting.RuleScope(r.Streams).Subset(scope) {
		return meta.AlertRule{}, model.StreamScope{}, meta.ErrNotFound
	}
	return r, scope, nil
}

// Get returns one rule visible to p.
func (s *Alerts) Get(ctx context.Context, p auth.Principal, id string) (meta.AlertRule, error) {
	r, _, err := s.load(ctx, p, id)
	return r, err
}

// Create validates and stores a rule (enabled unless stated otherwise).
func (s *Alerts) Create(ctx context.Context, actor Actor, p auth.Principal, in AlertInput) (meta.AlertRule, error) {
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return meta.AlertRule{}, err
	}
	doc, streams, err := s.validate(ctx, in, scope)
	if err != nil {
		return meta.AlertRule{}, err
	}
	r := meta.AlertRule{Name: strings.TrimSpace(in.Name), Enabled: in.Enabled == nil || *in.Enabled, Doc: doc,
		Streams: streams, State: string(alerting.StateOK)}
	if err := s.d.Repo.Create(ctx, &r); err != nil {
		return meta.AlertRule{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionAlertCreate, r.ID, map[string]any{"name": r.Name, "streams": r.Streams})
	return r, nil
}

// Update replaces name, enabled flag, document and streams of a rule visible to p
// (optimistic concurrency on Version).
func (s *Alerts) Update(ctx context.Context, actor Actor, p auth.Principal, id string, in AlertInput) (meta.AlertRule, error) {
	if in.Version < 1 {
		return meta.AlertRule{}, invalid("version", "is required")
	}
	r, scope, err := s.load(ctx, p, id)
	if err != nil {
		return meta.AlertRule{}, err
	}
	doc, streams, err := s.validate(ctx, in, scope)
	if err != nil {
		return meta.AlertRule{}, err
	}
	r.Name, r.Doc, r.Streams, r.Version = strings.TrimSpace(in.Name), doc, streams, in.Version
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if err := s.d.Repo.Update(ctx, &r); err != nil {
		return meta.AlertRule{}, err
	}
	s.d.Audit.Record(ctx, actor, ActionAlertUpdate, id, map[string]any{"version": r.Version, "enabled": r.Enabled,
		"streams": r.Streams})
	return r, nil
}

// Delete removes a rule visible to p and its history.
func (s *Alerts) Delete(ctx context.Context, actor Actor, p auth.Principal, id string) error {
	if _, _, err := s.load(ctx, p, id); err != nil {
		return err
	}
	if err := s.d.Repo.Delete(ctx, id); err != nil {
		return err
	}
	s.d.Audit.Record(ctx, actor, ActionAlertDelete, id, nil)
	return nil
}

// Test evaluates a rule visible to p now without persisting state or notifying.
func (s *Alerts) Test(ctx context.Context, p auth.Principal, id string) (alerting.Outcome, error) {
	r, _, err := s.load(ctx, p, id)
	if err != nil {
		return alerting.Outcome{}, err
	}
	return s.d.Evaluator.Evaluate(ctx, r, s.d.Now().UTC()), nil
}

// Mute suppresses the notifications of a rule visible to p until the given time (nil
// unmutes). The rule keeps being evaluated and its transitions recorded.
func (s *Alerts) Mute(ctx context.Context, actor Actor, p auth.Principal, id string, until *time.Time) (meta.AlertRule, error) {
	now := s.d.Now()
	if until != nil && (!until.After(now) || until.Sub(now) > maxMuteDuration) {
		return meta.AlertRule{}, invalid("until", "must be in the future and within one year")
	}
	for attempt := 0; ; attempt++ {
		r, _, err := s.load(ctx, p, id)
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
		err = s.d.Repo.Update(ctx, &r)
		if err == nil {
			s.d.Audit.Record(ctx, actor, ActionAlertMute, id, map[string]any{"until": until})
			return r, nil
		}
		if !errors.Is(err, meta.ErrConflict) || attempt+1 >= muteRetries {
			return meta.AlertRule{}, err
		}
	}
}

// History returns the newest transitions of a rule visible to p (limit 0 ⇒ 100, at most
// 1000).
func (s *Alerts) History(ctx context.Context, p auth.Principal, id string, limit int) ([]meta.AlertHistory, error) {
	if _, _, err := s.load(ctx, p, id); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	return s.d.Repo.History(ctx, id, min(limit, MaxHistoryLimit))
}

// validate checks name, streams, document and channel references and returns the canonical
// doc and the streams to store (sorted; nil for every stream).
func (s *Alerts) validate(ctx context.Context, in AlertInput, scope model.StreamScope) (json.RawMessage, []string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxNameLen {
		return nil, nil, invalid("name", "is required (at most 128 characters)")
	}
	streams, err := ruleStreams(in.Streams, scope)
	if err != nil {
		return nil, nil, err
	}
	doc, err := alerting.ParseRule(in.Doc)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range doc.Channels {
		if _, err := s.d.Channels.Get(ctx, id); err != nil {
			if errors.Is(err, meta.ErrNotFound) {
				return nil, nil, invalid("doc.channels", fmt.Sprintf("unknown channel %q", id))
			}
			return nil, nil, err
		}
	}
	raw, err := json.Marshal(doc)
	return raw, streams, err
}

// ruleStreams checks the streams of a rule against the editor's scope: nil (every stream)
// needs a scope of every stream; a list must be non-empty, valid and within the scope.
func ruleStreams(streams []string, scope model.StreamScope) ([]string, error) {
	if streams == nil {
		if !scope.All() {
			return nil, invalid("streams", "choose the streams the rule reads")
		}
		return nil, nil
	}
	if len(streams) == 0 || len(streams) > MaxSelectedStreams {
		return nil, invalid("streams", fmt.Sprintf("1 to %d streams, or null for every stream", MaxSelectedStreams))
	}
	for _, n := range streams {
		if !model.ValidStreamName(n) {
			return nil, invalid("streams", fmt.Sprintf("%q is not a valid stream name", n))
		}
	}
	sc := model.OnlyStreams(streams...)
	if !sc.Subset(scope) {
		return nil, invalid("streams", "includes streams you cannot see")
	}
	return sc.Names(), nil
}
