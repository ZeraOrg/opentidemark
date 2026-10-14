// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func TestAutoSpan(t *testing.T) {
	tests := []struct {
		rng    time.Duration
		target int
		want   time.Duration
	}{
		{time.Hour, 80, time.Minute},
		{time.Hour, 60, time.Minute},
		{time.Hour, 4, 30 * time.Minute},
		{time.Minute, 80, time.Second},
		{60 * 24 * time.Hour, 80, 24 * time.Hour},
		{400 * 24 * time.Hour, 80, 5 * 24 * time.Hour},
		{time.Hour, 0, time.Hour},
	}
	for _, tt := range tests {
		if got := AutoSpan(tt.rng, tt.target); got != tt.want {
			t.Errorf("AutoSpan(%v, %d) = %v, want %v", tt.rng, tt.target, got, tt.want)
		}
	}
}

func TestBuiltinFields(t *testing.T) {
	byName := map[string]FieldInfo{}
	for _, f := range BuiltinFields(model.SourceSpans) {
		if !f.Builtin {
			t.Errorf("%s not marked builtin", f.Name)
		}
		byName[f.Name] = f
	}
	if !byName["duration"].Numeric || byName["service"].Numeric || len(byName) != len(spansSchema().builtins) {
		t.Errorf("span builtins %+v", byName)
	}
	metrics := map[string]bool{}
	for _, f := range BuiltinFields(model.SourceMetrics) {
		metrics[f.Name] = f.Numeric
	}
	if !metrics["value"] || metrics["name"] {
		t.Errorf("metric builtins %v", metrics)
	}
}

// TestConnectionIDBuiltin: every source can filter by the ingesting connection (SPEC §10.4).
func TestConnectionIDBuiltin(t *testing.T) {
	for _, src := range sources() {
		found := false
		for _, f := range BuiltinFields(src) {
			found = found || f.Name == "connection_id"
		}
		if !found {
			t.Errorf("%s: no connection_id built-in", src)
		}
	}
}
