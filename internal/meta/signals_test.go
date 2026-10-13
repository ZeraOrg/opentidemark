// SPDX-License-Identifier: Apache-2.0

package meta

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSignalsUpdateIfUnchanged(t *testing.T) {
	ctx := context.Background()
	tick := t0
	db, err := Open(ctx, filepath.Join(t.TempDir(), DBFileName), WithClock(func() time.Time {
		tick = tick.Add(time.Second)
		return tick
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sig := NewSignals(db)
	s := &Signal{Name: "errors", TQL: "level:error", OwnerID: "u1", Source: "logs"}
	if err := sig.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	stale := s.UpdatedAt
	s.Name = "errors v2"
	if err := sig.UpdateIfUnchanged(ctx, s, stale); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if !s.UpdatedAt.After(stale) {
		t.Errorf("updated_at not advanced: %v", s.UpdatedAt)
	}
	if got, _ := sig.Get(ctx, s.ID); got.Name != "errors v2" || !got.UpdatedAt.Equal(s.UpdatedAt) {
		t.Errorf("stored %+v", got)
	}
	if err := sig.UpdateIfUnchanged(ctx, s, stale); !errors.Is(err, ErrConflict) {
		t.Errorf("stale update: %v", err)
	}
	if err := sig.UpdateIfUnchanged(ctx, &Signal{ID: "missing"}, stale); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing update: %v", err)
	}
}
