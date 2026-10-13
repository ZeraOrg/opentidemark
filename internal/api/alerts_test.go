// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChannelsAndAlertsFlow(t *testing.T) {
	e := newEnv(t)
	admin, editor, viewer := e.sessions["admin"], e.sessions["editor"], e.sessions["viewer"]
	var hook []byte
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { hook, _ = io.ReadAll(r.Body) }))
	defer srv.Close()

	w := e.do(admin, "POST", "/channels", map[string]any{"name": "hook", "kind": "webhook",
		"config": map[string]any{"url": srv.URL + "/secret-token", "secret": "k"}})
	ch := decodeBody[channelView](t, w)
	if w.Code != http.StatusCreated || strings.Contains(string(ch.Config), "secret-token") || !strings.Contains(string(ch.Config), "••••") {
		t.Fatalf("create channel %d %s", w.Code, w.Body.String())
	}
	if w := e.do(admin, "POST", "/channels", map[string]any{"name": "x", "kind": "slack", "config": map[string]any{"url": "ftp://x"}}); w.Code != http.StatusBadRequest {
		t.Errorf("bad channel %d", w.Code)
	}
	if l := decodeBody[[]channelView](t, e.do(viewer, "GET", "/channels", nil)); len(l) != 1 || strings.Contains(string(l[0].Config), "secret-token") {
		t.Errorf("list channels %+v", l)
	}
	res := decodeBody[map[string]any](t, e.do(admin, "POST", "/channels/"+ch.ID+"/test", nil))
	if res["ok"] != true || !strings.Contains(string(hook), `"state":"test"`) {
		t.Errorf("test channel %v %s", res, hook)
	}

	doc := `{"query":"level:error | stats count()","condition":{"op":">","threshold":5},"window":"5m","every":"1m",` +
		`"channels":["` + ch.ID + `"]}`
	w = e.do(editor, "POST", "/alerts", map[string]any{"name": "errors", "doc": json.RawMessage(doc)})
	rule := decodeBody[alertView](t, w)
	if w.Code != http.StatusCreated || !rule.Enabled || rule.State != "ok" || rule.Groups == nil {
		t.Fatalf("create alert %d %s", w.Code, w.Body.String())
	}
	w = e.do(editor, "POST", "/alerts", map[string]any{"name": "bad", "doc": json.RawMessage(`{"query":"level:error"}`)})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"field":"doc.`) {
		t.Errorf("invalid rule %d %s", w.Code, w.Body.String())
	}
	w = e.do(editor, "POST", "/alerts", map[string]any{"name": "bad", "doc": json.RawMessage(strings.Replace(doc, "level:error", "level >= erorr", 1))})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_query") {
		t.Errorf("invalid query %d %s", w.Code, w.Body.String())
	}
	tv := decodeBody[testView](t, e.do(editor, "POST", "/alerts/"+rule.ID+"/test", nil))
	if tv.State != "firing" || len(tv.Samples) != 1 || *tv.Samples[0].Value != 42 || len(tv.Transitions) != 1 {
		t.Errorf("test %+v", tv)
	}
	if got := decodeBody[alertView](t, e.do(viewer, "GET", "/alerts/"+rule.ID, nil)); got.State != "ok" {
		t.Errorf("test must not persist state: %+v", got)
	}
	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	w = e.do(editor, "POST", "/alerts/"+rule.ID+"/mute", map[string]any{"until": until})
	if muted := decodeBody[alertView](t, w); w.Code != http.StatusOK || !strings.Contains(string(muted.Doc), until) {
		t.Errorf("mute %d %s", w.Code, w.Body.String())
	}
	if w := e.do(editor, "POST", "/alerts/"+rule.ID+"/mute", map[string]any{"until": "2000-01-01T00:00:00Z"}); w.Code != http.StatusBadRequest {
		t.Errorf("mute in the past %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/channels/"+ch.ID, nil); w.Code != http.StatusConflict {
		t.Errorf("delete used channel %d", w.Code)
	}
	if h := decodeBody[[]historyView](t, e.do(viewer, "GET", "/alerts/"+rule.ID+"/history?limit=5", nil)); len(h) != 0 {
		t.Errorf("history %+v", h)
	}
	if w := e.do(viewer, "GET", "/alerts/"+rule.ID+"/history?limit=x", nil); w.Code != http.StatusBadRequest {
		t.Errorf("bad limit %d", w.Code)
	}
	upd := map[string]any{"name": "errors", "enabled": false, "doc": json.RawMessage(doc), "version": 2}
	if w := e.do(editor, "PUT", "/alerts/"+rule.ID, upd); w.Code != http.StatusOK {
		t.Errorf("update %d %s", w.Code, w.Body.String())
	}
	if w := e.do(editor, "PUT", "/alerts/"+rule.ID, upd); w.Code != http.StatusConflict {
		t.Errorf("stale update %d", w.Code)
	}
	if l := decodeBody[[]alertView](t, e.do(viewer, "GET", "/alerts", nil)); len(l) != 1 || l[0].Enabled {
		t.Errorf("list %+v", l)
	}
	chUpd := map[string]any{"name": "hook2", "kind": "webhook", "config": map[string]any{"url": "••••", "secret": "••••"}, "version": 1}
	if w := e.do(admin, "PUT", "/channels/"+ch.ID, chUpd); w.Code != http.StatusOK {
		t.Errorf("update channel %d %s", w.Code, w.Body.String())
	}
	if w := e.do(editor, "DELETE", "/alerts/"+rule.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete alert %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/channels/"+ch.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("delete channel %d", w.Code)
	}
}
