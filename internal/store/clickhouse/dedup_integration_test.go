// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/tidemark/internal/model"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// TestIntegrationReplayIsDeduplicated proves SPEC §8.5 item 7: re-sending a batch with the same
// insert_deduplication_token (a WAL replay or a retry after an unknown outcome) stores it once,
// also when the batch spans several partitions.
func TestIntegrationReplayIsDeduplicated(t *testing.T) {
	conn, env := setup(t)
	ctx := context.Background()
	if _, err := NewMigrator(conn, env.Database).Up(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(conn, env.Database)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Truncate(time.Hour)
	batch := make([]model.Event, 0, 3)
	for i := range 3 {
		ts := day.Add(-time.Duration(i) * 24 * time.Hour) // three partitions
		batch = append(batch, model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: ts, IngestedAt: ts, Stream: "dedup",
			Level: model.Information, Message: "replayed", Properties: map[string]any{"i": int64(i)}})
	}
	count := func() uint64 {
		var n uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM "+env.Database+".events WHERE stream = 'dedup'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tokCtx := store.WithDedupToken(ctx, "0190e5c4-0000-7000-8000-00000000abcd")
	for range 3 {
		if err := w.WriteEvents(tokCtx, batch); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != 3 {
		t.Fatalf("after three sends with one token: %d rows, want 3", n)
	}
	if err := w.WriteEvents(store.WithDedupToken(ctx, "another-batch"), batch); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 6 {
		t.Fatalf("a different token must insert again: %d rows, want 6", n)
	}
}

// TestIntegrationAddressFailover checks that the client fails over to the next address in order
// (SPEC §17.5: TIDEMARK_CLICKHOUSE_ADDR lists all replicas).
func TestIntegrationAddressFailover(t *testing.T) {
	_, env := setup(t)
	addrs := append([]string{"127.0.0.1:1"}, env.Addrs...)
	c, err := Open(context.Background(), Options{Addrs: addrs, Username: env.WriterUser, Password: env.WriterPassword})
	if err != nil {
		t.Fatalf("failover open: %v", err)
	}
	defer c.Close()
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}
