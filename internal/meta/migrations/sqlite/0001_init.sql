-- SPDX-License-Identifier: Apache-2.0
-- Tidemark metadata schema (SPEC §8.4). Times are RFC 3339 text in UTC; booleans are 0/1.
-- min_compatible_binary: 0.1.0

CREATE TABLE users (
  id                   TEXT PRIMARY KEY,
  username             TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name         TEXT NOT NULL DEFAULT '',
  password_hash        TEXT NOT NULL,
  role                 TEXT NOT NULL CHECK (role IN ('admin','editor','viewer')),
  must_change_password INTEGER NOT NULL DEFAULT 0,
  failed_logins        INTEGER NOT NULL DEFAULT 0,
  locked_until         TEXT,
  last_login_at        TEXT,
  disabled             INTEGER NOT NULL DEFAULT 0,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL
);

CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE connections (
  id                  TEXT PRIMARY KEY,
  name                TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description         TEXT NOT NULL DEFAULT '',
  key_prefix          TEXT NOT NULL,
  key_hash            TEXT NOT NULL UNIQUE,
  prev_key_hash       TEXT,
  prev_key_expires_at TEXT,
  protocols           TEXT NOT NULL DEFAULT '[]',
  default_stream      TEXT NOT NULL DEFAULT 'default',
  allowed_cidrs       TEXT NOT NULL DEFAULT '[]',
  minimum_level       TEXT NOT NULL DEFAULT '',
  applied_properties  TEXT NOT NULL DEFAULT '{}',
  rate_limit_eps      INTEGER NOT NULL DEFAULT 0,
  expires_at          TEXT,
  revoked_at          TEXT,
  last_used_at        TEXT,
  last_used_ip        TEXT NOT NULL DEFAULT '',
  events_received     INTEGER NOT NULL DEFAULT 0,
  created_at          TEXT NOT NULL,
  updated_at          TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_connections_prev_key ON connections(prev_key_hash) WHERE prev_key_hash IS NOT NULL;

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key_prefix   TEXT NOT NULL,
  key_hash     TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL CHECK (scopes IN ('read','write','admin')),
  expires_at   TEXT,
  revoked_at   TEXT,
  last_used_at TEXT,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);

CREATE TABLE signals (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  tql        TEXT NOT NULL,
  color      TEXT NOT NULL DEFAULT '',
  owner_id   TEXT NOT NULL,
  shared     INTEGER NOT NULL DEFAULT 0,
  source     TEXT NOT NULL DEFAULT 'logs',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE dashboards (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  owner_id    TEXT NOT NULL,
  shared      INTEGER NOT NULL DEFAULT 0,
  doc         TEXT NOT NULL DEFAULT '{}',
  version     INTEGER NOT NULL DEFAULT 1,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE TABLE alert_rules (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 1,
  doc          TEXT NOT NULL DEFAULT '{}',
  state        TEXT NOT NULL DEFAULT 'ok',
  state_since  TEXT,
  last_eval_at TEXT,
  last_value   REAL,
  last_error   TEXT NOT NULL DEFAULT '',
  version      INTEGER NOT NULL DEFAULT 1,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE TABLE alert_history (
  id         TEXT PRIMARY KEY,
  rule_id    TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
  at         TEXT NOT NULL,
  from_state TEXT NOT NULL,
  to_state   TEXT NOT NULL,
  value      REAL,
  message    TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_alert_history_rule ON alert_history(rule_id, at);

CREATE TABLE channels (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('webhook','slack','teams','email')),
  config     TEXT NOT NULL DEFAULT '{}',
  version    INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE streams (
  id                     TEXT PRIMARY KEY,
  name                   TEXT NOT NULL UNIQUE,
  description            TEXT NOT NULL DEFAULT '',
  retention_days         INTEGER,
  level_rules            TEXT NOT NULL DEFAULT '[]',
  spans_retention_days   INTEGER,
  metrics_retention_days INTEGER,
  paused                 INTEGER NOT NULL DEFAULT 0,
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL
);

CREATE TABLE indexed_fields (
  id          TEXT PRIMARY KEY,
  source      TEXT NOT NULL,
  key         TEXT NOT NULL,
  column_name TEXT NOT NULL UNIQUE,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL,
  UNIQUE (source, key)
);

CREATE TABLE purge_jobs (
  id            TEXT PRIMARY KEY,
  stream        TEXT NOT NULL DEFAULT '',
  source        TEXT NOT NULL,
  tql           TEXT NOT NULL DEFAULT '',
  "from"        TEXT NOT NULL,
  "to"          TEXT NOT NULL,
  status        TEXT NOT NULL,
  rows_estimate INTEGER NOT NULL DEFAULT 0,
  requested_by  TEXT NOT NULL,
  mutation_id   TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE audit_log (
  id         TEXT PRIMARY KEY,
  at         TEXT NOT NULL,
  actor      TEXT NOT NULL,
  ip         TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  target     TEXT NOT NULL DEFAULT '',
  details    TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX idx_audit_at ON audit_log(at);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
