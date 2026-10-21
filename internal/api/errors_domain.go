// SPDX-License-Identifier: Apache-2.0

package api

import (
	"errors"
	"net/http"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/importer"
	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/tql"
	"github.com/ZeraOrg/opentidemark/internal/update"
)

// classifyDomain maps errors of the dashboards, alerting and stream services (SPEC §11.1).
func classifyDomain(err error) (int, string, string, map[string]any, bool) {
	var qe *tql.Error
	var pe *service.PanelError
	var re *alerting.RuleError
	var ue *update.UnavailableError
	var are *update.ApplyRefusedError
	switch {
	case errors.As(err, &qe):
		details := map[string]any{"pos": qe.Pos, "len": qe.Len, "hint": qe.Hint}
		if errors.As(err, &pe) {
			details["panelId"] = pe.PanelID
		}
		return http.StatusBadRequest, "invalid_query", qe.Msg, details, true
	case errors.As(err, &re):
		return http.StatusBadRequest, "bad_request", re.Error(), map[string]any{"field": "doc." + re.Field}, true
	case errors.Is(err, service.ErrChannelInUse):
		return http.StatusConflict, "conflict", err.Error(), nil, true
	case errors.Is(err, service.ErrConfirmMismatch):
		return http.StatusBadRequest, "bad_request", err.Error(), map[string]any{"field": "confirm"}, true
	case errors.Is(err, service.ErrProtectedStream), errors.Is(err, service.ErrPurgeBusy),
		errors.Is(err, importer.ErrJobFinished):
		return http.StatusConflict, "conflict", err.Error(), nil, true
	case errors.As(err, &ue):
		return http.StatusConflict, "conflict", "the update check is unavailable", map[string]any{"reason": ue.Reason}, true
	case errors.As(err, &are):
		return http.StatusConflict, "conflict", "the update cannot be applied", map[string]any{"reason": are.Reason}, true
	case errors.Is(err, update.ErrVersionMismatch):
		return http.StatusBadRequest, "bad_request", err.Error(), map[string]any{"field": "version"}, true
	case errors.Is(err, importer.ErrFileTooLarge):
		return http.StatusRequestEntityTooLarge, "payload_too_large", err.Error(), nil, true
	default:
		return 0, "", "", nil, false
	}
}
