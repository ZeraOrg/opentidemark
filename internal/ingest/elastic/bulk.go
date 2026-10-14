// SPDX-License-Identifier: Apache-2.0

package elastic

import (
	"fmt"
	"net/http"

	"github.com/ZeraOrg/opentidemark/internal/ingest"
	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/normalize"
)

// Bulk actions.
const (
	actionIndex  = "index"
	actionCreate = "create"
	actionUpdate = "update"
	actionDelete = "delete"
)

// item is one bulk operation and its outcome.
type item struct {
	action string
	index  string
	id     string
	event  *model.Event
	errTyp string
	errMsg string
	status int
}

// BulkError is a request-level bulk parse failure.
type BulkError struct {
	Line   int
	Reason string
}

func (e *BulkError) Error() string { return fmt.Sprintf("line [%d]: %s", e.Line, e.Reason) }

// ParseBulk parses an NDJSON bulk body. defaultIndex applies to actions without _index.
func ParseBulk(body []byte, defaultIndex string) ([]item, error) {
	lines := ingest.SplitLines(body)
	var items []item
	for i := 0; i < len(lines); i++ {
		action, meta, err := parseAction(lines[i])
		if err != nil {
			return nil, err
		}
		it := item{action: action, index: stringField(meta, "_index", defaultIndex), id: stringField(meta, "_id", "")}
		switch action {
		case actionIndex, actionCreate:
			if i+1 >= len(lines) {
				return nil, &BulkError{Line: lines[i].Number, Reason: "missing source line"}
			}
			i++
			it.event, it.errMsg = parseSource(lines[i].Data, it.id)
			if it.errMsg != "" {
				it.status, it.errTyp = http.StatusBadRequest, "mapper_parsing_exception"
			}
		case actionUpdate:
			i++ // skip the partial document line
			it.status, it.errTyp, it.errMsg = http.StatusBadRequest, "action_not_supported", "update is not supported by OpenTidemark"
		case actionDelete:
			it.status, it.errTyp, it.errMsg = http.StatusBadRequest, "action_not_supported", "delete is not supported by OpenTidemark"
		}
		items = append(items, it)
	}
	return items, nil
}

func parseAction(l ingest.Line) (string, map[string]any, error) {
	obj, err := jsonx.DecodeObject(l.Data, jsonx.MaxDepth)
	if err != nil || len(obj) != 1 {
		return "", nil, &BulkError{Line: l.Number, Reason: "malformed action/metadata line"}
	}
	for action, v := range obj {
		meta, _ := v.(map[string]any)
		switch action {
		case actionIndex, actionCreate, actionUpdate, actionDelete:
			return action, meta, nil
		}
		return "", nil, &BulkError{Line: l.Number, Reason: fmt.Sprintf("unknown action [%s]", action)}
	}
	return "", nil, &BulkError{Line: l.Number, Reason: "malformed action/metadata line"}
}

func parseSource(data []byte, id string) (*model.Event, string) {
	obj, err := jsonx.DecodeObject(data, jsonx.MaxDepth)
	if err != nil {
		return nil, "failed to parse document: " + err.Error()
	}
	ev := EventFromDoc(obj, id)
	return &ev, ""
}

// EventFromDoc maps an Elasticsearch document with the §9.6 heuristics, as the live _bulk API
// does; a non-empty id is kept as property _id (also used by the importer, SPEC §9.9).
func EventFromDoc(obj map[string]any, id string) model.Event {
	ev := normalize.EventFromFields(normalize.Promote(obj, normalize.Options{}), model.ProtocolElastic)
	if id != "" {
		ev.Properties["_id"] = id
	}
	return ev
}

func stringField(m map[string]any, key, def string) string {
	if s, ok := m[key].(string); ok && s != "" {
		return s
	}
	return def
}

func (h *Handler) bulk(w http.ResponseWriter, r *http.Request, req *ingest.Request, urlIndex string) {
	start := h.d.Now()
	body, err := ingest.ReadBody(r, h.d.Limits, false)
	if err != nil {
		h.fail(w, err)
		return
	}
	items, err := ParseBulk(body, urlIndex)
	if err != nil {
		writeError(w, http.StatusBadRequest, "illegal_argument_exception", err.Error())
		return
	}
	var events []model.Event
	for i := range items {
		if it := &items[i]; it.event != nil {
			it.event.Stream = StreamFromIndex(it.index)
			events = append(events, *it.event)
		}
	}
	if err := h.d.CheckRate(req, len(events)); err != nil {
		h.fail(w, err)
		return
	}
	kept, _ := h.d.PrepareEvents(req, events)
	assignIDs(items, events)
	if err := h.d.Events.EnqueueEvents(r.Context(), kept); err != nil {
		h.fail(w, err)
		return
	}
	h.d.Accepted(req, model.SourceLogs, len(kept))
	ingest.WriteJSON(w, http.StatusOK, bulkResponse(items, tookMillis(start, h.d.Now())))
}

// assignIDs gives accepted items without an explicit _id the generated event id. events is
// the slice before filtering, in item order; filtered (dropped) events still report success.
func assignIDs(items []item, events []model.Event) {
	j := 0
	for i := range items {
		if items[i].event == nil {
			continue
		}
		if items[i].id == "" && j < len(events) {
			items[i].id = events[j].ID.String()
		}
		j++
	}
}

func bulkResponse(items []item, took int64) map[string]any {
	out := make([]map[string]any, 0, len(items))
	hasErrors := false
	for _, it := range items {
		res := map[string]any{"_index": it.index, "_id": it.id}
		if it.errTyp != "" {
			hasErrors = true
			res["status"] = it.status
			res["error"] = map[string]string{"type": it.errTyp, "reason": it.errMsg}
		} else {
			res["_version"], res["result"], res["status"] = 1, "created", http.StatusCreated
		}
		out = append(out, map[string]any{it.action: res})
	}
	return map[string]any{"took": took, "errors": hasErrors, "items": out}
}

func (h *Handler) single(w http.ResponseWriter, r *http.Request, req *ingest.Request, index, id string) {
	body, err := ingest.ReadBody(r, h.d.Limits, false)
	if err != nil {
		h.fail(w, err)
		return
	}
	obj, err := jsonx.DecodeObject(body, jsonx.MaxDepth)
	if err != nil {
		writeError(w, http.StatusBadRequest, "mapper_parsing_exception", "failed to parse document: "+err.Error())
		return
	}
	if err := h.d.CheckRate(req, 1); err != nil {
		h.fail(w, err)
		return
	}
	ev := EventFromDoc(obj, id)
	ev.Stream = StreamFromIndex(index)
	events := []model.Event{ev}
	kept, _ := h.d.PrepareEvents(req, events)
	if id == "" {
		id = events[0].ID.String()
	}
	if err := h.d.Events.EnqueueEvents(r.Context(), kept); err != nil {
		h.fail(w, err)
		return
	}
	h.d.Accepted(req, model.SourceLogs, len(kept))
	ingest.WriteJSON(w, http.StatusCreated, map[string]any{"_index": index, "_id": id, "result": "created", "_version": 1})
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	ingest.SetRetryHeaders(w, err)
	status := ingest.Status(err)
	typ := "illegal_argument_exception"
	switch status {
	case http.StatusServiceUnavailable:
		typ = "es_rejected_execution_exception"
	case http.StatusTooManyRequests:
		typ = "circuit_breaking_exception"
	case http.StatusRequestEntityTooLarge:
		typ = "content_too_long_exception"
	}
	writeError(w, status, typ, ingest.PublicMessage(err))
}
