// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/ingest/seq"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// seqCheckpoint is the resumable position of a Seq import. Seq returns events newest first and
// pages with afterId (exclusive), so the import walks from the newest event in the range to the
// oldest.
type seqCheckpoint struct {
	SignalsDone bool   `json:"signalsDone,omitempty"`
	AfterID     string `json:"afterId,omitempty"`
	Done        bool   `json:"done,omitempty"`
}

// seqProperty is an EventPropertyPart of the Seq API.
type seqProperty struct {
	Name  string `json:"Name"`
	Value any    `json:"Value"`
}

// seqToken is a MessageTemplateTokenPart: literal Text or a property RawText.
type seqToken struct {
	Text         string `json:"Text"`
	PropertyName string `json:"PropertyName"`
	RawText      string `json:"RawText"`
}

// seqEvent is the EventEntity of the Seq API (github.com/datalust/seq-api).
type seqEvent struct {
	ID              string        `json:"Id"`
	Timestamp       string        `json:"Timestamp"`
	Properties      []seqProperty `json:"Properties"`
	Resource        []seqProperty `json:"Resource"`
	MessageTemplate []seqToken    `json:"MessageTemplateTokens"`
	EventType       string        `json:"EventType"`
	Level           string        `json:"Level"`
	Exception       string        `json:"Exception"`
	RenderedMessage string        `json:"RenderedMessage"`
	TraceID         string        `json:"TraceId"`
	SpanID          string        `json:"SpanId"`
}

// seqFilter is a DescriptiveFilterPart; seqSignal a SignalEntity.
type seqFilter struct {
	Filter          string `json:"Filter"`
	FilterNonStrict string `json:"FilterNonStrict"`
}

type seqSignal struct {
	ID      string      `json:"Id"`
	Title   string      `json:"Title"`
	Filters []seqFilter `json:"Filters"`
}

// SignalSink stores imported signals (enabled = the filter translated to TQL).
type SignalSink interface {
	ImportSignal(ctx context.Context, owner, name, tqlText string, enabled bool) error
}

// seqSource reads events (GET /api/events, id paging) and signals (GET /api/signals) through
// Seq's HTTP API, authenticated with X-Seq-ApiKey.
type seqSource struct {
	cfg     SeqConfig
	req     *requester
	signals SignalSink
	owner   string
	warn    func(string)
}

func newSeqSource(cfg SeqConfig, signals SignalSink, owner string, warn func(string)) (*seqSource, error) {
	h := http.Header{}
	if cfg.APIKey != "" {
		h.Set("X-Seq-ApiKey", cfg.APIKey)
	}
	req, err := newRequester(cfg.URL, Auth{Type: AuthNone}, "", h)
	if err != nil {
		return nil, err
	}
	return &seqSource{cfg: cfg, req: req, signals: signals, owner: owner, warn: warn}, nil
}

// Next implements Source.
func (s *seqSource) Next(ctx context.Context, raw json.RawMessage) (Page, error) {
	var cp seqCheckpoint
	if err := decodeCheckpoint(raw, &cp); err != nil {
		return Page{}, err
	}
	if s.cfg.ImportSignals && !cp.SignalsDone {
		if err := s.importSignals(ctx); err != nil {
			return Page{}, err
		}
		cp.SignalsDone = true
		return seqPage(nil, cp, !s.cfg.events())
	}
	if !s.cfg.events() || cp.Done {
		return seqPage(nil, cp, true)
	}
	var events []seqEvent
	if err := s.req.do(ctx, http.MethodGet, "/api/events?"+s.eventsQuery(cp.AfterID), nil, &events); err != nil {
		return Page{}, err
	}
	mapped := make([]model.Event, 0, len(events))
	for i := range events {
		mapped = append(mapped, s.toEvent(&events[i]))
	}
	if len(events) > 0 {
		cp.AfterID = events[len(events)-1].ID
	}
	cp.Done = len(events) < s.cfg.PageSize
	p, err := seqPage(mapped, cp, cp.Done)
	if len(events) > 0 {
		p.Key = events[0].ID + ".." + events[len(events)-1].ID
	}
	return p, err
}

func seqPage(events []model.Event, cp seqCheckpoint, done bool) (Page, error) {
	raw, err := json.Marshal(cp)
	if err != nil {
		return Page{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	return Page{Events: events, Checkpoint: raw, Done: done}, nil
}

func (s *seqSource) eventsQuery(afterID string) string {
	q := url.Values{"count": {strconv.Itoa(s.cfg.PageSize)}, "render": {"true"}}
	if afterID != "" {
		q.Set("afterId", afterID)
	}
	if s.cfg.From != nil {
		q.Set("fromDateUtc", s.cfg.From.UTC().Format(time.RFC3339Nano))
	}
	if s.cfg.To != nil {
		q.Set("toDateUtc", s.cfg.To.UTC().Format(time.RFC3339Nano))
	}
	if s.cfg.Filter != "" {
		q.Set("filter", s.cfg.Filter)
	}
	return q.Encode()
}

// toEvent rewrites a Seq event into CLEF and maps it like live Seq ingestion (SPEC §9.2).
func (s *seqSource) toEvent(e *seqEvent) model.Event {
	obj := map[string]any{}
	for _, p := range e.Resource {
		obj[clefKey(p.Name)] = p.Value
	}
	for _, p := range e.Properties {
		obj[clefKey(p.Name)] = p.Value
	}
	set := func(k, v string) {
		if v != "" {
			obj[k] = v
		}
	}
	set("@t", e.Timestamp)
	set("@mt", templateText(e.MessageTemplate))
	set("@m", e.RenderedMessage)
	set("@l", e.Level)
	set("@x", e.Exception)
	set("@tr", e.TraceID)
	set("@sp", e.SpanID)
	set("@i", strings.TrimPrefix(e.EventType, "$"))
	ev := seq.EventFromCLEF(obj)
	if s.cfg.Stream != "" {
		ev.Stream = s.cfg.Stream
	}
	return ev
}

// clefKey escapes property names that start with '@' (CLEF reserves them).
func clefKey(name string) string {
	if strings.HasPrefix(name, "@") {
		return "@" + name
	}
	return name
}

// templateText rebuilds the message template from its tokens; literal braces are doubled.
func templateText(tokens []seqToken) string {
	var b strings.Builder
	for _, t := range tokens {
		if t.PropertyName != "" || t.RawText != "" {
			b.WriteString(t.RawText)
			continue
		}
		b.WriteString(strings.NewReplacer("{", "{{", "}", "}}").Replace(t.Text))
	}
	return b.String()
}

// importSignals translates every shared signal (SPEC §9.9); untranslatable ones are imported
// disabled and reported as warnings.
func (s *seqSource) importSignals(ctx context.Context) error {
	var signals []seqSignal
	if err := s.req.do(ctx, http.MethodGet, "/api/signals?shared=true", nil, &signals); err != nil {
		return fmt.Errorf("list Seq signals: %w", err)
	}
	for _, sig := range signals {
		parts := make([]string, 0, len(sig.Filters))
		for _, f := range sig.Filters {
			if f.Filter != "" {
				parts = append(parts, "("+f.Filter+")")
			}
		}
		original := strings.Join(parts, " and ")
		if original == "" {
			original = "*" // a signal without filters matches every event
		}
		text, err := TranslateSeq(original)
		if err != nil {
			text = original // kept for manual review; disabled signals are never applied
			s.warn(fmt.Sprintf("Seq signal %q imported disabled: %v", sig.Title, err))
		}
		if err := s.signals.ImportSignal(ctx, s.owner, sig.Title, text, err == nil); err != nil {
			return fmt.Errorf("store signal %q: %w", sig.Title, err)
		}
	}
	return nil
}

// Close implements Source.
func (s *seqSource) Close(context.Context, bool) error { return nil }
