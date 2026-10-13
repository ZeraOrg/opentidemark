// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/retention"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// Admin timeouts and limits.
const (
	adminTimeout     = 2 * time.Minute
	purgeTimeout     = 30 * time.Minute
	maxMappings      = 2000
	topServicesCount = 5
	rateWindowShort  = 5 * time.Minute
	rateWindowLong   = 24 * time.Hour
	partitionLayout  = "20060102"
	bloomFPRate      = "0.01"
	bloomGranularity = "4"
)

// Errors of the admin port.
var (
	ErrInvalidIdentifier = errors.New("invalid identifier")
	ErrUnknownSource     = errors.New("unknown source")
)

var (
	// indexedColumnPattern is the materialized column name (SPEC §12.4).
	indexedColumnPattern = regexp.MustCompile(`^f_[a-z0-9_]{1,64}$`)
	// IndexableKeyPattern restricts property keys that may be placed in DDL (no parameters are
	// possible there): no quotes, backslashes, whitespace or control characters.
	IndexableKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_.@:/\-]{1,128}$`)
)

// sourceTable describes the table of a source.
type sourceTable struct {
	table    string
	timeCol  string
	mapCol   string // string property map
	ingested string // column for ingest rates
}

var sourceTables = map[model.Source]sourceTable{
	model.SourceLogs:    {retention.TableEvents, "timestamp", "props", "ingested_at"},
	model.SourceSpans:   {retention.TableSpans, "start_time", "attrs", "ingested_at"},
	model.SourceMetrics: {retention.TableMetrics, "timestamp", "attrs", "timestamp"},
}

func tableOf(src model.Source) (sourceTable, error) {
	t, ok := sourceTables[src]
	if !ok {
		return sourceTable{}, fmt.Errorf("%w: %q", ErrUnknownSource, src)
	}
	return t, nil
}

// Admin implements store.StreamAdmin on the writer connection.
type Admin struct {
	conn      driver.Conn
	db        string
	retention *RetentionManager
	now       func() time.Time
}

// NewAdmin returns the stream admin for database db.
func NewAdmin(conn driver.Conn, db string, now func() time.Time) (*Admin, error) {
	rm, err := NewRetentionManager(conn, db)
	if err != nil {
		return nil, err
	}
	return &Admin{conn: conn, db: db, retention: rm, now: now}, nil
}

// ApplyRetention implements store.StreamAdmin.
func (a *Admin) ApplyRetention(ctx context.Context, rules []retention.Rule) error {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	return a.retention.ApplyRetention(ctx, rules)
}

func (a *Admin) qualified(table string) string { return a.db + "." + table }

// AddIndexedField creates the materialized column and its bloom-filter skip index, then
// starts MATERIALIZE INDEX for existing parts (asynchronous mutation).
func (a *Admin) AddIndexedField(ctx context.Context, f store.IndexedField) error {
	st, err := tableOf(f.Source)
	if err != nil {
		return err
	}
	if !indexedColumnPattern.MatchString(f.Column) || !IndexableKeyPattern.MatchString(f.Key) {
		return fmt.Errorf("%w: column %q key %q", ErrInvalidIdentifier, f.Column, f.Key)
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	for _, stmt := range indexedFieldDDL(a.qualified(st.table), st.mapCol, f) {
		if err := a.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("index field %s: %w", f.Column, err)
		}
	}
	return nil
}

// indexedFieldDDL builds the statements of AddIndexedField. Key and column are validated by
// the caller; the key contains no quote or backslash, so the literal needs no escaping.
func indexedFieldDDL(table, mapCol string, f store.IndexedField) []string {
	idx := "idx_" + f.Column
	return []string{
		"ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + f.Column + " String MATERIALIZED " + mapCol + "['" + f.Key + "']",
		"ALTER TABLE " + table + " ADD INDEX IF NOT EXISTS " + idx + " " + f.Column +
			" TYPE bloom_filter(" + bloomFPRate + ") GRANULARITY " + bloomGranularity,
		"ALTER TABLE " + table + " MATERIALIZE INDEX " + idx,
	}
}

// DropIndexedField removes the skip index and the materialized column.
func (a *Admin) DropIndexedField(ctx context.Context, f store.IndexedField) error {
	st, err := tableOf(f.Source)
	if err != nil {
		return err
	}
	if !indexedColumnPattern.MatchString(f.Column) {
		return fmt.Errorf("%w: column %q", ErrInvalidIdentifier, f.Column)
	}
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	table := a.qualified(st.table)
	for _, stmt := range []string{
		"ALTER TABLE " + table + " DROP INDEX IF EXISTS idx_" + f.Column,
		"ALTER TABLE " + table + " DROP COLUMN IF EXISTS " + f.Column,
	} {
		if err := a.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("drop indexed field %s: %w", f.Column, err)
		}
	}
	return nil
}

// DeleteStream removes every row of a stream from all telemetry tables and the field catalog
// with lightweight DELETEs.
func (a *Admin) DeleteStream(ctx context.Context, name string) error {
	if !model.ValidStreamName(name) {
		return fmt.Errorf("%w: stream %q", ErrInvalidIdentifier, name)
	}
	ctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	for _, t := range []string{retention.TableEvents, retention.TableSpans, retention.TableMetrics, "field_catalog"} {
		if err := a.conn.Exec(ctx, "DELETE FROM "+a.qualified(t)+" WHERE stream = ?", name); err != nil {
			return fmt.Errorf("delete stream %s from %s: %w", name, t, err)
		}
	}
	return nil
}

// Purge estimates (dry run) or deletes the rows of a request (SPEC §12.5). Without a stream
// and a filter, whole UTC days inside [From, To) are removed by dropping their partitions;
// the remaining rows are removed with a lightweight DELETE.
func (a *Admin) Purge(ctx context.Context, p store.PurgeRequest) (store.PurgeJob, error) {
	st, err := tableOf(p.Source)
	if err != nil {
		return store.PurgeJob{}, err
	}
	where, args := purgeWhere(st, p)
	table := a.qualified(st.table)
	if p.DryRun {
		ctx, cancel := context.WithTimeout(ctx, adminTimeout)
		defer cancel()
		var n uint64
		if err := a.conn.QueryRow(ctx, "SELECT count() FROM "+table+" WHERE "+where, args...).Scan(&n); err != nil {
			return store.PurgeJob{}, fmt.Errorf("estimate purge: %w", err)
		}
		return store.PurgeJob{RowsEstimate: n}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, purgeTimeout)
	defer cancel()
	var job store.PurgeJob
	if p.Stream == "" && p.Where == "" {
		for _, part := range WholeDayPartitions(p.From, p.To) {
			if err := a.conn.Exec(ctx, "ALTER TABLE "+table+" DROP PARTITION ID '"+part+"'"); err != nil {
				return job, fmt.Errorf("drop partition %s: %w", part, err)
			}
			job.DroppedPartitions = append(job.DroppedPartitions, part)
		}
	}
	if err := a.conn.Exec(ctx, "DELETE FROM "+table+" WHERE "+where, args...); err != nil {
		return job, fmt.Errorf("delete rows: %w", err)
	}
	job.LightweightDeleted = true
	return job, nil
}

// purgeWhere builds the condition of a purge: the time range (or the TQL condition, which
// contains it) and the stream.
func purgeWhere(st sourceTable, p store.PurgeRequest) (string, []any) {
	where := st.timeCol + " >= ? AND " + st.timeCol + " < ?"
	args := []any{p.From.UTC(), p.To.UTC()}
	if p.Where != "" {
		where, args = "("+p.Where+")", append([]any(nil), p.Args...)
	}
	if p.Stream != "" {
		where += " AND stream = ?"
		args = append(args, p.Stream)
	}
	return where, args
}

// WholeDayPartitions lists the toYYYYMMDD partition ids of the UTC days entirely inside
// [from, to).
func WholeDayPartitions(from, to time.Time) []string {
	from, to = from.UTC(), to.UTC()
	day := from.Truncate(24 * time.Hour)
	if day.Before(from) {
		day = day.Add(24 * time.Hour)
	}
	var out []string
	for !day.Add(24 * time.Hour).After(to) {
		out = append(out, day.Format(partitionLayout))
		day = day.Add(24 * time.Hour)
	}
	return out
}

// Mappings lists the property keys of a stream from the field catalog (SPEC §12.4).
func (a *Admin) Mappings(ctx context.Context, stream string) ([]store.FieldMapping, error) {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	rows, err := a.conn.Query(ctx, "SELECT source, key, groupUniqArray(numeric), sum(hits), min(hour), max(hour) FROM "+
		a.qualified("field_catalog")+" WHERE stream = ? GROUP BY source, key ORDER BY sum(hits) DESC, key LIMIT "+
		strconv.Itoa(maxMappings), stream)
	if err != nil {
		return nil, fmt.Errorf("query mappings: %w", err)
	}
	defer rows.Close()
	out := []store.FieldMapping{}
	for rows.Next() {
		var m store.FieldMapping
		var src string
		var numeric []uint8
		if err := rows.Scan(&src, &m.Key, &numeric, &m.Hits, &m.FirstSeen, &m.LastSeen); err != nil {
			return nil, fmt.Errorf("scan mapping: %w", err)
		}
		m.Source, m.Type = model.Source(src), mappingType(numeric)
		out = append(out, m)
	}
	return out, rows.Err()
}

// mappingType derives the detected type from the set of numeric flags seen.
func mappingType(flags []uint8) string {
	var num, str bool
	for _, f := range flags {
		if f == 1 {
			num = true
		} else {
			str = true
		}
	}
	switch {
	case num && str:
		return "mixed"
	case num:
		return "number"
	default:
		return "string"
	}
}
