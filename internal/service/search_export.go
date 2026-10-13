// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/store"
	"github.com/ZeraOrg/tidemark/internal/tql"
)

// ActionExport is the audit action of GET /export (SPEC §15.5).
const ActionExport = "export"

// Export formats.
const (
	ExportNDJSON = "ndjson"
	ExportCSV    = "csv"
)

// ExportRequest is the input of Export.
type ExportRequest struct {
	Q       string
	Range   Range
	Format  string
	Signals []string
}

// Export runs an events query page by page (newest first) and hands each page to emit, up to
// ExportMaxEvents rows. It is audited before the first page. It returns the rows exported.
func (s *Search) Export(ctx context.Context, actor Actor, p auth.Principal, req ExportRequest,
	emit func(store.Result) error) (int, error) {
	if req.Format != ExportNDJSON && req.Format != ExportCSV {
		return 0, invalid("format", "must be ndjson or csv")
	}
	if err := req.Range.valid(); err != nil {
		return 0, err
	}
	pr, err := s.prepare(ctx, p, req.Q, "", req.Signals)
	if err != nil {
		return 0, err
	}
	opts := s.options(pr, req.Range)
	opts.Limit = s.d.MaxRows
	c, err := tql.Compile(pr.q, opts)
	if err != nil {
		return 0, err
	}
	if c.Kind != tql.KindEvents {
		return 0, invalid("q", "export supports event queries only (no stats)")
	}
	s.d.Audit.Record(ctx, actor, ActionExport, string(c.Source), map[string]any{"q": req.Q, "from": req.Range.From,
		"to": req.Range.To, "format": req.Format, "signals": req.Signals})
	total := 0
	for {
		res, err := s.d.Store.Run(ctx, c)
		if err != nil {
			return total, err
		}
		if err := emit(res); err != nil {
			return total, err
		}
		total += len(res.Events) + len(res.Records)
		if res.NextCursor == nil || total >= ExportMaxEvents {
			return total, nil
		}
		opts.Cursor, opts.Limit = res.NextCursor, min(s.d.MaxRows, ExportMaxEvents-total)
		if c, err = tql.Compile(pr.q, opts); err != nil {
			return total, err
		}
	}
}
