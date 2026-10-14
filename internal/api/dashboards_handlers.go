// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"mime"
	"net/http"
	"net/netip"
	"regexp"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/service"
)

// DashboardsAPI serves /dashboards (SPEC §11.4, §13).
type DashboardsAPI struct {
	Dashboards *service.Dashboards
	ClientIP   func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *DashboardsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/dashboards", Viewer, a.list},
		{http.MethodPost, "/dashboards", Editor, a.create},
		{http.MethodGet, "/dashboards/templates", Viewer, a.templates},
		{http.MethodPost, "/dashboards/import", Editor, a.importDoc},
		{http.MethodGet, "/dashboards/{id}", Viewer, a.get},
		{http.MethodPut, "/dashboards/{id}", Editor, a.update},
		{http.MethodDelete, "/dashboards/{id}", Editor, a.delete},
		{http.MethodGet, "/dashboards/{id}/export", Viewer, a.export},
		{http.MethodPost, "/dashboards/{id}/duplicate", Editor, a.duplicate},
		{http.MethodPost, "/dashboards/{id}/panels/{panelId}/query", Viewer, a.resolve},
	}
}

type dashboardRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Shared      bool            `json:"shared"`
	Doc         json.RawMessage `json:"doc"`
	Version     int             `json:"version"`
}

func (d dashboardRequest) input() service.DashboardInput {
	return service.DashboardInput{Name: d.Name, Description: d.Description, Shared: d.Shared, Doc: d.Doc, Version: d.Version}
}

// dashboardView is the JSON form of a dashboard; Doc is omitted in lists.
type dashboardView struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	OwnerID     string          `json:"ownerId"`
	Shared      bool            `json:"shared"`
	Version     int             `json:"version"`
	Doc         json.RawMessage `json:"doc,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

func toDashboardView(d *meta.Dashboard, withDoc bool) dashboardView {
	v := dashboardView{ID: d.ID, Name: d.Name, Description: d.Description, OwnerID: d.OwnerID, Shared: d.Shared,
		Version: d.Version, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
	if withDoc {
		v.Doc = d.Doc
	}
	return v
}

func (a *DashboardsAPI) list(w http.ResponseWriter, r *http.Request) error {
	ds, err := a.Dashboards.List(r.Context(), principal(r))
	if err != nil {
		return err
	}
	out := make([]dashboardView, 0, len(ds))
	for i := range ds {
		out = append(out, toDashboardView(&ds[i], false))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *DashboardsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in dashboardRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	d, err := a.Dashboards.Create(r.Context(), principal(r), actor(r, a.ClientIP), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toDashboardView(&d, true))
	return nil
}

func (a *DashboardsAPI) templates(w http.ResponseWriter, r *http.Request) error {
	svc := r.URL.Query().Get("service")
	if len(svc) > maxTemplateService {
		return errBadRequest("service is too long")
	}
	tpls, err := a.Dashboards.Templates(svc)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, tpls)
	return nil
}

const maxTemplateService = 256

// exportDoc is the import/export format.
type exportDoc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Doc         json.RawMessage `json:"doc"`
}

func (a *DashboardsAPI) importDoc(w http.ResponseWriter, r *http.Request) error {
	var in exportDoc
	if err := decode(r, &in); err != nil {
		return err
	}
	d, err := a.Dashboards.Create(r.Context(), principal(r), actor(r, a.ClientIP),
		service.DashboardInput{Name: in.Name, Description: in.Description, Doc: in.Doc})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toDashboardView(&d, true))
	return nil
}

func (a *DashboardsAPI) get(w http.ResponseWriter, r *http.Request) error {
	d, err := a.Dashboards.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDashboardView(&d, true))
	return nil
}

func (a *DashboardsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in dashboardRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	d, err := a.Dashboards.Update(r.Context(), principal(r), actor(r, a.ClientIP), r.PathValue("id"), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toDashboardView(&d, true))
	return nil
}

func (a *DashboardsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Dashboards.Delete(r.Context(), principal(r), actor(r, a.ClientIP), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// fileNameUnsafe matches characters replaced in export file names.
var fileNameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (a *DashboardsAPI) export(w http.ResponseWriter, r *http.Request) error {
	d, err := a.Dashboards.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	name := fileNameUnsafe.ReplaceAllString(d.Name, "-")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "dashboard-" + name + ".json"}))
	writeJSON(w, http.StatusOK, exportDoc{Name: d.Name, Description: d.Description, Doc: d.Doc})
	return nil
}

type duplicateRequest struct {
	Name string `json:"name"`
}

func (a *DashboardsAPI) duplicate(w http.ResponseWriter, r *http.Request) error {
	var in duplicateRequest
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return err
		}
	}
	d, err := a.Dashboards.Duplicate(r.Context(), principal(r), actor(r, a.ClientIP), r.PathValue("id"), in.Name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toDashboardView(&d, true))
	return nil
}

type resolveRequest struct {
	Vars map[string][]string `json:"vars"`
}

func (a *DashboardsAPI) resolve(w http.ResponseWriter, r *http.Request) error {
	var in resolveRequest
	if r.ContentLength != 0 {
		if err := decode(r, &in); err != nil {
			return err
		}
	}
	q, err := a.Dashboards.ResolvePanel(r.Context(), principal(r), r.PathValue("id"), r.PathValue("panelId"), in.Vars)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"q": q})
	return nil
}
