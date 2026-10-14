// SPDX-License-Identifier: Apache-2.0

package normalize

import (
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// MaxFutureSkew is how far in the future a timestamp may be before it is clamped (SPEC §9.1).
const MaxFutureSkew = 7 * 24 * time.Hour

// Internal property keys added by OpenTidemark.
const (
	PropClockSkew = "_tidemark.clock_skew"
	PropTruncated = "_tidemark.truncated"
)

// EventFromFields builds an event from promoted fields for the given protocol.
func EventFromFields(f Fields, protocol string) model.Event {
	return model.Event{
		Timestamp:       f.Timestamp,
		Level:           f.Level,
		Service:         f.Service,
		Environment:     f.Environment,
		Host:            f.Host,
		MessageTemplate: f.Template,
		Message:         f.Message,
		Exception:       f.Exception,
		EventType:       f.EventType,
		TraceID:         f.TraceID,
		SpanID:          f.SpanID,
		Protocol:        protocol,
		Properties:      f.Properties,
		Stream:          f.Stream,
	}
}

// Finalize fills generated fields of an event: id (UUIDv7), ingest time, missing or
// far-future timestamps, rendered message and event type.
func Finalize(ev *model.Event, now time.Time) {
	now = now.UTC()
	if ev.ID == uuid.Nil {
		ev.ID = newID()
	}
	ev.IngestedAt = now
	if ev.Properties == nil {
		ev.Properties = map[string]any{}
	}
	switch {
	case ev.Timestamp.IsZero():
		ev.Timestamp = now
	case ev.Timestamp.After(now.Add(MaxFutureSkew)):
		ev.Timestamp = now
		ev.Properties[PropClockSkew] = true
	default:
		ev.Timestamp = ev.Timestamp.UTC()
	}
	if ev.Message == "" && ev.MessageTemplate != "" {
		ev.Message = Render(ev.MessageTemplate, ev.Properties)
	}
	if !ev.Level.Valid() {
		ev.Level = model.Information
	}
	if ev.EventType == "" {
		ev.EventType = model.ComputeEventType(ev.MessageTemplate, ev.Message)
	}
}

// newID returns a UUIDv7; it falls back to a random v4 id if the v7 generator fails.
func newID() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}
