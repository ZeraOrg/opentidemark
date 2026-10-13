// SPDX-License-Identifier: Apache-2.0

package retention

import (
	"errors"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func ptr(n int) *int { return &n }

func TestBuildSpecExample(t *testing.T) {
	p := Policy{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{
		Name: "api", LogsDays: ptr(90),
		LevelRules: []LevelRule{{Levels: []model.Level{model.Debug, model.Verbose, model.Debug}, Days: 3}},
	}}}
	rules, err := Build(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "toDateTime(timestamp) + toIntervalDay(3) DELETE WHERE stream = 'api' AND level_num IN (0,1),\n" +
		"    toDateTime(timestamp) + toIntervalDay(90) DELETE WHERE stream = 'api',\n" +
		"    toDateTime(timestamp) + toIntervalDay(30) DELETE WHERE stream NOT IN ('api')"
	if rules[0].Table != TableEvents || rules[0].TTL != want {
		t.Errorf("events TTL:\n%s\nwant:\n%s", rules[0].TTL, want)
	}
	if rules[1].Table != TableSpans || rules[1].TTL != "toDateTime(start_time) + toIntervalDay(7)" {
		t.Errorf("spans TTL %q", rules[1].TTL)
	}
	if rules[2].Table != TableMetrics || rules[2].TTL != "toDateTime(timestamp) + toIntervalDay(30)" {
		t.Errorf("metrics TTL %q", rules[2].TTL)
	}
}

func TestBuildVariants(t *testing.T) {
	tests := []struct {
		name    string
		streams []StreamPolicy
		table   int
		want    string
	}{
		{"defaults only", nil, 0, "toDateTime(timestamp) + toIntervalDay(30)"},
		{"level rule without days override", []StreamPolicy{{Name: "web", LevelRules: []LevelRule{{Levels: []model.Level{model.Verbose}, Days: 1}}}}, 0,
			"toDateTime(timestamp) + toIntervalDay(1) DELETE WHERE stream = 'web' AND level_num IN (0),\n    toDateTime(timestamp) + toIntervalDay(30)"},
		{"sorted streams", []StreamPolicy{{Name: "zeta", SpansDays: ptr(2)}, {Name: "alpha", SpansDays: ptr(14)}}, 1,
			"toDateTime(start_time) + toIntervalDay(14) DELETE WHERE stream = 'alpha',\n    toDateTime(start_time) + toIntervalDay(2) DELETE WHERE stream = 'zeta',\n    toDateTime(start_time) + toIntervalDay(7) DELETE WHERE stream NOT IN ('alpha', 'zeta')"},
		{"metrics override", []StreamPolicy{{Name: "m.1", MetricsDays: ptr(400)}}, 2,
			"toDateTime(timestamp) + toIntervalDay(400) DELETE WHERE stream = 'm.1',\n    toDateTime(timestamp) + toIntervalDay(30) DELETE WHERE stream NOT IN ('m.1')"},
		{"level rules ignored for spans", []StreamPolicy{{Name: "web", LevelRules: []LevelRule{{Levels: []model.Level{model.Debug}, Days: 1}}}}, 1,
			"toDateTime(start_time) + toIntervalDay(7)"},
	}
	for _, tt := range tests {
		rules, err := Build(Policy{Defaults: DefaultDefaults(), Streams: tt.streams})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if rules[tt.table].TTL != tt.want {
			t.Errorf("%s:\n%s\nwant\n%s", tt.name, rules[tt.table].TTL, tt.want)
		}
	}
}

func TestValidationRejectsInjection(t *testing.T) {
	bad := []Policy{
		{Defaults: Defaults{LogsDays: 0, SpansDays: 7, MetricsDays: 30}},
		{Defaults: Defaults{LogsDays: 30, SpansDays: 36501, MetricsDays: 30}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "x' OR 1=1 --", LogsDays: ptr(1)}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "api'); DROP TABLE events; --"}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "API"}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: ""}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a\nb"}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a", LogsDays: ptr(-1)}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a", MetricsDays: ptr(0)}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a", LevelRules: []LevelRule{{Levels: []model.Level{model.Debug}, Days: 0}}}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a", LevelRules: []LevelRule{{Days: 3}}}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a", LevelRules: []LevelRule{{Levels: []model.Level{9}, Days: 3}}}}},
		{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: "a"}, {Name: "a"}}},
	}
	for i, p := range bad {
		rules, err := Build(p)
		if !errors.Is(err, ErrInvalid) || rules != nil {
			t.Errorf("case %d accepted: %v", i, err)
		}
	}
}

func TestQuotedNamesAreSafe(t *testing.T) {
	// Every valid stream name, quoted, contains no quote or backslash characters.
	for _, name := range []string{"a", "a.b", "a_b-c", "0"} {
		rules, err := Build(Policy{Defaults: DefaultDefaults(), Streams: []StreamPolicy{{Name: name, LogsDays: ptr(5)}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(rules[0].TTL, "'") != 4 || strings.Contains(rules[0].TTL, `\`) {
			t.Errorf("%q: %s", name, rules[0].TTL)
		}
	}
}
