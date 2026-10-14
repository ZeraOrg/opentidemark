// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/ingest/elastic"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

// maxKibanaObjects bounds one saved-objects export.
const maxKibanaObjects = 10000

// StreamResolver creates (or finds) a stream by name; it is the pipeline's stream gate.
type StreamResolver interface {
	Resolve(ctx context.Context, name string) (string, bool)
}

// KibanaResult summarizes a saved-objects import.
type KibanaResult struct {
	Signals         int
	SignalsDisabled int
	Streams         int
	Warnings        []string
}

type kibanaObject struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		Title                 string `json:"title"`
		KibanaSavedObjectMeta struct {
			SearchSourceJSON string `json:"searchSourceJSON"`
		} `json:"kibanaSavedObjectMeta"`
	} `json:"attributes"`
}

type kibanaSearchSource struct {
	Query struct {
		Query    any    `json:"query"` // string, or an object for legacy Lucene DSL
		Language string `json:"language"`
	} `json:"query"`
	Filter []struct {
		Meta struct {
			Disabled bool `json:"disabled"`
		} `json:"meta"`
	} `json:"filter"`
}

// kibanaImporter converts saved searches to signals and index patterns / data views to
// streams (SPEC §9.9). Dashboards and visualizations are reported as not converted.
type kibanaImporter struct {
	signals SignalSink
	streams StreamResolver
	owner   string
	res     KibanaResult
}

// ImportKibana reads an .ndjson saved-objects export.
func ImportKibana(ctx context.Context, rd io.Reader, signals SignalSink, streams StreamResolver, owner string) (KibanaResult, error) {
	k := &kibanaImporter{signals: signals, streams: streams, owner: owner}
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, readBufferSize), maxCLEFLineSize)
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if n++; n > maxKibanaObjects {
			return k.res, fmt.Errorf("%w: more than %d saved objects", ErrPermanent, maxKibanaObjects)
		}
		var obj kibanaObject
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			k.warn("line %d: not a saved object: %v", n, err)
			continue
		}
		if err := k.object(ctx, &obj); err != nil {
			return k.res, err
		}
	}
	if err := sc.Err(); err != nil {
		return k.res, fmt.Errorf("%w: read export: %w", ErrPermanent, err)
	}
	return k.res, nil
}

func (k *kibanaImporter) warn(format string, args ...any) {
	k.res.Warnings = append(k.res.Warnings, fmt.Sprintf(format, args...))
}

func (k *kibanaImporter) object(ctx context.Context, obj *kibanaObject) error {
	switch obj.Type {
	case "search":
		return k.search(ctx, obj)
	case "index-pattern":
		k.indexPattern(ctx, obj.Attributes.Title)
	case "dashboard":
		k.warn("dashboard %q was not converted (rebuild it with the dashboard builder)", obj.Attributes.Title)
	case "":
		// the export summary line ({"exportedCount":…}) has no type
	default:
		k.warn("%s %q was not converted", obj.Type, obj.Attributes.Title)
	}
	return nil
}

func (k *kibanaImporter) search(ctx context.Context, obj *kibanaObject) error {
	name := strings.TrimSpace(obj.Attributes.Title)
	if name == "" {
		name = "Kibana search " + obj.ID
	}
	var src kibanaSearchSource
	original := ""
	err := json.Unmarshal([]byte(obj.Attributes.KibanaSavedObjectMeta.SearchSourceJSON), &src)
	if err == nil {
		text, isText := src.Query.Query.(string)
		original = text
		switch {
		case !isText && src.Query.Query != nil:
			err = errors.New("query DSL objects are not supported")
		case activeFilters(&src) > 0:
			err = errors.New("filter pills are not supported")
		}
	}
	tqlText := original
	if err == nil {
		tqlText, err = TranslateKQL(original)
	}
	if err != nil {
		k.res.SignalsDisabled++
		k.warn("saved search %q imported disabled: %v", name, err)
	}
	if tqlText == "" {
		tqlText = "*"
	}
	k.res.Signals++
	return k.signals.ImportSignal(ctx, k.owner, name, tqlText, err == nil)
}

func activeFilters(src *kibanaSearchSource) int {
	n := 0
	for _, f := range src.Filter {
		if !f.Meta.Disabled {
			n++
		}
	}
	return n
}

// indexPattern creates one stream per comma-separated pattern (wildcards and date suffixes
// removed, §9.3 rules).
func (k *kibanaImporter) indexPattern(ctx context.Context, title string) {
	for _, pattern := range strings.Split(title, ",") {
		base := strings.TrimRight(strings.TrimSpace(strings.ReplaceAll(pattern, "*", "")), "-._")
		if base == "" || strings.HasPrefix(base, "-") {
			k.warn("index pattern %q has no fixed prefix; no stream created", pattern)
			continue
		}
		name := model.NormalizeStream(elastic.StreamFromIndex(base))
		got, ok := k.streams.Resolve(ctx, name)
		switch {
		case got != name:
			k.warn("stream %q not created (stream limit reached)", name)
		case !ok:
			k.warn("stream %q exists and is paused", name)
		default:
			k.res.Streams++
		}
	}
}
