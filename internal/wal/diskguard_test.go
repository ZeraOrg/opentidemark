// SPDX-License-Identifier: Apache-2.0

package wal

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeDisk struct {
	free  uint64
	err   error
	reads int
	now   time.Time
}

func (d *fakeDisk) read() (uint64, error) { d.reads++; return d.free, d.err }
func (d *fakeDisk) clock() time.Time      { return d.now }

func TestDiskGuardAllow(t *testing.T) {
	tests := []struct {
		name    string
		minFree uint64
		free    uint64
		err     error
		sizes   []int64
		want    []bool
	}{
		{name: "plenty of space", minFree: 100, free: 1000, sizes: []int64{100, 100}, want: []bool{true, true}},
		{name: "appends between readings count", minFree: 100, free: 300, sizes: []int64{150, 50, 1}, want: []bool{true, true, false}},
		{name: "already below the floor", minFree: 100, free: 99, sizes: []int64{1}, want: []bool{false}},
		{name: "exactly at the floor", minFree: 100, free: 150, sizes: []int64{50, 1}, want: []bool{true, false}},
		{name: "disabled", minFree: 0, free: 0, sizes: []int64{1 << 30}, want: []bool{true}},
		{name: "unreadable disk never refuses", minFree: 100, err: errors.New("unsupported"), sizes: []int64{1 << 30}, want: []bool{true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDisk{free: tc.free, err: tc.err, now: time.Unix(1, 0)}
			g := NewDiskGuard(tc.minFree, d.read, d.clock)
			for i, n := range tc.sizes {
				if got := g.allow(n); got != tc.want[i] {
					t.Fatalf("allow #%d (%d bytes) = %v, want %v", i, n, got, tc.want[i])
				}
			}
		})
	}
}

func TestDiskGuardRecoversWhenSpaceIsFreed(t *testing.T) {
	d := &fakeDisk{free: 50, now: time.Unix(1, 0)}
	g := NewDiskGuard(100, d.read, d.clock)
	if g.allow(1) {
		t.Fatal("allowed below the floor")
	}
	d.free = 1 << 20
	if g.allow(1) {
		t.Fatal("reread the disk before the interval")
	}
	d.now = d.now.Add(DiskCheckInterval)
	if !g.allow(1) {
		t.Fatal("still refusing after space was freed")
	}
	if d.reads != 2 {
		t.Fatalf("disk read %d times, want 2", d.reads)
	}
}

func TestAppendRefusedBelowDiskFloor(t *testing.T) {
	d := &fakeDisk{free: 1 << 20, now: time.Unix(1, 0)}
	budget := NewBudget(0)
	l, _ := openLog(t, t.TempDir(), Options{Disk: NewDiskGuard(1<<20, d.read, d.clock), Budget: budget})
	defer l.Close()
	if _, err := l.Append(context.Background(), []byte("refused")); !errors.Is(err, ErrLowDisk) {
		t.Fatalf("append below the floor: err = %v", err)
	}
	if budget.Used() != 0 {
		t.Fatalf("refused append kept %d budget bytes", budget.Used())
	}
	d.free, d.now = 2<<20, d.now.Add(DiskCheckInterval)
	appendN(t, l, 0, 1)
	wantPayloads(t, readAll(t, l, Pos{}), "record-000")
}
