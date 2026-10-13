-- SPDX-License-Identifier: Apache-2.0
CREATE TABLE IF NOT EXISTS {db}.field_catalog (
  hour DateTime, source LowCardinality(String), stream LowCardinality(String),
  key LowCardinality(String), numeric UInt8, hits SimpleAggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree ORDER BY (source, stream, key, numeric, hour)
TTL hour + INTERVAL 30 DAY;

CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.mv_field_catalog_events TO {db}.field_catalog AS
SELECT toStartOfHour(timestamp) AS hour, 'logs' AS source, stream, k AS key,
       toUInt8(mapContains(props_num, k)) AS numeric, count() AS hits
FROM {db}.events ARRAY JOIN mapKeys(props) AS k GROUP BY hour, stream, key, numeric;

CREATE MATERIALIZED VIEW IF NOT EXISTS {db}.mv_field_catalog_spans TO {db}.field_catalog AS
SELECT toStartOfHour(start_time) AS hour, 'spans' AS source, stream, k AS key,
       toUInt8(mapContains(attrs_num, k)) AS numeric, count() AS hits
FROM {db}.spans ARRAY JOIN mapKeys(attrs) AS k GROUP BY hour, stream, key, numeric;
