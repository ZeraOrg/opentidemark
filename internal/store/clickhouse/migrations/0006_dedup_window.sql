-- SPDX-License-Identifier: Apache-2.0
-- Deduplicate retried and replayed insert batches by insert_deduplication_token (SPEC §8.5 item 7).
-- Replicated tables (cluster mode) use the built-in replicated deduplication in addition.
ALTER TABLE {db}.events MODIFY SETTING non_replicated_deduplication_window = 10000;
ALTER TABLE {db}.spans MODIFY SETTING non_replicated_deduplication_window = 10000;
ALTER TABLE {db}.metric_points MODIFY SETTING non_replicated_deduplication_window = 10000;
