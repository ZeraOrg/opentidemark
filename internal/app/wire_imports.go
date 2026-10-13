// SPDX-License-Identifier: Apache-2.0

package app

import (
	"log/slog"
	"path/filepath"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/tidemark/internal/auth"
	"github.com/ZeraOrg/tidemark/internal/importer"
	"github.com/ZeraOrg/tidemark/internal/meta"
	"github.com/ZeraOrg/tidemark/internal/service"
	ch "github.com/ZeraOrg/tidemark/internal/store/clickhouse"
)

// importsDir is the upload folder of clef-file imports inside the data directory.
const importsDir = "imports"

// importDeps are what the importer needs from the composition root (server and CLI).
type importDeps struct {
	meta     *meta.DB
	conn     driver.Conn
	database string
	writer   []ch.WriterOption
	secret   []byte
	streams  importer.StreamResolver
	audit    *service.Auditor
	elector  importer.Elector
	nodeID   string
	dataDir  string
	logger   *slog.Logger
}

// buildImporter wires the import job service and runner (SPEC §9.9).
func buildImporter(d importDeps) (*importer.Service, *importer.Runner, error) {
	box, err := auth.NewSecretBox(d.secret)
	if err != nil {
		return nil, nil, err
	}
	writer, err := ch.NewWriter(d.conn, d.database, d.writer...)
	if err != nil {
		return nil, nil, err
	}
	jobs := meta.NewImportJobs(d.meta)
	signals := importer.Signals{Store: meta.NewSignals(d.meta)}
	runner := importer.NewRunner(importer.RunnerDeps{Jobs: jobs, Writer: writer, Streams: d.streams, Signals: signals,
		Box: box, Elector: d.elector, NodeID: d.nodeID, IsPermanent: ch.IsPermanent, Audit: d.audit, Logger: d.logger,
		Now: time.Now})
	svc := importer.NewService(importer.ServiceDeps{Jobs: jobs, Box: box, Signals: signals, Streams: d.streams,
		Audit: d.audit, Runner: runner, UploadDir: filepath.Join(d.dataDir, importsDir), NodeID: d.nodeID, Now: time.Now})
	return svc, runner, nil
}
