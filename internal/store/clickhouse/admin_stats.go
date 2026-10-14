// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

// partTotals are the active-part totals of one table from system.parts.
type partTotals struct {
	compressed   uint64
	uncompressed uint64
}

// Stats implements store.StreamAdmin: per stream and source row counts, time range, ingest
// rates (rows/s over the last 5 min and 24 h), top services and storage bytes. Bytes come
// from system.parts per table and are apportioned to streams by row share (Estimated).
func (a *Admin) Stats(ctx context.Context) ([]store.StreamStats, error) {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	parts, err := a.partTotals(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.StreamStats{}
	for _, src := range []model.Source{model.SourceLogs, model.SourceSpans, model.SourceMetrics} {
		st := sourceTables[src]
		rows, err := a.streamRows(ctx, src, st)
		if err != nil {
			return nil, err
		}
		apportion(rows, parts[st.table])
		out = append(out, rows...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stream != out[j].Stream {
			return out[i].Stream < out[j].Stream
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

func (a *Admin) partTotals(ctx context.Context) (map[string]partTotals, error) {
	rows, err := a.conn.Query(ctx, `SELECT table, sum(data_compressed_bytes), sum(data_uncompressed_bytes)
		FROM system.parts WHERE database = ? AND active GROUP BY table`, a.db)
	if err != nil {
		return nil, fmt.Errorf("query parts: %w", err)
	}
	defer rows.Close()
	out := map[string]partTotals{}
	for rows.Next() {
		var t string
		var p partTotals
		if err := rows.Scan(&t, &p.compressed, &p.uncompressed); err != nil {
			return nil, fmt.Errorf("scan parts: %w", err)
		}
		out[t] = p
	}
	return out, rows.Err()
}

func (a *Admin) streamRows(ctx context.Context, src model.Source, st sourceTable) ([]store.StreamStats, error) {
	now := a.now().UTC()
	q := "SELECT stream, count(), min(" + st.timeCol + "), max(" + st.timeCol + "), countIf(" + st.ingested + " >= ?), countIf(" +
		st.ingested + " >= ?), topK(" + strconv.Itoa(topServicesCount) + ")(service) FROM " + a.qualified(st.table) +
		" GROUP BY stream ORDER BY stream"
	rows, err := a.conn.Query(ctx, q, now.Add(-rateWindowShort), now.Add(-rateWindowLong))
	if err != nil {
		return nil, fmt.Errorf("query %s stats: %w", st.table, err)
	}
	defer rows.Close()
	var out []store.StreamStats
	for rows.Next() {
		s := store.StreamStats{Source: src, Estimated: true}
		var short, long uint64
		if err := rows.Scan(&s.Stream, &s.Rows, &s.Oldest, &s.Newest, &short, &long, &s.TopServices); err != nil {
			return nil, fmt.Errorf("scan %s stats: %w", st.table, err)
		}
		s.RatePer5Min = float64(short) / rateWindowShort.Seconds()
		s.RatePer24h = float64(long) / rateWindowLong.Seconds()
		s.Oldest, s.Newest = s.Oldest.UTC(), s.Newest.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// apportion distributes the table's bytes over its streams by row share. Integer arithmetic
// keeps the sum of the shares within the table total.
func apportion(rows []store.StreamStats, p partTotals) {
	var total uint64
	for i := range rows {
		total += rows[i].Rows
	}
	if total == 0 {
		return
	}
	for i := range rows {
		rows[i].CompressedBytes = share(p.compressed, rows[i].Rows, total)
		rows[i].UncompressedBytes = share(p.uncompressed, rows[i].Rows, total)
	}
}

// share returns bytes*rows/total without overflow.
func share(bytes, rows, total uint64) uint64 {
	n := new(big.Int).Mul(new(big.Int).SetUint64(bytes), new(big.Int).SetUint64(rows))
	return n.Div(n, new(big.Int).SetUint64(total)).Uint64()
}
