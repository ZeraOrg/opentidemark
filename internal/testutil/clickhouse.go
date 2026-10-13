// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"os"
	"strings"
)

// ClickHouseEnv describes the server used by integration tests (build tag "integration").
type ClickHouseEnv struct {
	Addrs          []string
	Database       string
	WriterUser     string
	WriterPassword string
	ReaderUser     string
	ReaderPassword string
}

// ClickHouseFromEnv reads TIDEMARK_TEST_CLICKHOUSE_* variables; ok is false when
// TIDEMARK_TEST_CLICKHOUSE_ADDR is unset.
func ClickHouseFromEnv() (ClickHouseEnv, bool) {
	addr := os.Getenv("TIDEMARK_TEST_CLICKHOUSE_ADDR")
	if addr == "" {
		return ClickHouseEnv{}, false
	}
	get := func(key, def string) string {
		if v := os.Getenv("TIDEMARK_TEST_CLICKHOUSE_" + key); v != "" {
			return v
		}
		return def
	}
	return ClickHouseEnv{
		Addrs:          strings.Split(addr, ","),
		Database:       get("DATABASE", "tidemark"),
		WriterUser:     get("WRITER_USER", "tidemark_writer"),
		WriterPassword: get("WRITER_PASSWORD", ""),
		ReaderUser:     get("READER_USER", "tidemark_reader"),
		ReaderPassword: get("READER_PASSWORD", ""),
	}, true
}
