// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"context"
	"errors"
	"strings"
	"testing"

	ch "github.com/ClickHouse/clickhouse-go/v2"

	"github.com/ZeraOrg/opentidemark/internal/store"
)

func TestRenderForCluster(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"database", "CREATE DATABASE IF NOT EXISTS tidemark", "CREATE DATABASE IF NOT EXISTS tidemark ON CLUSTER 'tm'"},
		{"plain mergetree",
			"CREATE TABLE IF NOT EXISTS tidemark.spans (\n  a UInt8\n) ENGINE = MergeTree\nORDER BY a",
			"CREATE TABLE IF NOT EXISTS tidemark.spans ON CLUSTER 'tm' (\n  a UInt8\n) ENGINE = ReplicatedMergeTree('/clickhouse/tables/{shard}/{database}/spans', '{replica}')\nORDER BY a"},
		{"replacing with version column",
			"CREATE TABLE IF NOT EXISTS tidemark.retention_state (tbl String, applied_at DateTime64(3)) ENGINE = ReplacingMergeTree(applied_at) ORDER BY tbl",
			"CREATE TABLE IF NOT EXISTS tidemark.retention_state ON CLUSTER 'tm' (tbl String, applied_at DateTime64(3)) ENGINE = ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/{database}/retention_state', '{replica}', applied_at) ORDER BY tbl"},
		{"aggregating",
			"CREATE TABLE IF NOT EXISTS tidemark.field_catalog (\n  hits SimpleAggregateFunction(sum, UInt64)\n) ENGINE = AggregatingMergeTree ORDER BY (hits)",
			"CREATE TABLE IF NOT EXISTS tidemark.field_catalog ON CLUSTER 'tm' (\n  hits SimpleAggregateFunction(sum, UInt64)\n) ENGINE = ReplicatedAggregatingMergeTree('/clickhouse/tables/{shard}/{database}/field_catalog', '{replica}') ORDER BY (hits)"},
		{"materialized view",
			"CREATE MATERIALIZED VIEW IF NOT EXISTS tidemark.mv_x TO tidemark.field_catalog AS\nSELECT 1",
			"CREATE MATERIALIZED VIEW IF NOT EXISTS tidemark.mv_x ON CLUSTER 'tm' TO tidemark.field_catalog AS\nSELECT 1"},
		{"alter unchanged", "ALTER TABLE tidemark.events MODIFY SETTING x = 1", "ALTER TABLE tidemark.events MODIFY SETTING x = 1"},
	}
	for _, tc := range tests {
		got, err := RenderForCluster(tc.in, "tm")
		if err != nil || got != tc.want {
			t.Errorf("%s:\n got %q (%v)\nwant %q", tc.name, got, err, tc.want)
		}
	}
	if got, _ := RenderForCluster("CREATE DATABASE IF NOT EXISTS x", ""); got != "CREATE DATABASE IF NOT EXISTS x" {
		t.Errorf("single node changed: %q", got)
	}
	if _, err := RenderForCluster("CREATE DATABASE IF NOT EXISTS x", "bad'name"); !errors.Is(err, ErrInvalidCluster) {
		t.Errorf("invalid cluster: %v", err)
	}
	if _, err := RenderForCluster("CREATE FUNCTION f AS x -> x", "tm"); err == nil {
		t.Error("unknown CREATE accepted")
	}
}

// TestClusterPlanRendersEveryMigration checks the full embedded schema in cluster mode.
func TestClusterPlanRendersEveryMigration(t *testing.T) {
	plan, err := NewMigrator(nil, "tidemark", WithCluster("tidemark_cluster")).Plan()
	if err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, m := range plan {
		for _, s := range m.Statements {
			if !strings.HasPrefix(s, "CREATE") {
				continue
			}
			creates++
			if !strings.Contains(s, " ON CLUSTER 'tidemark_cluster'") {
				t.Errorf("%s: missing ON CLUSTER: %.80s", m.Name, s)
			}
			if strings.Contains(s, "ENGINE = ") && !strings.Contains(s, "ENGINE = Replicated") {
				t.Errorf("%s: engine not replicated: %.120s", m.Name, s)
			}
		}
	}
	if creates < 7 {
		t.Errorf("only %d CREATE statements rendered", creates)
	}
	single, _ := NewMigrator(nil, "tidemark").Plan()
	for _, m := range single {
		for _, s := range m.Statements {
			if strings.Contains(s, "ON CLUSTER") || strings.Contains(s, "Replicated") {
				t.Errorf("single-node plan contains cluster DDL: %.80s", s)
			}
		}
	}
}

func TestWriterInsertSettings(t *testing.T) {
	w, err := NewWriter(nil, "tidemark", WithInsertQuorum(QuorumAuto))
	if err != nil {
		t.Fatal(err)
	}
	s := w.InsertSettings(store.WithDedupToken(context.Background(), "batch-1"))
	if s["insert_deduplication_token"] != "batch-1" || s["insert_quorum"] != "auto" || s["insert_deduplicate"] != 1 {
		t.Errorf("settings %v", s)
	}
	plain, _ := NewWriter(nil, "tidemark")
	if got := plain.InsertSettings(context.Background()); len(got) != 0 {
		t.Errorf("plain settings %v", got)
	}
	for q, ok := range map[string]bool{"auto": true, "2": true, "0": false, "65": false, "x": false} {
		if ValidQuorum(q) != ok {
			t.Errorf("ValidQuorum(%q) != %v", q, ok)
		}
	}
	if _, err := NewWriter(nil, "tidemark", WithInsertQuorum("many")); err == nil {
		t.Error("invalid quorum accepted")
	}
}

func TestIsPermanent(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("dial tcp: connection refused"), false},
		{context.DeadlineExceeded, false},
		{&ch.Exception{Code: 252, Message: "too many parts"}, false},
		{&ch.Exception{Code: 999, Message: "keeper"}, false},
		{&ch.Exception{Code: 53, Message: "type mismatch"}, true},
		{&ch.Exception{Code: 27, Message: "cannot parse"}, true},
		{ErrRowRejected, true},
	}
	for _, tc := range tests {
		if got := IsPermanent(tc.err); got != tc.want {
			t.Errorf("IsPermanent(%v) = %v", tc.err, got)
		}
	}
}
