// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/service"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// StreamsAPI serves /streams, /indexed-fields and /purge (SPEC §11.5).
type StreamsAPI struct {
	Streams  *service.StreamAdmin
	Indexed  *service.IndexedFields
	Purges   *service.Purges
	ClientIP func(*http.Request) netip.Addr
}

// Routes implements Registrar.
func (a *StreamsAPI) Routes() []Route {
	return []Route{
		{http.MethodGet, "/streams", Viewer, a.list},
		{http.MethodPost, "/streams", Admin, a.create},
		{http.MethodPatch, "/streams/{name}", Admin, a.update},
		{http.MethodDelete, "/streams/{name}", Admin, a.delete},
		{http.MethodGet, "/streams/{name}/fields", Viewer, a.fields},
		{http.MethodGet, "/indexed-fields", Viewer, a.listIndexed},
		{http.MethodPost, "/indexed-fields", Admin, a.createIndexed},
		{http.MethodDelete, "/indexed-fields/{id}", Admin, a.deleteIndexed},
		{http.MethodPost, "/purge", Admin, a.purge},
		{http.MethodGet, "/purge/{id}", Admin, a.purgeStatus},
	}
}

// optionalInt distinguishes an absent field from an explicit null.
type optionalInt struct {
	set   bool
	value *int
}

func (o *optionalInt) UnmarshalJSON(b []byte) error {
	o.set = true
	if string(b) == "null" {
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	o.value = &n
	return nil
}

func (o optionalInt) ptr() **int {
	if !o.set {
		return nil
	}
	return &o.value
}

type levelRuleJSON struct {
	Levels []string `json:"levels"`
	Days   int      `json:"days"`
}

type streamRequest struct {
	Name                 string           `json:"name"`
	Description          *string          `json:"description"`
	RetentionDays        optionalInt      `json:"retentionDays"`
	SpansRetentionDays   optionalInt      `json:"spansRetentionDays"`
	MetricsRetentionDays optionalInt      `json:"metricsRetentionDays"`
	LevelRules           *[]levelRuleJSON `json:"levelRules"`
	Paused               *bool            `json:"paused"`
}

func (s streamRequest) input() service.StreamInput {
	in := service.StreamInput{Name: s.Name, Description: s.Description, RetentionDays: s.RetentionDays.ptr(),
		SpansRetentionDays: s.SpansRetentionDays.ptr(), MetricsRetentionDays: s.MetricsRetentionDays.ptr(), Paused: s.Paused}
	if s.LevelRules != nil {
		rules := make([]meta.LevelRule, 0, len(*s.LevelRules))
		for _, r := range *s.LevelRules {
			rules = append(rules, meta.LevelRule{Levels: r.Levels, Days: r.Days})
		}
		in.LevelRules = &rules
	}
	return in
}

// sourceStatsView is the statistics of one stream and source.
type sourceStatsView struct {
	Rows              uint64    `json:"rows"`
	Oldest            time.Time `json:"oldest"`
	Newest            time.Time `json:"newest"`
	RatePer5Min       float64   `json:"ratePer5Min"`
	RatePer24h        float64   `json:"ratePer24h"`
	CompressedBytes   uint64    `json:"compressedBytes"`
	UncompressedBytes uint64    `json:"uncompressedBytes"`
	CompressionRatio  float64   `json:"compressionRatio"`
	Estimated         bool      `json:"estimated"`
	TopServices       []string  `json:"topServices"`
}

type streamView struct {
	ID                   string                     `json:"id"`
	Name                 string                     `json:"name"`
	Description          string                     `json:"description"`
	RetentionDays        *int                       `json:"retentionDays"`
	SpansRetentionDays   *int                       `json:"spansRetentionDays"`
	MetricsRetentionDays *int                       `json:"metricsRetentionDays"`
	LevelRules           []levelRuleJSON            `json:"levelRules"`
	Paused               bool                       `json:"paused"`
	CreatedAt            time.Time                  `json:"createdAt"`
	UpdatedAt            time.Time                  `json:"updatedAt"`
	Stats                map[string]sourceStatsView `json:"stats,omitempty"`
}

func toStreamView(s meta.Stream, stats map[model.Source]store.StreamStats) streamView {
	v := streamView{ID: s.ID, Name: s.Name, Description: s.Description, RetentionDays: s.RetentionDays,
		SpansRetentionDays: s.SpansRetentionDays, MetricsRetentionDays: s.MetricsRetentionDays, Paused: s.Paused,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, LevelRules: []levelRuleJSON{}}
	for _, r := range s.LevelRules {
		v.LevelRules = append(v.LevelRules, levelRuleJSON(r))
	}
	if stats != nil {
		v.Stats = map[string]sourceStatsView{}
		for src := range stats {
			st := stats[src]
			sv := sourceStatsView{Rows: st.Rows, Oldest: st.Oldest, Newest: st.Newest, RatePer5Min: st.RatePer5Min,
				RatePer24h: st.RatePer24h, CompressedBytes: st.CompressedBytes, UncompressedBytes: st.UncompressedBytes,
				Estimated: st.Estimated, TopServices: st.TopServices}
			if st.CompressedBytes > 0 {
				sv.CompressionRatio = float64(st.UncompressedBytes) / float64(st.CompressedBytes)
			}
			if sv.TopServices == nil {
				sv.TopServices = []string{}
			}
			v.Stats[string(src)] = sv
		}
	}
	return v
}

type streamListView struct {
	Streams        []streamView `json:"streams"`
	StatsUpdatedAt *time.Time   `json:"statsUpdatedAt"`
}

func (a *StreamsAPI) list(w http.ResponseWriter, r *http.Request) error {
	streams, at, err := a.Streams.List(r.Context(), principal(r))
	if err != nil {
		return err
	}
	out := streamListView{Streams: make([]streamView, 0, len(streams))}
	if !at.IsZero() {
		out.StatsUpdatedAt = &at
	}
	for i := range streams {
		out.Streams = append(out.Streams, toStreamView(streams[i].Stream, streams[i].Stats))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (a *StreamsAPI) create(w http.ResponseWriter, r *http.Request) error {
	var in streamRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	s, err := a.Streams.Create(r.Context(), actor(r, a.ClientIP), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toStreamView(s, nil))
	return nil
}

func (a *StreamsAPI) update(w http.ResponseWriter, r *http.Request) error {
	var in streamRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	if in.Name != "" {
		return errBadRequest("name cannot be changed")
	}
	s, err := a.Streams.Update(r.Context(), actor(r, a.ClientIP), r.PathValue("name"), in.input())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toStreamView(s, nil))
	return nil
}

type confirmRequest struct {
	Confirm string `json:"confirm"`
}

func (a *StreamsAPI) delete(w http.ResponseWriter, r *http.Request) error {
	var in confirmRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	if err := a.Streams.Delete(r.Context(), actor(r, a.ClientIP), r.PathValue("name"), in.Confirm); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type fieldMappingView struct {
	Source    string    `json:"source"`
	Key       string    `json:"key"`
	Type      string    `json:"type"`
	Hits      uint64    `json:"hits"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	Indexed   bool      `json:"indexed"`
}

func (a *StreamsAPI) fields(w http.ResponseWriter, r *http.Request) error {
	maps, err := a.Streams.Fields(r.Context(), principal(r), r.PathValue("name"))
	if err != nil {
		return err
	}
	out := make([]fieldMappingView, 0, len(maps))
	for _, m := range maps {
		out = append(out, fieldMappingView{Source: string(m.Source), Key: m.Key, Type: m.Type, Hits: m.Hits,
			FirstSeen: m.FirstSeen, LastSeen: m.LastSeen, Indexed: m.Indexed})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type indexedFieldView struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Key       string    `json:"key"`
	Column    string    `json:"column"`
	CreatedAt time.Time `json:"createdAt"`
}

func toIndexedView(f meta.IndexedField) indexedFieldView {
	return indexedFieldView{ID: f.ID, Source: f.Source, Key: f.Key, Column: f.ColumnName, CreatedAt: f.CreatedAt}
}

func (a *StreamsAPI) listIndexed(w http.ResponseWriter, r *http.Request) error {
	fields, err := a.Indexed.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]indexedFieldView, 0, len(fields))
	for _, f := range fields {
		out = append(out, toIndexedView(f))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type indexedFieldRequest struct {
	Source string `json:"source"`
	Key    string `json:"key"`
}

func (a *StreamsAPI) createIndexed(w http.ResponseWriter, r *http.Request) error {
	var in indexedFieldRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	f, err := a.Indexed.Create(r.Context(), actor(r, a.ClientIP), model.Source(in.Source), in.Key)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toIndexedView(f))
	return nil
}

func (a *StreamsAPI) deleteIndexed(w http.ResponseWriter, r *http.Request) error {
	if err := a.Indexed.Delete(r.Context(), actor(r, a.ClientIP), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type purgeRequest struct {
	Stream string `json:"stream"`
	Source string `json:"source"`
	Q      string `json:"q"`
	From   string `json:"from"`
	To     string `json:"to"`
	DryRun bool   `json:"dryRun"`
}

type purgeJobView struct {
	ID                string    `json:"id"`
	Stream            string    `json:"stream"`
	Source            string    `json:"source"`
	Q                 string    `json:"q"`
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
	Status            string    `json:"status"`
	RowsEstimate      int64     `json:"rowsEstimate"`
	RequestedBy       string    `json:"requestedBy"`
	DroppedPartitions string    `json:"droppedPartitions"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

func toPurgeJobView(j meta.PurgeJob) purgeJobView {
	return purgeJobView{ID: j.ID, Stream: j.Stream, Source: j.Source, Q: j.TQL, From: j.From, To: j.To, Status: j.Status,
		RowsEstimate: j.RowsEstimate, RequestedBy: j.RequestedBy, DroppedPartitions: j.MutationID,
		CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
}

func (a *StreamsAPI) purge(w http.ResponseWriter, r *http.Request) error {
	var in purgeRequest
	if err := decode(r, &in); err != nil {
		return err
	}
	now := time.Now()
	from, err := ParseTimeParam(in.From, now)
	if err != nil {
		return err
	}
	to, err := ParseTimeParam(in.To, now)
	if err != nil {
		return err
	}
	out, err := a.Purges.Request(r.Context(), actor(r, a.ClientIP), service.PurgeInput{Stream: in.Stream,
		Source: model.Source(in.Source), Query: in.Q, From: from, To: to, DryRun: in.DryRun})
	if err != nil {
		return err
	}
	if out.Job == nil {
		writeJSON(w, http.StatusOK, map[string]any{"dryRun": true, "rowsEstimate": out.RowsEstimate})
		return nil
	}
	writeJSON(w, http.StatusAccepted, toPurgeJobView(*out.Job))
	return nil
}

func (a *StreamsAPI) purgeStatus(w http.ResponseWriter, r *http.Request) error {
	j, err := a.Purges.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toPurgeJobView(j))
	return nil
}
