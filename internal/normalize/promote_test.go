// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	m, err := jsonx.DecodeObject([]byte(s), jsonx.MaxDepth)
	if err != nil {
		t.Fatalf("decode %s: %v", s, err)
	}
	return m
}

func TestPromoteFields(t *testing.T) {
	ts := time.Date(2026, 10, 13, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		in    string
		opts  Options
		check func(t *testing.T, f Fields)
	}{
		{"clef", `{"@t":"2026-10-13T08:00:00Z","@mt":"Hello {Name}","@m":"Hello Bob","@l":"Warning","@x":"boom","@i":"0a1b2c3d","@tr":"4BF92F3577B34DA6A3CE929D0E0E4736","@sp":"00F067AA0BA902B7","Name":"Bob","@@t":"user-t","@r":["x"]}`, Options{CLEF: true},
			func(t *testing.T, f Fields) {
				want := Fields{Timestamp: ts, Template: "Hello {Name}", Message: "Hello Bob", Level: model.Warning, HasLevel: true,
					Exception: "boom", EventType: "0a1b2c3d", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", SpanID: "00f067aa0ba902b7"}
				assertCore(t, f, want)
				assertProps(t, f, []string{"Name", "@t", "@r"}, []string{"@mt", "@m", "@l", "@x", "@i", "@tr", "@sp", "@@t"})
				if f.Properties["@t"] != "user-t" {
					t.Errorf("@@t not unescaped: %v", f.Properties["@t"])
				}
			}},
		{"clef numeric event type and stream", `{"@m":"x","@i":305419896,"@stream":"Orders API"}`, Options{CLEF: true},
			func(t *testing.T, f Fields) {
				if f.EventType != "12345678" || f.Stream != "Orders API" {
					t.Errorf("got %q %q", f.EventType, f.Stream)
				}
				assertProps(t, f, nil, []string{"@stream", "@i"})
			}},
		{"clef unusable reserved keys dropped", `{"@m":"x","@t":"garbage","@i":"zz"}`, Options{CLEF: true},
			func(t *testing.T, f Fields) {
				assertProps(t, f, nil, []string{"@t", "@i"})
				if !f.Timestamp.IsZero() {
					t.Error("garbage timestamp parsed")
				}
			}},
		{"ecs nested", `{"@timestamp":"2026-10-13T08:00:00.000Z","message":"GET /","log":{"level":"error"},"service":{"name":"checkout"},"host":{"name":"web-1"},"trace":{"id":"4bf92f3577b34da6a3ce929d0e0e4736"},"error":{"type":"IOException","message":"disk full"}}`, Options{},
			func(t *testing.T, f Fields) {
				assertCore(t, f, Fields{Timestamp: ts, Message: "GET /", Level: model.Error, HasLevel: true, Service: "checkout",
					Host: "web-1", TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", Exception: "IOException: disk full"})
				assertProps(t, f, []string{"log", "service", "host", "trace", "error"}, []string{"@timestamp", "message"})
			}},
		{"flat dotted keys win", `{"service.name":"flat","service":{"name":"nested"},"msg":"m"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Service != "flat" {
					t.Errorf("service %q", f.Service)
				}
				assertProps(t, f, []string{"service.name", "msg"}, nil)
			}},
		{"syslog severity integer", `{"message":"m","syslog":{"severity":3}}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Level != model.Error {
					t.Errorf("level %v", f.Level)
				}
			}},
		{"severity integer is syslog", `{"message":"m","severity":4}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Level != model.Warning {
					t.Errorf("level %v", f.Level)
				}
			}},
		{"level numeric ordinal", `{"message":"m","level":4}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Level != model.Error {
					t.Errorf("level %v", f.Level)
				}
			}},
		{"level unknown", `{"message":"m","level":"loud"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Level != model.Information || !f.HasLevel {
					t.Errorf("level %v %v", f.Level, f.HasLevel)
				}
			}},
		{"level object", `{"message":"m","level":{"x":1}}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Level != model.Information || !f.HasLevel {
					t.Errorf("level %v", f.Level)
				}
			}},
		{"no level", `{"message":"m"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.HasLevel || f.Level != model.Information {
					t.Error("unexpected level")
				}
			}},
		{"python style", `{"msg":"oops","levelname":"CRITICAL","exc_info":"Traceback...","logger_name":"worker","hostname":"h1","env":"prod","time":1791878400}`, Options{},
			func(t *testing.T, f Fields) {
				assertCore(t, f, Fields{Timestamp: ts, Message: "oops", Level: model.Fatal, HasLevel: true, Exception: "Traceback...",
					Service: "worker", Host: "h1", Environment: "prod"})
			}},
		{"kubernetes service", `{"log":"line","kubernetes":{"labels":{"app":"payments"},"container":{"name":"c"}}}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Service != "payments" || f.Message != "line" {
					t.Errorf("service %q message %q", f.Service, f.Message)
				}
			}},
		{"template only", `{"MessageTemplate":"Hi {X}","X":1}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Template != "Hi {X}" || f.Message != "" || f.FallbackUsed {
					t.Errorf("template %q message %q", f.Template, f.Message)
				}
			}},
		{"fallback json message", `{"a":1,"b":"two"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Message != `{"a":1,"b":"two"}` || !f.FallbackUsed {
					t.Errorf("message %q", f.Message)
				}
			}},
		{"non string message ignored", `{"message":{"x":1},"text":"t"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Message != "t" {
					t.Errorf("message %q", f.Message)
				}
				assertProps(t, f, []string{"message"}, nil)
			}},
		{"exception object", `{"message":"m","exception":{"type":"E"}}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Exception != `{"type":"E"}` {
					t.Errorf("exception %q", f.Exception)
				}
			}},
		{"host object not string", `{"message":"m","host":{"ip":"1.2.3.4"},"MachineName":"PC1"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Host != "PC1" {
					t.Errorf("host %q", f.Host)
				}
			}},
		{"env priority", `{"message":"m","deployment.environment":"staging","environment":"prod"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.Environment != "staging" {
					t.Errorf("env %q", f.Environment)
				}
			}},
		{"non clef keeps @i and _stream", `{"message":"m","@i":"abc","_stream":"s"}`, Options{},
			func(t *testing.T, f Fields) {
				if f.EventType != "" || f.Stream != "" {
					t.Error("clef-only fields promoted")
				}
				assertProps(t, f, []string{"@i", "_stream"}, nil)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := decode(t, tt.in)
			before := jsonx.Compact(obj)
			tt.check(t, Promote(obj, tt.opts))
			if jsonx.Compact(obj) != before {
				t.Error("input mutated")
			}
		})
	}
}

func assertCore(t *testing.T, got, want Fields) {
	t.Helper()
	if !got.Timestamp.Equal(want.Timestamp) || got.Message != want.Message || got.Template != want.Template ||
		got.Level != want.Level || got.HasLevel != want.HasLevel || got.Exception != want.Exception ||
		got.Service != want.Service || got.Environment != want.Environment || got.Host != want.Host ||
		got.TraceID != want.TraceID || got.SpanID != want.SpanID || got.EventType != want.EventType {
		t.Errorf("fields mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func assertProps(t *testing.T, f Fields, present, absent []string) {
	t.Helper()
	for _, k := range present {
		if _, ok := f.Properties[k]; !ok {
			t.Errorf("property %q missing from %v", k, f.Properties)
		}
	}
	for _, k := range absent {
		if _, ok := f.Properties[k]; ok {
			t.Errorf("property %q should be absent", k)
		}
	}
}

func TestFallbackTruncation(t *testing.T) {
	obj := map[string]any{"k": strings.Repeat("é", 3000)}
	f := Promote(obj, Options{})
	if len(f.Message) > MaxFallbackMessage || !strings.HasPrefix(f.Message, `{"k":"é`) {
		t.Errorf("len %d", len(f.Message))
	}
	if !json.Valid([]byte(f.Message+`"}`)) && len(f.Message) == 0 {
		t.Error("unexpected")
	}
	if truncateUTF8("abc", 10) != "abc" {
		t.Error("short string changed")
	}
}

func TestLookup(t *testing.T) {
	obj := map[string]any{"a": map[string]any{"b.c": "x", "d": map[string]any{"e": "y"}}, "a.f": "z"}
	tests := map[string]any{"a.b.c": "x", "a.d.e": "y", "a.f": "z"}
	for k, want := range tests {
		if got, ok := Lookup(obj, k); !ok || got != want {
			t.Errorf("Lookup(%q) = %v,%v", k, got, ok)
		}
	}
	if _, ok := Lookup(obj, "a.zz"); ok {
		t.Error("missing found")
	}
	if _, ok := Lookup(obj, "q.r"); ok {
		t.Error("missing found")
	}
}

func TestEventTypeValue(t *testing.T) {
	tests := map[any]string{"0xABCDEF01": "abcdef01", "1f": "0000001f", "xyz": "", json.Number("16"): "00000010", json.Number("-1"): "", 3.5: ""}
	for in, want := range tests {
		if got := eventTypeValue(in); got != want {
			t.Errorf("eventTypeValue(%v) = %q want %q", in, got, want)
		}
	}
}
