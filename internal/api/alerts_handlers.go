// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"time"

	"github.com/ZeraOrg/tidemark/internal/alerting"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
)

// AlertsAPI serves /alerts and /channels (SPEC §11.4, §14).
type AlertsAPI struct {
	Alerts   *service.Alerts
	Channels *service.Channels
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *AlertsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/alerts", Viewer, a.list},
		{http.MethodPost, "/alerts", Editor, a.create},
		{http.MethodGet, "/alerts/{id}", Viewer, a.get},
		{http.MethodPut, "/alerts/{id}", Editor, a.update},
		{http.MethodDelete, "/alerts/{id}", Editor, a.delete},
		{http.MethodPost, "/alerts/{id}/test", Editor, a.test},
		{http.MethodPost, "/alerts/{id}/mute", Editor, a.mute},
		{http.MethodGet, "/alerts/{id}/history", Viewer, a.history},
		{http.MethodGet, "/channels", Viewer, a.listChannels},
		{http.MethodPost, "/channels", Admin, a.createChannel},
		{http.MethodGet, "/channels/{id}", Viewer, a.getChannel},
		{http.MethodPut, "/channels/{id}", Admin, a.updateChannel},
		{http.MethodDelete, "/channels/{id}", Admin, a.deleteChannel},
		{http.MethodPost, "/channels/{id}/test", Admin, a.testChannel},
	}
}

type alertRequest struct {
	Name    string          `json:"name"`
	Enabled *bool           `json:"enabled"`
	Doc     json.RawMessage `json:"doc"`
	Streams []string        `json:"streams"` // null or absent: every stream
	Version int             `json:"version"`
}

func (r alertRequest) input() service.AlertInput {
	return service.AlertInput{Name: r.Name, Enabled: r.Enabled, Doc: r.Doc, Streams: r.Streams, Version: r.Version}
}

type groupView struct {
	Labels map[string]string `json:"labels"`
	State  string            `json:"state"`
	Since  time.Time         `json:"since"`
	Value  *float64          `json:"value"`
}

type alertView struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Doc        json.RawMessage `json:"doc"`
	Streams    []string        `json:"streams"` // null: every stream
	State      string          `json:"state"`
	StateSince *time.Time      `json:"stateSince"`
	LastEvalAt *time.Time      `json:"lastEvalAt"`
	LastValue  *float64        `json:"lastValue"`
	LastError  string          `json:"lastError"`
	Groups     []groupView     `json:"groups"`
	Version    int             `json:"version"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

func toGroupViews(s alerting.RuleState) []groupView {
	out := make([]groupView, 0, len(s.Groups))
	for _, g := range s.Groups {
		labels := g.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		out = append(out, groupView{Labels: labels, State: string(g.State), Since: g.Since, Value: g.Value})
	}
	sort.Slice(out, func(i, j int) bool { return alerting.GroupKey(out[i].Labels) < alerting.GroupKey(out[j].Labels) })
	return out
}

func toAlertView(r *meta.AlertRule) alertView {
	return alertView{ID: r.ID, Name: r.Name, Enabled: r.Enabled, Doc: r.Doc, Streams: r.Streams, State: r.State, StateSince: r.StateSince,
		LastEvalAt: r.LastEvalAt, LastValue: r.LastValue, LastError: r.LastError,
		Groups: toGroupViews(alerting.DecodeRuleState(r.EvalState)), Version: r.Version, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (a *AlertsAPI) list(w http.ResponseWriter, r *http.Request) error {
	rules, err := a.Alerts.List(r.Context(), principal(r))
	if err != nil {
		return err
	}
	out := make([]alertView, 0, len(rules))
	for i := range rules {
		out = append(out, toAlertView(&rules[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *AlertsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in alertRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	rule, err := a.Alerts.Create(r.Context(), actor(r, a.ClientIP), principal(r), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAlertView(&rule))
	return nil
}

func (a *AlertsAPI) get(w http.ResponseWriter, r *http.Request) error {
	rule, err := a.Alerts.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAlertView(&rule))
	return nil
}

func (a *AlertsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in alertRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	rule, err := a.Alerts.Update(r.Context(), actor(r, a.ClientIP), principal(r), r.PathValue("id"), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAlertView(&rule))
	return nil
}

func (a *AlertsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Alerts.Delete(r.Context(), actor(r, a.ClientIP), principal(r), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type sampleView struct {
	Labels map[string]string `json:"labels"`
	Value  *float64          `json:"value"`
}

type transitionView struct {
	Labels  map[string]string `json:"labels"`
	From    string            `json:"from"`
	To      string            `json:"to"`
	Message string            `json:"message"`
}

type testView struct {
	State       string           `json:"state"`
	Error       string           `json:"error,omitempty"`
	Samples     []sampleView     `json:"samples"`
	Groups      []groupView      `json:"groups"`
	Transitions []transitionView `json:"transitions"`
}

func (a *AlertsAPI) test(w http.ResponseWriter, r *http.Request) error {
	out, err := a.Alerts.Test(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		return err
	}
	v := testView{State: string(out.State.Aggregate()), Samples: []sampleView{}, Groups: toGroupViews(out.State),
		Transitions: []transitionView{}}
	if out.Err != nil {
		v.Error = out.Err.Error()
	}
	for _, s := range out.Samples {
		v.Samples = append(v.Samples, sampleView{Labels: nonNilLabels(s.Labels), Value: s.Value})
	}
	for _, t := range out.Transitions {
		v.Transitions = append(v.Transitions, transitionView{Labels: nonNilLabels(t.Labels), From: string(t.From),
			To: string(t.To), Message: t.Message})
	}
	writeJSON(w, http.StatusOK, v)
	return nil
}

func nonNilLabels(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

type muteRequest struct {
	Until *time.Time `json:"until"`
}

func (a *AlertsAPI) mute(w http.ResponseWriter, r *http.Request) error {
	var in muteRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	rule, err := a.Alerts.Mute(r.Context(), actor(r, a.ClientIP), principal(r), r.PathValue("id"), in.Until)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAlertView(&rule))
	return nil
}

type historyView struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	FromState string            `json:"fromState"`
	ToState   string            `json:"toState"`
	Value     *float64          `json:"value"`
	Message   string            `json:"message"`
	Group     map[string]string `json:"group"`
}

func (a *AlertsAPI) history(w http.ResponseWriter, r *http.Request) error {
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			return errBadRequest("limit must be a positive integer")
		}
		limit = n
	}
	hs, err := a.Alerts.History(r.Context(), principal(r), r.PathValue("id"), limit)
	if err != nil {
		return err
	}
	out := make([]historyView, 0, len(hs))
	for i := range hs {
		h := &hs[i]
		group := map[string]string{}
		if h.GroupKey != "" {
			_ = json.Unmarshal([]byte(h.GroupKey), &group) // keys are written by alerting.GroupKey
		}
		out = append(out, historyView{ID: h.ID, At: h.At, FromState: h.FromState, ToState: h.ToState, Value: h.Value,
			Message: h.Message, Group: group})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type channelRequest struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"`
	Config  json.RawMessage `json:"config"`
	Version int             `json:"version"`
}

type channelView struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Config    json.RawMessage `json:"config"`
	Version   int             `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func toChannelView(c *meta.Channel) channelView {
	return channelView{ID: c.ID, Name: c.Name, Kind: c.Kind, Config: c.Config, Version: c.Version, CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt}
}

func (a *AlertsAPI) listChannels(w http.ResponseWriter, r *http.Request) error {
	chs, err := a.Channels.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]channelView, 0, len(chs))
	for i := range chs {
		out = append(out, toChannelView(&chs[i]))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *AlertsAPI) createChannel(w http.ResponseWriter, r *http.Request) error {
	var in channelRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := a.Channels.Create(r.Context(), actor(r, a.ClientIP), service.ChannelInput{Name: in.Name, Kind: in.Kind, Config: in.Config})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toChannelView(&c))
	return nil
}

func (a *AlertsAPI) getChannel(w http.ResponseWriter, r *http.Request) error {
	c, err := a.Channels.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toChannelView(&c))
	return nil
}

func (a *AlertsAPI) updateChannel(w http.ResponseWriter, r *http.Request) error {
	var in channelRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	c, err := a.Channels.Update(r.Context(), actor(r, a.ClientIP), r.PathValue("id"),
		service.ChannelInput{Name: in.Name, Kind: in.Kind, Config: in.Config, Version: in.Version})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toChannelView(&c))
	return nil
}

func (a *AlertsAPI) deleteChannel(w http.ResponseWriter, r *http.Request) error {
	if err := a.Channels.Delete(r.Context(), actor(r, a.ClientIP), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *AlertsAPI) testChannel(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Channels.Test(r.Context(), actor(r, a.ClientIP), r.PathValue("id"))
	if err != nil {
		return err
	}
	out := map[string]any{"ok": res.OK}
	if !res.OK {
		out["error"] = res.Error
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
