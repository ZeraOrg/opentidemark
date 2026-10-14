// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	ch "github.com/ZeraOrg/opentidemark/internal/store/clickhouse"
)

const (
	connectRetryBase = time.Second
	connectRetryMax  = 10 * time.Second
)

// openClickHouse connects the writer (retrying while the server starts), migrates, and
// connects the read-only reader.
func (a *App) openClickHouse(ctx context.Context) error {
	c := a.cfg.ClickHouse
	w, err := a.connectWithRetry(ctx, ch.Options{Addrs: c.Addrs, Username: c.WriterUser, Password: c.WriterPassword, TLS: c.TLS})
	if err != nil {
		return err
	}
	a.chWriter = w
	if err := a.migrateClickHouse(ctx, w); err != nil {
		return err
	}
	r, err := ch.Open(ctx, ch.Options{Addrs: c.Addrs, Database: c.Database, Username: c.ReaderUser, Password: c.ReaderPassword,
		TLS: c.TLS, QueryTimeout: a.cfg.QueryTimeout, MaxMemoryBytes: ch.DefaultReaderMemory, MaxResultRows: a.cfg.QueryMaxRows})
	if err != nil {
		return fmt.Errorf("clickhouse reader: %w", err)
	}
	a.chReader = r
	return nil
}

// connectWithRetry retries the writer connection for up to clickhouseStartupWait.
func (a *App) connectWithRetry(ctx context.Context, o ch.Options) (driver.Conn, error) {
	deadline := time.Now().Add(clickhouseStartupWait)
	delay := connectRetryBase
	for {
		c, err := ch.Open(ctx, o)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil, fmt.Errorf("clickhouse writer: %w", err)
		}
		a.logger.Warn("waiting for clickhouse", "err", err, "retry_in", delay.String())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, connectRetryMax)
	}
}
