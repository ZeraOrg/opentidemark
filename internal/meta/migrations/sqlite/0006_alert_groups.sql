-- SPDX-License-Identifier: Apache-2.0
-- Per-group alert evaluation state (SPEC §14.3) and the group of each history row.
-- Additive only (SPEC §8.5): binaries without this migration keep working.
-- min_compatible_binary: 0.1.0

ALTER TABLE alert_rules ADD COLUMN eval_state TEXT NOT NULL DEFAULT '{}';
ALTER TABLE alert_history ADD COLUMN group_key TEXT NOT NULL DEFAULT '';
