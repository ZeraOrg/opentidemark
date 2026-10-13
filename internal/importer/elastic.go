// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/ZeraOrg/tidemark/internal/ingest/elastic"
	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// Elasticsearch reading modes and keep-alive (SPEC §9.9).
const (
	modePIT        = "pit"
	modeScroll     = "scroll"
	esKeepAlive    = "10m"
	esCloseTimeout = 10 * time.Second
)

// esCheckpoint is the resumable position of an Elasticsearch/OpenSearch import.
//
// LastSort/AtLast (the last time sort value and how many documents carrying it were imported)
// make the import resumable without the original context: a new point in time or scroll is
// opened with the time field >= ResumeFrom (= LastSort), and its first request asks for
// PageSize + AtLast documents and skips the first AtLast, so the next page holds exactly the
// documents the lost context would have returned (same page key ⇒ same insert token).
type esCheckpoint struct {
	Mode        string        `json:"mode,omitempty"`
	PIT         string        `json:"pit,omitempty"`
	ScrollID    string        `json:"scrollId,omitempty"`
	ScrollSize  int           `json:"scrollSize,omitempty"`
	SearchAfter []json.Number `json:"searchAfter,omitempty"`
	LastSort    json.Number   `json:"lastSort,omitempty"`
	AtLast      int64         `json:"atLast,omitempty"`
	ResumeFrom  json.Number   `json:"resumeFrom,omitempty"`
	ResumeSkip  int64         `json:"resumeSkip,omitempty"`
	Total       int64         `json:"total,omitempty"`
}

type esHit struct {
	Index  string          `json:"_index"`
	ID     string          `json:"_id"`
	Source json.RawMessage `json:"_source"`
	Sort   []json.Number   `json:"sort"`
}

type esSearchResponse struct {
	PITID    string `json:"pit_id"`
	ScrollID string `json:"_scroll_id"`
	Hits     struct {
		Total json.RawMessage `json:"total"` // {"value":N} (7+) or N (6.x)
		Hits  []esHit         `json:"hits"`
	} `json:"hits"`
}

// elasticSource reads an index pattern with point in time + search_after, or scroll when the
// cluster has no point-in-time API (OpenSearch, older Elasticsearch).
//
// A scroll context is stateful on the server, so it is only continued within one run while
// no request failed; otherwise the import resumes from LastSort with a new scroll.
type elasticSource struct {
	cfg         ElasticConfig
	req         *requester
	last        esCheckpoint // for Close
	scrollTrust bool         // the stored scroll id belongs to this run's last successful page
}

func newElasticSource(cfg ElasticConfig) (*elasticSource, error) {
	req, err := newRequester(cfg.URL, cfg.Auth, cfg.CACert, nil)
	if err != nil {
		return nil, err
	}
	return &elasticSource{cfg: cfg, req: req}, nil
}

// Next implements Source.
func (s *elasticSource) Next(ctx context.Context, raw json.RawMessage) (Page, error) {
	var cp esCheckpoint
	if err := decodeCheckpoint(raw, &cp); err != nil {
		return Page{}, err
	}
	if cp.Mode == modeScroll && cp.ScrollID != "" && !s.scrollTrust {
		cp = s.reopen(ctx, cp)
	}
	resp, cp, size, err := s.fetch(ctx, cp)
	if err != nil {
		s.scrollTrust = false
		return Page{}, err
	}
	s.scrollTrust = true
	return s.page(ctx, resp, cp, size)
}

// fetch runs the next search, opening a context first when needed and reopening it once when
// it expired. It returns the number of documents requested.
func (s *elasticSource) fetch(ctx context.Context, cp esCheckpoint) (esSearchResponse, esCheckpoint, int, error) {
	if cp.Mode == "" {
		cp = s.open(ctx, cp)
	}
	resp, size, err := s.search(ctx, &cp)
	if err != nil && isStatus(err, http.StatusNotFound) && (cp.PIT != "" || cp.ScrollID != "") {
		cp = s.reopen(ctx, cp)
		resp, size, err = s.search(ctx, &cp)
	}
	if err != nil {
		return esSearchResponse{}, cp, 0, err
	}
	if resp.PITID != "" {
		cp.PIT = resp.PITID
	}
	if resp.ScrollID != "" {
		cp.ScrollID = resp.ScrollID
	}
	if cp.Total == 0 {
		cp.Total = parseTotal(resp.Hits.Total)
	}
	return resp, cp, size, nil
}

// open starts reading: a point in time when available, otherwise a scroll.
func (s *elasticSource) open(ctx context.Context, cp esCheckpoint) esCheckpoint {
	var pit struct {
		ID string `json:"id"`
	}
	path := "/" + url.PathEscape(s.cfg.Index) + "/_pit?keep_alive=" + esKeepAlive
	if err := s.req.do(ctx, http.MethodPost, path, nil, &pit); err == nil && pit.ID != "" {
		cp.Mode, cp.PIT = modePIT, pit.ID
		return cp
	}
	cp.Mode = modeScroll
	return cp
}

// reopen replaces a lost context and resumes after LastSort/AtLast.
func (s *elasticSource) reopen(ctx context.Context, cp esCheckpoint) esCheckpoint {
	cp.PIT, cp.ScrollID, cp.ScrollSize, cp.SearchAfter, cp.Mode = "", "", 0, nil, ""
	cp.ResumeFrom, cp.ResumeSkip = cp.LastSort, cp.AtLast
	return s.open(ctx, cp)
}

// search sends the next request of the context; cp.ScrollSize is set when a scroll opens.
func (s *elasticSource) search(ctx context.Context, cp *esCheckpoint) (esSearchResponse, int, error) {
	var out esSearchResponse
	size := s.cfg.PageSize + int(cp.ResumeSkip)
	var err error
	switch {
	case cp.Mode == modeScroll && cp.ScrollID != "":
		size = cp.ScrollSize
		body := map[string]any{"scroll": esKeepAlive, "scroll_id": cp.ScrollID}
		err = s.req.do(ctx, http.MethodPost, "/_search/scroll", body, &out)
	case cp.Mode == modeScroll:
		cp.ScrollSize = size
		path := "/" + url.PathEscape(s.cfg.Index) + "/_search?scroll=" + esKeepAlive
		err = s.req.do(ctx, http.MethodPost, path, s.body(cp, size, false), &out)
	default:
		err = s.req.do(ctx, http.MethodPost, "/_search", s.body(cp, size, true), &out)
	}
	return out, size, err
}

// body builds the search request: the configured range and query, the resume range, ascending
// time order (plus _shard_doc as the point-in-time tie-breaker).
func (s *elasticSource) body(cp *esCheckpoint, size int, pit bool) map[string]any {
	sort := []any{map[string]any{s.cfg.TimeField: map[string]any{"order": "asc", "unmapped_type": "date"}}}
	b := map[string]any{"size": size, "query": s.query(cp), "track_total_hits": true}
	if pit {
		sort = append(sort, map[string]any{"_shard_doc": "asc"})
		b["pit"] = map[string]any{"id": cp.PIT, "keep_alive": esKeepAlive}
		if len(cp.SearchAfter) > 0 {
			b["search_after"] = cp.SearchAfter
		}
	}
	b["sort"] = sort
	return b
}

func (s *elasticSource) query(cp *esCheckpoint) map[string]any {
	var filters []any
	rng := map[string]any{}
	if s.cfg.From != nil {
		rng["gte"] = s.cfg.From.UTC().Format(time.RFC3339Nano)
	}
	if s.cfg.To != nil {
		rng["lt"] = s.cfg.To.UTC().Format(time.RFC3339Nano)
	}
	if len(rng) > 0 {
		filters = append(filters, map[string]any{"range": map[string]any{s.cfg.TimeField: rng}})
	}
	if cp.ResumeFrom != "" {
		filters = append(filters, map[string]any{"range": map[string]any{s.cfg.TimeField: map[string]any{
			"gte": cp.ResumeFrom.String(), "format": "epoch_millis"}}})
	}
	if s.cfg.Query != nil {
		filters = append(filters, s.cfg.Query)
	}
	if len(filters) == 0 {
		return map[string]any{"match_all": map[string]any{}}
	}
	return map[string]any{"bool": map[string]any{"filter": filters}}
}

// page maps the hits and advances the checkpoint; size is the number of documents requested.
func (s *elasticSource) page(ctx context.Context, resp esSearchResponse, cp esCheckpoint, size int) (Page, error) {
	p := Page{Total: cp.Total}
	key := sha256.New()
	for _, h := range resp.Hits.Hits {
		if cp.ResumeSkip > 0 && len(h.Sort) > 0 && h.Sort[0] == cp.LastSort {
			cp.ResumeSkip-- // imported before the context was lost
			continue
		}
		cp.ResumeSkip = 0
		cp = advanceSort(cp, h.Sort)
		key.Write([]byte(h.Index + "\x00" + h.ID + "\x00"))
		obj, err := jsonx.DecodeObject(h.Source, jsonx.MaxDepth)
		if err != nil {
			p.Skipped++
			p.LastError = fmt.Sprintf("document %s/%s: %v", h.Index, h.ID, err)
			continue
		}
		ev := elastic.EventFromDoc(obj, h.ID)
		ev.Stream = s.streamFor(h.Index)
		p.Events = append(p.Events, ev)
	}
	cp.ResumeSkip = 0
	p.Key = hex.EncodeToString(key.Sum(nil))
	if n := len(resp.Hits.Hits); n > 0 && len(resp.Hits.Hits[n-1].Sort) > 0 {
		cp.SearchAfter = resp.Hits.Hits[n-1].Sort
	}
	p.Done = len(resp.Hits.Hits) < size
	s.last = cp
	if p.Done {
		s.release(ctx, cp)
		cp.PIT, cp.ScrollID = "", ""
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		return Page{}, fmt.Errorf("encode checkpoint: %w", err)
	}
	p.Checkpoint = raw
	return p, nil
}

// advanceSort tracks the last time sort value and how many documents carried it.
func advanceSort(cp esCheckpoint, sort []json.Number) esCheckpoint {
	if len(sort) == 0 {
		return cp
	}
	if sort[0] == cp.LastSort {
		cp.AtLast++
	} else {
		cp.LastSort, cp.AtLast = sort[0], 1
	}
	return cp
}

func (s *elasticSource) streamFor(index string) string {
	if s.cfg.Stream != "" {
		return s.cfg.Stream
	}
	return model.NormalizeStream(elastic.StreamFromIndex(index))
}

// Close implements Source: a finished job releases the point in time or scroll still open; an
// interrupted one keeps it so the resumed job can continue within the keep-alive.
func (s *elasticSource) Close(ctx context.Context, final bool) error {
	if final {
		s.release(ctx, s.last)
	}
	return nil
}

func (s *elasticSource) release(ctx context.Context, cp esCheckpoint) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), esCloseTimeout)
	defer cancel()
	// Best effort: an unreleased context expires on the server after esKeepAlive.
	switch {
	case cp.PIT != "":
		_ = s.req.do(cctx, http.MethodDelete, "/_pit", map[string]any{"id": cp.PIT}, nil)
	case cp.ScrollID != "":
		_ = s.req.do(cctx, http.MethodDelete, "/_search/scroll", map[string]any{"scroll_id": []string{cp.ScrollID}}, nil)
	}
}

func parseTotal(raw json.RawMessage) int64 {
	var obj struct {
		Value int64 `json:"value"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Value > 0 {
		return obj.Value
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	return 0
}

// decodeCheckpoint decodes a stored checkpoint ("" and "{}" = start).
func decodeCheckpoint(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: invalid checkpoint: %w", ErrPermanent, err)
	}
	return nil
}
