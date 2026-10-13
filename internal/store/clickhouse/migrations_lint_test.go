// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// Migration lint (SPEC §8.5 item 1): ClickHouse migrations are forward-only and additive.
var (
	forbiddenDDL = []*regexp.Regexp{
		regexp.MustCompile(`\bDROP\b`), regexp.MustCompile(`\bRENAME\b`), regexp.MustCompile(`\bMODIFY\s+COLUMN\b`),
		regexp.MustCompile(`\bTRUNCATE\b`), regexp.MustCompile(`\bDELETE\b`), regexp.MustCompile(`\bDETACH\b`),
		regexp.MustCompile(`\bEXCHANGE\b`), regexp.MustCompile(`\bREPLACE\s+PARTITION\b`), regexp.MustCompile(`\bCLEAR\s+COLUMN\b`),
	}
	allowedDDL = []*regexp.Regexp{
		regexp.MustCompile(`^CREATE (TABLE|MATERIALIZED VIEW|DATABASE) IF NOT EXISTS\s`),
		regexp.MustCompile(`^ALTER TABLE \S+ (ADD COLUMN IF NOT EXISTS|ADD INDEX IF NOT EXISTS|MODIFY TTL|MODIFY SETTING)\s`),
	}
)

func lintStatement(stmt string) error {
	upper := strings.ToUpper(strings.Join(strings.Fields(stmt), " "))
	for _, re := range forbiddenDDL {
		if re.MatchString(upper) {
			return fmt.Errorf("destructive DDL %q is not allowed in migrations", re.String())
		}
	}
	for _, re := range allowedDDL {
		if re.MatchString(upper) {
			return nil
		}
	}
	return fmt.Errorf("statement is not one of the additive forms: %.80s", stmt)
}

func TestMigrationsAreAdditive(t *testing.T) {
	migrations, err := LoadMigrations("tidemark")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		for i, stmt := range m.Statements {
			if err := lintStatement(stmt); err != nil {
				t.Errorf("%s statement %d: %v", m.Name, i+1, err)
			}
		}
	}
}

func TestLintRejectsDestructiveDDL(t *testing.T) {
	bad := []string{
		"DROP TABLE tidemark.events",
		"ALTER TABLE tidemark.events DROP COLUMN x",
		"ALTER TABLE tidemark.events RENAME COLUMN a TO b",
		"ALTER TABLE tidemark.events MODIFY COLUMN message LowCardinality(String)",
		"TRUNCATE TABLE tidemark.events",
		"ALTER TABLE tidemark.events DELETE WHERE 1",
		"DELETE FROM tidemark.events WHERE 1",
		"CREATE TABLE tidemark.x (a UInt8) ENGINE = MergeTree ORDER BY a",
		"ALTER TABLE tidemark.events ADD COLUMN x String",
		"RENAME TABLE tidemark.a TO tidemark.b",
	}
	for _, s := range bad {
		if err := lintStatement(s); err == nil {
			t.Errorf("lint accepted %q", s)
		}
	}
	good := []string{
		"ALTER TABLE tidemark.events ADD COLUMN IF NOT EXISTS f_x String MATERIALIZED props['x']",
		"ALTER TABLE tidemark.events ADD INDEX IF NOT EXISTS idx_f_x f_x TYPE bloom_filter(0.01) GRANULARITY 4",
		"ALTER TABLE tidemark.events MODIFY TTL toDateTime(timestamp) + toIntervalDay(30)",
		"alter table tidemark.events modify setting non_replicated_deduplication_window = 10000",
	}
	for _, s := range good {
		if err := lintStatement(s); err != nil {
			t.Errorf("lint rejected %q: %v", s, err)
		}
	}
}
