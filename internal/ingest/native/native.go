// SPDX-License-Identifier: Apache-2.0

// Package native implements the native JSON / NDJSON log endpoint (SPEC §9.5).
package native

import (
	"bytes"
	"errors"
	"net/http"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/ingest"
	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/normalize"
)

// Path is the native ingestion route.
const Path = "/api/v1/ingest/logs"

// MaxReportedErrors caps the per-item errors in a response.
const MaxReportedErrors = 20

// Handler serves the native endpoint.
type Handler struct{ d *ingest.Deps }

// New returns a native handler.
func New(d *ingest.Deps) *Handler { return &Handler{d: d} }

// Register adds the route to mux.
func (h *Handler) Register(mux *http.ServeMux) { mux.HandleFunc("POST "+Path, h.serve) }

// ItemError reports why one item was rejected.
type ItemError struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// Response is the 202 body.
type Response struct {
	Accepted int         `json:"accepted"`
	Rejected int         `json:"rejected"`
	Errors   []ItemError `json:"errors"`
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	req, err := h.d.Authenticate(r, auth.ProtocolNative, false)
	if err != nil {
		writeError(w, err)
		return
	}
	body, err := ingest.ReadBody(r, h.d.Limits, false)
	if err != nil {
		writeError(w, err)
		return
	}
	objs, itemErrs, err := Parse(body)
	if err != nil {
		writeError(w, err)
		return
	}
	events := make([]model.Event, 0, len(objs))
	for _, o := range objs {
		events = append(events, normalize.EventFromFields(normalize.Promote(o, normalize.Options{CLEF: true}), model.ProtocolNative))
	}
	if err := h.d.CheckRate(req, len(events)); err != nil {
		writeError(w, err)
		return
	}
	kept, _ := h.d.PrepareEvents(req, events)
	if err := h.d.Events.EnqueueEvents(r.Context(), kept); err != nil {
		writeError(w, err)
		return
	}
	h.d.Accepted(req, model.SourceLogs, len(kept))
	resp := Response{Accepted: len(kept), Rejected: len(itemErrs), Errors: itemErrs}
	if len(resp.Errors) > MaxReportedErrors {
		resp.Errors = resp.Errors[:MaxReportedErrors]
	}
	if resp.Errors == nil {
		resp.Errors = []ItemError{}
	}
	ingest.WriteJSON(w, http.StatusAccepted, resp)
}

// Parse accepts a JSON object, a JSON array of objects or NDJSON. Items that are not
// objects (or invalid NDJSON lines) are reported as item errors.
func Parse(body []byte) ([]map[string]any, []ItemError, error) {
	trimmed := bytes.TrimSpace(body)
	switch {
	case len(trimmed) == 0:
		return nil, nil, nil
	case trimmed[0] == '[':
		return parseArray(trimmed)
	case trimmed[0] == '{':
		v, err := jsonx.Decode(trimmed, jsonx.MaxDepth)
		if err == nil {
			return []map[string]any{v.(map[string]any)}, nil, nil
		}
		if errors.Is(err, jsonx.ErrTooDeep) {
			return nil, nil, badRequest(err.Error())
		}
		return parseNDJSON(trimmed)
	default:
		return nil, nil, badRequest("body must be a JSON object, an array of objects or NDJSON")
	}
}

func parseArray(body []byte) ([]map[string]any, []ItemError, error) {
	v, err := jsonx.Decode(body, jsonx.MaxDepth)
	if err != nil {
		return nil, nil, badRequest(err.Error())
	}
	arr, _ := v.([]any)
	var objs []map[string]any
	var errs []ItemError
	for i, item := range arr {
		if o, ok := item.(map[string]any); ok {
			objs = append(objs, o)
			continue
		}
		errs = append(errs, ItemError{Index: i, Reason: "item is not a JSON object"})
	}
	return objs, errs, nil
}

func parseNDJSON(body []byte) ([]map[string]any, []ItemError, error) {
	var objs []map[string]any
	var errs []ItemError
	for i, l := range ingest.SplitLines(body) {
		o, err := jsonx.DecodeObject(l.Data, jsonx.MaxDepth)
		if err != nil {
			errs = append(errs, ItemError{Index: i, Reason: err.Error()})
			continue
		}
		objs = append(objs, o)
	}
	return objs, errs, nil
}

func badRequest(msg string) error { return &requestError{msg: msg} }

type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }
func (e *requestError) Unwrap() error { return ingest.ErrBadRequest }

// writeError writes the OpenTidemark API error shape (SPEC §11.1).
func writeError(w http.ResponseWriter, err error) {
	ingest.SetRetryHeaders(w, err)
	status := ingest.Status(err)
	ingest.WriteJSON(w, status, map[string]any{"error": map[string]string{"code": code(status), "message": ingest.PublicMessage(err)}})
}

func code(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "backpressure"
	case http.StatusUnsupportedMediaType:
		return "bad_request"
	default:
		return "internal"
	}
}
