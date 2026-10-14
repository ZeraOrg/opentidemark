// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/auth"
	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/store"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// Live tail limits (SPEC §11.3).
const (
	MaxTailsPerUser = 20
	TailMaxPerPoll  = 50 // events per poll (one poll per second ⇒ 50 events/s)
	TailInterval    = time.Second
	TailHeartbeat   = 15 * time.Second
	// tailLookback / tailFuture bound the event timestamps a tail scans (partition pruning):
	// events stamped more than an hour in the past are not tailed; future timestamps are
	// clamped at ingest to at most 7 days ahead (§9.1).
	tailLookback = time.Hour
	tailFuture   = 7*24*time.Hour + time.Hour
)

// ErrTooManyTails is returned when a user already has MaxTailsPerUser live tails.
var ErrTooManyTails = errors.New("too many concurrent live tails")

// tailLimiter counts open tails per user.
type tailLimiter struct {
	mu   sync.Mutex
	max  int
	open map[string]int
}

func newTailLimiter(maxPerUser int) *tailLimiter {
	return &tailLimiter{max: maxPerUser, open: map[string]int{}}
}

func (l *tailLimiter) acquire(user string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[user] >= l.max {
		return false
	}
	l.open[user]++
	return true
}

func (l *tailLimiter) release(user string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[user] <= 1 {
		delete(l.open, user)
		return
	}
	l.open[user]--
}

// Tail is one live-tail subscription. Poll returns the events ingested since the previous
// poll (at most TailMaxPerPoll, the excess is reported as dropped). The stream scope is
// resolved again on every poll, so access changes apply to open tails (SPEC §15.10).
type Tail struct {
	s     *Search
	pr    *prepared
	p     auth.Principal
	sel   Selection
	last  time.Time
	close sync.Once
}

// OpenTail validates the query (logs, no stats) and reserves a tail slot for p's user.
func (s *Search) OpenTail(ctx context.Context, p auth.Principal, q string, signals []string, sel Selection) (*Tail, error) {
	pr, err := s.prepare(ctx, p, q, "", signals, sel)
	if err != nil {
		return nil, err
	}
	if pr.q.Source != model.SourceLogs {
		return nil, invalid("q", "live tail supports logs only")
	}
	now := s.d.Now()
	c, err := tql.Compile(pr.q, s.options(pr, Range{From: now.Add(-tailLookback), To: now}))
	if err != nil {
		return nil, err
	}
	if c.Kind != tql.KindEvents {
		return nil, invalid("q", "live tail does not support stats")
	}
	if !s.tails.acquire(p.UserID) {
		return nil, ErrTooManyTails
	}
	return &Tail{s: s, pr: pr, p: p, sel: sel, last: now.Add(-s.d.TailLag)}, nil
}

// Poll returns the events ingested since the last poll and the number dropped by the rate cap.
func (t *Tail) Poll(ctx context.Context) ([]store.TailEvent, uint64, error) {
	now := t.s.d.Now()
	until := now.Add(-t.s.d.TailLag)
	if !until.After(t.last) {
		return nil, 0, nil
	}
	scope, err := t.s.scope(ctx, t.p, t.sel)
	if err != nil {
		return nil, 0, err
	}
	pr := *t.pr
	pr.streams = scope
	f, err := t.s.filter(&pr, Range{From: now.Add(-tailLookback), To: now.Add(tailFuture)})
	if err != nil {
		return nil, 0, err
	}
	evs, total, err := t.s.d.Store.Tail(ctx, f, t.last, until, TailMaxPerPoll)
	if err != nil {
		return nil, 0, err
	}
	t.last = until
	var dropped uint64
	if total > uint64(len(evs)) {
		dropped = total - uint64(len(evs))
	}
	return evs, dropped, nil
}

// Close releases the tail slot; it is safe to call more than once.
func (t *Tail) Close() {
	t.close.Do(func() { t.s.tails.release(t.p.UserID) })
}
