// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/meta"
)

// Dashboard audit actions.
const (
	ActionDashboardCreate = "dashboard.create"
	ActionDashboardUpdate = "dashboard.update"
	ActionDashboardDelete = "dashboard.delete"
	templateServiceVar    = "service"
	copySuffix            = " (copy)"
)

//go:embed templates/dashboards/*.json
var dashboardTemplateFS embed.FS

// DashboardRepo is the dashboard persistence.
type DashboardRepo interface {
	Create(ctx context.Context, d *meta.Dashboard) error
	Get(ctx context.Context, id string) (meta.Dashboard, error)
	ListVisible(ctx context.Context, userID string) ([]meta.Dashboard, error)
	Update(ctx context.Context, d *meta.Dashboard) error
	Delete(ctx context.Context, id string) error
}

// DashboardInput is a create/update/import request.
type DashboardInput struct {
	Name        string
	Description string
	Shared      bool
	Doc         json.RawMessage
	Version     int // update only: the version the client edited
}

// DashboardTemplate is a built-in dashboard (SPEC §13.3).
type DashboardTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Doc         json.RawMessage `json:"doc"`
}

// Dashboards manages dashboards: CRUD with optimistic concurrency, import/export, duplicate,
// templates and variable resolution (SPEC §13).
type Dashboards struct {
	repo      DashboardRepo
	audit     *Auditor
	templates []DashboardTemplate
}

// NewDashboards returns the dashboard service; the embedded templates are validated.
func NewDashboards(repo DashboardRepo, audit *Auditor) (*Dashboards, error) {
	tpls, err := LoadDashboardTemplates()
	if err != nil {
		return nil, err
	}
	return &Dashboards{repo: repo, audit: audit, templates: tpls}, nil
}

// LoadDashboardTemplates reads and validates the embedded templates, sorted by id.
func LoadDashboardTemplates() ([]DashboardTemplate, error) {
	files, err := fs.Glob(dashboardTemplateFS, "templates/dashboards/*.json")
	if err != nil {
		return nil, err
	}
	out := make([]DashboardTemplate, 0, len(files))
	for _, f := range files {
		raw, err := dashboardTemplateFS.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t DashboardTemplate
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("template %s: %w", path.Base(f), err)
		}
		doc, err := ParseDashboardDoc(t.Doc)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", path.Base(f), err)
		}
		if t.Doc, err = json.Marshal(doc); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Templates returns the built-in templates; a non-empty service becomes the default of the
// templates' "service" variable.
func (s *Dashboards) Templates(service string) ([]DashboardTemplate, error) {
	out := make([]DashboardTemplate, 0, len(s.templates))
	for _, t := range s.templates {
		if service != "" {
			doc, err := ParseDashboardDoc(t.Doc)
			if err != nil {
				return nil, err
			}
			for i := range doc.Variables {
				if doc.Variables[i].Name == templateServiceVar {
					doc.Variables[i].Default = []string{service}
				}
			}
			if err := doc.Validate(); err != nil {
				return nil, err
			}
			if t.Doc, err = json.Marshal(doc); err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// List returns the dashboards visible to p (own and shared).
func (s *Dashboards) List(ctx context.Context, p auth.Principal) ([]meta.Dashboard, error) {
	return s.repo.ListVisible(ctx, p.UserID)
}

// Get returns a dashboard visible to p; others are reported as not found.
func (s *Dashboards) Get(ctx context.Context, p auth.Principal, id string) (meta.Dashboard, error) {
	d, err := s.repo.Get(ctx, id)
	if err != nil {
		return meta.Dashboard{}, err
	}
	if d.OwnerID != p.UserID && !d.Shared {
		return meta.Dashboard{}, meta.ErrNotFound
	}
	return d, nil
}

// Create stores a new dashboard owned by p.
func (s *Dashboards) Create(ctx context.Context, p auth.Principal, actor Actor, in DashboardInput) (meta.Dashboard, error) {
	doc, err := normalizeDashboard(in)
	if err != nil {
		return meta.Dashboard{}, err
	}
	d := meta.Dashboard{Name: strings.TrimSpace(in.Name), Description: in.Description, OwnerID: p.UserID, Shared: in.Shared, Doc: doc}
	if err := s.repo.Create(ctx, &d); err != nil {
		return meta.Dashboard{}, err
	}
	s.audit.Record(ctx, actor, ActionDashboardCreate, d.ID, map[string]any{"name": d.Name})
	return d, nil
}

// Update replaces a dashboard if in.Version matches (409 otherwise). Only the owner, or an
// admin for a shared dashboard, may change it.
func (s *Dashboards) Update(ctx context.Context, p auth.Principal, actor Actor, id string, in DashboardInput) (meta.Dashboard, error) {
	d, err := s.writable(ctx, p, id)
	if err != nil {
		return meta.Dashboard{}, err
	}
	if in.Version < 1 {
		return meta.Dashboard{}, invalid("version", "is required")
	}
	doc, err := normalizeDashboard(in)
	if err != nil {
		return meta.Dashboard{}, err
	}
	d.Name, d.Description, d.Shared, d.Doc, d.Version = strings.TrimSpace(in.Name), in.Description, in.Shared, doc, in.Version
	if err := s.repo.Update(ctx, &d); err != nil {
		return meta.Dashboard{}, err
	}
	s.audit.Record(ctx, actor, ActionDashboardUpdate, d.ID, map[string]any{"version": d.Version})
	return d, nil
}

// Delete removes a dashboard (same permission as Update).
func (s *Dashboards) Delete(ctx context.Context, p auth.Principal, actor Actor, id string) error {
	if _, err := s.writable(ctx, p, id); err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		return err
	}
	s.audit.Record(ctx, actor, ActionDashboardDelete, id, nil)
	return nil
}

// Duplicate copies a visible dashboard as a private dashboard of p.
func (s *Dashboards) Duplicate(ctx context.Context, p auth.Principal, actor Actor, id, name string) (meta.Dashboard, error) {
	src, err := s.Get(ctx, p, id)
	if err != nil {
		return meta.Dashboard{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = truncateName(src.Name, maxNameLen-len(copySuffix)) + copySuffix
	}
	return s.Create(ctx, p, actor, DashboardInput{Name: name, Description: src.Description, Doc: src.Doc})
}

// ResolvePanel substitutes variables into a panel query and returns canonical TQL ready for
// POST /query (SPEC §13.1). Unknown variables are errors.
func (s *Dashboards) ResolvePanel(ctx context.Context, p auth.Principal, id, panelID string, vars map[string][]string) (string, error) {
	d, err := s.Get(ctx, p, id)
	if err != nil {
		return "", err
	}
	doc, err := ParseDashboardDoc(d.Doc)
	if err != nil {
		return "", err
	}
	panel, ok := doc.panel(panelID)
	if !ok || panel.Type == PanelMarkdown {
		return "", meta.ErrNotFound
	}
	q, err := ResolveQuery(panel.Query, doc.variableDefaults(), vars)
	if err != nil {
		return "", &PanelError{PanelID: panelID, Err: err}
	}
	return q.String(), nil
}

// errNotOwner hides dashboards the principal may not change.
var errNotOwner = fmt.Errorf("%w: only the owner (or an admin for shared dashboards) can change a dashboard", ErrForbidden)

func (s *Dashboards) writable(ctx context.Context, p auth.Principal, id string) (meta.Dashboard, error) {
	d, err := s.Get(ctx, p, id)
	if err != nil {
		return meta.Dashboard{}, err
	}
	if d.OwnerID != p.UserID && (!d.Shared || !p.Role.Allows(auth.RoleAdmin)) {
		return meta.Dashboard{}, errNotOwner
	}
	return d, nil
}

// normalizeDashboard validates name and document and returns the canonical document JSON.
func normalizeDashboard(in DashboardInput) (json.RawMessage, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > maxNameLen {
		return nil, invalid("name", "is required (at most 128 characters)")
	}
	if len(in.Description) > maxDescription {
		return nil, invalid("description", "is too long")
	}
	doc, err := ParseDashboardDoc(in.Doc)
	if err != nil {
		return nil, err
	}
	if doc.Variables == nil {
		doc.Variables = []Variable{}
	}
	if doc.Panels == nil {
		doc.Panels = []Panel{}
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode dashboard: %w", err)
	}
	return raw, nil
}

func truncateName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
