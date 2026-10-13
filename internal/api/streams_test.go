// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
)

func TestStreamsFlow(t *testing.T) {
	e := newEnv(t)
	admin, viewer := e.sessions["admin"], e.sessions["viewer"]
	now := time.Now().UTC()
	e.admin.stats = []store.StreamStats{{Stream: "api", Source: model.SourceLogs, Rows: 10, CompressedBytes: 100,
		UncompressedBytes: 400, Estimated: true, Oldest: now, Newest: now}}

	w := e.do(admin, "POST", "/streams", map[string]any{"name": "api", "retentionDays": 90,
		"levelRules": []map[string]any{{"levels": []string{"debug"}, "days": 3}}})
	if w.Code != http.StatusCreated {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	if e.applier.calls == 0 {
		t.Error("retention not applied on create")
	}
	if w := e.do(admin, "POST", "/streams", map[string]any{"name": "Bad Name"}); w.Code != http.StatusBadRequest {
		t.Errorf("invalid name %d", w.Code)
	}
	list := decodeBody[streamListView](t, e.do(viewer, "GET", "/streams", nil))
	var api *streamView
	for i := range list.Streams {
		if list.Streams[i].Name == "api" {
			api = &list.Streams[i]
		}
	}
	if api == nil || api.Stats["logs"].Rows != 10 || api.Stats["logs"].CompressionRatio != 4 || list.StatsUpdatedAt == nil ||
		*api.RetentionDays != 90 || api.LevelRules[0].Levels[0] != "Debug" {
		t.Fatalf("list %+v", list)
	}

	w = e.do(admin, "PATCH", "/streams/api", map[string]any{"paused": true, "retentionDays": nil})
	got := decodeBody[streamView](t, w)
	if w.Code != http.StatusOK || !got.Paused || got.RetentionDays != nil {
		t.Errorf("patch %d %+v", w.Code, got)
	}
	if w := e.do(admin, "PATCH", "/streams/api", map[string]any{"name": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("rename %d", w.Code)
	}
	if w := e.do(admin, "PATCH", "/streams/nope", map[string]any{"paused": true}); w.Code != http.StatusNotFound {
		t.Errorf("missing %d", w.Code)
	}

	e.admin.mappings = []store.FieldMapping{{Source: model.SourceLogs, Key: "user.id", Type: "number", Hits: 3}}
	fields := decodeBody[[]fieldMappingView](t, e.do(viewer, "GET", "/streams/api/fields", nil))
	if len(fields) != 1 || fields[0].Key != "user.id" || fields[0].Indexed {
		t.Errorf("fields %+v", fields)
	}

	if w := e.do(admin, "DELETE", "/streams/api", map[string]string{"confirm": "API"}); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "confirm") {
		t.Errorf("bad confirm %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "DELETE", "/streams/default", map[string]string{"confirm": "default"}); w.Code != http.StatusConflict {
		t.Errorf("default %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/streams/api", map[string]string{"confirm": "api"}); w.Code != http.StatusNoContent {
		t.Errorf("delete %d %s", w.Code, w.Body.String())
	}
}

func TestIndexedFieldsAndPurgeFlow(t *testing.T) {
	e := newEnv(t)
	admin, viewer := e.sessions["admin"], e.sessions["viewer"]
	w := e.do(admin, "POST", "/indexed-fields", map[string]string{"source": "logs", "key": "user.id"})
	f := decodeBody[indexedFieldView](t, w)
	if w.Code != http.StatusCreated || f.Column != "f_user_id" {
		t.Fatalf("create %d %+v", w.Code, f)
	}
	if w := e.do(admin, "POST", "/indexed-fields", map[string]string{"source": "logs", "key": "user.id"}); w.Code != http.StatusConflict {
		t.Errorf("duplicate %d", w.Code)
	}
	if w := e.do(admin, "POST", "/indexed-fields", map[string]string{"source": "logs", "key": "x'y"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad key %d", w.Code)
	}
	if l := decodeBody[[]indexedFieldView](t, e.do(viewer, "GET", "/indexed-fields", nil)); len(l) != 1 {
		t.Errorf("list %+v", l)
	}
	if w := e.do(admin, "DELETE", "/indexed-fields/"+f.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete %d", w.Code)
	}

	dry := decodeBody[map[string]any](t, e.do(admin, "POST", "/purge", map[string]any{"source": "logs", "from": "now-2d",
		"to": "now-1d", "q": "level:debug", "dryRun": true}))
	if dry["rowsEstimate"] != float64(7) || dry["dryRun"] != true {
		t.Errorf("dry run %v", dry)
	}
	if w := e.do(admin, "POST", "/purge", map[string]any{"source": "logs", "from": "now-2d", "to": "now-1d", "q": "level >= erorr"}); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "invalid_query") {
		t.Errorf("bad query %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "POST", "/purge", map[string]any{"source": "logs", "from": "yesterday", "to": "now"}); w.Code != http.StatusBadRequest {
		t.Errorf("bad time %d", w.Code)
	}
	w = e.do(admin, "POST", "/purge", map[string]any{"source": "logs", "stream": "default", "from": "now-2d", "to": "now-1d"})
	job := decodeBody[purgeJobView](t, w)
	if w.Code != http.StatusAccepted || job.Status != "pending" || job.RowsEstimate != 7 {
		t.Fatalf("job %d %+v", w.Code, job)
	}
	if got := decodeBody[purgeJobView](t, e.do(admin, "GET", "/purge/"+job.ID, nil)); got.ID != job.ID {
		t.Errorf("status %+v", got)
	}
}
