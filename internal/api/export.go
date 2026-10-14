// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// exportWriteTimeout replaces the server write deadline for an export download (up to
// service.ExportMaxEvents rows may take longer than the default 120 s).
const exportWriteTimeout = 10 * time.Minute

// export streams the events of a query as NDJSON or CSV (SPEC §11.3, editor+, audited).
func (a *SearchAPI) export(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	rng, err := a.queryRange(q)
	if err != nil {
		return err
	}
	format := q.Get("format")
	if format == "" {
		format = service.ExportNDJSON
	}
	bw := bufio.NewWriter(w)
	var enc exportEncoder
	started := false
	start := func() error {
		started = true
		rc := http.NewResponseController(w)
		if err := rc.SetWriteDeadline(time.Now().Add(exportWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return fmt.Errorf("extend write deadline: %w", err)
		}
		ctype, ext := "application/x-ndjson", "ndjson"
		enc = &ndjsonEncoder{enc: json.NewEncoder(bw)}
		if format == service.ExportCSV {
			ctype, ext = "text/csv; charset=utf-8", "csv"
			enc = &csvEncoder{w: csv.NewWriter(bw)}
		}
		w.Header().Set("Content-Type", ctype)
		w.Header().Set("Content-Disposition", `attachment; filename="tidemark-export.`+ext+`"`)
		w.WriteHeader(http.StatusOK)
		return nil
	}
	_, err = a.Search.Export(r.Context(), actor(r, a.ClientIP), principal(r), service.ExportRequest{Q: q.Get("q"),
		Range: rng, Format: format, Signals: listParam(q, "signals"), Select: selectionParam(q)}, func(res store.Result) error {
		if !started {
			if err := start(); err != nil {
				return err
			}
		}
		return enc.page(res)
	})
	if err != nil && !started {
		return err
	}
	if err == nil {
		err = bw.Flush()
	}
	if err != nil && r.Context().Err() == nil {
		a.Logger.Warn("export aborted", "err", err)
	}
	return nil
}

// exportEncoder writes one result page.
type exportEncoder interface {
	page(res store.Result) error
}

type ndjsonEncoder struct{ enc *json.Encoder }

func (e *ndjsonEncoder) page(res store.Result) error {
	for _, ev := range res.Events {
		if err := e.enc.Encode(toEventView(ev)); err != nil {
			return err
		}
	}
	for _, rec := range res.Records {
		if err := e.enc.Encode(rec); err != nil {
			return err
		}
	}
	return nil
}

// eventCSVHeader is the CSV layout of log events; properties are one JSON column.
func eventCSVHeader() []string {
	return []string{"timestamp", "level", "stream", "service", "environment", "host", "message", "exception",
		"trace_id", "span_id", "event_type", "id", "properties"}
}

type csvEncoder struct {
	w      *csv.Writer
	header bool
}

func (e *csvEncoder) page(res store.Result) error {
	if !e.header {
		e.header = true
		if err := e.w.Write(csvHeader(res)); err != nil {
			return err
		}
	}
	for _, ev := range res.Events {
		props, err := json.Marshal(ev.Properties)
		if err != nil {
			return err
		}
		row := []string{ev.Timestamp.UTC().Format(time.RFC3339Nano), ev.Level.String(), ev.Stream, ev.Service,
			ev.Environment, ev.Host, ev.Message, ev.Exception, ev.TraceID, ev.SpanID, ev.EventType, ev.ID.String(), string(props)}
		if err := e.w.Write(safeCells(row)); err != nil {
			return err
		}
	}
	for _, rec := range res.Records {
		row := make([]string, 0, len(res.Columns))
		for _, c := range res.Columns {
			row = append(row, cellText(rec[c.Name]))
		}
		if err := e.w.Write(safeCells(row)); err != nil {
			return err
		}
	}
	e.w.Flush()
	return e.w.Error()
}

func csvHeader(res store.Result) []string {
	if res.Records == nil {
		return eventCSVHeader()
	}
	out := make([]string, 0, len(res.Columns))
	for _, c := range res.Columns {
		out = append(out, c.Name)
	}
	return out
}

// cellText renders a record value: times as RFC 3339, strings as-is, everything else as JSON.
func cellText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case json.RawMessage:
		return string(x)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// safeCells neutralizes spreadsheet formula injection: a cell starting with = + - @ tab or
// carriage return is prefixed with a single quote.
func safeCells(row []string) []string {
	for i, c := range row {
		if c != "" && strings.ContainsRune("=+-@\t\r", rune(c[0])) {
			row[i] = "'" + c
		}
	}
	return row
}
