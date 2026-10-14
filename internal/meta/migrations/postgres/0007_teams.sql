-- SPDX-License-Identifier: Apache-2.0
-- Teams, their members and stream grants (SPEC §8.4, §15.10), and the stream scope of alert
-- rules (§14.1; NULL = all streams).
-- Additive only (SPEC §8.5): binaries without this migration keep working and ignore teams.
-- min_compatible_binary: 0.1.0

CREATE TABLE teams (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_teams_name ON teams (lower(name));

CREATE TABLE team_members (
  team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (team_id, user_id)
);
CREATE INDEX idx_team_members_user ON team_members(user_id);

CREATE TABLE team_streams (
  team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  stream     TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (team_id, stream)
);
CREATE INDEX idx_team_streams_stream ON team_streams(stream);

ALTER TABLE alert_rules ADD COLUMN streams TEXT;
