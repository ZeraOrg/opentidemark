// SPDX-License-Identifier: Apache-2.0

package selfmetrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// TestWALBytes: the size of each signal's write-ahead log is read at scrape time.
func TestWALBytes(t *testing.T) {
	m := New()
	size := int64(1024)
	m.WALBytes("events", func() int64 { return size })
	m.WALBytes("spans", func() int64 { return 0 })
	size = 4096
	w := httptest.NewRecorder()
	m.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{`tidemark_wal_bytes{signal="events"} 4096`, `tidemark_wal_bytes{signal="spans"} 0`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
}

// TestUpdateAvailable: the gauge follows the checker at scrape time.
func TestUpdateAvailable(t *testing.T) {
	m := New()
	available := false
	m.UpdateAvailable(func() bool { return available })
	for _, tc := range []struct {
		set  bool
		want string
	}{{false, "tidemark_update_available 0"}, {true, "tidemark_update_available 1"}} {
		available = tc.set
		w := httptest.NewRecorder()
		m.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("missing %q", tc.want)
		}
	}
}

func TestMetricsAndHandler(t *testing.T) {
	m := New()
	m.Accepted("seq", model.SourceLogs, 5)
	m.Dropped("seq", "below_minimum_level", 2)
	m.Rejected("seq", "auth")
	m.Rerouted("otlp", "stream_not_allowed", 3)
	p := m.Pipeline()
	p.Enqueued("events", 5)
	p.Written("events", 4)
	p.Dropped("events", "stream_paused", 1)
	p.DeadLettered("events", 1)
	p.WriteFailed("events")
	p.QueueDepth("events", 3)
	tot := m.Totals()
	if tot["ingested.logs"] != 5 || tot["written.events"] != 4 || tot["deadlettered.events"] != 1 ||
		tot["ingest_dropped.below_minimum_level"] != 2 || tot["ingest_rejected.auth"] != 1 || tot["pipeline_dropped.stream_paused"] != 1 ||
		tot["ingest_rerouted.stream_not_allowed"] != 3 {
		t.Errorf("totals %v", tot)
	}
	w := httptest.NewRecorder()
	m.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(w.Body)
	for _, want := range []string{`tidemark_ingest_items_total{protocol="seq",source="logs"} 5`, `tidemark_pipeline_queue_depth{signal="events"} 3`,
		`tidemark_ingest_rerouted_total{protocol="otlp",reason="stream_not_allowed"} 3`,
		"go_goroutines"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q", want)
		}
	}
	h := m.Handler("s3cret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 401 {
		t.Errorf("no token: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.Header.Set("Authorization", "Bearer s3cret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("token: %d", w.Code)
	}
}
