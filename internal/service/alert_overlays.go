// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// Overlay limits (SPEC §14.4).
const (
	MaxOverlays       = 5
	MaxOverlayRuleIDs = 5
	maxOverlayIDLen   = 64
)

// OverlayRequest asks for the alert rules to draw on a chart (SPEC §14.4): the rules whose
// query equals the chart's (ignoring span), the rules named in RuleIDs, and a draft rule.
type OverlayRequest struct {
	Query   string
	Range   Range
	RuleIDs []string
	Draft   *OverlayDraft
}

// OverlayDraft is an unsaved rule document (the rule editor's preview) and its streams.
type OverlayDraft struct {
	Doc     json.RawMessage
	Streams []string
}

// Overlay is one rule drawn on a chart: its condition, and for deviation rules the rolling
// band per group (or Error when the band cannot be computed for this range).
type Overlay struct {
	RuleID    string
	Name      string
	Draft     bool
	Condition alerting.Condition
	Window    time.Duration // the rule's window: the width of a band point
	Bands     []alerting.GroupBand
	Error     string
}

// Overlays returns at most MaxOverlays rules to draw on a chart: the draft first, then the
// rules named in RuleIDs, then the visible rules whose query matches the chart's query, each
// by name. Unknown or invisible ids are ignored. A deviation rule's band reads the rule's
// streams (the draft's streams for a draft).
func (s *Alerts) Overlays(ctx context.Context, p auth.Principal, req OverlayRequest) ([]Overlay, error) {
	if err := req.Range.valid(); err != nil {
		return nil, err
	}
	if len(req.RuleIDs) > MaxOverlayRuleIDs {
		return nil, invalid("rules", "at most 5 rule ids")
	}
	scope, err := s.d.Access.Scope(ctx, p)
	if err != nil {
		return nil, err
	}
	var out []Overlay
	if req.Draft != nil {
		o, err := s.draftOverlay(ctx, req, scope)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	rules, err := s.List(ctx, p)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rules, func(a, b meta.AlertRule) int { return strings.Compare(a.Name, b.Name) })
	canon, chartOK := alerting.CanonicalQuery(req.Query)
	for _, pass := range []bool{true, false} { // named rules first, then query matches
		for _, r := range rules {
			if len(out) >= MaxOverlays {
				return out, nil
			}
			doc, err := alerting.ParseRule(r.Doc)
			if err != nil || slices.ContainsFunc(out, func(o Overlay) bool { return o.RuleID == r.ID }) {
				continue
			}
			if pass && !slices.Contains(req.RuleIDs, r.ID) {
				continue
			}
			if !pass {
				rq, ok := alerting.CanonicalQuery(doc.Query)
				if !chartOK || !ok || rq != canon {
					continue
				}
			}
			out = append(out, s.overlay(ctx, Overlay{RuleID: r.ID, Name: r.Name}, doc, alerting.RuleScope(r.Streams), req.Range))
		}
	}
	return out, nil
}

func (s *Alerts) draftOverlay(ctx context.Context, req OverlayRequest, scope model.StreamScope) (Overlay, error) {
	streams, err := ruleStreams(req.Draft.Streams, scope)
	if err != nil {
		return Overlay{}, err
	}
	doc, err := alerting.ParseRule(req.Draft.Doc)
	if err != nil {
		return Overlay{}, err
	}
	return s.overlay(ctx, Overlay{Name: "draft", Draft: true}, doc, alerting.RuleScope(streams), req.Range), nil
}

func (s *Alerts) overlay(ctx context.Context, o Overlay, doc *alerting.RuleDoc, streams model.StreamScope, rng Range) Overlay {
	o.Condition, o.Window = doc.Condition, time.Duration(doc.Window)
	if !doc.Condition.IsDeviation() {
		return o
	}
	bands, err := s.d.Evaluator.Bands(ctx, doc, streams, rng.From, rng.To)
	if err != nil {
		o.Error = err.Error()
		return o
	}
	o.Bands = bands
	return o
}

// ValidOverlayIDs reports whether ids can name rules of a panel or request.
func ValidOverlayIDs(ids []string) bool {
	if len(ids) > MaxOverlayRuleIDs {
		return false
	}
	for i, id := range ids {
		if id == "" || len(id) > maxOverlayIDLen || slices.Contains(ids[:i], id) {
			return false
		}
	}
	return true
}
