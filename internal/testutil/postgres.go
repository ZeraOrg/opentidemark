// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

const schemaIDBytes = 6

// PostgresDSN returns TIDEMARK_TEST_POSTGRES_DSN, skipping the test when it is unset.
func PostgresDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("TIDEMARK_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TIDEMARK_TEST_POSTGRES_DSN not set")
	}
	return dsn
}

// PostgresSchemaDSN creates a fresh schema (dropped at cleanup) and returns dsn with its
// search_path set to it, so every PostgreSQL test is isolated (SPEC §8.4).
func PostgresSchemaDSN(t testing.TB, dsn string) string {
	t.Helper()
	b := make([]byte, schemaIDBytes)
	_, _ = rand.Read(b) // crypto/rand never fails on supported platforms
	schema := "tm_test_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) // best-effort cleanup
		_ = admin.Close()
	})
	if !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}
