// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const apiDoc = `{"version":1,"timeRange":{"from":"now-1h","to":"now"},"refresh":"off",
 "variables":[{"name":"svc","type":"text","multi":false,"default":["*"]}],
 "panels":[{"id":"p1","title":"Errors","type":"stat","grid":{"x":0,"y":0,"w":3,"h":3},
  "query":"service:$svc and level >= error | stats count()","options":{}}]}`

func TestDashboardsFlow(t *testing.T) {
	e := newEnv(t)
	editor, viewer, admin := e.sessions["editor"], e.sessions["viewer"], e.sessions["admin"]
	body := map[string]any{"name": "Ops", "doc": json.RawMessage(apiDoc)}
	w := e.do(editor, "POST", "/dashboards", body)
	created := decodeBody[dashboardView](t, w)
	if w.Code != http.StatusCreated || created.Version != 1 || len(created.Doc) == 0 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	bad := map[string]any{"name": "Bad", "doc": json.RawMessage(strings.Replace(apiDoc, "level >= error", "level >= erorr", 1))}
	w = e.do(editor, "POST", "/dashboards", bad)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"panelId":"p1"`) ||
		!strings.Contains(w.Body.String(), "invalid_query") {
		t.Errorf("bad query %d %s", w.Code, w.Body.String())
	}
	if w := e.do(viewer, "GET", "/dashboards/"+created.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("private visible to viewer: %d", w.Code)
	}
	upd := map[string]any{"name": "Ops", "shared": true, "doc": json.RawMessage(apiDoc), "version": 1}
	if w := e.do(editor, "PUT", "/dashboards/"+created.ID, upd); w.Code != http.StatusOK {
		t.Fatalf("update %d %s", w.Code, w.Body.String())
	}
	if w := e.do(editor, "PUT", "/dashboards/"+created.ID, upd); w.Code != http.StatusConflict {
		t.Errorf("stale version %d", w.Code)
	}
	if l := decodeBody[[]dashboardView](t, e.do(viewer, "GET", "/dashboards", nil)); len(l) != 1 || l[0].Doc != nil {
		t.Errorf("list %+v", l)
	}
	res := decodeBody[map[string]string](t, e.do(viewer, "POST", "/dashboards/"+created.ID+"/panels/p1/query",
		map[string]any{"vars": map[string][]string{"svc": {"api"}}}))
	if res["q"] != `service:"api" and level >= "error" | stats count()` {
		t.Errorf("resolve %v", res)
	}
	w = e.do(viewer, "GET", "/dashboards/"+created.ID+"/export", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), `filename=dashboard-Ops.json`) {
		t.Errorf("export %d %v", w.Code, w.Header())
	}
	exported := w.Body.String()
	if w := e.do(admin, "POST", "/dashboards/import", exported); w.Code != http.StatusCreated {
		t.Errorf("import %d %s", w.Code, w.Body.String())
	}
	w = e.do(admin, "POST", "/dashboards/"+created.ID+"/duplicate", nil)
	if dup := decodeBody[dashboardView](t, w); w.Code != http.StatusCreated || dup.Name != "Ops (copy)" {
		t.Errorf("duplicate %d %+v", w.Code, dup)
	}
	tpls := decodeBody[[]map[string]any](t, e.do(viewer, "GET", "/dashboards/templates?service=checkout", nil))
	if len(tpls) != 4 || !strings.Contains(tpls[0]["doc"].(map[string]any)["variables"].([]any)[0].(map[string]any)["default"].([]any)[0].(string), "checkout") {
		t.Errorf("templates %v", tpls)
	}
	if w := e.do(viewer, "GET", "/dashboards/templates?service="+strings.Repeat("s", 300), nil); w.Code != http.StatusBadRequest {
		t.Errorf("long service %d", w.Code)
	}
	if w := e.do(admin, "DELETE", "/dashboards/"+created.ID, nil); w.Code != http.StatusNoContent {
		t.Errorf("admin delete shared %d", w.Code)
	}
}
