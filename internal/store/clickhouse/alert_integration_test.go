// SPDX-License-Identifier: Apache-2.0

//go:build integration

package clickhouse

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ZeraOrg/opentidemark/internal/alerting"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	"github.com/ZeraOrg/opentidemark/internal/model"
)

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type leaderAlways struct{}

func (leaderAlways) IsLeader(string) bool { return true }

type captured struct {
	mu   sync.Mutex
	list []alerting.Notification
}

func (c *captured) Enqueue(n alerting.Notification, _ []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, n)
}

// TestIntegrationAlertEvaluation evaluates a grouped rule on events written to ClickHouse:
// only the group above the threshold fires, the state and history are persisted and one
// notification is enqueued.
func TestIntegrationAlertEvaluation(t *testing.T) {
	e := setupAdmin(t)
	ctx := context.Background()
	now := time.Now().UTC()
	w, err := NewWriter(e.conn, e.db)
	if err != nil {
		t.Fatal(err)
	}
	recent := func(svc string, level model.Level) model.Event {
		return model.Event{ID: uuid.Must(uuid.NewV7()), Timestamp: now.Add(-time.Minute), IngestedAt: now, Stream: "api",
			Level: level, Service: svc, Message: "boom", Protocol: "native"}
	}
	if err := w.WriteEvents(ctx, []model.Event{recent("checkout", model.Error), recent("checkout", model.Fatal),
		recent("checkout", model.Error), recent("frontend", model.Error), recent("frontend", model.Information)}); err != nil {
		t.Fatal(err)
	}
	db, err := meta.Open(ctx, filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rules := meta.NewAlertRules(db)
	doc := json.RawMessage(`{"query":"level >= error | stats count() by service","condition":{"op":">","threshold":2},
		"window":"5m","every":"1m","for":"0s","severity":"critical","channels":["c1"],"groupBy":["service"]}`)
	rule := &meta.AlertRule{Name: "errors", Enabled: true, Doc: doc}
	if err := rules.Create(ctx, rule); err != nil {
		t.Fatal(err)
	}
	notes := &captured{}
	ev := alerting.NewEvaluator(alerting.EvaluatorDeps{Rules: rules, Querier: newAlertReader(t, e), Notify: notes,
		Clock: wallClock{}, Leader: leaderAlways{}, Database: e.db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if ran := ev.Tick(ctx); ran != 1 {
		t.Fatalf("ran %d rules", ran)
	}
	got, err := rules.Get(ctx, rule.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "firing" || got.LastError != "" || got.LastValue == nil || *got.LastValue != 3 {
		t.Fatalf("rule after tick: state=%s err=%q value=%v", got.State, got.LastError, got.LastValue)
	}
	hist, err := rules.History(ctx, rule.ID, 10)
	if err != nil || len(hist) != 1 || hist[0].ToState != "firing" || hist[0].GroupKey != `{"service":"checkout"}` {
		t.Fatalf("history %+v %v", hist, err)
	}
	if len(notes.list) != 1 || notes.list[0].Group["service"] != "checkout" || notes.list[0].State != "firing" {
		t.Fatalf("notifications %+v", notes.list)
	}
	if ran := ev.Tick(ctx); ran != 0 {
		t.Errorf("rule re-ran before `every` elapsed: %d", ran)
	}
}

// newAlertReader returns the production query engine on the read-only connection.
func newAlertReader(t *testing.T, e *adminEnv) *Reader {
	t.Helper()
	r, err := NewReader(e.reader, ReaderConfig{Database: e.db, QueryTimeout: 30 * time.Second, MaxMemoryBytes: DefaultReaderMemory})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
