-- SPDX-License-Identifier: Apache-2.0
-- Last TTL applied per table, so retention is re-applied only when it changes (SPEC §12.3).
CREATE TABLE IF NOT EXISTS {db}.retention_state (
  tbl        String,
  ttl        String,
  applied_at DateTime64(3, 'UTC')
) ENGINE = ReplacingMergeTree(applied_at) ORDER BY tbl;
