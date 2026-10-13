// SPDX-License-Identifier: Apache-2.0

// Package clickhouse implements the storage ports on ClickHouse (SPEC §8.2, §8.3): schema
// migrations, batch writers, retention DDL and connection setup for the writer and the
// read-only reader users.
package clickhouse

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"regexp"
	"time"

	ch "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// Connection defaults.
const (
	dialTimeout     = 10 * time.Second
	readTimeout     = 5 * time.Minute
	pingTimeout     = 10 * time.Second
	maxOpenConns    = 16
	maxIdleConns    = 4
	connMaxLifetime = time.Hour
	bytesPerGiB     = 1 << 30
)

// dbNamePattern validates the database identifier substituted into DDL and queries.
var dbNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// ErrInvalidDatabase is returned for database names that are not safe identifiers.
var ErrInvalidDatabase = errors.New("invalid ClickHouse database name")

// ValidDatabase reports whether name may be used as the database identifier.
func ValidDatabase(name string) bool { return dbNamePattern.MatchString(name) }

// Options configure one connection pool.
type Options struct {
	Addrs    []string
	Database string // default database of the session ("" = server default)
	Username string
	Password string
	TLS      bool
	// QueryTimeout and MaxMemoryBytes become session settings (reader only; 0 = unset).
	QueryTimeout   time.Duration
	MaxMemoryBytes int64
	MaxResultRows  int
}

// Open connects and pings the server.
func Open(ctx context.Context, o Options) (driver.Conn, error) {
	settings := ch.Settings{}
	if o.QueryTimeout > 0 {
		settings["max_execution_time"] = int(o.QueryTimeout.Seconds())
	}
	if o.MaxMemoryBytes > 0 {
		settings["max_memory_usage"] = o.MaxMemoryBytes
	}
	if o.MaxResultRows > 0 {
		settings["max_result_rows"] = o.MaxResultRows
		settings["result_overflow_mode"] = "break"
	}
	opts := &ch.Options{
		Addr:             o.Addrs,
		Auth:             ch.Auth{Database: o.Database, Username: o.Username, Password: o.Password},
		DialTimeout:      dialTimeout,
		ReadTimeout:      readTimeout,
		MaxOpenConns:     maxOpenConns,
		MaxIdleConns:     maxIdleConns,
		ConnMaxLifetime:  connMaxLifetime,
		Compression:      &ch.Compression{Method: ch.CompressionLZ4},
		Settings:         settings,
		ConnOpenStrategy: ch.ConnOpenInOrder,
	}
	if o.TLS {
		opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	conn, err := ch.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := conn.Ping(pctx); err != nil {
		_ = conn.Close() // report the ping failure
		return nil, fmt.Errorf("ping clickhouse %v as %s: %w", o.Addrs, o.Username, err)
	}
	return conn, nil
}

// DefaultReaderMemory is the max_memory_usage of user queries (SPEC §10.6).
const DefaultReaderMemory = bytesPerGiB

// Pinger checks reachability of a connection (readiness).
type Pinger struct{ Conn driver.Conn }

// Ping pings the server with a timeout.
func (p Pinger) Ping(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return p.Conn.Ping(pctx)
}

// ServerVersion returns the server version as "major.minor.patch".
func ServerVersion(_ context.Context, conn driver.Conn) (string, error) {
	v, err := conn.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("server version: %w", err)
	}
	return fmt.Sprintf("%d.%d.%d", v.Version.Major, v.Version.Minor, v.Version.Patch), nil
}
