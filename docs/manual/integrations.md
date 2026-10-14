<!-- SPDX-License-Identifier: Apache-2.0 -->
# Integrations

How OpenTidemark works with third-party products, in both directions where that exists: Seq
clients and imports from Seq, Elastic Stack shippers through the Elasticsearch `_bulk` ingestion
API and imports from Elasticsearch, OpenSearch and Kibana, OpenTelemetry® SDKs and the
Collector, Prometheus® in both directions, and ClickHouse® as the storage backend. Each section
gives a tested configuration, the limitations and links.

OpenTidemark implements the ingestion protocols named below for interoperability. It is not
affiliated with, endorsed or sponsored by the owners of these products, and compatibility
statements describe protocol interoperability only (see the
[trademark notice](#trademark-notice)).

## Seq

OpenTidemark accepts the Seq ingestion API (CLEF and the legacy JSON format), so applications that
log with Serilog or NLog through their Seq sinks only change the server URL and API key. It is
an alternative to Seq, not a drop-in for Seq's query or management APIs.

| Direction | How |
|---|---|
| Applications → OpenTidemark | `Serilog.Sinks.Seq`, `NLog.Targets.Seq`, `seqcli ingest` to `POST /api/events/raw` or `/ingest/clef` on port 5341 or the HTTP listener |
| Seq → OpenTidemark (migration) | `tidemark import seq` (events and signals through Seq's HTTP API), or `tidemark import clef` for exported CLEF/NDJSON files |

Configuration: [Ingestion › Serilog](ingestion.md#seq-clients-serilog-net) and
[NLog](ingestion.md#seq-clients-nlog). Keep existing Seq API keys by creating connections with an
admin-supplied `key`.

```bash
export SEQ_KEY=<SEQ_API_KEY_WITH_READ_PERMISSION>
tidemark import seq --url https://seq.example.com --api-key-env SEQ_KEY --signals --stream seq-history
```

Tested: CLEF and legacy JSON with curl against port 5341 (`201 {"MinimumLevelAccepted":null}`).

Limitations:

- Ingestion and import only. Seq's query, signal-management and app APIs are not provided; `GET /api`
  answers `{"Product":"Tidemark","Version":"…"}` for client probes.
- Seq filter expressions are translated to TQL during the signal import; untranslatable ones are
  imported **disabled** with a warning.
- `?apiKey=` in the URL works but is logged as a warning; use the `X-Seq-ApiKey` header.

Links: [Migrating from Seq](../migrate-from-seq.md), [Sending data › Seq](../ingestion.md#seq).

## The Elastic Stack

OpenTidemark accepts the Elasticsearch `_bulk` ingestion API (`index` and `create` actions, single
`_doc`/`_create` documents) on port 9200 and under `/es/` on the HTTP listener, and answers the
start-up calls shippers make (cluster info, `_license`, templates, ILM, ingest pipelines, data
streams) without effect. It works with Filebeat, Fluent Bit and Vector; Logstash sends through its
`http` output to the native JSON endpoint. It is not an Elasticsearch implementation and does not
identify itself as Elasticsearch: search, mapping, update and delete APIs are not available.

| Direction | How |
|---|---|
| Shippers → OpenTidemark | Elasticsearch outputs of Filebeat, Fluent Bit and Vector (and other `_bulk` clients without a product check); Logstash `http` output → `POST /api/v1/ingest/logs` |
| Elasticsearch / OpenSearch → OpenTidemark (migration) | `tidemark import elastic` (point in time + `search_after`, scroll fallback), 7.10+, 8.x, OpenSearch 1–2 |
| Kibana → OpenTidemark (migration) | `tidemark import kibana <export.ndjson>`: saved searches ⇒ signals, index patterns/data views ⇒ streams; dashboards and visualisations are listed as not converted |

### Tested shipper versions

| Shipper | Version tested | Output and auth | Result |
|---|---|---|---|
| Filebeat | 8.15.3 | `output.elasticsearch`, `api_key: "tidemark:<key>"` | Works |
| Fluent Bit | 3.1.9 | `es` output, `HTTP_User`/`HTTP_Passwd` | Works |
| Vector | 0.41.1 | `elasticsearch` sink (`api_version: v8`), `auth.strategy: basic` | Works |
| Logstash | 8.15.3 | `http` output to `/api/v1/ingest/logs`, header `X-Tidemark-Key` | Works |
| Logstash | 8.15.3 (`logstash-output-elasticsearch` 11.22.9) | `elasticsearch` output | **Not supported**: fails at start-up (see below) |

Configurations: [Ingestion › `_bulk` shippers](ingestion.md#bulk-shippers-filebeat) and
[Ingestion › Logstash](ingestion.md#logstash).

### Logstash {#logstash}

OpenTidemark does not identify itself as Elasticsearch: it sends no `X-Elastic-Product` header and
its cluster info carries no tagline. Logstash's `elasticsearch` output performs a product check
(it requires that header when the reported version is 7.14 or later) and has no option to turn
it off, so it stops at start-up:

```
[ERROR][logstash.javapipeline][main] Pipeline error {:pipeline_id=>"main", :exception=>#<LogStash::ConfigurationError: Could not connect to a compatible version of Elasticsearch>, …}
[FATAL][org.logstash.Logstash] Logstash stopped processing because of an error: (SystemExit) exit
```

Use Logstash's `http` output to the native JSON endpoint instead (the connection must allow the
native protocol):

```ruby
filter {
  # Optional: choose the stream; without it the connection's default stream is used.
  mutate { add_field => { "_stream" => "logstash-app" } }
}
output {
  http {
    url => "https://tidemark.example.com:8080/api/v1/ingest/logs"
    http_method => "post"
    format => "json_batch"
    headers => { "X-Tidemark-Key" => "${TIDEMARK_KEY}" }
  }
}
```

`format => "json_batch"` posts one JSON array per batch. `@timestamp`, `message`, `level` (or
`log.level`) and `host.name` become the timestamp, message, level and host; the other fields stay
properties (`event.original`, `@version`, …). The output retries `429` and `503`, so
backpressure loses nothing. The connection snippet **Logstash** in the UI shows the same output.

Official Elasticsearch language clients (7.14 and later) perform the same product check and are
expected to refuse the connection; they were not tested.

### Limitations

- Only `index` and `create` bulk actions; `update`/`delete` items fail with `action_not_supported`.
- Template, ILM, pipeline and data-stream calls are acknowledged and ignored (ingest pipelines do
  not run; do the processing in the shipper).
- `_index` becomes the stream name with date suffixes removed; there are no per-index mappings.
- The reported version is `8.11.0`, the API level of the implemented `_bulk` format; clients that
  branch on the version behave as with 8.x.

Links: [Migrating from Elasticsearch, OpenSearch and Kibana](../migrate-from-elastic.md),
[Sending data › Elasticsearch](../ingestion.md#elasticsearch).

## OpenTelemetry

OpenTidemark receives OTLP logs, traces and metrics over gRPC (`:4317`) and HTTP (`:4318`, and
`/otlp/` on the HTTP listener), protobuf or JSON, from OpenTelemetry SDKs and the OpenTelemetry
Collector.

| Direction | How |
|---|---|
| SDKs → OpenTidemark | `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_PROTOCOL`, `OTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=<key>` |
| Collector → OpenTidemark | `otlphttp` exporter with header `X-Tidemark-Key`, or `otlp` (gRPC) with `x-tidemark-key` |
| Collector as protocol bridge | Syslog, Fluent forward, Prometheus scraping, host metrics … → OTLP → OpenTidemark |

Tested: Python SDK 1.27.0 (traces, logs, metrics) and `otel/opentelemetry-collector-contrib`
0.120.0 (`otlphttp` and `otlp` exporters, `prometheus`, `syslog` and `fluentforward`
receivers through `deploy/otel-collector.yaml`).

Configurations: [Ingestion › OpenTelemetry SDKs](ingestion.md#opentelemetry-sdks),
[Collector](ingestion.md#opentelemetry-collector), [Syslog and Fluent forward](ingestion.md#syslog-and-fluent-forward-collector-profile).

Limitations:

- OpenTidemark does not export telemetry to other OTLP back-ends.
- Resource attributes of spans are kept as JSON; only logs have an `environment` column.
- Syslog through the Collector keeps the raw line as `message`; host and app name are properties
  (`hostname`, `appname`), not the built-in `host`/`service`.
- Exporter timeouts: keep `retry_on_failure` and `sending_queue` enabled; `503`/`UNAVAILABLE` are
  retryable.

## Prometheus

| Direction | How | Status |
|---|---|---|
| Prometheus → scrapes OpenTidemark | `GET /metrics` on the HTTP listener, `TIDEMARK_METRICS_ENABLED=true`, bearer `TIDEMARK_METRICS_TOKEN` | Available |
| Prometheus-instrumented apps → OpenTidemark | OpenTelemetry Collector `prometheus` receiver → `otlphttp` exporter | Available |
| Prometheus `remote_write` → OpenTidemark | Native receiver | **Planned (M4), not available** |
| PromQL against OpenTidemark | — | Not available; query metrics with TQL |

Tested: `prom/prometheus` v2.53.2 scraping `/metrics` with `authorization.credentials_file` (target
`up`), `promtool check rules` on the shipped rules, `prom/node-exporter` v1.8.2 scraped by the
Collector and queried in OpenTidemark.

Configurations: [Metrics › Scraping OpenTidemark with Prometheus](metrics.md#scraping-opentidemark-with-prometheus)
and [Metrics › Prometheus-instrumented applications](metrics.md#prometheus-apps).

Limitations:

- Histograms are stored with their buckets, but TQL does not compute histogram quantiles from
  buckets.
- Labels are stored as text attributes.
- Self-metrics have no ClickHouse-reachability gauge; `tidemark_wal_bytes` and
  `tidemark_pipeline_write_errors_total` show a backlog and failing inserts, `/readyz` shows
  reachability.

## ClickHouse

ClickHouse is OpenTidemark's telemetry store; it is a required component, not an optional
integration.

| Topic | Details |
|---|---|
| Version | 25.8 LTS (`clickhouse/clickhouse-server:25.8`); HA reference requires 24.8+ and is tested with 25.8 |
| Database and users | Database `tidemark`; `tidemark_writer` (DDL, inserts, admin) and `tidemark_reader` (`readonly=2`, 1 GiB, 60 s) from `deploy/clickhouse/users.d/tidemark.xml`, passwords via `from_env` |
| Server settings | `deploy/clickhouse/config.d/tidemark.xml`: log level `warning`, no MySQL/PostgreSQL ports, crash reports off, `backups` disk; `docker-listen.xml` only in Compose |
| Schema | Created and migrated by OpenTidemark at start-up (additive migrations only) |
| HA | Cluster `tidemark_cluster`, 1 shard × 3 replicas, Keeper quorum of 3, `ReplicatedMergeTree`, insert quorum (`deploy/ha/clickhouse/`) |
| Backups | `tidemark backup` runs `BACKUP TABLE … TO Disk('backups', …)` |
| Windows | No native ClickHouse build; run it in Docker Desktop (WSL2) |

Tested: OpenTidemark against `clickhouse/clickhouse-server:25.8` (25.8.33.6) with the repository's
`config.d` and `users.d` files; `tidemark doctor` all `ok`; `tidemark backup` wrote a ClickHouse
backup of five tables.

Limitations and rules:

- Do not write to the `tidemark` database yourself and do not change the schema; use the API
  (retention, indexed fields, purge).
- Direct read access for BI tools is possible with an additional read-only ClickHouse user, but
  the table layout is internal and may change between releases (additively).
- Never expose ClickHouse ports to untrusted networks
  ([Security hardening](security-hardening.md#clickhouse-users-and-network-isolation)).

Links: [Installation › ClickHouse from the native packages](../installation.md#52-clickhouse-from-the-native-packages),
[`deploy/ha/README.md`](../../deploy/ha/README.md).

## Trademark notice {#trademark-notice}

Elasticsearch, Kibana, Logstash and Beats are trademarks of elasticsearch B.V., registered in the
U.S. and in other countries. OpenTelemetry and Prometheus are registered trademarks of The Linux
Foundation. OpenSearch is a trademark of LF Projects, LLC. ClickHouse is a registered trademark of
ClickHouse, Inc. Seq is a trademark of Datalust Pty Ltd. All other names are trademarks of their
respective owners. OpenTidemark is an independent project, not affiliated with, endorsed or
sponsored by any of them. Compatibility statements describe protocol interoperability only.
