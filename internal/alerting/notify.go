// SPDX-License-Identifier: Apache-2.0

package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

// Delivery settings (SPEC §14.2).
const (
	SendTimeout       = 10 * time.Second
	MaxRetries        = 3
	retryBase         = time.Second
	queueSize         = 1000
	dispatchWorkers   = 2
	dedupeWindow      = time.Minute
	dedupeSweepAfter  = 1000
	UserAgent         = "Tidemark-Alerting/1"
	maxResponseDrain  = 64 * 1024
	testRuleName      = "Tidemark test notification"
	testRuleID        = "test"
	stateTestDelivery = "test"
)

// ErrUnknownKind is returned for a channel without a registered notifier.
var ErrUnknownKind = errors.New("no notifier for channel kind")

// Notification is one alert message (SPEC §14.2).
type Notification struct {
	RuleID        string
	RuleName      string
	Severity      string
	Description   string
	State         string
	PreviousState string
	Value         *float64
	Op            string
	Threshold     float64
	Group         map[string]string
	At            time.Time
	URL           string
	Message       string
}

// Notifier delivers notifications of one channel kind; ch.Config holds the decrypted config.
type Notifier interface {
	Kind() string
	Notify(ctx context.Context, ch meta.Channel, n Notification) error
}

// PermanentError marks failures that retrying cannot fix (e.g. HTTP 4xx).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }

// Unwrap returns the cause.
func (e *PermanentError) Unwrap() error { return e.Err }

// ChannelResolver returns a channel with its secrets decrypted.
type ChannelResolver interface {
	Resolve(ctx context.Context, id string) (meta.Channel, error)
}

// job is one queued delivery.
type job struct {
	channelID string
	n         Notification
}

// Dispatcher delivers notifications asynchronously with a 10 s timeout per attempt and 3
// retries with exponential backoff (1 s, 2 s, 4 s), and drops duplicates of the same rule,
// group and state within one minute.
type Dispatcher struct {
	channels  ChannelResolver
	notifiers map[string]Notifier
	logger    *slog.Logger
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error
	queue     chan job

	mu     sync.Mutex
	recent map[string]time.Time
}

// NewDispatcher returns a dispatcher for the given notifiers.
func NewDispatcher(channels ChannelResolver, logger *slog.Logger, now func() time.Time, notifiers ...Notifier) *Dispatcher {
	d := &Dispatcher{channels: channels, notifiers: map[string]Notifier{}, logger: logger, now: now, sleep: sleepCtx,
		queue: make(chan job, queueSize), recent: map[string]time.Time{}}
	for _, n := range notifiers {
		d.notifiers[n.Kind()] = n
	}
	return d
}

// WithSleep replaces the backoff sleep (tests).
func (d *Dispatcher) WithSleep(f func(ctx context.Context, d time.Duration) error) *Dispatcher {
	d.sleep = f
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Enqueue queues n for every channel; duplicates and a full queue are dropped (logged).
func (d *Dispatcher) Enqueue(n Notification, channelIDs []string) {
	if d.duplicate(n) {
		d.logger.Debug("duplicate notification dropped", "rule", n.RuleID, "state", n.State)
		return
	}
	for _, id := range channelIDs {
		select {
		case d.queue <- job{channelID: id, n: n}:
		default:
			d.logger.Error("notification queue full; dropped", "rule", n.RuleID, "channel", id)
		}
	}
}

func (d *Dispatcher) duplicate(n Notification) bool {
	key := n.RuleID + "\x00" + GroupKey(n.Group) + "\x00" + n.State
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if at, ok := d.recent[key]; ok && now.Sub(at) < dedupeWindow {
		return true
	}
	if len(d.recent) >= dedupeSweepAfter {
		for k, at := range d.recent {
			if now.Sub(at) >= dedupeWindow {
				delete(d.recent, k)
			}
		}
	}
	d.recent[key] = now
	return false
}

// Run delivers queued notifications until ctx ends.
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range dispatchWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-d.queue:
					if err := d.Deliver(ctx, j.channelID, j.n); err != nil {
						d.logger.Error("notification failed", "rule", j.n.RuleID, "channel", j.channelID, "err", err)
					}
				}
			}
		}()
	}
	wg.Wait()
}

// Deliver sends n to one channel with retries.
func (d *Dispatcher) Deliver(ctx context.Context, channelID string, n Notification) error {
	ch, err := d.channels.Resolve(ctx, channelID)
	if err != nil {
		return fmt.Errorf("channel %s: %w", channelID, err)
	}
	notifier, ok := d.notifiers[ch.Kind]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKind, ch.Kind)
	}
	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			if err := d.sleep(ctx, retryBase<<(attempt-1)); err != nil {
				return errors.Join(lastErr, err)
			}
		}
		actx, cancel := context.WithTimeout(ctx, SendTimeout)
		lastErr = notifier.Notify(actx, ch, n)
		cancel()
		var perm *PermanentError
		if lastErr == nil || errors.As(lastErr, &perm) {
			return lastErr
		}
	}
	return fmt.Errorf("after %d retries: %w", MaxRetries, lastErr)
}

// Test sends a synthetic notification to a channel once (no retries), for POST
// /channels/{id}/test.
func (d *Dispatcher) Test(ctx context.Context, ch meta.Channel, publicURL string) error {
	notifier, ok := d.notifiers[ch.Kind]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownKind, ch.Kind)
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	n := Notification{RuleID: testRuleID, RuleName: testRuleName, Severity: SeverityInfo, State: stateTestDelivery,
		PreviousState: string(StateOK), At: d.now().UTC(), URL: publicURL,
		Message: "This is a test notification from Tidemark."}
	return notifier.Notify(ctx, ch, n)
}
