-- SPDX-License-Identifier: Apache-2.0
CREATE TABLE IF NOT EXISTS {db}.metric_points (
  timestamp     DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  stream        LowCardinality(String),
  service       LowCardinality(String),
  name          LowCardinality(String),
  description   String,
  unit          LowCardinality(String),
  kind          LowCardinality(String),
  temporality   LowCardinality(String),
  is_monotonic  Bool,
  value         Float64 CODEC(Gorilla, ZSTD(1)),
  count         UInt64,
  sum           Float64,
  min           Float64,
  max           Float64,
  bucket_bounds Array(Float64),
  bucket_counts Array(UInt64),
  attrs         Map(LowCardinality(String), String),
  series_id     UInt64,
  connection_id LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (stream, name, series_id, timestamp);
