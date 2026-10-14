// SPDX-License-Identifier: Apache-2.0

package app

import (
	"net/http"
	"net/netip"
	"time"

	"github.com/ZeraOrg/tidemark/internal/api"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
)

// tailLagSlack is added to the batch wait to get the live-tail lag: rows carry the ingest time
// but become visible only after their batch is inserted (SPEC §11.3).
const tailLagSlack = 2 * time.Second

// searchAPIs builds the query layer, the search and signals services and their API modules
// (SPEC §11.3, §11.4). The returned Streams must be closed when the servers shut down.
func (a *App) searchAPIs(audit *service.Auditor, access service.ScopeResolver, ip func(*http.Request) netip.Addr) (
	[]api.Registrar, *api.Streams, error) {
	reader, err := ch.NewReader(a.chReader, ch.ReaderConfig{Database: a.cfg.ClickHouse.Database,
		QueryTimeout: a.cfg.QueryTimeout, MaxMemoryBytes: ch.DefaultReaderMemory})
	if err != nil {
		return nil, nil, err
	}
	a.querier = reader
	signals := meta.NewSignals(a.meta)
	search := service.NewSearch(service.SearchDeps{Store: reader, Access: access, Signals: signals, Indexed: meta.NewIndexedFields(a.meta),
		Audit: audit, Database: a.cfg.ClickHouse.Database, MaxRows: a.cfg.QueryMaxRows,
		TailLag: a.cfg.BatchMaxWait + tailLagSlack, Now: time.Now})
	streams := api.NewStreams()
	return []api.Registrar{
		&api.SearchAPI{Search: search, Now: time.Now, ClientIP: ip, Logger: a.logger, Streams: streams},
		&api.SignalsAPI{Signals: service.NewSignals(signals)},
	}, streams, nil
}

// onShutdown registers f to run when the HTTP servers begin shutting down.
func (a *App) onShutdown(f func()) {
	for _, l := range a.listeners {
		l.server.RegisterOnShutdown(f)
	}
}
