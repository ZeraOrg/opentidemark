// SPDX-License-Identifier: Apache-2.0

// Package normalize maps loosely structured input onto the normalized model (SPEC §9.6),
// renders message templates (§9.2) and finalizes events (§9.1).
package normalize

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strconv"

	"github.com/ZeraOrg/tidemark/internal/jsonx"
)

// maxPlainFloat is the magnitude above which floats are rendered in exponent form.
const maxPlainFloat = 1e21

// FormatFloat renders f as the shortest decimal text (exponent form only for huge or tiny values).
func FormatFloat(f float64) string {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if abs := math.Abs(f); abs != 0 && (abs >= maxPlainFloat || abs < 1e-6) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// Scalar renders a scalar value as text. ok is false for nil, maps and slices.
func Scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return FormatFloat(x), true
	case float32:
		return FormatFloat(float64(x)), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case int32:
		return strconv.FormatInt(int64(x), 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case uint32:
		return strconv.FormatUint(uint64(x), 10), true
	case []byte:
		return base64.StdEncoding.EncodeToString(x), true
	default:
		return "", false
	}
}

// Text renders any value as text: scalars plainly, nil as "null", everything else as compact JSON.
func Text(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := Scalar(v); ok {
		return s
	}
	return jsonx.Compact(v)
}

// Number returns the numeric value of v for numbers and booleans (true = 1).
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// isInteger reports whether v is an integer-typed value (JSON number without fraction/exponent).
func isInteger(v any) (int64, bool) {
	switch x := v.(type) {
	case json.Number:
		n, err := strconv.ParseInt(x.String(), 10, 64)
		return n, err == nil
	case int:
		return int64(x), true
	case int64:
		return x, true
	case int32:
		return int64(x), true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x), true
		}
	}
	return 0, false
}
