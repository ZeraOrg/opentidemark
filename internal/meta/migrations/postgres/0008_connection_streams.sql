-- SPDX-License-Identifier: Apache-2.0
-- Streams a connection may write to (SPEC §9.1, §11.5; '[]' = any stream).
-- Additive only (SPEC §8.5): binaries without this migration keep working and ignore the list.
-- min_compatible_binary: 0.1.0

ALTER TABLE connections ADD COLUMN allowed_streams TEXT NOT NULL DEFAULT '[]';
