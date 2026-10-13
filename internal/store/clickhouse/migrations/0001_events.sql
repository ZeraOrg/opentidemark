-- SPDX-License-Identifier: Apache-2.0
CREATE TABLE IF NOT EXISTS {db}.events (
  timestamp        DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
  ingested_at      DateTime64(3, 'UTC') DEFAULT now64(3) CODEC(Delta, ZSTD(1)),
  id               UUID,
  stream           LowCardinality(String),
  level            LowCardinality(String),
  level_num        UInt8,
  service          LowCardinality(String),
  environment      LowCardinality(String),
  host             LowCardinality(String),
  message_template String CODEC(ZSTD(3)),
  message          String CODEC(ZSTD(3)),
  exception        String CODEC(ZSTD(3)),
  event_type       LowCardinality(String),
  trace_id         String CODEC(ZSTD(1)),
  span_id          String CODEC(ZSTD(1)),
  protocol         LowCardinality(String),
  connection_id    LowCardinality(String),
  props            Map(LowCardinality(String), String) CODEC(ZSTD(3)),
  props_num        Map(LowCardinality(String), Float64) CODEC(ZSTD(3)),
  props_json       String CODEC(ZSTD(3)),
  INDEX idx_id id TYPE bloom_filter(0.001) GRANULARITY 4,
  INDEX idx_trace trace_id TYPE bloom_filter(0.01) GRANULARITY 4,
  INDEX idx_msg lower(message) TYPE ngrambf_v1(3, 65536, 3, 0) GRANULARITY 4,
  INDEX idx_exc lower(exception) TYPE ngrambf_v1(3, 65536, 3, 0) GRANULARITY 4,
  INDEX idx_prop_keys mapKeys(props) TYPE bloom_filter(0.01) GRANULARITY 4,
  INDEX idx_prop_vals mapValues(props) TYPE bloom_filter(0.01) GRANULARITY 4
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (stream, toStartOfFiveMinutes(timestamp), service, level_num, timestamp)
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 0;
