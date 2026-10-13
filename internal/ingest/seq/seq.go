// SPDX-License-Identifier: Apache-2.0

// Package seq implements the Seq-compatible ingestion endpoints (SPEC §9.2): CLEF and the
// legacy JSON format, plus the GET /api probe.
package seq

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/normalize"
)

// CLEFContentType is the CLEF media type used by Serilog's Seq sink.
const CLEFContentType = "application/vnd.serilog.clef"

// Handler serves the Seq endpoints.
type Handler struct {
	d *ingest.Deps
}

// New returns a Seq handler.
func New(d *ingest.Deps) *Handler { return &Handler{d: d} }

// Register adds the Seq routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/events/raw", h.raw)
	mux.HandleFunc("POST /ingest/clef", h.clef)
	mux.HandleFunc("GET /api", h.probe)
	mux.HandleFunc("GET /api/{$}", h.probe)
}

func (h *Handler) probe(w http.ResponseWriter, _ *http.Request) {
	ingest.WriteJSON(w, http.StatusOK, map[string]string{"Product": "Tidemark", "Version": h.d.Version})
}

func (h *Handler) raw(w http.ResponseWriter, r *http.Request) {
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	_, clefQuery := r.URL.Query()["clef"]
	h.ingest(w, r, strings.HasPrefix(ct, CLEFContentType) || clefQuery)
}

func (h *Handler) clef(w http.ResponseWriter, r *http.Request) { h.ingest(w, r, true) }

func (h *Handler) ingest(w http.ResponseWriter, r *http.Request, clef bool) {
	req, err := h.d.Authenticate(r, auth.ProtocolSeq, true)
	if err != nil {
		writeError(w, err)
		return
	}
	body, err := ingest.ReadBody(r, h.d.Limits, false)
	if err != nil {
		writeError(w, err)
		return
	}
	var events []model.Event
	if clef {
		events, err = ParseCLEF(body)
	} else {
		events, err = ParseLegacy(body)
	}
	if err != nil {
		writeError(w, fmt.Errorf("%w: %w", ingest.ErrBadRequest, err))
		return
	}
	if err := h.d.CheckRate(req, len(events)); err != nil {
		writeError(w, err)
		return
	}
	events, _ = h.d.PrepareEvents(req, events)
	if err := h.d.Events.EnqueueEvents(r.Context(), events); err != nil {
		writeError(w, err)
		return
	}
	h.d.Accepted(req, model.SourceLogs, len(events))
	ingest.WriteJSON(w, http.StatusCreated, map[string]any{"MinimumLevelAccepted": minimumLevel(req.Conn)})
}

func minimumLevel(c auth.Connection) any {
	if c.MinimumLevel == nil {
		return nil
	}
	return c.MinimumLevel.String()
}

// errorBody is the Seq error shape.
type errorBody struct {
	Error string `json:"Error"`
}

func writeError(w http.ResponseWriter, err error) {
	ingest.SetRetryHeaders(w, err)
	ingest.WriteJSON(w, ingest.Status(err), errorBody{Error: publicSeqMessage(err)})
}

func publicSeqMessage(err error) string {
	var le *LineError
	if errors.As(err, &le) {
		return le.Error()
	}
	return ingest.PublicMessage(err)
}

// LineError reports the first invalid line when every line of a batch is invalid.
type LineError struct {
	Line   int
	Reason string
}

func (e *LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Reason) }

// ParseCLEF parses newline-delimited CLEF. Invalid lines are skipped; if every line is
// invalid the first error is returned.
func ParseCLEF(body []byte) ([]model.Event, error) {
	lines := ingest.SplitLines(body)
	events := make([]model.Event, 0, len(lines))
	var first *LineError
	for _, l := range lines {
		obj, err := jsonx.DecodeObject(l.Data, jsonx.MaxDepth)
		if err != nil {
			if first == nil {
				first = &LineError{Line: l.Number, Reason: err.Error()}
			}
			continue
		}
		events = append(events, EventFromCLEF(obj))
	}
	if len(events) == 0 && first != nil {
		return nil, first
	}
	return events, nil
}

// EventFromCLEF maps one decoded CLEF object (SPEC §9.2); the importer uses it for CLEF files and
// Seq events (SPEC §9.9).
func EventFromCLEF(obj map[string]any) model.Event {
	f := normalize.Promote(obj, normalize.Options{CLEF: true})
	if f.FallbackUsed {
		f.Message = "" // CLEF events without @m/@mt keep an empty message
	}
	return normalize.EventFromFields(f, model.ProtocolSeq)
}

// ParseLegacy parses {"Events":[…]}. Invalid elements are skipped; if all are invalid the
// first error is returned.
func ParseLegacy(body []byte) ([]model.Event, error) {
	root, err := jsonx.DecodeObject(body, jsonx.MaxDepth)
	if err != nil {
		return nil, &LineError{Line: 1, Reason: err.Error()}
	}
	raw, ok := root["Events"].([]any)
	if !ok {
		return nil, &LineError{Line: 1, Reason: `expected {"Events":[…]}`}
	}
	events := make([]model.Event, 0, len(raw))
	var first *LineError
	for i, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			if first == nil {
				first = &LineError{Line: i + 1, Reason: "event is not an object"}
			}
			continue
		}
		events = append(events, eventFromLegacy(obj))
	}
	if len(events) == 0 && first != nil {
		return nil, first
	}
	return events, nil
}

// eventFromLegacy maps a legacy event by rewriting it into CLEF keys first.
func eventFromLegacy(obj map[string]any) model.Event {
	clef := map[string]any{}
	if props, ok := obj["Properties"].(map[string]any); ok {
		for k, v := range props {
			clef[k] = v
		}
	}
	for src, dst := range map[string]string{"Timestamp": "@t", "Level": "@l", "MessageTemplate": "@mt",
		"RenderedMessage": "@m", "Exception": "@x"} {
		if v, ok := obj[src]; ok && v != nil && v != "" {
			clef[dst] = v
		}
	}
	return EventFromCLEF(clef)
}
