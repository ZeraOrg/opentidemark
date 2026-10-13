// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZeraOrg/tidemark/internal/jsonx"
	"github.com/ZeraOrg/tidemark/internal/model"
)

// MaxFallbackMessage is the maximum size of the JSON message used when no message is found.
const MaxFallbackMessage = 2048

// Candidate keys per promoted field, in priority order (SPEC §9.6).
var (
	timestampKeys   = []string{"@t", "@timestamp", "timestamp", "time", "ts", "Timestamp", "date"}
	messageKeys     = []string{"@m", "message", "msg", "Message", "RenderedMessage", "log", "text"}
	templateKeys    = []string{"@mt", "MessageTemplate", "message_template", "{OriginalFormat}"}
	levelKeys       = []string{"@l", "level", "log.level", "Level", "severity", "severity_text", "levelname", "log_level", "loglevel", "syslog.severity"}
	exceptionKeys   = []string{"@x", "exception", "Exception", "error.stack_trace", "stack_trace", "stacktrace", "exc_info", "error.message"}
	serviceKeys     = []string{"service.name", "service", "Service", "ServiceName", "Application", "application", "app", "app.name", "kubernetes.labels.app", "kubernetes.container.name", "container.name", "logger_name"}
	environmentKeys = []string{"deployment.environment.name", "deployment.environment", "environment", "Environment", "env"}
	hostKeys        = []string{"host.name", "host.hostname", "hostname", "host", "MachineName", "agent.hostname"}
	traceIDKeys     = []string{"@tr", "trace.id", "trace_id", "traceId", "TraceId"}
	spanIDKeys      = []string{"@sp", "span.id", "span_id", "spanId", "SpanId"}
	streamKeys      = []string{"@stream", "_stream"}
)

// consumedKeys are removed from properties when they were used for promotion.
var consumedKeys = map[string]bool{
	"@t": true, "@m": true, "@mt": true, "@l": true, "@x": true, "@i": true, "@tr": true, "@sp": true,
	"@timestamp": true, "message": true, "@stream": true, "_stream": true,
}

// clefReserved are the CLEF keys that never become properties in CLEF mode.
var clefReserved = map[string]bool{
	"@t": true, "@m": true, "@mt": true, "@l": true, "@x": true, "@i": true, "@tr": true, "@sp": true,
	"@stream": true,
}

// syslogSeverityKeys hold syslog severities when their value is an integer.
var syslogSeverityKeys = map[string]bool{"syslog.severity": true, "severity": true}

// Options controls protocol-specific promotion behaviour.
type Options struct {
	// CLEF enables Seq/CLEF conventions: "@@key" escapes, "@i" event type and the
	// "@stream"/"_stream" stream selector.
	CLEF bool
}

// Fields is the result of promoting well-known keys of an input object.
type Fields struct {
	Timestamp    time.Time // zero when absent or unparseable
	Message      string
	Template     string
	Level        model.Level
	HasLevel     bool
	Exception    string
	Service      string
	Environment  string
	Host         string
	TraceID      string
	SpanID       string
	EventType    string
	Stream       string // explicit stream selector (CLEF mode), "" if none
	Properties   map[string]any
	FallbackUsed bool // message was synthesized from the object's JSON
}

// Promote extracts the well-known fields of obj per SPEC §9.6. obj is not modified.
func Promote(obj map[string]any, opts Options) Fields {
	used := map[string]bool{}
	f := Fields{Level: model.Information}
	f.Timestamp = promoteTime(obj, used)
	f.Template = firstString(obj, templateKeys, used)
	f.Message = firstString(obj, messageKeys, used)
	f.Level, f.HasLevel = promoteLevel(obj, used)
	f.Exception = promoteException(obj, used)
	f.Service = firstString(obj, serviceKeys, used)
	f.Environment = firstString(obj, environmentKeys, used)
	f.Host = firstString(obj, hostKeys, used)
	f.TraceID = NormalizeTraceID(firstString(obj, traceIDKeys, used))
	f.SpanID = NormalizeSpanID(firstString(obj, spanIDKeys, used))
	if opts.CLEF {
		f.EventType = eventTypeValue(obj["@i"])
		if f.EventType != "" {
			used["@i"] = true
		}
		f.Stream = firstString(obj, streamKeys, used)
	}
	f.Properties = buildProperties(obj, used, opts)
	if f.Message == "" && f.Template == "" {
		f.Message = truncateUTF8(jsonx.Compact(obj), MaxFallbackMessage)
		f.FallbackUsed = true
	}
	return f
}

// Lookup finds key in obj; dotted keys also match nested objects ("a.b" ⇒ obj["a"]["b"]).
func Lookup(obj map[string]any, key string) (any, bool) {
	if v, ok := obj[key]; ok {
		return v, true
	}
	for i := 0; i < len(key); i++ {
		if key[i] != '.' {
			continue
		}
		sub, ok := obj[key[:i]].(map[string]any)
		if !ok {
			continue
		}
		if v, ok := Lookup(sub, key[i+1:]); ok {
			return v, true
		}
	}
	return nil, false
}

func firstString(obj map[string]any, keys []string, used map[string]bool) string {
	for _, k := range keys {
		v, ok := Lookup(obj, k)
		if !ok {
			continue
		}
		if s, isStr := v.(string); isStr && s != "" {
			used[k] = true
			return s
		}
	}
	return ""
}

func promoteTime(obj map[string]any, used map[string]bool) time.Time {
	for _, k := range timestampKeys {
		v, ok := Lookup(obj, k)
		if !ok {
			continue
		}
		if t, ok := ParseTime(v); ok {
			used[k] = true
			return t
		}
	}
	return time.Time{}
}

func promoteLevel(obj map[string]any, used map[string]bool) (model.Level, bool) {
	for _, k := range levelKeys {
		v, ok := Lookup(obj, k)
		if !ok || v == nil {
			continue
		}
		used[k] = true
		if n, isInt := isInteger(v); isInt && syslogSeverityKeys[k] {
			l, _ := model.SyslogLevel(int(n))
			return l, true
		}
		s, ok := Scalar(v)
		if !ok {
			return model.Information, true
		}
		return model.ParseLevelOrDefault(s), true
	}
	return model.Information, false
}

func promoteException(obj map[string]any, used map[string]bool) string {
	for _, k := range exceptionKeys {
		v, ok := Lookup(obj, k)
		if !ok || v == nil || v == "" {
			continue
		}
		used[k] = true
		text := Text(v)
		if k == "error.message" {
			if typ, ok := Lookup(obj, "error.type"); ok {
				if ts, isStr := typ.(string); isStr && ts != "" {
					text = ts + ": " + text
				}
			}
		}
		return text
	}
	return ""
}

// eventTypeValue converts a CLEF "@i" value (hex string or number) to 8 lower-case hex chars.
func eventTypeValue(v any) string {
	switch x := v.(type) {
	case string:
		s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(x)), "0x")
		if n, err := strconv.ParseUint(s, 16, 32); err == nil {
			return fmt.Sprintf("%08x", n)
		}
	default:
		if n, ok := isInteger(v); ok && n >= 0 {
			return fmt.Sprintf("%08x", n&0xffffffff) // low 32 bits, as the former uint32 conversion
		}
	}
	return ""
}

func buildProperties(obj map[string]any, used map[string]bool, opts Options) map[string]any {
	props := make(map[string]any, len(obj))
	for k, v := range obj {
		if used[k] && consumedKeys[k] {
			continue
		}
		if opts.CLEF && strings.HasPrefix(k, "@@") {
			props[k[1:]] = v
			continue
		}
		if opts.CLEF && clefReserved[k] {
			continue // reserved CLEF keys are never properties, even when unusable
		}
		props[k] = v
	}
	return props
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
