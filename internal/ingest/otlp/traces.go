// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/normalize"
)

// TracesToSpans maps an OTLP traces request to spans (SPEC §9.4 Traces).
func TracesToSpans(req *coltracepb.ExportTraceServiceRequest) []model.Span {
	var out []model.Span
	for _, rs := range req.GetResourceSpans() {
		res := newResourceInfo(rs.GetResource())
		for _, ss := range rs.GetScopeSpans() {
			scope := ss.GetScope().GetName()
			for _, sp := range ss.GetSpans() {
				out = append(out, spanToModel(sp, res, scope))
			}
		}
	}
	return out
}

func spanToModel(sp *tracepb.Span, res resourceInfo, scope string) model.Span {
	a := attrs(sp.GetAttributes())
	if scope != "" {
		a[attrScopeName] = scope
	}
	start, end := nanos(sp.GetStartTimeUnixNano()), nanos(sp.GetEndTimeUnixNano())
	var dur time.Duration
	if !start.IsZero() && end.After(start) {
		dur = end.Sub(start)
	}
	return model.Span{
		TraceID:            normalize.HexID(sp.GetTraceId()),
		SpanID:             normalize.HexID(sp.GetSpanId()),
		ParentSpanID:       normalize.HexID(sp.GetParentSpanId()),
		Stream:             res.stream,
		Service:            res.service,
		Name:               sp.GetName(),
		Kind:               spanKind(sp.GetKind()),
		Start:              start,
		Duration:           dur,
		StatusCode:         statusCode(sp.GetStatus().GetCode()),
		StatusMessage:      sp.GetStatus().GetMessage(),
		Attributes:         a,
		ResourceAttributes: res.attrs,
		Events:             spanEvents(sp.GetEvents()),
		Links:              spanLinks(sp.GetLinks()),
	}
}

func spanKind(k tracepb.Span_SpanKind) string {
	switch k {
	case tracepb.Span_SPAN_KIND_SERVER:
		return model.SpanKindServer
	case tracepb.Span_SPAN_KIND_CLIENT:
		return model.SpanKindClient
	case tracepb.Span_SPAN_KIND_PRODUCER:
		return model.SpanKindProducer
	case tracepb.Span_SPAN_KIND_CONSUMER:
		return model.SpanKindConsumer
	default:
		return model.SpanKindInternal
	}
}

func statusCode(c tracepb.Status_StatusCode) string {
	switch c {
	case tracepb.Status_STATUS_CODE_OK:
		return model.StatusOK
	case tracepb.Status_STATUS_CODE_ERROR:
		return model.StatusError
	default:
		return model.StatusUnset
	}
}

func spanEvents(evs []*tracepb.Span_Event) []model.SpanEvent {
	out := make([]model.SpanEvent, 0, len(evs))
	for _, e := range evs {
		out = append(out, model.SpanEvent{Time: nanos(e.GetTimeUnixNano()), Name: e.GetName(), Attributes: attrs(e.GetAttributes())})
	}
	return out
}

func spanLinks(links []*tracepb.Span_Link) []model.SpanLink {
	out := make([]model.SpanLink, 0, len(links))
	for _, l := range links {
		out = append(out, model.SpanLink{TraceID: normalize.HexID(l.GetTraceId()), SpanID: normalize.HexID(l.GetSpanId()),
			TraceState: l.GetTraceState(), Attributes: attrs(l.GetAttributes())})
	}
	return out
}
