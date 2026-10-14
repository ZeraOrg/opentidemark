// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ZeraOrg/tidemark/internal/service"
	"github.com/ZeraOrg/tidemark/internal/store"
)

// tail streams new events as Server-Sent Events (SPEC §11.3): `event: events` with a JSON
// array, `event: dropped` with the number of events above the per-subscriber rate, and a
// comment heartbeat. The server write deadline is lifted for the stream; it ends when the
// client disconnects, the query fails or the server shuts down (Streams).
func (a *SearchAPI) tail(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	t, err := a.Search.OpenTail(r.Context(), principal(r), q.Get("q"), listParam(q, "signals"), selectionParam(q))
	if err != nil {
		return err
	}
	defer t.Close()
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return fmt.Errorf("lift write deadline: %w", err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: rc}
	s.comment("connected")
	a.streamTail(r, t, s)
	return nil
}

func (a *SearchAPI) streamTail(r *http.Request, t *service.Tail, s *sse) {
	poll := time.NewTicker(a.tailInterval())
	defer poll.Stop()
	beat := time.NewTicker(service.TailHeartbeat)
	defer beat.Stop()
	for s.err == nil {
		select {
		case <-r.Context().Done():
			return
		case <-a.Streams.Done():
			return
		case <-beat.C:
			s.comment("heartbeat")
		case <-poll.C:
			evs, dropped, err := t.Poll(r.Context())
			if err != nil {
				if r.Context().Err() == nil {
					a.Logger.Warn("live tail poll failed", "err", err)
					s.event("error", map[string]string{"message": "query failed"})
				}
				return
			}
			if len(evs) > 0 {
				s.event("events", tailViews(evs))
			}
			if dropped > 0 {
				s.event("dropped", dropped)
			}
		}
	}
}

func (a *SearchAPI) tailInterval() time.Duration {
	if a.TailInterval > 0 {
		return a.TailInterval
	}
	return service.TailInterval
}

func tailViews(evs []store.TailEvent) []eventView {
	out := make([]eventView, 0, len(evs))
	for _, te := range evs {
		ev := te.Event
		ev.IngestedAt = te.IngestedAt
		out = append(out, toEventView(ev))
	}
	return out
}

// sse writes Server-Sent Events and flushes after each; the first write error is kept and
// ends the stream.
type sse struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	err error
}

func (s *sse) comment(text string) {
	s.write([]byte(": " + text + "\n\n"))
}

func (s *sse) event(name string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		s.err = err
		return
	}
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(name)
	b.WriteString("\ndata: ")
	b.Write(raw)
	b.WriteString("\n\n")
	s.write(b.Bytes())
}

func (s *sse) write(p []byte) {
	if s.err != nil {
		return
	}
	if _, err := s.w.Write(p); err != nil {
		s.err = err
		return
	}
	if err := s.rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.err = err
	}
}

// Streams signals long-running responses (live tail) to end when the server shuts down, so
// graceful shutdown is not held open by streaming connections.
type Streams struct {
	done chan struct{}
	once sync.Once
}

// NewStreams returns an open Streams.
func NewStreams() *Streams { return &Streams{done: make(chan struct{})} }

// Done is closed when streams must end; a nil Streams never ends.
func (s *Streams) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

// Close ends every stream; it is safe to call more than once.
func (s *Streams) Close() { s.once.Do(func() { close(s.done) }) }
