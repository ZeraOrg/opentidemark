// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func TestFinalize(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	tests := []struct {
		name  string
		in    model.Event
		check func(t *testing.T, ev model.Event)
	}{
		{"missing timestamp", model.Event{Message: "m"}, func(t *testing.T, ev model.Event) {
			if !ev.Timestamp.Equal(now) || ev.Properties[PropClockSkew] != nil {
				t.Errorf("ts %v", ev.Timestamp)
			}
		}},
		{"future clamp", model.Event{Message: "m", Timestamp: now.Add(8 * 24 * time.Hour)}, func(t *testing.T, ev model.Event) {
			if !ev.Timestamp.Equal(now) || ev.Properties[PropClockSkew] != true {
				t.Errorf("ts %v props %v", ev.Timestamp, ev.Properties)
			}
		}},
		{"near future kept", model.Event{Message: "m", Timestamp: now.Add(6 * 24 * time.Hour)}, func(t *testing.T, ev model.Event) {
			if !ev.Timestamp.Equal(now.Add(6 * 24 * time.Hour)) {
				t.Errorf("ts %v", ev.Timestamp)
			}
		}},
		{"render template", model.Event{MessageTemplate: "Hi {Name}", Properties: map[string]any{"Name": "Ann"}, Timestamp: past},
			func(t *testing.T, ev model.Event) {
				if ev.Message != "Hi Ann" || ev.EventType != model.ComputeEventType("Hi {Name}", "") {
					t.Errorf("message %q type %q", ev.Message, ev.EventType)
				}
			}},
		{"keeps event type and id", model.Event{Message: "m", EventType: "deadbeef", ID: uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")},
			func(t *testing.T, ev model.Event) {
				if ev.EventType != "deadbeef" || ev.ID.String() != "01890a5d-ac96-774b-bcce-b302099a8057" {
					t.Error("overwritten")
				}
			}},
		{"invalid level", model.Event{Message: "m", Level: model.Level(9)}, func(t *testing.T, ev model.Event) {
			if ev.Level != model.Information {
				t.Errorf("level %v", ev.Level)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.in
			Finalize(&ev, now)
			if ev.ID == uuid.Nil || ev.ID.Version() != 7 && tt.in.ID == uuid.Nil {
				t.Errorf("id %v", ev.ID)
			}
			if !ev.IngestedAt.Equal(now) || ev.Properties == nil {
				t.Error("ingested/properties")
			}
			tt.check(t, ev)
		})
	}
}

func TestEventFromFields(t *testing.T) {
	f := Fields{Message: "m", Level: model.Error, Service: "s", Stream: "x", Properties: map[string]any{"a": 1}}
	ev := EventFromFields(f, model.ProtocolNative)
	if ev.Message != "m" || ev.Level != model.Error || ev.Service != "s" || ev.Protocol != "native" || ev.Stream != "x" {
		t.Errorf("%+v", ev)
	}
}
