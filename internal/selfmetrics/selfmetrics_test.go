// SPDX-License-Identifier: Apache-2.0

package selfmetrics

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/model"
)

func TestMetricsAndHandler(t *testing.T) {
	m := New()
	m.Accepted("seq", model.SourceLogs, 5)
	m.Dropped("seq", "below_minimum_level", 2)
	m.Rejected("seq", "auth")
	p := m.Pipeline()
	p.Enqueued("events", 5)
	p.Written("events", 4)
	p.Dropped("events", "stream_paused", 1)
	p.DeadLettered("events", 1)
	p.WriteFailed("events")
	p.QueueDepth("events", 3)
	tot := m.Totals()
	if tot["ingested.logs"] != 5 || tot["written.events"] != 4 || tot["deadlettered.events"] != 1 ||
		tot["ingest_dropped.below_minimum_level"] != 2 || tot["ingest_rejected.auth"] != 1 || tot["pipeline_dropped.stream_paused"] != 1 {
		t.Errorf("totals %v", tot)
	}
	w := httptest.NewRecorder()
	m.Handler("").ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(w.Body)
	for _, want := range []string{`tidemark_ingest_items_total{protocol="seq",source="logs"} 5`, `tidemark_pipeline_queue_depth{signal="events"} 3`,
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
