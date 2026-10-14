// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
)

func TestWholeDayPartitions(t *testing.T) {
	d := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	tests := []struct {
		from, to string
		want     []string
	}{
		{"2026-10-01T00:00:00Z", "2026-10-02T00:00:00Z", []string{"20261001"}},
		{"2026-10-01T00:00:00Z", "2026-10-01T23:59:59Z", nil},
		{"2026-10-01T06:00:00Z", "2026-10-04T06:00:00Z", []string{"20261002", "20261003"}},
		{"2026-10-01T00:00:00Z", "2026-10-03T00:00:00Z", []string{"20261001", "20261002"}},
		{"2026-10-03T00:00:00Z", "2026-10-01T00:00:00Z", nil},
		{"2026-10-01T02:00:00+02:00", "2026-10-02T00:00:00Z", []string{"20261001"}},
	}
	for _, tc := range tests {
		if got := WholeDayPartitions(d(tc.from), d(tc.to)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s..%s = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestPurgeWhere(t *testing.T) {
	from, to := time.Unix(100, 0), time.Unix(200, 0)
	st := sourceTables[model.SourceSpans]
	w, args := purgeWhere(st, store.PurgeRequest{From: from, To: to})
	if w != "start_time >= ? AND start_time < ?" || len(args) != 2 {
		t.Errorf("plain: %s %v", w, args)
	}
	w, args = purgeWhere(st, store.PurgeRequest{From: from, To: to, Stream: "api", Where: "x = ?", Args: []any{1}})
	if w != "(x = ?) AND stream = ?" || !reflect.DeepEqual(args, []any{1, "api"}) {
		t.Errorf("filtered: %s %v", w, args)
	}
}

func TestIndexedFieldDDL(t *testing.T) {
	got := indexedFieldDDL("tidemark.events", "props", store.IndexedField{Source: model.SourceLogs, Key: "user.id", Column: "f_user_id"})
	want := []string{
		"ALTER TABLE tidemark.events ADD COLUMN IF NOT EXISTS f_user_id String MATERIALIZED props['user.id']",
		"ALTER TABLE tidemark.events ADD INDEX IF NOT EXISTS idx_f_user_id f_user_id TYPE bloom_filter(0.01) GRANULARITY 4",
		"ALTER TABLE tidemark.events MATERIALIZE INDEX idx_f_user_id",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestIndexedFieldValidation(t *testing.T) {
	a := &Admin{db: "tidemark"} // validation fails before the connection is used
	bad := []store.IndexedField{
		{Source: "nope", Key: "k", Column: "f_k"},
		{Source: model.SourceLogs, Key: "a'b", Column: "f_ab"},
		{Source: model.SourceLogs, Key: `a\b`, Column: "f_ab"},
		{Source: model.SourceLogs, Key: "a b", Column: "f_ab"},
		{Source: model.SourceLogs, Key: "k", Column: "f_K"},
		{Source: model.SourceLogs, Key: "k", Column: "x; DROP"},
		{Source: model.SourceLogs, Key: strings.Repeat("k", 129), Column: "f_k"},
	}
	for _, f := range bad {
		if err := a.AddIndexedField(context.Background(), f); err == nil {
			t.Errorf("AddIndexedField(%+v) accepted", f)
		}
	}
	if err := a.DropIndexedField(context.Background(), store.IndexedField{Source: model.SourceLogs, Column: "f_x;"}); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("drop: %v", err)
	}
	if err := a.DeleteStream(context.Background(), "Bad'Name"); !errors.Is(err, ErrInvalidIdentifier) {
		t.Errorf("delete stream: %v", err)
	}
	if _, err := a.Purge(context.Background(), store.PurgeRequest{Source: "x"}); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("purge: %v", err)
	}
}

func TestMappingType(t *testing.T) {
	for _, tc := range []struct {
		in   []uint8
		want string
	}{{nil, "string"}, {[]uint8{0}, "string"}, {[]uint8{1}, "number"}, {[]uint8{1, 0}, "mixed"}} {
		if got := mappingType(tc.in); got != tc.want {
			t.Errorf("%v = %s", tc.in, got)
		}
	}
}

func TestApportion(t *testing.T) {
	rows := []store.StreamStats{{Stream: "a", Rows: 1}, {Stream: "b", Rows: 3}}
	apportion(rows, partTotals{compressed: 1000, uncompressed: 4000})
	if rows[0].CompressedBytes != 250 || rows[1].CompressedBytes != 750 || rows[1].UncompressedBytes != 3000 {
		t.Errorf("%+v", rows)
	}
	empty := []store.StreamStats{{Stream: "a"}}
	apportion(empty, partTotals{compressed: 10})
	if empty[0].CompressedBytes != 0 {
		t.Error("zero rows must get no bytes")
	}
	if got := share(^uint64(0), 3, 4); got != ^uint64(0)/4*3+2 {
		t.Errorf("large share = %d", got)
	}
}
