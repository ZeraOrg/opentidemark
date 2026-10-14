// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/ZeraOrg/opentidemark/internal/config"
	"github.com/ZeraOrg/opentidemark/internal/meta"
	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
)

// Timeouts of the operator commands.
const (
	opsConnectTimeout       = 15 * time.Second
	opsQueryTimeout         = 30 * time.Second
	clickhouseBackupTimeout = 12 * time.Hour
)

// ErrNoMetadata is returned when the SQLite metadata database does not exist yet.
var ErrNoMetadata = errors.New("metadata database does not exist yet (start the server once)")

// Ops implements the offline operator commands of SPEC §8.5 item 8: backup, restore, migrate
// and doctor. Output for humans goes to out.
type Ops struct {
	cfg     config.Config
	version string
	out     io.Writer
	logger  *slog.Logger
}

// NewOps returns the operator commands for cfg.
func NewOps(cfg config.Config, version string, out io.Writer, logger *slog.Logger) *Ops {
	return &Ops{cfg: cfg, version: version, out: out, logger: logger}
}

func (o *Ops) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(o.out, format, args...) // console output; a broken pipe is not actionable
}

func (o *Ops) sqlitePath() string { return filepath.Join(o.cfg.DataDir, meta.DBFileName) }

// openMeta opens the metadata database; migrate=false leaves the schema untouched.
func (o *Ops) openMeta(ctx context.Context, migrate bool) (*meta.DB, error) {
	opts := []meta.Option{meta.WithBinaryVersion(o.version), meta.WithBackupDir(o.cfg.BackupDir)}
	if !migrate {
		opts = append(opts, meta.WithoutMigrations())
	}
	if o.cfg.HA() {
		return meta.OpenPostgres(ctx, o.cfg.Meta.DSN, opts...)
	}
	if _, err := os.Stat(o.sqlitePath()); !migrate && errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoMetadata
	}
	return meta.Open(ctx, o.sqlitePath(), opts...)
}

// openWriter connects the ClickHouse writer user (all configured addresses, in order).
func (o *Ops) openWriter(ctx context.Context) (driver.Conn, error) {
	return o.openWriterAt(ctx, o.cfg.ClickHouse.Addrs)
}

func (o *Ops) openWriterAt(ctx context.Context, addrs []string) (driver.Conn, error) {
	c := o.cfg.ClickHouse
	cctx, cancel := context.WithTimeout(ctx, opsConnectTimeout)
	defer cancel()
	return ch.Open(cctx, ch.Options{Addrs: addrs, Username: c.WriterUser, Password: c.WriterPassword, TLS: c.TLS})
}

func closeQuietly(c io.Closer) {
	_ = c.Close() // command is finishing; the primary result was already reported
}

// OpenMeta opens (and migrates) the configured metadata database for offline admin commands.
func (o *Ops) OpenMeta(ctx context.Context) (*meta.DB, error) { return o.openMeta(ctx, true) }
