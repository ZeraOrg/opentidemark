// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/httpx"
	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// Local sentinel errors.
var (
	errNotFound = errors.New("not found")
	errCSRF     = fmt.Errorf("%w: CSRF token missing or invalid", auth.ErrForbidden)
)

// badRequest is an input error with an optional field.
type badRequest struct {
	msg     string
	details map[string]any
}

func (e *badRequest) Error() string { return e.msg }

func errBadRequest(format string, args ...any) error {
	return &badRequest{msg: fmt.Sprintf(format, args...)}
}

// errorBody is the error envelope (SPEC §11.1).
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// classify maps an error to status, code, client message and details.
func classify(err error) (int, string, string, map[string]any) {
	var br *badRequest
	var ve *service.ValidationError
	var rl *rateLimited
	var mbe *http.MaxBytesError
	if status, code, msg, details, ok := classifyDomain(err); ok {
		return status, code, msg, details
	}
	var qe *tql.Error
	switch {
	case errors.As(err, &qe):
		return http.StatusBadRequest, "invalid_query", qe.Msg, map[string]any{"pos": qe.Pos, "len": qe.Len, "hint": qe.Hint}
	case errors.Is(err, tql.ErrInvalidOptions):
		return http.StatusBadRequest, "bad_request", "invalid query options", nil
	case errors.Is(err, service.ErrTooManyTails):
		return http.StatusTooManyRequests, "rate_limited", err.Error(), nil
	case errors.As(err, &br):
		return http.StatusBadRequest, "bad_request", br.msg, br.details
	case errors.As(err, &ve):
		return http.StatusBadRequest, "bad_request", ve.Error(), map[string]any{"field": ve.Field}
	case errors.As(err, &mbe):
		return http.StatusRequestEntityTooLarge, "payload_too_large", "request body too large", nil
	case errors.Is(err, service.ErrInvalidPassword):
		return http.StatusBadRequest, "bad_request", err.Error(), map[string]any{"field": "currentPassword"}
	case errors.Is(err, auth.ErrInvalidCredentials):
		return http.StatusUnauthorized, "unauthorized", auth.ErrInvalidCredentials.Error(), nil
	case errors.Is(err, auth.ErrUnauthorized):
		return http.StatusUnauthorized, "unauthorized", "authentication required", nil
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, service.ErrForbidden):
		return http.StatusForbidden, "forbidden", forbiddenMessage(err), nil
	case errors.Is(err, service.ErrLastAdmin):
		return http.StatusConflict, "conflict", err.Error(), nil
	case errors.Is(err, errNotFound), errors.Is(err, meta.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound, "not_found", "not found", nil
	case errors.Is(err, meta.ErrConflict):
		return http.StatusConflict, "conflict", "conflict", nil
	case errors.As(err, &rl), errors.Is(err, auth.ErrRateLimited):
		return http.StatusTooManyRequests, "rate_limited", "too many requests", nil
	default:
		return http.StatusInternalServerError, "internal", "internal error", nil
	}
}

func forbiddenMessage(err error) string {
	if errors.Is(err, errCSRF) {
		return "CSRF token missing or invalid"
	}
	return "forbidden"
}

func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	status, code, msg, details := classify(err)
	if status == http.StatusInternalServerError {
		logger.Error("api error", "err", err, "path", r.URL.Path, "request_id", httpx.RequestIDFrom(r.Context()))
	}
	var rl *rateLimited
	if errors.As(err, &rl) {
		w.Header().Set("Retry-After", rl.seconds())
	}
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: msg, Details: details}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"error":{"code":"internal","message":"internal error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // the client may have disconnected; nothing to do
}

// decode reads a JSON body into v, rejecting unknown fields, trailing data and deep nesting.
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return errBadRequest("could not read request body")
	}
	if err := jsonx.CheckDepth(body, jsonx.MaxDepth); err != nil {
		return errBadRequest("request body is nested too deeply")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errBadRequest("invalid JSON body: %s", err.Error())
	}
	if dec.More() {
		return errBadRequest("invalid JSON body: unexpected data after the object")
	}
	return nil
}
