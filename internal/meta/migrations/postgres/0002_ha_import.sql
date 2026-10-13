-- SPDX-License-Identifier: Apache-2.0
-- Leases (SPEC §17.5), import jobs (SPEC §9.9) and disabled signals (imported untranslatable filters).
-- Additive only (SPEC §8.5): binaries without this migration keep working.
-- min_compatible_binary: 0.1.0

CREATE TABLE leases (
  name       TEXT PRIMARY KEY,
  holder     TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE import_jobs (
  id               TEXT PRIMARY KEY,
  kind             TEXT NOT NULL CHECK (kind IN ('elastic','seq','clef-file','kibana')),
  name             TEXT NOT NULL DEFAULT '',
  config           TEXT NOT NULL DEFAULT '{}',
  status           TEXT NOT NULL,
  checkpoint       TEXT NOT NULL DEFAULT '{}',
  rows_imported    BIGINT NOT NULL DEFAULT 0,
  rows_total       BIGINT NOT NULL DEFAULT 0,
  errors           BIGINT NOT NULL DEFAULT 0,
  last_error       TEXT NOT NULL DEFAULT '',
  warnings         TEXT NOT NULL DEFAULT '[]',
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  requested_by     TEXT NOT NULL,
  started_at       TEXT,
  finished_at      TEXT,
  created_at       TEXT NOT NULL,
  updated_at       TEXT NOT NULL
);
CREATE INDEX idx_import_jobs_status ON import_jobs(status);

ALTER TABLE signals ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0;
