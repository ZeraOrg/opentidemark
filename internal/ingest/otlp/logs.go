// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"maps"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/normalize"
)

// LogsToEvents maps an OTLP logs request to events (SPEC §9.4 Logs).
func LogsToEvents(req *collogspb.ExportLogsServiceRequest) []model.Event {
	var out []model.Event
	for _, rl := range req.GetResourceLogs() {
		res := newResourceInfo(rl.GetResource())
		for _, sl := range rl.GetScopeLogs() {
			scope := sl.GetScope().GetName()
			for _, lr := range sl.GetLogRecords() {
				out = append(out, logRecordToEvent(lr, res, scope))
			}
		}
	}
	return out
}

func logRecordToEvent(lr *logspb.LogRecord, res resourceInfo, scope string) model.Event {
	props := make(map[string]any, len(res.attrs)+len(lr.GetAttributes())+1)
	maps.Copy(props, res.attrs)
	message := ""
	switch body := anyValue(lr.GetBody(), 0).(type) {
	case nil:
	case string:
		message = body
	case map[string]any:
		maps.Copy(props, body)
		message = firstStr(body, "message", "msg")
	default:
		message = normalize.Text(body)
	}
	recAttrs := attrs(lr.GetAttributes())
	maps.Copy(props, recAttrs)
	if scope != "" {
		props[attrScopeName] = scope
	}
	ts := nanos(lr.GetTimeUnixNano())
	if ts.IsZero() {
		ts = nanos(lr.GetObservedTimeUnixNano())
	}
	return model.Event{
		Timestamp:       ts,
		Level:           model.OTLPLevel(int32(lr.GetSeverityNumber()), lr.GetSeverityText()),
		Service:         firstNonEmpty(str(props[attrService]), res.service),
		Environment:     firstNonEmpty(firstStr(props, attrEnvNew, attrEnvOld), res.env),
		Host:            firstNonEmpty(str(props[attrHost]), res.host),
		MessageTemplate: firstStr(recAttrs, attrOrigFormat, attrTemplateText),
		Message:         message,
		Exception:       exceptionText(props),
		TraceID:         normalize.HexID(lr.GetTraceId()),
		SpanID:          normalize.HexID(lr.GetSpanId()),
		Protocol:        model.ProtocolOTLP,
		Stream:          res.stream,
		Properties:      props,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
