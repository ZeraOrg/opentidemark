// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// WAL record payload (SPEC §9.8): version byte 1 | batch UUID (16 bytes) | JSON array of rows.
// Non-finite floats (not representable in JSON) travel as strings ("NaN", "+Inf", "-Inf").
const (
	payloadVersion = 1
	payloadHeader  = 1 + 16
)

// ErrPayload is returned for WAL payloads that cannot be decoded.
var ErrPayload = errors.New("invalid wal payload")

// Codec converts one enqueued batch to and from a WAL payload.
type Codec[T any] interface {
	Encode(id uuid.UUID, rows []T) ([]byte, error)
	Decode(payload []byte) (uuid.UUID, []T, error)
}

// jsonCodec encodes rows as JSON, mapping each row through a wire form.
type jsonCodec[T, W any] struct {
	toWire   func(T) W
	fromWire func(W) T
}

func (c jsonCodec[T, W]) Encode(id uuid.UUID, rows []T) ([]byte, error) {
	wire := make([]W, len(rows))
	for i := range rows {
		wire[i] = c.toWire(rows[i])
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("encode wal payload: %w", err)
	}
	out := make([]byte, 0, payloadHeader+len(body))
	out = append(out, payloadVersion)
	out = append(out, id[:]...)
	return append(out, body...), nil
}

func (c jsonCodec[T, W]) Decode(p []byte) (uuid.UUID, []T, error) {
	if len(p) < payloadHeader || p[0] != payloadVersion {
		return uuid.Nil, nil, fmt.Errorf("%w: header", ErrPayload)
	}
	id, err := uuid.FromBytes(p[1:payloadHeader])
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("%w: %w", ErrPayload, err)
	}
	dec := json.NewDecoder(bytes.NewReader(p[payloadHeader:]))
	dec.UseNumber()
	var wire []W
	if err := dec.Decode(&wire); err != nil {
		return uuid.Nil, nil, fmt.Errorf("%w: %w", ErrPayload, err)
	}
	rows := make([]T, len(wire))
	for i := range wire {
		rows[i] = c.fromWire(wire[i])
	}
	return id, rows, nil
}

// EventCodec, SpanCodec and MetricCodec are the WAL codecs of the three signals.
func EventCodec() Codec[model.Event] {
	return jsonCodec[model.Event, model.Event]{
		toWire:   func(e model.Event) model.Event { e.Properties = sanitizeMap(e.Properties); return e },
		fromWire: func(e model.Event) model.Event { return e },
	}
}

// SpanCodec encodes spans.
func SpanCodec() Codec[model.Span] {
	return jsonCodec[model.Span, model.Span]{
		toWire: func(s model.Span) model.Span {
			s.Attributes, s.ResourceAttributes = sanitizeMap(s.Attributes), sanitizeMap(s.ResourceAttributes)
			for i := range s.Events {
				s.Events[i].Attributes = sanitizeMap(s.Events[i].Attributes)
			}
			for i := range s.Links {
				s.Links[i].Attributes = sanitizeMap(s.Links[i].Attributes)
			}
			return s
		},
		fromWire: func(s model.Span) model.Span { return s },
	}
}

// wireMetric is model.MetricPoint with JSON-safe floats.
type wireMetric struct {
	model.MetricPoint
	Value        jsonFloat   `json:"Value"`
	Sum          jsonFloat   `json:"Sum"`
	Min          jsonFloat   `json:"Min"`
	Max          jsonFloat   `json:"Max"`
	BucketBounds []jsonFloat `json:"BucketBounds"`
}

// MetricCodec encodes metric points.
func MetricCodec() Codec[model.MetricPoint] {
	return jsonCodec[model.MetricPoint, wireMetric]{
		toWire: func(m model.MetricPoint) wireMetric {
			w := wireMetric{MetricPoint: m, Value: jsonFloat(m.Value), Sum: jsonFloat(m.Sum), Min: jsonFloat(m.Min), Max: jsonFloat(m.Max)}
			w.Attributes = sanitizeMap(m.Attributes)
			if m.BucketBounds != nil {
				w.BucketBounds = make([]jsonFloat, len(m.BucketBounds))
				for i, b := range m.BucketBounds {
					w.BucketBounds[i] = jsonFloat(b)
				}
			}
			return w
		},
		fromWire: func(w wireMetric) model.MetricPoint {
			m := w.MetricPoint
			m.Value, m.Sum, m.Min, m.Max = float64(w.Value), float64(w.Sum), float64(w.Min), float64(w.Max)
			m.BucketBounds = nil
			if w.BucketBounds != nil {
				m.BucketBounds = make([]float64, len(w.BucketBounds))
				for i, b := range w.BucketBounds {
					m.BucketBounds[i] = float64(b)
				}
			}
			return m
		},
	}
}

// jsonFloat marshals non-finite values as strings.
type jsonFloat float64

func (f jsonFloat) MarshalJSON() ([]byte, error) {
	v := float64(f)
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return json.Marshal(strconv.FormatFloat(v, 'g', -1, 64))
	}
	return json.Marshal(v)
}

func (f *jsonFloat) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("float %q: %w", s, err)
	}
	*f = jsonFloat(v)
	return nil
}

// sanitizeMap replaces non-finite floats in nested values by their text form.
func sanitizeMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = sanitizeValue(v)
	}
	return out
}

func sanitizeValue(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return strconv.FormatFloat(x, 'g', -1, 64)
		}
	case float32:
		return sanitizeValue(float64(x))
	case map[string]any:
		return sanitizeMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = sanitizeValue(e)
		}
		return out
	}
	return v
}
