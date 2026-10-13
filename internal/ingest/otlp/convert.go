// SPDX-License-Identifier: Apache-2.0

// Package otlp implements OTLP ingestion over gRPC and HTTP (protobuf and JSON) for logs,
// traces and metrics (SPEC §9.4).
package otlp

import (
	"strings"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

// Well-known attribute keys.
const (
	attrStream        = "tidemark.stream"
	attrService       = "service.name"
	attrScopeName     = "otel.scope.name"
	attrEnvNew        = "deployment.environment.name"
	attrEnvOld        = "deployment.environment"
	attrHost          = "host.name"
	attrOrigFormat    = "{OriginalFormat}"
	attrTemplateText  = "message_template.text"
	attrExcType       = "exception.type"
	attrExcMessage    = "exception.message"
	attrExcStacktrace = "exception.stacktrace"
)

// maxAnyValueDepth bounds recursion into nested AnyValues.
const maxAnyValueDepth = 32

// anyValue converts an OTLP AnyValue to a plain Go value.
func anyValue(v *commonpb.AnyValue, depth int) any {
	if v == nil || depth > maxAnyValueDepth {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BytesValue:
		return x.BytesValue
	case *commonpb.AnyValue_ArrayValue:
		out := make([]any, 0, len(x.ArrayValue.GetValues()))
		for _, e := range x.ArrayValue.GetValues() {
			out = append(out, anyValue(e, depth+1))
		}
		return out
	case *commonpb.AnyValue_KvlistValue:
		return kvMap(x.KvlistValue.GetValues(), depth+1)
	default:
		return nil
	}
}

// kvMap converts attributes to a map (later duplicates win).
func kvMap(kvs []*commonpb.KeyValue, depth int) map[string]any {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if kv.GetKey() == "" {
			continue
		}
		m[kv.GetKey()] = anyValue(kv.GetValue(), depth)
	}
	return m
}

// attrs converts top-level attributes.
func attrs(kvs []*commonpb.KeyValue) map[string]any { return kvMap(kvs, 0) }

// resourceInfo is the per-resource context of a request.
type resourceInfo struct {
	attrs   map[string]any
	stream  string
	service string
	env     string
	host    string
}

func newResourceInfo(r *resourcepb.Resource) resourceInfo {
	a := attrs(r.GetAttributes())
	info := resourceInfo{attrs: a, stream: str(a[attrStream]), service: str(a[attrService]),
		env: firstStr(a, attrEnvNew, attrEnvOld), host: str(a[attrHost])}
	delete(a, attrStream)
	return info
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// nanos converts a unix-nanosecond timestamp (0 ⇒ zero time).
func nanos(ns uint64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(ns)).UTC() //nolint:gosec // OTLP timestamps fit in int64 until year 2262
}

// exceptionText builds "type: message\nstacktrace" from OTel exception attributes.
func exceptionText(a map[string]any) string {
	typ, msg, stack := str(a[attrExcType]), str(a[attrExcMessage]), str(a[attrExcStacktrace])
	head := typ
	if msg != "" {
		head = strings.TrimPrefix(typ+": "+msg, ": ")
	}
	switch {
	case head != "" && stack != "":
		return head + "\n" + stack
	case head != "":
		return head
	default:
		return stack
	}
}
