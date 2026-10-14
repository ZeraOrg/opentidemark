// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"sort"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// fieldType is the semantic type of a resolved field.
type fieldType int

const (
	typeString   fieldType = iota // text column or property
	typeNumber                    // numeric column
	typeLevel                     // level (level_num / level)
	typeTime                      // the source time column
	typeDuration                  // span duration (duration_ns)
	typeUUID                      // event id
)

// builtin is a built-in field of a source (SPEC §10.4). column is a trusted
// identifier from this allow-list, never user input.
type builtin struct {
	name        string   // canonical name
	aliases     []string // accepted alternative names (case-insensitive)
	column      string   // ClickHouse column
	typ         fieldType
	lowerStored bool // values are stored lower case (hex ids): equality can skip lower()
}

// ColumnType describes the type of a result column.
type ColumnType string

// Result column types.
const (
	ColumnTime     ColumnType = "time"
	ColumnString   ColumnType = "string"
	ColumnNumber   ColumnType = "number"
	ColumnBool     ColumnType = "bool"
	ColumnJSON     ColumnType = "json"
	ColumnMap      ColumnType = "map"
	ColumnArray    ColumnType = "array"
	ColumnDuration ColumnType = "duration_ms"
)

// Column is a result column: Name is the SQL result name (and the user-facing
// alias for stats results).
type Column struct {
	Name string     `json:"name"`
	Type ColumnType `json:"type"`
}

// eventColumn is a column selected for event results.
type eventColumn struct {
	expr string // trusted SQL expression
	col  Column
}

// sourceSchema describes how one source maps onto ClickHouse.
type sourceSchema struct {
	src       model.Source
	table     string
	timeCol   string
	idCol     string // keyset tie-breaker column
	idCast    string // function applied to the cursor id parameter ("" = none)
	propsCol  string // Map(String,String) of properties
	numCol    string // Map(String,Float64) of numeric properties; "" if none
	textCols  []string
	builtins  []builtin
	eventCols []eventColumn
}

func str(name, column string, aliases ...string) builtin {
	return builtin{name: name, column: column, typ: typeString, aliases: aliases}
}

func num(name, column string) builtin {
	return builtin{name: name, column: column, typ: typeNumber}
}

func hexID(name, column string, aliases ...string) builtin {
	return builtin{name: name, column: column, typ: typeString, aliases: aliases, lowerStored: true}
}

// connectionID is the built-in for the id of the connection that ingested a row (SPEC §10.4);
// ids are lower-case UUIDs.
func connectionID() builtin {
	return builtin{name: "connection_id", column: "connection_id", typ: typeString, aliases: []string{"connectionid"}, lowerStored: true}
}

func ec(expr, name string, t ColumnType) eventColumn {
	return eventColumn{expr: expr, col: Column{Name: name, Type: t}}
}

func logsSchema() *sourceSchema {
	return &sourceSchema{
		src: model.SourceLogs, table: "events", timeCol: "timestamp", idCol: "id", idCast: "toUUIDOrZero",
		propsCol: "props", numCol: "props_num", textCols: []string{"message", "exception"},
		builtins: []builtin{
			{name: "@timestamp", aliases: []string{"timestamp", "@t"}, column: "timestamp", typ: typeTime},
			{name: "level", aliases: []string{"@l"}, column: "level_num", typ: typeLevel},
			str("message", "message", "@m", "msg"),
			str("template", "message_template", "@mt", "messagetemplate"),
			str("exception", "exception", "@x"),
			str("service", "service"), str("environment", "environment", "env"),
			str("host", "host"), str("stream", "stream"),
			hexID("trace_id", "trace_id", "traceid", "@tr"), hexID("span_id", "span_id", "spanid", "@sp"),
			str("event_type", "event_type", "@i"), str("protocol", "protocol"), connectionID(),
			{name: "id", column: "id", typ: typeUUID, lowerStored: true},
		},
		eventCols: []eventColumn{
			ec("timestamp", "timestamp", ColumnTime), ec("id", "id", ColumnString),
			ec("stream", "stream", ColumnString), ec("level", "level", ColumnString),
			ec("level_num", "level_num", ColumnNumber), ec("service", "service", ColumnString),
			ec("environment", "environment", ColumnString), ec("host", "host", ColumnString),
			ec("message_template", "message_template", ColumnString), ec("message", "message", ColumnString),
			ec("exception", "exception", ColumnString), ec("event_type", "event_type", ColumnString),
			ec("trace_id", "trace_id", ColumnString), ec("span_id", "span_id", ColumnString),
			ec("protocol", "protocol", ColumnString), ec("connection_id", "connection_id", ColumnString),
			ec("props_json", "props_json", ColumnJSON),
		},
	}
}

func spansSchema() *sourceSchema {
	return &sourceSchema{
		src: model.SourceSpans, table: "spans", timeCol: "start_time", idCol: "span_id",
		propsCol: "attrs", numCol: "attrs_num", textCols: []string{"name", "status_message"},
		builtins: []builtin{
			{name: "@timestamp", aliases: []string{"timestamp", "start_time"}, column: "start_time", typ: typeTime},
			hexID("trace_id", "trace_id", "traceid"), hexID("span_id", "span_id", "spanid"),
			hexID("parent_span_id", "parent_span_id"),
			str("service", "service"), str("name", "name"), str("kind", "kind"),
			str("status", "status_code", "status_code"), str("status_message", "status_message"),
			{name: "duration", aliases: []string{"@duration"}, column: "duration_ns", typ: typeDuration},
			str("stream", "stream"), connectionID(),
		},
		eventCols: []eventColumn{
			ec("start_time", "start_time", ColumnTime), ec("trace_id", "trace_id", ColumnString),
			ec("span_id", "span_id", ColumnString), ec("parent_span_id", "parent_span_id", ColumnString),
			ec("stream", "stream", ColumnString), ec("service", "service", ColumnString),
			ec("name", "name", ColumnString), ec("kind", "kind", ColumnString),
			ec("duration_ns / 1e6", "duration_ms", ColumnDuration),
			ec("status_code", "status_code", ColumnString), ec("status_message", "status_message", ColumnString),
			ec("connection_id", "connection_id", ColumnString), ec("attrs", "attrs", ColumnMap),
			ec("attrs_num", "attrs_num", ColumnMap), ec("resource_json", "resource_json", ColumnJSON),
			ec("events_json", "events_json", ColumnJSON), ec("links_json", "links_json", ColumnJSON),
		},
	}
}

func metricsSchema() *sourceSchema {
	return &sourceSchema{
		src: model.SourceMetrics, table: "metric_points", timeCol: "timestamp", idCol: "series_id",
		idCast: "toUInt64OrZero", propsCol: "attrs", textCols: []string{"name", "description"},
		builtins: []builtin{
			{name: "@timestamp", aliases: []string{"timestamp"}, column: "timestamp", typ: typeTime},
			str("name", "name"), str("service", "service"), str("kind", "kind"), str("unit", "unit"),
			num("value", "value"), num("count", "`count`"), num("sum", "`sum`"),
			num("min", "`min`"), num("max", "`max`"), str("stream", "stream"), connectionID(),
		},
		eventCols: []eventColumn{
			ec("timestamp", "timestamp", ColumnTime), ec("stream", "stream", ColumnString),
			ec("service", "service", ColumnString), ec("name", "name", ColumnString),
			ec("description", "description", ColumnString), ec("unit", "unit", ColumnString),
			ec("kind", "kind", ColumnString), ec("temporality", "temporality", ColumnString),
			ec("is_monotonic", "is_monotonic", ColumnBool), ec("value", "value", ColumnNumber),
			ec("`count`", "count", ColumnNumber), ec("`sum`", "sum", ColumnNumber),
			ec("`min`", "min", ColumnNumber), ec("`max`", "max", ColumnNumber),
			ec("bucket_bounds", "bucket_bounds", ColumnArray), ec("bucket_counts", "bucket_counts", ColumnArray),
			ec("attrs", "attrs", ColumnMap), ec("series_id", "series_id", ColumnNumber),
		},
	}
}

func schemaFor(src model.Source) *sourceSchema {
	switch src {
	case model.SourceSpans:
		return spansSchema()
	case model.SourceMetrics:
		return metricsSchema()
	default:
		return logsSchema()
	}
}

// resolvedField is a field after resolution (SPEC §10.4).
type resolvedField struct {
	name string   // as written
	bi   *builtin // nil for a property
	key  string   // property key
}

func (r resolvedField) typ() fieldType {
	if r.bi == nil {
		return typeString
	}
	return r.bi.typ
}

// propertyPrefixes force property resolution (SPEC §10.4).
func propertyPrefixes() []string { return []string{"props.", "attrs."} }

func (s *sourceSchema) lookupBuiltin(name string) *builtin {
	bare := strings.TrimPrefix(name, "@")
	for i := range s.builtins {
		b := &s.builtins[i]
		if strings.EqualFold(name, b.name) || strings.EqualFold(bare, strings.TrimPrefix(b.name, "@")) {
			return b
		}
		for _, a := range b.aliases {
			if strings.EqualFold(name, a) || (!strings.HasPrefix(a, "@") && strings.EqualFold(bare, a)) {
				return b
			}
		}
	}
	return nil
}

// builtinNames lists canonical built-in names (for hints and autocomplete).
func (s *sourceSchema) builtinNames() []string {
	out := make([]string, 0, len(s.builtins))
	for _, b := range s.builtins {
		out = append(out, b.name)
	}
	sort.Strings(out)
	return out
}

func (s *sourceSchema) resolve(f Field) (resolvedField, error) {
	r := resolvedField{name: f.Name}
	if !f.Quoted {
		for _, p := range propertyPrefixes() {
			if len(f.Name) > len(p) && strings.EqualFold(f.Name[:len(p)], p) {
				r.key = f.Name[len(p):]
				return r, nil
			}
		}
	}
	if b := s.lookupBuiltin(f.Name); b != nil {
		r.bi = b
		return r, nil
	}
	if !f.Quoted && strings.HasPrefix(f.Name, "@") {
		e := errAt(f.Pos, f.End-f.Pos, "unknown built-in field '%s' for source %s", f.Name, s.src)
		return r, e.withHint(didYouMean(f.Name, s.builtinNames()))
	}
	r.key = f.Name
	return r, nil
}

// BuiltinFields lists the canonical built-in fields of src (SPEC §10.4) for the field list;
// Numeric is set for numeric and duration columns.
func BuiltinFields(src model.Source) []FieldInfo {
	s := schemaFor(src)
	out := make([]FieldInfo, 0, len(s.builtins))
	for _, b := range s.builtins {
		out = append(out, FieldInfo{Name: b.name, Builtin: true, Numeric: b.typ == typeNumber || b.typ == typeDuration})
	}
	return out
}

// sources lists the sources in a stable order.
func sources() []model.Source {
	return []model.Source{model.SourceLogs, model.SourceSpans, model.SourceMetrics}
}

// parseSourceName parses a source name case-insensitively.
func parseSourceName(v string) (model.Source, bool) {
	s := model.Source(strings.ToLower(v))
	return s, s.Valid()
}

func sourceNames() []string {
	out := make([]string, 0, 3)
	for _, s := range sources() {
		out = append(out, string(s))
	}
	return out
}
