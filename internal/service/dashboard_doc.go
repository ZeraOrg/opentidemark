// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// Dashboard document limits (SPEC §13.1).
const (
	DashboardDocVersion = 1
	GridColumns         = 12
	MaxPanels           = 100
	MaxVariables        = 20
	maxGridY            = 10000
	maxPanelHeight      = 100
	maxTitleLen         = 200
	maxNameLen          = 128
	maxQueryLen         = 8192
	maxMarkdownLen      = 32 * 1024
	maxVarValues        = 1000
	maxVarValueLen      = 1024
	maxThresholds       = 20
	maxFieldLen         = 128
	minRefresh          = 5 * time.Second
	maxRefresh          = 24 * time.Hour
	maxDecimals         = 10
)

// Panel types (SPEC §13.1).
const (
	PanelTimeseries = "timeseries"
	PanelBar        = "bar"
	PanelStat       = "stat"
	PanelGauge      = "gauge"
	PanelTable      = "table"
	PanelPie        = "pie"
	PanelLogs       = "logs"
	PanelTraces     = "traces"
	PanelMarkdown   = "markdown"
)

// Variable types.
const (
	VarFieldValues = "field-values"
	VarCustom      = "custom"
	VarText        = "text"
)

var (
	panelIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	varNamePattern  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	unitPattern     = regexp.MustCompile(`^[A-Za-z0-9_/%.\-]{1,32}$`)
	relativeTime    = regexp.MustCompile(`^now(-[0-9]{1,6}[smhdw])?$`)
	refreshPattern  = regexp.MustCompile(`^[0-9]{1,5}(s|m|h)$`)
	panelTypes      = []string{PanelTimeseries, PanelBar, PanelStat, PanelGauge, PanelTable, PanelPie, PanelLogs, PanelTraces, PanelMarkdown}
	statsPanelTypes = []string{PanelTimeseries, PanelBar, PanelStat, PanelGauge, PanelPie}
	legendValues    = []string{"", "bottom", "right", "hidden"}
	styleValues     = []string{"", "line", "area", "bars"}
	colorTokens     = []string{"accent", "success", "warning", "danger", "info", "chart1", "chart2", "chart3", "chart4",
		"chart5", "chart6", "chart7", "chart8"}
)

// DashboardDoc is the dashboard document (SPEC §13.1).
type DashboardDoc struct {
	Version   int        `json:"version"`
	TimeRange TimeRange  `json:"timeRange"`
	Refresh   string     `json:"refresh"`
	Variables []Variable `json:"variables"`
	Panels    []Panel    `json:"panels"`
}

// TimeRange is the default range of a dashboard (relative or RFC 3339).
type TimeRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Variable is a dashboard variable ($name in panel queries).
type Variable struct {
	Name    string       `json:"name"`
	Label   string       `json:"label,omitempty"`
	Type    string       `json:"type"`
	Source  model.Source `json:"source,omitempty"`
	Field   string       `json:"field,omitempty"`
	Options []string     `json:"options,omitempty"`
	Multi   bool         `json:"multi"`
	Default []string     `json:"default"`
}

// Panel is one dashboard panel.
type Panel struct {
	ID      string       `json:"id"`
	Title   string       `json:"title"`
	Type    string       `json:"type"`
	Grid    Grid         `json:"grid"`
	Query   string       `json:"query,omitempty"`
	Options PanelOptions `json:"options"`
}

// Grid places a panel on the 12-column grid.
type Grid struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// PanelOptions are the display options of a panel.
type PanelOptions struct {
	Stack      bool        `json:"stack,omitempty"`
	Style      string      `json:"style,omitempty"`
	Unit       string      `json:"unit,omitempty"`
	Decimals   *int        `json:"decimals,omitempty"`
	Legend     string      `json:"legend,omitempty"`
	Thresholds []Threshold `json:"thresholds,omitempty"`
	Content    string      `json:"content,omitempty"`
}

// Threshold colours values at or above Value.
type Threshold struct {
	Value float64 `json:"value"`
	Color string  `json:"color"`
}

// PanelError is a query error of one panel; it unwraps to the tql or validation error.
type PanelError struct {
	PanelID string
	Err     error
}

func (e *PanelError) Error() string { return fmt.Sprintf("panel %s: %v", e.PanelID, e.Err) }

// Unwrap returns the underlying error.
func (e *PanelError) Unwrap() error { return e.Err }

// ParseDashboardDoc strictly decodes and validates a document; unknown fields are errors.
func ParseDashboardDoc(raw json.RawMessage) (DashboardDoc, error) {
	var doc DashboardDoc
	if len(raw) == 0 {
		return doc, invalid("doc", "is required")
	}
	if err := jsonx.CheckDepth(raw, jsonx.MaxDepth); err != nil {
		return doc, invalid("doc", "is nested too deeply")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return doc, invalid("doc", err.Error())
	}
	if dec.More() {
		return doc, invalid("doc", "unexpected data after the document")
	}
	err := doc.Validate()
	return doc, err
}

// Validate checks every limit of SPEC §13.1 and that each panel query parses, substitutes
// with the variable defaults and passes tql.Check.
func (d *DashboardDoc) Validate() error {
	if d.Version != DashboardDocVersion {
		return invalid("doc.version", "must be 1")
	}
	if err := d.validateHeader(); err != nil {
		return err
	}
	defaults, err := d.validateVariables()
	if err != nil {
		return err
	}
	if len(d.Panels) > MaxPanels {
		return invalid("doc.panels", fmt.Sprintf("at most %d panels", MaxPanels))
	}
	seen := map[string]bool{}
	for i := range d.Panels {
		p := &d.Panels[i]
		if seen[p.ID] {
			return invalid(fmt.Sprintf("doc.panels[%d].id", i), "must be unique")
		}
		seen[p.ID] = true
		if err := p.validate(i, defaults); err != nil {
			return err
		}
	}
	return nil
}

func (d *DashboardDoc) validateHeader() error {
	for _, t := range []struct{ field, v string }{{"doc.timeRange.from", d.TimeRange.From}, {"doc.timeRange.to", d.TimeRange.To}} {
		if !validDashboardTime(t.v) {
			return invalid(t.field, "must be now, now-<n><s|m|h|d|w> or an RFC 3339 time")
		}
	}
	if d.Refresh == "" || d.Refresh == "off" {
		return nil
	}
	dur, err := time.ParseDuration(d.Refresh)
	if !refreshPattern.MatchString(d.Refresh) || err != nil || dur < minRefresh || dur > maxRefresh {
		return invalid("doc.refresh", "must be off or a duration like 30s, 5m, 1h between 5s and 24h")
	}
	return nil
}

func validDashboardTime(v string) bool {
	if relativeTime.MatchString(v) {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, v)
	return err == nil
}

func (d *DashboardDoc) validateVariables() (map[string][]string, error) {
	if len(d.Variables) > MaxVariables {
		return nil, invalid("doc.variables", fmt.Sprintf("at most %d variables", MaxVariables))
	}
	defaults := map[string][]string{}
	for i := range d.Variables {
		v := &d.Variables[i]
		field := fmt.Sprintf("doc.variables[%d]", i)
		if !varNamePattern.MatchString(v.Name) {
			return nil, invalid(field+".name", "must match ^[A-Za-z_][A-Za-z0-9_]{0,63}$")
		}
		if _, dup := defaults[v.Name]; dup {
			return nil, invalid(field+".name", "must be unique")
		}
		if err := v.validate(field); err != nil {
			return nil, err
		}
		if v.Default == nil {
			v.Default = []string{}
		}
		defaults[v.Name] = v.Default
	}
	return defaults, nil
}

func (v *Variable) validate(field string) error {
	if len(v.Label) > maxTitleLen {
		return invalid(field+".label", "is too long")
	}
	switch v.Type {
	case VarFieldValues:
		if !v.Source.Valid() || v.Field == "" || len(v.Field) > maxFieldLen || len(v.Options) > 0 {
			return invalid(field, "field-values needs source (logs|spans|metrics) and field, and no options")
		}
	case VarCustom:
		if len(v.Options) == 0 || v.Source != "" || v.Field != "" {
			return invalid(field, "custom needs options, and no source or field")
		}
	case VarText:
		if v.Source != "" || v.Field != "" || len(v.Options) > 0 {
			return invalid(field, "text takes no source, field or options")
		}
	default:
		return invalid(field+".type", "must be field-values, custom or text")
	}
	if err := checkValues(field+".options", v.Options); err != nil {
		return err
	}
	if err := checkValues(field+".default", v.Default); err != nil {
		return err
	}
	if !v.Multi && len(v.Default) > 1 {
		return invalid(field+".default", "a single-value variable has at most one default")
	}
	return nil
}

func checkValues(field string, vals []string) error {
	if len(vals) > maxVarValues {
		return invalid(field, fmt.Sprintf("at most %d values", maxVarValues))
	}
	for _, v := range vals {
		if len(v) > maxVarValueLen {
			return invalid(field, "value is too long")
		}
	}
	return nil
}

func (p *Panel) validate(i int, defaults map[string][]string) error {
	field := fmt.Sprintf("doc.panels[%d]", i)
	switch {
	case !panelIDPattern.MatchString(p.ID):
		return invalid(field+".id", "must match ^[A-Za-z0-9_-]{1,64}$")
	case len(p.Title) > maxTitleLen:
		return invalid(field+".title", "is too long")
	case !slices.Contains(panelTypes, p.Type):
		return invalid(field+".type", "must be one of "+strings.Join(panelTypes, ", "))
	}
	if err := p.Grid.validate(field + ".grid"); err != nil {
		return err
	}
	if err := p.Options.validate(field+".options", p.Type); err != nil {
		return err
	}
	if p.Type == PanelMarkdown {
		if p.Query != "" {
			return invalid(field+".query", "markdown panels have no query")
		}
		return nil
	}
	if strings.TrimSpace(p.Query) == "" || len(p.Query) > maxQueryLen {
		return invalid(field+".query", "is required (at most 8 KiB)")
	}
	q, err := ResolveQuery(p.Query, defaults, nil)
	if err != nil {
		return &PanelError{PanelID: p.ID, Err: err}
	}
	if err := checkPanelKind(p.Type, q); err != nil {
		return &PanelError{PanelID: p.ID, Err: invalid(field+".query", err.Error())}
	}
	return nil
}

func (g Grid) validate(field string) error {
	if g.X < 0 || g.W < 1 || g.X+g.W > GridColumns || g.Y < 0 || g.Y > maxGridY || g.H < 1 || g.H > maxPanelHeight {
		return invalid(field, fmt.Sprintf("needs 0 ≤ x, 1 ≤ w, x+w ≤ %d, 0 ≤ y ≤ %d, 1 ≤ h ≤ %d", GridColumns, maxGridY, maxPanelHeight))
	}
	return nil
}

func (o PanelOptions) validate(field, panelType string) error {
	switch {
	case !slices.Contains(styleValues, o.Style):
		return invalid(field+".style", "must be line, area or bars")
	case !slices.Contains(legendValues, o.Legend):
		return invalid(field+".legend", "must be bottom, right or hidden")
	case o.Unit != "" && !unitPattern.MatchString(o.Unit):
		return invalid(field+".unit", "must be 1-32 characters of A-Z a-z 0-9 _ / % . -")
	case o.Decimals != nil && (*o.Decimals < 0 || *o.Decimals > maxDecimals):
		return invalid(field+".decimals", "must be between 0 and 10")
	case len(o.Thresholds) > maxThresholds:
		return invalid(field+".thresholds", "at most 20 thresholds")
	case len(o.Content) > maxMarkdownLen, o.Content != "" && panelType != PanelMarkdown:
		return invalid(field+".content", "only markdown panels have content (at most 32 KiB)")
	}
	for _, t := range o.Thresholds {
		if !slices.Contains(colorTokens, t.Color) {
			return invalid(field+".thresholds", "color must be one of "+strings.Join(colorTokens, ", "))
		}
	}
	return nil
}

// errPanelKind describes queries that do not fit a panel type.
var errPanelKind = errors.New("query does not fit the panel type")

// checkPanelKind matches the result kind to the panel type: charts and single values need a
// stats or top stage, logs panels an event query, traces panels a spans event query.
func checkPanelKind(panelType string, q *tql.Query) error {
	aggregated := false
	for _, st := range q.Stages {
		switch st.(type) {
		case *tql.Stats, *tql.Top:
			aggregated = true
		}
	}
	switch {
	case slices.Contains(statsPanelTypes, panelType) && !aggregated:
		return fmt.Errorf("%w: %s panels need a stats or top stage", errPanelKind, panelType)
	case panelType == PanelLogs && (aggregated || q.Source != model.SourceLogs):
		return fmt.Errorf("%w: logs panels need a logs query without stats", errPanelKind)
	case panelType == PanelTraces && (aggregated || q.Source != model.SourceSpans):
		return fmt.Errorf("%w: traces panels need a spans query without stats", errPanelKind)
	}
	return nil
}

// ResolveQuery parses text, substitutes variables on the AST (SPEC §13.1: values given in vals
// win over defaults) and checks the result. The returned query is safe to compile.
func ResolveQuery(text string, defaults, vals map[string][]string) (*tql.Query, error) {
	q, err := tql.Parse(text)
	if err != nil {
		return nil, err
	}
	merged := make(map[string][]string, len(defaults))
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range vals {
		if _, ok := merged[k]; !ok {
			return nil, invalid("vars", fmt.Sprintf("unknown variable %q", k))
		}
		if err := checkValues("vars."+k, v); err != nil {
			return nil, err
		}
		merged[k] = v
	}
	sub, err := tql.Substitute(q, merged)
	if err != nil {
		return nil, err
	}
	if err := tql.Check(sub); err != nil {
		return nil, err
	}
	return sub, nil
}

// variableDefaults returns name ⇒ default values.
func (d *DashboardDoc) variableDefaults() map[string][]string {
	out := make(map[string][]string, len(d.Variables))
	for i := range d.Variables {
		out[d.Variables[i].Name] = d.Variables[i].Default
	}
	return out
}

// panel returns the panel with id.
func (d *DashboardDoc) panel(id string) (*Panel, bool) {
	for i := range d.Panels {
		if d.Panels[i].ID == id {
			return &d.Panels[i], true
		}
	}
	return nil, false
}
