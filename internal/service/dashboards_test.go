// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

const validDoc = `{"version":1,"timeRange":{"from":"now-6h","to":"now"},"refresh":"30s",
 "variables":[{"name":"service","type":"field-values","source":"logs","field":"service","multi":true,"default":["*"]}],
 "panels":[{"id":"p1","title":"Errors","type":"timeseries","grid":{"x":0,"y":0,"w":6,"h":4},
  "query":"level >= error and service:$service | stats count() by service span auto",
  "options":{"stack":true,"unit":"short","legend":"bottom","thresholds":[{"value":100,"color":"danger"}]}}]}`

// mutate applies a JSON patch-like edit to validDoc.
func mutate(t *testing.T, edit func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validDoc), &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	b, _ := json.Marshal(m)
	return string(b)
}

func panel0(m map[string]any) map[string]any { return m["panels"].([]any)[0].(map[string]any) }
func var0(m map[string]any) map[string]any   { return m["variables"].([]any)[0].(map[string]any) }

func TestDashboardDocValidation(t *testing.T) {
	if _, err := ParseDashboardDoc(json.RawMessage(validDoc)); err != nil {
		t.Fatalf("valid doc: %v", err)
	}
	manyPanels := func(m map[string]any) {
		var ps []any
		for i := range MaxPanels + 1 {
			p := map[string]any{"id": fmt.Sprintf("p%d", i), "title": "", "type": "markdown", "grid": map[string]any{"x": 0, "y": i, "w": 1, "h": 1},
				"options": map[string]any{}}
			ps = append(ps, p)
		}
		m["panels"] = ps
	}
	tests := []struct {
		name string
		doc  string
	}{
		{"empty", ``},
		{"not json", `{`},
		{"unknown field", mutate(t, func(m map[string]any) { m["extra"] = 1 })},
		{"unknown option", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["color"] = "red" })},
		{"trailing", validDoc + `{}`},
		{"version", mutate(t, func(m map[string]any) { m["version"] = 2 })},
		{"bad from", mutate(t, func(m map[string]any) { m["timeRange"].(map[string]any)["from"] = "yesterday" })},
		{"bad to", mutate(t, func(m map[string]any) { m["timeRange"].(map[string]any)["to"] = "now+1h" })},
		{"refresh too fast", mutate(t, func(m map[string]any) { m["refresh"] = "1s" })},
		{"refresh garbage", mutate(t, func(m map[string]any) { m["refresh"] = "1h30m" })},
		{"refresh too slow", mutate(t, func(m map[string]any) { m["refresh"] = "48h" })},
		{"too many panels", mutate(t, manyPanels)},
		{"dup panel", mutate(t, func(m map[string]any) { m["panels"] = append(m["panels"].([]any), panel0(m)) })},
		{"panel id", mutate(t, func(m map[string]any) { panel0(m)["id"] = "a b" })},
		{"panel type", mutate(t, func(m map[string]any) { panel0(m)["type"] = "heatmap" })},
		{"title", mutate(t, func(m map[string]any) { panel0(m)["title"] = strings.Repeat("t", 201) })},
		{"grid x", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": -1, "y": 0, "w": 1, "h": 1} })},
		{"grid w", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": 0, "y": 0, "w": 0, "h": 1} })},
		{"grid overflow", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": 8, "y": 0, "w": 6, "h": 1} })},
		{"grid h", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": 0, "y": 0, "w": 1, "h": 101} })},
		{"grid y", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": 0, "y": 10001, "w": 1, "h": 1} })},
		{"grid type", mutate(t, func(m map[string]any) { panel0(m)["grid"] = map[string]any{"x": "0", "y": 0, "w": 1, "h": 1} })},
		{"no query", mutate(t, func(m map[string]any) { panel0(m)["query"] = " " })},
		{"long query", mutate(t, func(m map[string]any) { panel0(m)["query"] = strings.Repeat("a", maxQueryLen+1) })},
		{"syntax", mutate(t, func(m map[string]any) { panel0(m)["query"] = "level:error | stats" })},
		{"check", mutate(t, func(m map[string]any) { panel0(m)["query"] = "level >= erorr | stats count()" })},
		{"unknown var", mutate(t, func(m map[string]any) { panel0(m)["query"] = "service:$nope | stats count()" })},
		{"stats needed", mutate(t, func(m map[string]any) { panel0(m)["query"] = "level:error" })},
		{"logs panel stats", mutate(t, func(m map[string]any) { panel0(m)["type"] = "logs" })},
		{"traces panel logs", mutate(t, func(m map[string]any) {
			panel0(m)["type"] = "traces"
			panel0(m)["query"] = "level:error"
		})},
		{"markdown query", mutate(t, func(m map[string]any) { panel0(m)["type"] = "markdown" })},
		{"content on chart", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["content"] = "x" })},
		{"style", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["style"] = "pie" })},
		{"legend", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["legend"] = "top" })},
		{"unit", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["unit"] = "a b" })},
		{"decimals", mutate(t, func(m map[string]any) { panel0(m)["options"].(map[string]any)["decimals"] = 11 })},
		{"threshold color", mutate(t, func(m map[string]any) {
			panel0(m)["options"].(map[string]any)["thresholds"] = []any{map[string]any{"value": 1, "color": "#f00"}}
		})},
		{"var name", mutate(t, func(m map[string]any) { var0(m)["name"] = "1x" })},
		{"var dup", mutate(t, func(m map[string]any) { m["variables"] = append(m["variables"].([]any), var0(m)) })},
		{"var type", mutate(t, func(m map[string]any) { var0(m)["type"] = "query" })},
		{"var source", mutate(t, func(m map[string]any) { var0(m)["source"] = "files" })},
		{"var field", mutate(t, func(m map[string]any) { delete(var0(m), "field") })},
		{"custom no options", mutate(t, func(m map[string]any) {
			var0(m)["type"] = "custom"
			delete(var0(m), "source")
			delete(var0(m), "field")
		})},
		{"text with field", mutate(t, func(m map[string]any) { var0(m)["type"] = "text"; delete(var0(m), "source") })},
		{"single many defaults", mutate(t, func(m map[string]any) { var0(m)["multi"] = false; var0(m)["default"] = []string{"a", "b"} })},
		{"long default", mutate(t, func(m map[string]any) { var0(m)["default"] = []string{strings.Repeat("v", 1025)} })},
	}
	for _, tc := range tests {
		if _, err := ParseDashboardDoc(json.RawMessage(tc.doc)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	var pe *PanelError
	var qe *tql.Error
	_, err := ParseDashboardDoc(json.RawMessage(mutate(t, func(m map[string]any) { panel0(m)["query"] = "level >= erorr | stats count()" })))
	if !errors.As(err, &pe) || pe.PanelID != "p1" || !errors.As(err, &qe) || qe.Hint == "" {
		t.Errorf("panel error %v", err)
	}
}

func TestDashboardDocAccepts(t *testing.T) {
	ok := []string{
		mutate(t, func(m map[string]any) { m["refresh"] = "off" }),
		mutate(t, func(m map[string]any) { m["refresh"] = "" }),
		mutate(t, func(m map[string]any) {
			m["timeRange"] = map[string]any{"from": "2026-10-01T00:00:00Z", "to": "now-1w"}
		}),
		mutate(t, func(m map[string]any) {
			var0(m)["type"] = "custom"
			delete(var0(m), "source")
			delete(var0(m), "field")
			var0(m)["options"] = []string{"a", "b"}
			var0(m)["default"] = []string{"a"}
		}),
		mutate(t, func(m map[string]any) { delete(var0(m), "default") }),
		mutate(t, func(m map[string]any) {
			panel0(m)["type"] = "logs"
			panel0(m)["query"] = "service:$service and level:error"
		}),
		mutate(t, func(m map[string]any) {
			panel0(m)["type"] = "markdown"
			delete(panel0(m), "query")
			panel0(m)["options"] = map[string]any{"content": "# hi"}
		}),
		mutate(t, func(m map[string]any) { panel0(m)["type"] = "table"; panel0(m)["query"] = "level:error" }),
	}
	for i, doc := range ok {
		if _, err := ParseDashboardDoc(json.RawMessage(doc)); err != nil {
			t.Errorf("case %d: %v", i, err)
		}
	}
}

// TestDashboardTemplatesCompile checks every panel of every built-in template parses,
// substitutes and compiles, with the default and with a concrete service (SPEC §13.3).
func TestDashboardTemplatesCompile(t *testing.T) {
	tpls, err := LoadDashboardTemplates()
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"http-red", "ingestion-health", "log-volume", "service-overview"}
	if len(tpls) != len(wantIDs) {
		t.Fatalf("templates %d", len(tpls))
	}
	to := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for i, tpl := range tpls {
		if tpl.ID != wantIDs[i] || tpl.Name == "" || tpl.Description == "" {
			t.Errorf("template %+v", tpl)
		}
		doc, err := ParseDashboardDoc(tpl.Doc)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		for _, vars := range []map[string][]string{nil, {"service": {"checkout"}}, {"service": {"a", "b"}}} {
			for _, p := range doc.Panels {
				if p.Type == PanelMarkdown {
					continue
				}
				q, err := ResolveQuery(p.Query, doc.variableDefaults(), vars)
				if err != nil {
					t.Fatalf("%s/%s: %v", tpl.ID, p.ID, err)
				}
				if _, err := tql.Compile(q, tql.Options{Database: "tidemark", From: to.Add(-time.Hour), To: to}); err != nil {
					t.Errorf("%s/%s compile %q: %v", tpl.ID, p.ID, q.String(), err)
				}
			}
		}
	}
}

func newDashboards(t *testing.T) *Dashboards {
	t.Helper()
	db := openDB(t)
	d, err := NewDashboards(meta.NewDashboards(db), NewAuditor(meta.NewAuditLog(db), quiet(), time.Now))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDashboardTemplatesWithService(t *testing.T) {
	d := newDashboards(t)
	tpls, err := d.Templates("checkout")
	if err != nil {
		t.Fatal(err)
	}
	for _, tpl := range tpls {
		if !strings.Contains(string(tpl.Doc), `"default":["checkout"]`) {
			t.Errorf("%s: %s", tpl.ID, tpl.Doc)
		}
	}
	plain, _ := d.Templates("")
	if strings.Contains(string(plain[0].Doc), "checkout") {
		t.Error("default templates changed")
	}
}

func TestDashboardsCRUD(t *testing.T) {
	d := newDashboards(t)
	ctx := context.Background()
	alice := auth.Principal{UserID: "alice", Role: auth.RoleEditor}
	bob := auth.Principal{UserID: "bob", Role: auth.RoleEditor}
	root := auth.Principal{UserID: "root", Role: auth.RoleAdmin}
	in := DashboardInput{Name: " Ops ", Doc: json.RawMessage(validDoc)}
	if _, err := d.Create(ctx, alice, SystemActor, DashboardInput{Name: "", Doc: json.RawMessage(validDoc)}); err == nil {
		t.Error("empty name accepted")
	}
	if _, err := d.Create(ctx, alice, SystemActor, DashboardInput{Name: "x", Description: strings.Repeat("d", 2000), Doc: json.RawMessage(validDoc)}); err == nil {
		t.Error("long description accepted")
	}
	created, err := d.Create(ctx, alice, SystemActor, in)
	if err != nil || created.Name != "Ops" || created.Version != 1 {
		t.Fatalf("%+v %v", created, err)
	}
	if _, err := d.Get(ctx, bob, created.ID); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("private dashboard visible: %v", err)
	}
	in.Shared, in.Version = true, 1
	upd, err := d.Update(ctx, alice, SystemActor, created.ID, in)
	if err != nil || upd.Version != 2 || !upd.Shared {
		t.Fatalf("update %+v %v", upd, err)
	}
	if _, err := d.Update(ctx, alice, SystemActor, created.ID, in); !errors.Is(err, meta.ErrConflict) {
		t.Errorf("stale version: %v", err)
	}
	in.Version = 0
	if _, err := d.Update(ctx, alice, SystemActor, created.ID, in); err == nil {
		t.Error("missing version accepted")
	}
	in.Version = 2
	if _, err := d.Update(ctx, bob, SystemActor, created.ID, in); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-owner update: %v", err)
	}
	if got, err := d.Get(ctx, bob, created.ID); err != nil || got.ID != created.ID {
		t.Errorf("shared get: %v", err)
	}
	if l, _ := d.List(ctx, bob); len(l) != 1 {
		t.Errorf("bob list %d", len(l))
	}
	dup, err := d.Duplicate(ctx, bob, SystemActor, created.ID, "")
	if err != nil || dup.Name != "Ops (copy)" || dup.OwnerID != "bob" || dup.Shared {
		t.Fatalf("duplicate %+v %v", dup, err)
	}
	if err := d.Delete(ctx, bob, SystemActor, created.ID); !errors.Is(err, ErrForbidden) {
		t.Errorf("non-owner delete: %v", err)
	}
	if err := d.Delete(ctx, root, SystemActor, created.ID); err != nil {
		t.Errorf("admin delete shared: %v", err)
	}
	if err := d.Delete(ctx, root, SystemActor, dup.ID); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("admin delete private: %v", err)
	}
}

func TestDashboardResolvePanel(t *testing.T) {
	d := newDashboards(t)
	ctx := context.Background()
	p := auth.Principal{UserID: "u", Role: auth.RoleViewer}
	db, err := d.Create(ctx, p, SystemActor, DashboardInput{Name: "x", Doc: json.RawMessage(validDoc)})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		vars map[string][]string
		want string
	}{
		{nil, `level >= "error" | stats count() by service span auto`},
		{map[string][]string{"service": {"api"}}, `level >= "error" and service:"api" | stats count() by service span auto`},
		{map[string][]string{"service": {"a", "b c"}}, `level >= "error" and (service:"a" or service:"b c") | stats count() by service span auto`},
		{map[string][]string{"service": {"x' OR 1=1 --"}}, `level >= "error" and service:"x' OR 1=1 --" | stats count() by service span auto`},
	}
	for _, tc := range tests {
		got, err := d.ResolvePanel(ctx, p, db.ID, "p1", tc.vars)
		if err != nil || got != tc.want {
			t.Errorf("%v: %q %v; want %q", tc.vars, got, err, tc.want)
		}
		if q, err := tql.Parse(got); err != nil || q.String() != got || tql.Check(q) != nil {
			t.Errorf("canonical %q does not round-trip: %v", got, err)
		}
	}
	if _, err := d.ResolvePanel(ctx, p, db.ID, "p1", map[string][]string{"other": {"x"}}); err == nil {
		t.Error("unknown variable accepted")
	}
	if _, err := d.ResolvePanel(ctx, p, db.ID, "nope", nil); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("missing panel: %v", err)
	}
	if _, err := d.ResolvePanel(ctx, auth.Principal{UserID: "other"}, db.ID, "p1", nil); !errors.Is(err, meta.ErrNotFound) {
		t.Errorf("invisible: %v", err)
	}
}
