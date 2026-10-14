// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// maxSafeInteger is the largest integer a JSON (JavaScript) number holds exactly; larger
// integers (e.g. series ids) are rendered as decimal strings.
const maxSafeInteger = 1<<53 - 1

// scanGeneric scans one row into values of the column scan types.
func scanGeneric(rows driver.Rows) ([]any, error) {
	types := rows.ColumnTypes()
	dest := make([]any, len(types))
	for i, ct := range types {
		dest[i] = reflect.New(ct.ScanType()).Interface()
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, fmt.Errorf("scan row: %w", err)
	}
	out := make([]any, len(dest))
	for i, d := range dest {
		out[i] = deref(reflect.ValueOf(d).Elem())
	}
	return out, nil
}

// deref unwraps (nullable) pointers; nil pointers become nil.
func deref(v reflect.Value) any {
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	return v.Interface()
}

// jsonValue converts a scanned value into a JSON-encodable value: UTC times, UUID strings,
// non-finite floats as nil, unsafe integers as strings, JSON columns as raw JSON.
func jsonValue(v any, t tql.ColumnType) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		return x.UTC()
	case uuid.UUID:
		return x.String()
	case float64:
		return finite(x)
	case float32:
		return finite(float64(x))
	case uint64:
		if x > maxSafeInteger {
			return strconv.FormatUint(x, 10)
		}
		return x
	case int64:
		if x > maxSafeInteger || x < -maxSafeInteger {
			return strconv.FormatInt(x, 10)
		}
		return x
	case string:
		if t == tql.ColumnJSON && json.Valid([]byte(x)) {
			return json.RawMessage(x)
		}
		return x
	case []float64:
		out := make([]any, len(x))
		for i, f := range x {
			out[i] = finite(f)
		}
		return out
	case map[string]float64:
		out := make(map[string]any, len(x))
		for k, f := range x {
			out[k] = finite(f)
		}
		return out
	}
	return v
}

func finite(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return f
}

// epochMillis converts a bucket time to epoch milliseconds.
func epochMillis(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.UnixMilli()
	}
	return v
}

// decodeObject decodes a JSON object keeping numbers exact; invalid input yields an empty map.
func decodeObject(s string) map[string]any {
	out := map[string]any{}
	if s == "" {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
