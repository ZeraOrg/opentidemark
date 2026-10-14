// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// Defaults applied by Compile.
const (
	DefaultLimit       = 200   // events page size when Options.Limit is 0
	DefaultMaxRows     = 10000 // table/timeseries row cap when Options.MaxRows is 0
	DefaultAutoBuckets = 80    // target bucket count for `span auto`
)

// Identifier validation for option values that become SQL identifiers.
var (
	reDatabase   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	reIndexedCol = regexp.MustCompile(`^f_[a-z0-9_]{1,64}$`)
)

// Cursor is the keyset position of the last event of the previous page;
// the next page returns events strictly before it (SPEC §11.3).
// ID is the event UUID (logs), span_id (spans) or series_id (metrics).
type Cursor struct {
	Timestamp time.Time
	ID        string
}

// Options parameterise compilation. From/To are required.
type Options struct {
	Database      string                             // ClickHouse database ("" = unqualified tables); validated identifier
	From, To      time.Time                          // request time range [From, To)
	Now           time.Time                          // reference for relative literals (zero ⇒ To)
	Limit         int                                // events page size (0 ⇒ DefaultLimit; capped by MaxRows)
	MaxRows       int                                // row cap for table/timeseries (0 ⇒ DefaultMaxRows)
	Cursor        *Cursor                            // events only: return events before this position
	AutoSpan      time.Duration                      // bucket used for `span auto` (0 ⇒ chosen for ≤ DefaultAutoBuckets buckets)
	IndexedFields map[model.Source]map[string]string // property key ⇒ materialized column f_<slug> (§12.4)
	ExtraFilters  []*Query                           // active signals: filters ANDed with the query (no stages)
	// Streams is the caller's stream scope (SPEC §15.10) and is required: an unset scope fails
	// with ErrInvalidOptions. All adds no condition, an empty scope adds `AND 0` (no rows), any
	// other scope `AND stream IN (?, …)`.
	Streams model.StreamScope
}

// Compiled is a ready-to-run ClickHouse statement. SQL contains only trusted
// text and positional `?` placeholders; Args holds every user value in order.
type Compiled struct {
	Kind              ResultKind
	Source            model.Source
	SQL               string
	Args              []any
	Columns           []Column
	Span              time.Duration // timeseries bucket size
	Limit             int           // effective row limit
	InterpretedAsText bool
}

// Compile checks q and compiles it to ClickHouse SQL. Errors are *Error for
// problems in the query and wrap ErrInvalidOptions for unusable options.
func Compile(q *Query, opts Options) (*Compiled, error) {
	c, err := newCompiler(q, opts)
	if err != nil {
		return nil, err
	}
	out := &Compiled{Kind: c.p.kind, Source: c.s.src, InterpretedAsText: q.InterpretedAsText}
	var b sqlBuf
	if c.p.kind == KindEvents {
		c.events(&b, out)
	} else if err := c.statsQuery(&b, out); err != nil {
		return nil, err
	}
	out.SQL, out.Args = b.String(), b.args
	return out, nil
}

// CompileWhere returns only the WHERE condition (time range, filter, where
// stages before stats, extra filters) for reuse by histogram, facets and tail.
// Stages after the filter are ignored.
func CompileWhere(q *Query, opts Options) (string, []any, error) {
	c, err := newCompiler(q, opts)
	if err != nil {
		return "", nil, err
	}
	var b sqlBuf
	c.where(&b)
	return b.String(), b.args, nil
}

// compiler holds the state of one compilation.
type compiler struct {
	p       *plan
	s       *sourceSchema
	o       Options
	indexed map[string]string
	extra   []Expr
	scope   *havingScope // set while rendering HAVING
}

func newCompiler(q *Query, opts Options) (*compiler, error) {
	p, err := analyze(q)
	if err != nil {
		return nil, err
	}
	o, err := normalizeOptions(opts)
	if err != nil {
		return nil, err
	}
	c := &compiler{p: p, s: p.schema, o: o, indexed: o.IndexedFields[p.schema.src]}
	for i, ef := range o.ExtraFilters {
		if ef == nil {
			continue
		}
		e, err := extraFilter(i, ef, p.schema.src)
		if err != nil {
			return nil, err
		}
		if e != nil {
			c.extra = append(c.extra, e)
		}
	}
	return c, nil
}

func extraFilter(i int, ef *Query, src model.Source) (Expr, error) {
	ep, err := analyze(ef)
	if err != nil {
		return nil, fmt.Errorf("%w: extra filter %d: %w", ErrInvalidOptions, i, err)
	}
	if ep.schema.src != src {
		return nil, fmt.Errorf("%w: extra filter %d is for source %s, query is %s", ErrInvalidOptions, i, ep.schema.src, src)
	}
	if len(ef.Stages) > 0 {
		return nil, fmt.Errorf("%w: extra filter %d must not have stages", ErrInvalidOptions, i)
	}
	return ef.Filter, nil
}

func normalizeOptions(o Options) (Options, error) {
	if !o.Streams.IsSet() {
		return o, fmt.Errorf("%w: a stream scope is required", ErrInvalidOptions)
	}
	if o.From.IsZero() || o.To.IsZero() || !o.From.Before(o.To) {
		return o, fmt.Errorf("%w: a time range with From before To is required", ErrInvalidOptions)
	}
	if o.Database != "" && !reDatabase.MatchString(o.Database) {
		return o, fmt.Errorf("%w: invalid database name", ErrInvalidOptions)
	}
	for src, m := range o.IndexedFields {
		for key, col := range m {
			if !reIndexedCol.MatchString(col) {
				return o, fmt.Errorf("%w: invalid indexed column for %s key %q", ErrInvalidOptions, src, key)
			}
		}
	}
	if o.Now.IsZero() {
		o.Now = o.To
	}
	if o.MaxRows <= 0 {
		o.MaxRows = DefaultMaxRows
	}
	if o.Limit <= 0 {
		o.Limit = DefaultLimit
	}
	o.Limit = min(o.Limit, o.MaxRows)
	if o.AutoSpan < 0 || (o.AutoSpan > 0 && o.AutoSpan%time.Second != 0) {
		return o, fmt.Errorf("%w: AutoSpan must be a whole number of seconds", ErrInvalidOptions)
	}
	return o, nil
}

func (c *compiler) table() string {
	if c.o.Database == "" {
		return c.s.table
	}
	return c.o.Database + "." + c.s.table
}

// where renders the full WHERE condition.
func (c *compiler) where(b *sqlBuf) {
	b.w(c.s.timeCol, " >= ")
	b.arg(c.o.From.UTC())
	b.w(" AND ", c.s.timeCol, " < ")
	b.arg(c.o.To.UTC())
	c.streamScope(b)
	for _, e := range append(append([]Expr{}, c.p.filters...), c.extra...) {
		b.w(" AND ")
		c.exprParen(b, e)
	}
}

// streamScope renders the stream condition of the caller's scope (SPEC §15.10). Every source
// table has the `stream` column first in its sort key, so the condition prunes by primary index.
func (c *compiler) streamScope(b *sqlBuf) {
	switch sc := c.o.Streams; {
	case sc.All():
	case sc.Empty():
		b.w(" AND 0")
	default:
		b.w(" AND stream IN (")
		for i, n := range sc.Names() {
			if i > 0 {
				b.w(", ")
			}
			b.arg(n)
		}
		b.w(")")
	}
}

// exprParen renders e wrapped in parentheses unless it already is.
func (c *compiler) exprParen(b *sqlBuf, e Expr) {
	switch e.(type) {
	case *And, *Or:
		c.expr(b, e)
	default:
		b.w("(")
		c.expr(b, e)
		b.w(")")
	}
}

// events renders the event-list query with keyset pagination.
func (c *compiler) events(b *sqlBuf, out *Compiled) {
	b.w("SELECT ")
	if c.p.fields != nil {
		out.Columns = c.projection(b)
	} else {
		for i, ecol := range c.s.eventCols {
			if i > 0 {
				b.w(", ")
			}
			b.w(ecol.expr)
			if ecol.expr != ecol.col.Name {
				b.w(" AS ", ecol.col.Name)
			}
			out.Columns = append(out.Columns, ecol.col)
		}
	}
	b.w(" FROM ", c.table(), " WHERE ")
	c.where(b)
	if cur := c.o.Cursor; cur != nil {
		b.w(" AND (", c.s.timeCol, ", ", c.s.idCol, ") < (")
		b.arg(cur.Timestamp.UTC())
		b.w(", ")
		if c.s.idCast != "" {
			b.w(c.s.idCast, "(")
			b.arg(cur.ID)
			b.w(")")
		} else {
			b.arg(cur.ID)
		}
		b.w(")")
	}
	b.w(" ORDER BY ", c.s.timeCol, " DESC, ", c.s.idCol, " DESC LIMIT ")
	out.Limit = c.o.Limit
	if c.p.limit > 0 {
		out.Limit = min(out.Limit, c.p.limit)
	}
	b.arg(out.Limit)
}

func columnTypeOf(t fieldType) ColumnType {
	switch t {
	case typeNumber, typeLevel:
		return ColumnNumber
	case typeTime:
		return ColumnTime
	case typeDuration:
		return ColumnDuration
	}
	return ColumnString
}

// projection renders `fields` columns: time, id, then each field as f<i>.
func (c *compiler) projection(b *sqlBuf) []Column {
	cols := []Column{c.s.eventCols[0].col, {Name: c.s.idCol, Type: ColumnString}}
	b.w(c.s.timeCol, ", ", c.s.idCol)
	for i, f := range c.p.fields {
		a := c.access(f)
		b.w(", ")
		a.val(b)
		b.w(" AS f", strconv.Itoa(i))
		r, _ := c.s.resolve(f)
		t := columnTypeOf(r.typ())
		if r.typ() == typeLevel {
			t = ColumnString
		}
		cols = append(cols, Column{Name: f.Name, Type: t})
	}
	return cols
}

// chooseSpan resolves the bucket size and validates the bucket count.
func (c *compiler) chooseSpan(sc *SpanClause) (time.Duration, error) {
	rng := c.o.To.Sub(c.o.From)
	d := sc.Dur
	if sc.Auto {
		d = c.o.AutoSpan
		if d == 0 {
			d = autoSpan(rng, DefaultAutoBuckets)
		}
	}
	if buckets := math.Ceil(float64(rng) / float64(d)); buckets > MaxBuckets {
		e := nodeErr(sc, "span %s gives %.0f buckets for this time range (max %d)", formatDuration(d), buckets, MaxBuckets)
		return 0, e.withHint("use a larger span or 'span auto'")
	}
	return d, nil
}

// autoSpans are the candidate bucket sizes (SPEC §11.3).
func autoSpans() []time.Duration {
	return []time.Duration{time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second, time.Minute,
		5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour, 3 * time.Hour, 6 * time.Hour,
		12 * time.Hour, 24 * time.Hour}
}

// AutoSpan returns the smallest bucket of SPEC §11.3 (1s … 1d, then whole days) that splits
// rng into at most target buckets. It is used by the histogram endpoint and `span auto`.
func AutoSpan(rng time.Duration, target int) time.Duration {
	return autoSpan(rng, max(target, 1))
}

// autoSpan picks the smallest candidate giving at most target buckets.
func autoSpan(rng time.Duration, target int) time.Duration {
	spans := autoSpans()
	for _, s := range spans {
		if int64(rng/s) <= int64(target) {
			return s
		}
	}
	day := spans[len(spans)-1]
	n := (rng/day + time.Duration(target) - 1) / time.Duration(target)
	return n * day
}
