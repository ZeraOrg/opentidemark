-- SPDX-License-Identifier: Apache-2.0
CREATE TABLE IF NOT EXISTS {db}.spans (
  start_time       DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
  ingested_at      DateTime64(3, 'UTC') DEFAULT now64(3),
  trace_id         String,
  span_id          String,
  parent_span_id   String,
  stream           LowCardinality(String),
  service          LowCardinality(String),
  name             LowCardinality(String),
  kind             LowCardinality(String),
  duration_ns      UInt64 CODEC(T64, ZSTD(1)),
  status_code      LowCardinality(String),
  status_message   String,
  connection_id    LowCardinality(String),
  attrs            Map(LowCardinality(String), String) CODEC(ZSTD(3)),
  attrs_num        Map(LowCardinality(String), Float64) CODEC(ZSTD(3)),
  resource_json    String CODEC(ZSTD(3)),
  events_json      String CODEC(ZSTD(3)),
  links_json       String CODEC(ZSTD(3)),
  INDEX idx_trace trace_id TYPE bloom_filter(0.001) GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(start_time)
ORDER BY (stream, service, name, start_time);
