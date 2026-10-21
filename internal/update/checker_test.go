// SPDX-License-Identifier: Apache-2.0

package update

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeSource answers with the queued results in order (the last one repeats).
type fakeSource struct {
	mu      sync.Mutex
	results []sourceResult
	calls   int
	pre     []bool
}

type sourceResult struct {
	releases []ReleaseInfo
	err      error
}

func (f *fakeSource) Releases(_ context.Context, prereleases bool) ([]ReleaseInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.results[min(f.calls, len(f.results)-1)]
	f.calls++
	f.pre = append(f.pre, prereleases)
	return r.releases, r.err
}

// fakeClock is a settable clock.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

var released = Build{Version: "0.4.0", Distribution: DistributionArchive}

func newChecker(src Source, clock *fakeClock, b Build, install Install) *Checker {
	return NewChecker(CheckerDeps{Source: src, Build: b, Install: install, NodeID: "node-1", Now: clock.Now,
		After:  func(time.Duration) <-chan time.Time { panic("After called") },
		Rand:   func(n int64) int64 { return n / 2 },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

var linuxService = Install{Enabled: true, Kind: KindSystemd, Platform: "linux/amd64"}

func TestCheckerSchedule(t *testing.T) {
	ok := sourceResult{releases: []ReleaseInfo{rel("v0.5.0")}}
	down := sourceResult{err: ErrUnreachable}
	limited := sourceResult{err: &RateLimitError{Until: testNow.Add(3 * time.Hour)}}
	const first, daily = 8 * time.Minute, 24 * time.Hour // with Rand = n/2
	tests := []struct {
		name    string
		results []sourceResult
		want    []time.Duration
	}{
		{"success", []sourceResult{ok}, []time.Duration{first, daily, daily}},
		{"retry then success", []sourceResult{down, down, ok}, []time.Duration{first, time.Hour, time.Hour, daily}},
		{"six attempts then daily", []sourceResult{down}, []time.Duration{first, time.Hour, time.Hour, time.Hour,
			time.Hour, time.Hour, daily, time.Hour}},
		{"rate limit waits for reset", []sourceResult{limited, ok}, []time.Duration{first, 3 * time.Hour, daily}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{results: tc.results}
			c := newChecker(src, &fakeClock{now: testNow}, released, linuxService)
			ctx, cancel := context.WithCancel(context.Background())
			waits := make(chan time.Duration)
			c.d.After = func(d time.Duration) <-chan time.Time {
				select {
				case waits <- d:
				case <-ctx.Done():
					return make(chan time.Time) // never fires; Run sees ctx.Done
				}
				fired := make(chan time.Time, 1)
				fired <- testNow
				return fired
			}
			done := make(chan struct{})
			go func() { c.Run(ctx); close(done) }()
			for i, want := range tc.want {
				if got := <-waits; got != want {
					t.Errorf("wait %d = %v, want %v", i, got, want)
				}
			}
			cancel()
			<-done
			if src.calls < len(tc.want)-1 {
				t.Errorf("calls = %d", src.calls)
			}
		})
	}
}

func TestCheckerScheduleBounds(t *testing.T) {
	for _, r := range []func(int64) int64{func(int64) int64 { return 0 }, func(n int64) int64 { return n - 1 }} {
		c := newChecker(&fakeSource{}, &fakeClock{now: testNow}, released, linuxService)
		c.d.Rand = r
		if d := c.daily(); d < 23*time.Hour || d >= 25*time.Hour {
			t.Errorf("daily = %v", d)
		}
	}
}

func TestCheckerNeverRunsWhenOff(t *testing.T) {
	tests := []struct {
		name    string
		build   Build
		install Install
		reason  string
	}{
		{"disabled", released, Install{Enabled: false, Kind: KindSystemd, Platform: "linux/amd64"}, ReasonDisabled},
		{"dev build", Build{Version: "dev"}, Install{Enabled: true, Kind: KindDev, Platform: "linux/amd64"}, ReasonDev},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{results: []sourceResult{{}}}
			c := newChecker(src, &fakeClock{now: testNow}, tc.build, tc.install)
			c.Run(context.Background()) // returns at once; After would panic
			_, err := c.CheckNow(context.Background())
			var ue *UnavailableError
			if !errors.As(err, &ue) || ue.Reason != tc.reason || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("err = %v", err)
			}
			if src.calls != 0 {
				t.Fatalf("source called %d times", src.calls)
			}
		})
	}
}

func TestCheckNowRateLimit(t *testing.T) {
	clock := &fakeClock{now: testNow}
	src := &fakeSource{results: []sourceResult{{releases: []ReleaseInfo{rel("v0.5.0")}}}}
	c := newChecker(src, clock, released, linuxService)
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock.Add(45 * time.Second)
	_, err := c.CheckNow(context.Background())
	var tooSoon *TooSoonError
	if !errors.As(err, &tooSoon) || tooSoon.RetryAfter != 15*time.Second || !errors.Is(err, ErrTooSoon) {
		t.Fatalf("err = %v", err)
	}
	clock.Add(15 * time.Second)
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if src.calls != 2 {
		t.Fatalf("calls = %d", src.calls)
	}
}

func TestCheckerStatus(t *testing.T) {
	clock := &fakeClock{now: testNow}
	src := &fakeSource{results: []sourceResult{{releases: []ReleaseInfo{rel("v0.5.0")}}, {err: &RateLimitError{}}}}
	c := newChecker(src, clock, released, linuxService)
	s := c.Status()
	if !s.Enabled || s.Current != "0.4.0" || s.NodeID != "node-1" || s.InstallKind != KindSystemd || s.State != StateIdle ||
		s.Available || s.Latest != nil || s.CheckedAt != nil || s.Apply != (Apply{Reason: ReasonHelperMissing}) {
		t.Fatalf("initial status %+v", s)
	}
	s, err := c.CheckNow(context.Background())
	if err != nil || !s.Available || s.Latest == nil || s.Latest.Version != "0.5.0" || s.Error != "" ||
		s.CheckedAt == nil || !s.CheckedAt.Equal(testNow) || !c.Available() {
		t.Fatalf("after success %+v %v", s, err)
	}
	clock.Add(MinCheckGap)
	s, err = c.CheckNow(context.Background())
	if err != nil || s.Error != CodeRateLimited || !s.Available || s.Latest == nil || !s.CheckedAt.Equal(testNow.Add(MinCheckGap)) {
		t.Fatalf("after failure %+v %v", s, err)
	}
	if src.pre[0] {
		t.Error("a stable node must ask for the latest stable release only")
	}
}

func TestCheckerPrereleaseNodeAsksForAll(t *testing.T) {
	src := &fakeSource{results: []sourceResult{{releases: []ReleaseInfo{rel("v0.5.0-rc.2")}}}}
	c := newChecker(src, &fakeClock{now: testNow}, Build{Version: "0.5.0-rc.1", Distribution: DistributionArchive}, linuxService)
	s, err := c.CheckNow(context.Background())
	if err != nil || !src.pre[0] || s.Latest == nil || s.Latest.Version != "0.5.0-rc.2" || !s.Latest.Prerelease {
		t.Fatalf("%+v %v %v", s, err, src.pre)
	}
}

func TestErrorCode(t *testing.T) {
	tests := map[error]string{
		ErrUnreachable:      CodeUnreachable,
		&RateLimitError{}:   CodeRateLimited,
		ErrInvalidResponse:  CodeInvalidResponse,
		ErrNotFound:         CodeNotFound,
		context.Canceled:    CodeUnreachable,
		errors.New("other"): CodeUnreachable,
	}
	for err, want := range tests {
		if got := errorCode(err); got != want {
			t.Errorf("errorCode(%v) = %q, want %q", err, got, want)
		}
	}
}
