// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/auth"
)

// importView mirrors the ImportJob response shape of SPEC §11.5.
type importView struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind"`
	Name            string         `json:"name"`
	Status          string         `json:"status"`
	Config          map[string]any `json:"config"`
	RowsImported    int64          `json:"rowsImported"`
	Warnings        []string       `json:"warnings"`
	CancelRequested bool           `json:"cancelRequested"`
	RequestedBy     string         `json:"requestedBy"`
	StartedAt       *string        `json:"startedAt"`
	FinishedAt      *string        `json:"finishedAt"`
}

func TestImportsAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	body := map[string]any{"kind": "seq", "name": "old seq", "config": map[string]any{
		"url": "https://seq.example.internal", "apiKey": "top-secret-key", "importSignals": true}}
	w := e.do(admin, "POST", "/imports", body)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "top-secret-key") {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	job := decodeBody[importView](t, w)
	if job.Kind != "seq" || job.Status != "queued" || job.Warnings == nil || job.Config["url"] == nil ||
		job.Config["apiKey"] != nil || !strings.HasPrefix(job.RequestedBy, "user:") || job.StartedAt != nil {
		t.Fatalf("job view %+v", job)
	}
	if w := e.do(admin, "GET", "/imports/"+job.ID, nil); w.Code != http.StatusOK {
		t.Fatalf("get: %d", w.Code)
	}
	w = e.do(admin, "POST", "/imports/"+job.ID+"/cancel", "{}")
	if got := decodeBody[importView](t, w); w.Code != http.StatusOK || got.Status != "cancelled" || !got.CancelRequested {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "POST", "/imports/"+job.ID+"/cancel", "{}"); w.Code != http.StatusConflict {
		t.Fatalf("second cancel: %d", w.Code)
	}
	for name, bad := range map[string]any{
		"kind":   map[string]any{"kind": "splunk", "config": map[string]any{}},
		"file":   map[string]any{"kind": "clef-file", "config": map[string]any{}},
		"config": map[string]any{"kind": "elastic", "config": map[string]any{"url": "https://es", "index": "a b"}},
	} {
		if w := e.do(admin, "POST", "/imports", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	w = e.do(admin, "GET", "/imports", nil)
	var list []importView
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %s", w.Body.String())
	}
}

func multipartImport(t *testing.T, job string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("job", job)
	for name, content := range files {
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write([]byte(content))
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestImportsUpload(t *testing.T) {
	e := newEnv(t)
	admin := e.sessions["admin"]
	send := func(body *bytes.Buffer, ctype string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", Prefix+"/imports", body)
		req.Header.Set("Content-Type", ctype)
		for _, ck := range admin.cookies {
			req.AddCookie(ck)
		}
		req.Header.Set(auth.CSRFHeader, admin.csrf)
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, req)
		return w
	}
	clef := `{"@t":"2026-09-01T00:00:00Z","@mt":"hello"}` + "\n"
	body, ctype := multipartImport(t, `{"kind":"clef-file","config":{"stream":"old"}}`, map[string]string{"a.clef": clef})
	w := send(body, ctype)
	if w.Code != http.StatusCreated {
		t.Fatalf("clef upload: %d %s", w.Code, w.Body.String())
	}
	job := decodeBody[importView](t, w)
	files, _ := job.Config["files"].([]any)
	if job.Status != "queued" || len(files) != 1 || strings.Contains(w.Body.String(), "path") {
		t.Fatalf("clef job %s", w.Body.String())
	}
	export := `{"type":"search","id":"s","attributes":{"title":"Errors","kibanaSavedObjectMeta":{"searchSourceJSON":"{\"query\":{\"query\":\"level:error\",\"language\":\"kuery\"}}"}}}`
	body, ctype = multipartImport(t, `{"kind":"kibana"}`, map[string]string{"export.ndjson": export})
	w = send(body, ctype)
	if got := decodeBody[importView](t, w); w.Code != http.StatusCreated || got.Status != "completed" || got.RowsImported != 1 || got.FinishedAt == nil {
		t.Fatalf("kibana: %d %s", w.Code, w.Body.String())
	}
	body, ctype = multipartImport(t, `{"kind":"seq","config":{"url":"https://seq.local"}}`, map[string]string{"x": "y"})
	if w := send(body, ctype); w.Code != http.StatusBadRequest {
		t.Fatalf("files for seq: %d", w.Code)
	}
	body, ctype = multipartImport(t, `not json`, nil)
	if w := send(body, ctype); w.Code != http.StatusBadRequest {
		t.Fatalf("bad job part: %d", w.Code)
	}
}
