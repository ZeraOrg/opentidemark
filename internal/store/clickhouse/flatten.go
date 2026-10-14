// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/ZeraOrg/opentidemark/internal/jsonx"
	"github.com/ZeraOrg/opentidemark/internal/normalize"
)

// Flattening limits (SPEC §8.3).
const (
	MaxPropertyKeys   = 512
	MaxValueBytes     = 32 * 1024
	TruncatedSuffix   = "…[truncated]"
	maxFlattenDepth   = 64
	flattenSeparator  = "."
	truncatedPropText = "true"
)

// Flattened holds the map columns of one row.
type Flattened struct {
	Strings   map[string]string
	Numbers   map[string]float64
	Truncated bool
}

// Flatten applies the property flattening rules: nested objects joined with ".", numbers
// as text and as float, booleans as "true"/"false" and 1/0, arrays as compact JSON, nulls
// omitted, at most 512 keys and 32 KiB per value. Keys are visited in sorted order so the
// key limit is deterministic.
func Flatten(props map[string]any) Flattened {
	f := Flattened{Strings: make(map[string]string, len(props)), Numbers: map[string]float64{}}
	f.walk("", props, 0)
	if f.Truncated {
		f.Strings[normalize.PropTruncated] = truncatedPropText
		f.Numbers[normalize.PropTruncated] = 1
	}
	return f
}

func (f *Flattened) walk(prefix string, m map[string]any, depth int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		name := k
		if prefix != "" {
			name = prefix + flattenSeparator + k
		}
		if nested, ok := m[k].(map[string]any); ok && depth < maxFlattenDepth {
			f.walk(name, nested, depth+1)
			continue
		}
		f.add(name, m[k])
	}
}

func (f *Flattened) add(key string, v any) {
	if v == nil {
		return
	}
	if len(f.Strings) >= MaxPropertyKeys {
		f.Truncated = true
		return
	}
	text, num, isNum := scalarText(v)
	f.Strings[key] = f.limit(text)
	if isNum {
		f.Numbers[key] = num
	}
}

func (f *Flattened) limit(s string) string {
	if len(s) <= MaxValueBytes {
		return s
	}
	f.Truncated = true
	cut := MaxValueBytes - len(TruncatedSuffix)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + TruncatedSuffix
}

// scalarText renders one value; isNum is set for numbers and booleans.
func scalarText(v any) (text string, num float64, isNum bool) {
	switch x := v.(type) {
	case string:
		return x, 0, false
	case bool:
		if x {
			return "true", 1, true
		}
		return "false", 0, true
	case json.Number:
		return numberText(x)
	case []any, map[string]any:
		return jsonx.Compact(Sanitize(x)), 0, false
	default:
		if n, ok := normalize.Number(v); ok {
			s, _ := normalize.Scalar(v)
			return s, n, !math.IsNaN(n) && !math.IsInf(n, 0)
		}
		if s, ok := normalize.Scalar(v); ok {
			return s, 0, false
		}
		return jsonx.Compact(Sanitize(v)), 0, false
	}
}

// numberText keeps integer text exactly and renders other numbers as shortest decimals.
func numberText(n json.Number) (string, float64, bool) {
	f, err := n.Float64()
	if err != nil {
		return n.String(), 0, false
	}
	if _, err := strconv.ParseInt(n.String(), 10, 64); err == nil {
		return n.String(), f, true
	}
	if _, err := strconv.ParseUint(n.String(), 10, 64); err == nil {
		return n.String(), f, true
	}
	return normalize.FormatFloat(f), f, true
}

// Sanitize replaces non-finite floats (not representable in JSON) with their text form.
func Sanitize(v any) any {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return normalize.FormatFloat(x)
		}
	case float32:
		return Sanitize(float64(x))
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = Sanitize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = Sanitize(e)
		}
		return out
	}
	return v
}
