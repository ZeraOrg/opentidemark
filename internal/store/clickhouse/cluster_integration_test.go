// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/testutil"
)

// TestIntegrationCluster runs the schema ON CLUSTER against a 1-shard, N-replica cluster with
// ClickHouse Keeper (SPEC §17.5). Enabled by TIDEMARK_TEST_CLICKHOUSE_CLUSTER (cluster name) and
// TIDEMARK_TEST_CLICKHOUSE_CLUSTER_ADDRS (one native address per replica, comma separated);
// credentials come from the TIDEMARK_TEST_CLICKHOUSE_* variables.
func TestIntegrationCluster(t *testing.T) {
	cluster, addrs := os.Getenv("TIDEMARK_TEST_CLICKHOUSE_CLUSTER"), os.Getenv("TIDEMARK_TEST_CLICKHOUSE_CLUSTER_ADDRS")
	env, ok := testutil.ClickHouseFromEnv()
	if cluster == "" || addrs == "" || !ok {
		t.Skip("cluster test needs TIDEMARK_TEST_CLICKHOUSE_CLUSTER and TIDEMARK_TEST_CLICKHOUSE_CLUSTER_ADDRS")
	}
	ctx := context.Background()
	replicas := strings.Split(addrs, ",")
	conns := make([]driver.Conn, len(replicas))
	for i, a := range replicas {
		c, err := Open(ctx, Options{Addrs: []string{a}, Username: env.WriterUser, Password: env.WriterPassword})
		if err != nil {
			t.Fatalf("replica %s: %v", a, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		conns[i] = c
	}
	db := env.Database
	if err := conns[0].Exec(ctx, "DROP DATABASE IF EXISTS "+db+" ON CLUSTER '"+cluster+"' SYNC"); err != nil {
		t.Fatal(err)
	}
	m := NewMigrator(conns[0], db, WithCluster(cluster))
	applied, err := m.Up(ctx)
	known, _ := LoadMigrations(db)
	if err != nil || len(applied) != len(known) {
		t.Fatalf("cluster migrate: %v %v", applied, err)
	}
	if again, err := NewMigrator(conns[1], db, WithCluster(cluster)).Up(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second node re-applied %v %v", again, err)
	}
	for i, c := range conns {
		var engine string
		if err := c.QueryRow(ctx, "SELECT engine FROM system.tables WHERE database = ? AND name = 'events'", db).Scan(&engine); err != nil ||
			engine != "ReplicatedMergeTree" {
			t.Errorf("replica %d events engine %q %v", i+1, engine, err)
		}
	}
	checkQuorumInsertAndDedup(ctx, t, conns, db)
	failover, err := Open(ctx, Options{Addrs: append([]string{"127.0.0.1:1"}, replicas[1:]...), Username: env.WriterUser, Password: env.WriterPassword})
	if err != nil {
		t.Fatalf("failover: %v", err)
	}
	_ = failover.Close()
}

func checkQuorumInsertAndDedup(ctx context.Context, t *testing.T, conns []driver.Conn, db string) {
	t.Helper()
	now := time.Now().UTC()
	batch := []model.Event{}
	for i := range 3 {
		batch = append(batch, model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: now.Add(-time.Duration(i) * 24 * time.Hour),
			IngestedAt: now, Stream: "ha", Message: "replicated"})
	}
	tok := store.WithDedupToken(ctx, "cluster-batch-1")
	for i, c := range conns[:2] {
		w, err := NewWriter(c, db, WithInsertQuorum(QuorumAuto))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.WriteEvents(tok, batch); err != nil {
			t.Fatalf("quorum insert via replica %d: %v", i+1, err)
		}
	}
	for i, c := range conns {
		if err := c.Exec(ctx, "SYSTEM SYNC REPLICA "+db+".events"); err != nil {
			t.Fatalf("sync replica %d: %v", i+1, err)
		}
		var n uint64
		if err := c.QueryRow(ctx, "SELECT count() FROM "+db+".events WHERE stream = 'ha'").Scan(&n); err != nil || n != 3 {
			t.Errorf("replica %d holds %d rows (want 3, replay via another replica deduplicated): %v", i+1, n, err)
		}
	}
}
