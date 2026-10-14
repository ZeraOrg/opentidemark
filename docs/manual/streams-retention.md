<!-- SPDX-License-Identifier: Apache-2.0 -->
# Streams and retention

Manage OpenTidemark's data lifecycle: create and route streams, set default, per-stream and
per-level retention, read storage statistics, inspect field mappings, decide when an indexed
field is worth its cost, pause streams and purge data safely with a dry run first.

All write operations in this chapter require the **admin** role; reading streams and mappings
requires viewer. UI: **Streams** (`/streams`, tabs *Overview*, *Retention*, *Mappings*, *Purge*).

## Streams

A stream is a named dataset that holds events, spans and metric points. It plays the role of an
Elasticsearch index/data stream or a Seq retention scope. Retention, purge, statistics and read
access ([teams](users-access.md#teams)) are per stream; `GET /streams` and the **Streams** page
list only the streams the caller may see.

- Names: `^[a-z0-9][a-z0-9._-]{0,63}$`. Ingestion normalises names (lower-case, invalid
  characters ⇒ `-`); the API requires valid names.
- Streams are created automatically on first write while fewer than `TIDEMARK_MAX_STREAMS` (200)
  exist, with the default retention. Creating them up front lets you set retention before data
  arrives.
- The stream `default` always exists and cannot be deleted.
- How data is routed: [Ingestion › Routing to streams](ingestion.md#routing-to-streams).

### Create and configure

```bash
curl -sS "$TM/api/v1/streams" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "name": "payments",
  "description": "Payments services, all environments",
  "retentionDays": 90,
  "spansRetentionDays": 14,
  "metricsRetentionDays": 30,
  "levelRules": [{"levels": ["Verbose", "Debug"], "days": 3}]
}'
# 201; an existing name ⇒ 409 conflict

# Change retention only (absent fields unchanged, null = back to the default)
curl -sS -X PATCH "$TM/api/v1/streams/payments" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"retentionDays": 60, "spansRetentionDays": null}'
```

Limits: retention days 1–36,500; at most 6 level rules; levels accept any level alias and are
stored as canonical names.

UI: **Streams › New stream**, then the *Retention* tab of the stream (*Retention*, *Level rules*,
*TTL preview*).

## Retention {#retention}

Retention is implemented as ClickHouse® TTL rules; expired rows are removed by ClickHouse's
background merges, not instantly at midnight.

| Level | Default | Where to set |
|---|---|---|
| Installation defaults | logs 30, spans 7, metrics 30 days | **Streams › Default retention**, or `PATCH /settings` |
| Per stream, per signal | inherits the default | `retentionDays`, `spansRetentionDays`, `metricsRetentionDays` |
| Per stream, per level (logs) | none | `levelRules` (e.g. Verbose/Debug 3 days) |

```bash
# Installation defaults (admin)
curl -sS -X PATCH "$TM/api/v1/settings" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"retention":{"logsDays":14,"spansDays":7,"metricsDays":30}}'
curl -sS "$TM/api/v1/settings" -H "Authorization: Bearer $TM_TOKEN"
# ⇒ {"retention":{"logsDays":14,"spansDays":7,"metricsDays":30},"access":{"denyByDefault":false},"ui":{}}
```

The resulting TTL for `events` looks like:

```text
TTL toDateTime(timestamp) + toIntervalDay(3)  DELETE WHERE stream = 'api' AND level_num IN (0,1),
    toDateTime(timestamp) + toIntervalDay(90) DELETE WHERE stream = 'api',
    toDateTime(timestamp) + toIntervalDay(30) DELETE WHERE stream NOT IN ('api')
```

Rules are re-applied at startup and on every change (idempotent; skipped when unchanged). In HA
mode the node holding the `retention` lease applies them.

Planning tips:

- Lowering retention deletes data on the next merges; raising it never brings back deleted data.
- Debug-heavy services: a level rule (Verbose/Debug 1–3 days) usually saves more storage than
  shortening the stream's retention.
- Before importing historical data, extend the target stream's retention; the import UI warns
  when imported data is older than the retention.
- `field_catalog` (autocomplete and mappings) keeps 30 days regardless of stream retention.

## Storage statistics

```bash
curl -sS "$TM/api/v1/streams" -H "Authorization: Bearer $TM_TOKEN" |
  jq '.statsUpdatedAt, (.streams[] | {name, logs: .stats.logs | {rows, compressedBytes, compressionRatio, ratePer5Min, topServices}})'
```

Per stream and source (`logs`, `spans`, `metrics`): `rows`, `oldest`, `newest`, `ratePer5Min` and
`ratePer24h` (rows/s averaged over the window), `compressedBytes`, `uncompressedBytes`,
`compressionRatio` (uncompressed ÷ compressed), `estimated` (always `true`: bytes are apportioned
from table totals by row share) and up to five `topServices`.

A collector refreshes the cache every 5 minutes (on the `stats` lease holder in HA);
`GET /streams` refreshes on demand when the cache is older. For very small streams the ratio can
be below 1 because of per-part overhead.

UI: **Streams** list (*Rows*, *Size (compressed)*, *Ratio*), and the *Overview* tab of each stream.

## Field mappings

`GET /api/v1/streams/{name}/fields` lists every property key seen in the stream (from the field
catalog, last 30 days): `source`, `key`, `type` (`string`, `number` or `mixed`), `hits`,
`firstSeen`, `lastSeen` (hour granularity) and `indexed`.

```bash
curl -sS "$TM/api/v1/streams/payments/fields" -H "Authorization: Bearer $TM_TOKEN" |
  jq -r '.[] | [.source, .key, .type, .hits, .indexed] | @tsv' | head -20
```

Use mappings to find `mixed` fields (a key that is sometimes a number and sometimes text, which
makes numeric comparisons skip the text values) and accidental high-cardinality keys (for example
keys that embed ids). Fix them at the source.

UI: *Mappings* tab of a stream (*Search keys*).

## Indexed fields

An indexed field adds a materialised `String` column (`f_<slug>`) plus a bloom-filter skip index
to the ClickHouse table, then materialises the index for existing parts in the background.

| When it helps | When it does not |
|---|---|
| Exact, case-sensitive equality on a high-cardinality key: `OrderId = "A-1"`, `user.id in ["u1","u2"]` | Case-insensitive `key:value` (still reads the column but cannot skip granules) |
| The key appears in many events and queries filter on it often | Numeric comparisons (they use the numeric map), `!=`, wildcards, regex |
| Large time ranges where skipping granules matters | Low-cardinality keys (level-like values) — the bloom filter skips nothing |

Cost: extra storage for the column and index on every row of the table (all streams), extra
insert work, and a background `MATERIALIZE INDEX` mutation on creation. Limits: 50 indexed
fields; one per key and source; keys must match `^[A-Za-z0-9_.@:/\-]{1,128}$`.

```bash
# Index (admin); source logs ⇒ events.props, spans ⇒ spans.attrs, metrics ⇒ metric_points.attrs
curl -sS "$TM/api/v1/indexed-fields" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"source":"logs","key":"OrderId"}'
# ⇒ 201 {"id":"…","source":"logs","key":"OrderId","column":"f_orderid","createdAt":"…"}; again ⇒ 409

curl -sS "$TM/api/v1/indexed-fields" -H "Authorization: Bearer $TM_TOKEN"            # list (viewer)
curl -sS -X DELETE "$TM/api/v1/indexed-fields/<ID>" -H "Authorization: Bearer $TM_TOKEN"   # drops index and column ⇒ 204
```

Write queries that can use it: `OrderId = "A-1"` (case-sensitive) rather than `OrderId:a-1`.
UI: *Index* action on a key in the *Mappings* tab.

## Pause and resume

A paused stream accepts requests (clients see success) but drops its rows in the pipeline,
counted as `tidemark_pipeline_dropped_total{reason="stream_paused"}` and in `GET /system`. Use it
to stop a runaway producer without touching client configuration.

```bash
curl -sS -X PATCH "$TM/api/v1/streams/payments" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"paused": true}'     # false to resume
```

UI: *Pause stream* / *Resume stream*.

## Purge {#purge}

Purge deletes data by stream, time range and optional TQL filter. **Always run a dry run first.**

How it executes:

- Without stream and filter, whole UTC days inside `[from, to)` are removed by dropping
  partitions; the remainder with a lightweight `DELETE`.
- With a stream or filter, a lightweight `DELETE FROM … WHERE` is issued.
- Real purges become jobs (`pending` ⇒ `running` ⇒ `done | failed`), run one at a time on the
  `purge` lease holder; at most 16 pending jobs (more ⇒ `409`).
- Every purge is audited (`purge.request`, `purge.done`, `purge.failed`).

```bash
# 1. Dry run: estimate only, never deletes
curl -sS "$TM/api/v1/purge" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "stream": "payments", "source": "logs", "q": "level:debug and service:checkout",
  "from": "2026-10-01T00:00:00Z", "to": "2026-10-08T00:00:00Z", "dryRun": true}'
# ⇒ 200 {"dryRun":true,"rowsEstimate":12345}

# 2. Real purge with the same body and "dryRun": false ⇒ 202 job
curl -sS "$TM/api/v1/purge" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "stream": "payments", "source": "logs", "q": "level:debug and service:checkout",
  "from": "2026-10-01T00:00:00Z", "to": "2026-10-08T00:00:00Z", "dryRun": false}' | tee purge.json

# 3. Follow the job
curl -sS "$TM/api/v1/purge/$(jq -r .id purge.json)" -H "Authorization: Bearer $TM_TOKEN" | jq '{status, rowsEstimate, droppedPartitions}'
```

Rules: `from` and `to` are required (RFC 3339 or relative like `now-7d`); `q` is filter-only (no
`stats`, `where` allowed); `source` is `logs`, `spans` or `metrics` and must match any `source`
clause in `q`; omit `stream` to purge across all streams. UI: *Purge* tab (*Filter (TQL,
optional)*, *Time range*, dry run with *Estimates rows, never deletes*, typed confirmation).

## Delete a stream

Deleting a stream pauses it, removes its rows from `events`, `spans`, `metric_points` and
`field_catalog` with lightweight deletes, then removes the metadata row and every team grant for
the name (audited as `stream.delete` with `revokedFromTeams`). New data with that stream name
re-creates the stream but not the grants, so stop or re-route the producers first.

```bash
curl -sS -X DELETE "$TM/api/v1/streams/old-app" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"confirm":"old-app"}'
# 204; wrong confirm ⇒ 400; "default" ⇒ 409
```

