// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

func TestStreamCond(t *testing.T) {
	tests := []struct {
		name  string
		scope model.StreamScope
		sql   string
		args  []any
		err   error
	}{
		{"unset", model.StreamScope{}, "", nil, store.ErrNoScope},
		{"all", model.AllStreams(), "1", nil, nil},
		{"none", model.NoStreams(), "0", nil, nil},
		{"one", model.OnlyStreams("a"), "stream IN (?)", []any{"a"}, nil},
		{"injection only as args", model.OnlyStreams("b'); DROP TABLE events; --", "a"), "stream IN (?, ?)",
			[]any{"a", "b'); DROP TABLE events; --"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sql, args, err := streamCond(tc.scope)
			if sql != tc.sql || !slices.Equal(args, tc.args) || !errors.Is(err, tc.err) {
				t.Errorf("streamCond = %q %v %v, want %q %v %v", sql, args, err, tc.sql, tc.args, tc.err)
			}
		})
	}
}

// TestReadsWithoutScopeFailClosed checks that every read rejects an unset scope before it
// reaches the connection (nil here).
func TestReadsWithoutScopeFailClosed(t *testing.T) {
	ctx := context.Background()
	r, err := NewReader(nil, ReaderConfig{Database: "tidemark", QueryTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewAdmin(nil, "tidemark", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	var unset model.StreamScope
	f := store.Filter{Source: model.SourceLogs, Where: "1"}
	rng := store.TimeRange{From: time.Now().Add(-time.Hour), To: time.Now()}
	reads := map[string]func() error{
		"Histogram": func() error { _, err := r.Histogram(ctx, f, time.Minute); return err },
		"Newest":    func() error { _, err := r.Newest(ctx, f, 1); return err },
		"Tail":      func() error { _, _, err := r.Tail(ctx, f, rng.From, rng.To, 1); return err },
		"Traces":    func() error { _, err := r.Traces(ctx, f, 1); return err },
		"Fields":    func() error { _, err := r.Fields(ctx, unset, model.SourceLogs, rng); return err },
		"Event":     func() error { _, err := r.Event(ctx, unset, uuid.Must(uuid.NewV7())); return err },
		"Context":   func() error { _, _, err := r.Context(ctx, unset, model.Event{}, 1, 1); return err },
		"Trace":     func() error { _, _, err := r.Trace(ctx, unset, "aa"); return err },
		"MetricNames": func() error {
			_, err := r.MetricNames(ctx, unset, "", rng, 1)
			return err
		},
		"Mappings": func() error { _, err := a.Mappings(ctx, unset, "default"); return err },
	}
	for name, read := range reads {
		if err := read(); !errors.Is(err, store.ErrNoScope) {
			t.Errorf("%s without scope: %v, want ErrNoScope", name, err)
		}
	}
}
