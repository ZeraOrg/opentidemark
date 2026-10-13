// SPDX-License-Identifier: Apache-2.0

package api

import (
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// eventView is the JSON form of a log event (SPEC §11.3).
type eventView struct {
	ID              string         `json:"id"`
	Timestamp       time.Time      `json:"timestamp"`
	IngestedAt      *time.Time     `json:"ingestedAt,omitempty"`
	Stream          string         `json:"stream"`
	Level           string         `json:"level"`
	Service         string         `json:"service"`
	Environment     string         `json:"environment"`
	Host            string         `json:"host"`
	MessageTemplate string         `json:"messageTemplate"`
	Message         string         `json:"message"`
	Exception       string         `json:"exception"`
	EventType       string         `json:"eventType"`
	TraceID         string         `json:"traceId"`
	SpanID          string         `json:"spanId"`
	Protocol        string         `json:"protocol"`
	ConnectionID    string         `json:"connectionId"`
	Properties      map[string]any `json:"properties"`
}

func toEventView(ev model.Event) eventView {
	v := eventView{ID: ev.ID.String(), Timestamp: ev.Timestamp.UTC(), Stream: ev.Stream, Level: ev.Level.String(),
		Service: ev.Service, Environment: ev.Environment, Host: ev.Host, MessageTemplate: ev.MessageTemplate,
		Message: ev.Message, Exception: ev.Exception, EventType: ev.EventType, TraceID: ev.TraceID, SpanID: ev.SpanID,
		Protocol: ev.Protocol, ConnectionID: ev.ConnectionID, Properties: ev.Properties}
	if !ev.IngestedAt.IsZero() {
		t := ev.IngestedAt.UTC()
		v.IngestedAt = &t
	}
	if v.Properties == nil {
		v.Properties = map[string]any{}
	}
	return v
}

func toEventViews(evs []model.Event) []eventView {
	out := make([]eventView, 0, len(evs))
	for _, ev := range evs {
		out = append(out, toEventView(ev))
	}
	return out
}

// spanView is the JSON form of a span.
type spanView struct {
	TraceID            string            `json:"traceId"`
	SpanID             string            `json:"spanId"`
	ParentSpanID       string            `json:"parentSpanId"`
	Stream             string            `json:"stream"`
	Service            string            `json:"service"`
	Name               string            `json:"name"`
	Kind               string            `json:"kind"`
	Start              time.Time         `json:"start"`
	DurationMs         float64           `json:"durationMs"`
	StatusCode         string            `json:"statusCode"`
	StatusMessage      string            `json:"statusMessage"`
	Attributes         map[string]any    `json:"attributes"`
	ResourceAttributes map[string]any    `json:"resourceAttributes"`
	Events             []model.SpanEvent `json:"events"`
	Links              []model.SpanLink  `json:"links"`
}

const nanosPerMilli = float64(time.Millisecond)

func toSpanView(s model.Span) spanView {
	v := spanView{TraceID: s.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID, Stream: s.Stream, Service: s.Service,
		Name: s.Name, Kind: s.Kind, Start: s.Start.UTC(), DurationMs: float64(s.Duration) / nanosPerMilli,
		StatusCode: s.StatusCode, StatusMessage: s.StatusMessage, Attributes: s.Attributes,
		ResourceAttributes: s.ResourceAttributes, Events: s.Events, Links: s.Links}
	if v.Attributes == nil {
		v.Attributes = map[string]any{}
	}
	if v.ResourceAttributes == nil {
		v.ResourceAttributes = map[string]any{}
	}
	if v.Events == nil {
		v.Events = []model.SpanEvent{}
	}
	if v.Links == nil {
		v.Links = []model.SpanLink{}
	}
	return v
}

// statsView is the query cost block.
type statsView struct {
	ElapsedMs         int64  `json:"elapsedMs"`
	RowsRead          uint64 `json:"rowsRead"`
	BytesRead         uint64 `json:"bytesRead"`
	InterpretedAsText bool   `json:"interpretedAsText"`
}

// seriesView is one timeseries.
type seriesView struct {
	Key    map[string]any `json:"key"`
	Points [][]any        `json:"points"`
}

// queryView is the response of POST /query; fields depend on kind (SPEC §11.3). Span is in
// milliseconds. Events holds eventView objects for log events and column-keyed objects for
// spans, metric points and `fields` projections (then Columns is set).
type queryView struct {
	Kind       tql.ResultKind `json:"kind"`
	Events     any            `json:"events,omitempty"`
	NextCursor *string        `json:"nextCursor,omitempty"`
	Span       int64          `json:"span,omitempty"`
	Columns    []tql.Column   `json:"columns,omitempty"`
	Rows       [][]any        `json:"rows,omitempty"`
	Series     []seriesView   `json:"series,omitempty"`
	Stats      statsView      `json:"stats"`
}

func toQueryView(res store.Result) queryView {
	v := queryView{Kind: res.Kind, Stats: statsView{ElapsedMs: res.Stats.ElapsedMs, RowsRead: res.Stats.RowsRead,
		BytesRead: res.Stats.BytesRead, InterpretedAsText: res.Stats.InterpretedAsText}}
	switch res.Kind {
	case tql.KindEvents:
		if res.Records != nil {
			v.Events, v.Columns = res.Records, res.Columns
		} else {
			v.Events = toEventViews(res.Events)
		}
		if res.NextCursor != nil {
			c := service.EncodeCursor(res.NextCursor)
			v.NextCursor = &c
		}
	case tql.KindTimeseries:
		v.Span, v.Columns = res.Span.Milliseconds(), res.Columns
		v.Series = make([]seriesView, 0, len(res.Series))
		for _, s := range res.Series {
			v.Series = append(v.Series, seriesView{Key: s.Key, Points: s.Points})
		}
	default:
		v.Columns, v.Rows = res.Columns, res.Rows
		if v.Rows == nil {
			v.Rows = [][]any{}
		}
	}
	return v
}

// histogramView is the response of GET /histogram (span in milliseconds).
type histogramView struct {
	Span    int64                 `json:"span"`
	Buckets []histogramBucketView `json:"buckets"`
}

type histogramBucketView struct {
	T      time.Time         `json:"t"`
	Counts map[string]uint64 `json:"counts"`
}

func toHistogramView(h store.Histogram) histogramView {
	v := histogramView{Span: h.Span.Milliseconds(), Buckets: make([]histogramBucketView, 0, len(h.Buckets))}
	for _, b := range h.Buckets {
		v.Buckets = append(v.Buckets, histogramBucketView{T: b.T.UTC(), Counts: b.Counts})
	}
	return v
}

type facetValueView struct {
	Value string `json:"value"`
	Count uint64 `json:"count"`
}

type facetView struct {
	Field      string           `json:"field"`
	Values     []facetValueView `json:"values"`
	OtherCount uint64           `json:"otherCount"`
	Sampled    bool             `json:"sampled"`
}

func toFacetViews(fs []service.Facet) []facetView {
	out := make([]facetView, 0, len(fs))
	for _, f := range fs {
		v := facetView{Field: f.Field, OtherCount: f.OtherCount, Sampled: f.Sampled, Values: make([]facetValueView, 0, len(f.Values))}
		for _, fv := range f.Values {
			v.Values = append(v.Values, facetValueView{Value: fv.Value, Count: fv.Count})
		}
		out = append(out, v)
	}
	return out
}

type fieldView struct {
	Name    string `json:"name"`
	Builtin bool   `json:"builtin"`
	Numeric bool   `json:"numeric"`
	Hits    uint64 `json:"hits"`
}

type traceSummaryView struct {
	TraceID     string    `json:"traceId"`
	RootService string    `json:"rootService"`
	RootName    string    `json:"rootName"`
	Start       time.Time `json:"start"`
	DurationMs  float64   `json:"durationMs"`
	SpanCount   uint64    `json:"spanCount"`
	ErrorCount  uint64    `json:"errorCount"`
	Services    []string  `json:"services"`
}

type metricNameView struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Unit        string `json:"unit"`
	Description string `json:"description"`
}

// signalView is the JSON form of a signal; updatedAt is the optimistic-concurrency token.
type signalView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TQL       string    `json:"tql"`
	Color     string    `json:"color"`
	OwnerID   string    `json:"ownerId"`
	Shared    bool      `json:"shared"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toSignalView(s meta.Signal) signalView {
	return signalView{ID: s.ID, Name: s.Name, TQL: s.TQL, Color: s.Color, OwnerID: s.OwnerID, Shared: s.Shared,
		Source: s.Source, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}
