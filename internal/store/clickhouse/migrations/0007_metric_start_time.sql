-- SPDX-License-Identifier: Apache-2.0
-- Start of the aggregation period of cumulative points (OTLP StartTimeUnixNano, SPEC §9.4);
-- histogram aggregates use it to detect process restarts. Rows stored earlier read 0 (unknown).
ALTER TABLE {db}.metric_points ADD COLUMN IF NOT EXISTS start_time DateTime64(9, 'UTC') DEFAULT 0 CODEC(Delta, ZSTD(1)) AFTER timestamp;
