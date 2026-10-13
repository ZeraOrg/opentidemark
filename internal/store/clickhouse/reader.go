// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// Reader limits and defaults.
const (
	// readonlyLevel is the ClickHouse readonly mode of user queries (SPEC §10.6).
	readonlyLevel = 2
	// clientTimeoutSlack is added to the server-side max_execution_time for the Go deadline,
	// so the server reports the timeout instead of the client cutting the connection.
	clientTimeoutSlack = 5 * time.Second
	// nanoDigits / milliDigits are the fractional digits of time arguments per column type.
	nanoDigits  = 9
	milliDigits = 3
)

// ReaderConfig configures the query layer.
type ReaderConfig struct {
	Database       string        // validated identifier
	QueryTimeout   time.Duration // max_execution_time
	MaxMemoryBytes int64         // max_memory_usage
}

// Reader implements store.Querier with the read-only reader connection. Every query runs with
// the per-query settings of SPEC §10.6 (max_execution_time, max_result_rows, max_memory_usage,
// readonly=2) and a client-side deadline.
type Reader struct {
	conn driver.Conn
	cfg  ReaderConfig
}

var _ store.Querier = (*Reader)(nil)

// NewReader returns a reader for cfg.Database.
func NewReader(conn driver.Conn, cfg ReaderConfig) (*Reader, error) {
	if !ValidDatabase(cfg.Database) {
		return nil, ErrInvalidDatabase
	}
	if cfg.QueryTimeout <= 0 {
		return nil, fmt.Errorf("reader: query timeout must be positive")
	}
	if cfg.MaxMemoryBytes <= 0 {
		cfg.MaxMemoryBytes = DefaultReaderMemory
	}
	return &Reader{conn: conn, cfg: cfg}, nil
}

// progress accumulates rows and bytes read reported by the server.
type progress struct {
	rows  atomic.Uint64
	bytes atomic.Uint64
}

// queryCtx returns a context with the per-query settings and a deadline. maxRows = 0 lifts
// max_result_rows: the setting also applies to subqueries, so statements whose subqueries may
// legitimately return more rows rely on their own LIMIT instead.
func (r *Reader) queryCtx(ctx context.Context, maxRows int, p *progress) (context.Context, context.CancelFunc) {
	settings := ch.Settings{
		"max_execution_time": int(r.cfg.QueryTimeout.Seconds()),
		"max_memory_usage":   r.cfg.MaxMemoryBytes,
		"readonly":           readonlyLevel,
		"max_result_rows":    maxRows,
	}
	if maxRows > 0 {
		settings["result_overflow_mode"] = "break"
	}
	opts := []ch.QueryOption{ch.WithSettings(settings)}
	if p != nil {
		opts = append(opts, ch.WithProgress(func(pr *ch.Progress) {
			p.rows.Add(pr.Rows)
			p.bytes.Add(pr.Bytes)
		}))
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.QueryTimeout+clientTimeoutSlack)
	return ch.Context(ctx, opts...), cancel
}

// query runs sql with converted arguments.
func (r *Reader) query(ctx context.Context, maxRows int, p *progress, src model.Source, sql string, args []any) (driver.Rows, context.CancelFunc, error) {
	qctx, cancel := r.queryCtx(ctx, maxRows, p)
	rows, err := r.conn.Query(qctx, sql, bindArgs(src, args)...)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("query: %w", err)
	}
	return rows, cancel, nil
}

// table returns the qualified table and time column of a source (allow-list).
func (r *Reader) table(src model.Source) (table, timeCol string) {
	switch src {
	case model.SourceSpans:
		return r.cfg.Database + ".spans", "start_time"
	case model.SourceMetrics:
		return r.cfg.Database + ".metric_points", "timestamp"
	default:
		return r.cfg.Database + ".events", "timestamp"
	}
}

// bindArgs converts time arguments to text with full precision: the driver's positional
// binding would format time.Time with second precision, which breaks keyset cursors and
// sub-second ranges. ClickHouse parses the text when comparing with DateTime64 columns.
func bindArgs(src model.Source, args []any) []any {
	digits := nanoDigits
	if src == model.SourceMetrics {
		digits = milliDigits
	}
	out := make([]any, len(args))
	for i, a := range args {
		if t, ok := a.(time.Time); ok {
			out[i] = formatTimeArg(t, digits)
			continue
		}
		out[i] = a
	}
	return out
}

// formatTimeArg renders t in UTC as "YYYY-MM-DD hh:mm:ss.fff…" with the given digits.
func formatTimeArg(t time.Time, digits int) string {
	layout := "2006-01-02 15:04:05." + zeros(digits)
	return t.UTC().Format(layout)
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

// Run executes a compiled query and shapes the result by kind.
func (r *Reader) Run(ctx context.Context, c *tql.Compiled) (store.Result, error) {
	start := time.Now()
	var p progress
	maxRows := 0
	if c.Kind == tql.KindEvents {
		maxRows = c.Limit
	}
	rows, cancel, err := r.query(ctx, maxRows, &p, c.Source, c.SQL, c.Args)
	if err != nil {
		return store.Result{}, err
	}
	defer cancel()
	defer rows.Close()
	res := store.Result{Kind: c.Kind, Columns: c.Columns, Span: c.Span}
	switch {
	case c.Kind == tql.KindEvents && c.Source == model.SourceLogs && isLogEventColumns(c.Columns):
		err = r.collectEvents(rows, c, &res)
	case c.Kind == tql.KindEvents:
		err = collectRecords(rows, c, &res)
	case c.Kind == tql.KindTimeseries:
		err = collectSeries(rows, c, &res)
	default:
		res.Rows, err = collectRows(rows, c.Columns)
	}
	if err != nil {
		return store.Result{}, err
	}
	res.Stats = store.QueryStats{ElapsedMs: time.Since(start).Milliseconds(), RowsRead: p.rows.Load(),
		BytesRead: p.bytes.Load(), InterpretedAsText: c.InterpretedAsText}
	return res, nil
}

func (r *Reader) collectEvents(rows driver.Rows, c *tql.Compiled, res *store.Result) error {
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return err
		}
		res.Events = append(res.Events, ev)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	if n := len(res.Events); n > 0 && n >= c.Limit {
		last := res.Events[n-1]
		res.NextCursor = &tql.Cursor{Timestamp: last.Timestamp, ID: last.ID.String()}
	}
	return nil
}

func collectRecords(rows driver.Rows, c *tql.Compiled, res *store.Result) error {
	timeIdx, idIdx := cursorColumns(c)
	var lastTime time.Time
	var lastID string
	for rows.Next() {
		vals, err := scanGeneric(rows)
		if err != nil {
			return err
		}
		rec := make(map[string]any, len(vals))
		for i, v := range vals {
			if i < len(c.Columns) {
				rec[c.Columns[i].Name] = jsonValue(v, c.Columns[i].Type)
			}
		}
		if t, ok := vals[timeIdx].(time.Time); ok {
			lastTime = t
		}
		lastID = fmt.Sprint(vals[idIdx])
		res.Records = append(res.Records, rec)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read rows: %w", err)
	}
	if n := len(res.Records); n > 0 && n >= c.Limit {
		res.NextCursor = &tql.Cursor{Timestamp: lastTime, ID: lastID}
	}
	return nil
}

// cursorColumns returns the indexes of the time column (always first) and the keyset id
// column (logs id, spans span_id, metrics series_id; the second column of a projection).
func cursorColumns(c *tql.Compiled) (timeIdx, idIdx int) {
	name := map[model.Source]string{model.SourceLogs: "id", model.SourceSpans: "span_id",
		model.SourceMetrics: "series_id"}[c.Source]
	for i, col := range c.Columns {
		if i > 0 && col.Name == name {
			return 0, i
		}
	}
	return 0, min(1, len(c.Columns)-1)
}

func collectRows(rows driver.Rows, cols []tql.Column) ([][]any, error) {
	out := [][]any{}
	for rows.Next() {
		vals, err := scanGeneric(rows)
		if err != nil {
			return nil, err
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			t := tql.ColumnString
			if i < len(cols) {
				t = cols[i].Type
			}
			row[i] = jsonValue(v, t)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	return out, nil
}

// collectSeries groups timeseries rows (@bucket, by…, aggregates…) into series keyed by the
// by-field values, in order of first appearance; points are [epoch ms, aggregates…].
func collectSeries(rows driver.Rows, c *tql.Compiled, res *store.Result) error {
	nBy := countGroupColumns(rows.Columns())
	if nBy+1 > len(c.Columns) {
		return fmt.Errorf("read series: unexpected result shape")
	}
	index := map[string]int{}
	for rows.Next() {
		vals, err := scanGeneric(rows)
		if err != nil {
			return err
		}
		key := map[string]any{}
		for i := 1; i <= nBy; i++ {
			key[c.Columns[i].Name] = jsonValue(vals[i], c.Columns[i].Type)
		}
		k := fmt.Sprint(vals[1 : 1+nBy])
		si, ok := index[k]
		if !ok {
			si = len(res.Series)
			index[k] = si
			res.Series = append(res.Series, store.Series{Key: key})
		}
		point := []any{epochMillis(vals[0])}
		for i := 1 + nBy; i < len(vals); i++ {
			point = append(point, jsonValue(vals[i], c.Columns[i].Type))
		}
		res.Series[si].Points = append(res.Series[si].Points, point)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read series: %w", err)
	}
	return nil
}

// countGroupColumns counts the by-field columns of a stats result. The compiler names them
// g0, g1, … (and aggregates a0, a1, …), SPEC §10.6.
func countGroupColumns(names []string) int {
	n := 0
	for _, name := range names {
		if len(name) > 1 && name[0] == 'g' && strings.Trim(name[1:], "0123456789") == "" {
			n++
		}
	}
	return n
}
