<!-- SPDX-License-Identifier: Apache-2.0 -->
# OpenTidemark Operations Manual

The operations manual for engineers and automation agents who deploy, secure, feed, query and run
OpenTidemark in production: architecture, deployment topologies, hardening, users and teams,
ingestion recipes, metrics, retention, dashboards, alerting, day-2 operations and the automation
API.

## How to use this manual

The manual is task-oriented. Each chapter starts with the concepts it needs and then gives
complete, copy-pasteable procedures. The user documentation stays the reference for syntax and
settings; the manual links to it instead of repeating it.

| You want to… | Read |
|---|---|
| Pick a topology and size a production node | [Deployment](deploy.md) |
| Lock an installation down before it takes traffic | [Security hardening](security-hardening.md) |
| Manage users, roles, API tokens and teams | [Users and access](users-access.md) |
| Connect applications and shippers | [Ingestion](ingestion.md), [Integrations](integrations.md) |
| Bring in metrics, or monitor OpenTidemark with Prometheus® | [Metrics](metrics.md) |
| Control retention, storage and indexes | [Streams and retention](streams-retention.md) |
| Investigate incidents | [Querying](querying.md) |
| Build dashboards, keep them in git | [Dashboards](dashboards.md) |
| Page someone | [Alerting](alerting.md) |
| Back up, upgrade, recover, troubleshoot | [Operations](operations.md) |
| Automate everything | [API for automation](api.md), [Guide for agents](agents.md) |

Conventions:

- Placeholders are written `<LIKE_THIS>`. Never paste real keys or passwords into tickets, chat or
  shell history; read them from files or environment variables as the examples do.
- `$TM` is the base URL of the HTTP listener, for example `https://tidemark.example.com` behind a
  proxy or `http://localhost:8080` on the host. `$TM_TOKEN` is an API token (`tmt_…`),
  `$TM_KEY` a connection key (`tmk_…`).
- Shell examples are POSIX `sh`/`bash`; PowerShell variants are given where Windows differs.
- The procedures were executed against a test installation built from the same source where
  that was possible; the exceptions are named in the chapters.

## Architecture overview

OpenTidemark is a single Go binary, `tidemark`, that serves the web UI, the HTTP API and every
ingestion protocol. Telemetry is stored in ClickHouse®; metadata (users, teams, sessions, keys,
connections, signals, dashboards, alerts, settings, audit log) lives in SQLite in the data
directory, or in PostgreSQL in high-availability mode.

```text
 Clients                                    tidemark (one binary, one process)
 ───────                                    ───────────────────────────────────────────────────────
 Serilog / NLog (Seq API) ─── :5341, :8080 ─▶ ingest/seq ──┐
 Filebeat, Fluent Bit,                                       │   auth (connection key)
 Vector (_bulk) ──────────── :9200, :8080/es ▶ ingest/elastic┤   normalize (§9.6)
 OpenTelemetry SDKs /       gRPC :4317                       ├─▶ pipeline ─▶ WAL ─▶ batcher ─▶ ClickHouse
 Collector (OTLP)  ──────── HTTP :4318, :8080/otlp ▶ ingest/otlp │  (bounded queue,   (fsync     (writer user)
 Logstash, any JSON ──────── :8080 ─▶ ingest/native ─────────┘   backpressure)      before ack)
                                                                                         │
 Browser / automation ────── :8080 ─▶ web UI + /api/v1 ── TQL compiler ── reader (readonly=2) ┘
                                       alerting · retention · stream stats · purge · imports
                                       metadata: SQLite (<data>/tidemark.db) or PostgreSQL (HA)

 Syslog 5514 tcp/udp, Fluent forward 24224 ─▶ OpenTelemetry Collector (optional) ─▶ OTLP/HTTP :4318
```

### Components

| Component | Role | State |
|---|---|---|
| `tidemark serve` | UI, API, ingestion listeners, pipeline, alert evaluator, retention manager, stats collector, purge and import runners | WAL, `secret.key`, SQLite metadata (single node) in the data directory |
| ClickHouse 25.8 | Telemetry tables `events`, `spans`, `metric_points`, `field_catalog` in database `tidemark` | ClickHouse volume |
| PostgreSQL 15+ (HA only) | Shared metadata for all nodes, leases, cache invalidation (`LISTEN/NOTIFY`) | External, highly available |
| OpenTelemetry® Collector (optional) | Receives syslog and Fluent forward, forwards over OTLP/HTTP | Stateless (in-memory queue) |
| Load balancer (HA only) | Spreads HTTP, ingestion and OTLP traffic; health-checks `/readyz` | Stateless |

### Ports

| Port | Listener (variable) | Serves |
|---|---|---|
| 8080/tcp | `TIDEMARK_HTTP_ADDR` | Web UI, `/api/v1`, native JSON ingest, Seq endpoints, `_bulk` under `/es/`, OTLP/HTTP under `/otlp/`, `/healthz`, `/readyz`, `/metrics` (when enabled) |
| 5341/tcp | `TIDEMARK_SEQ_ADDR` | Seq endpoints only (plus `/healthz`, `/readyz`) |
| 9200/tcp | `TIDEMARK_ES_ADDR` | Elasticsearch `_bulk` ingestion API at the root path |
| 4317/tcp | `TIDEMARK_OTLP_GRPC_ADDR` | OTLP/gRPC |
| 4318/tcp | `TIDEMARK_OTLP_HTTP_ADDR` | OTLP/HTTP (`/v1/logs`, `/v1/traces`, `/v1/metrics`) |
| 5514/tcp+udp, 24224/tcp | Collector profile | Syslog (RFC 5424), Fluent forward |
| 9000/tcp | ClickHouse | Native protocol used by OpenTidemark. Never expose it. |

Every OpenTidemark listener except the HTTP listener can be disabled with `""`. Port 8080 alone is
sufficient for all ingestion protocols except OTLP/gRPC, which makes a single reverse-proxied
HTTPS endpoint practical.

### Data flow and durability

1. A client sends a request with a **connection key**. The key decides the allowed protocols,
   source networks, default stream, allowed streams, minimum level, applied properties and rate
   limit.
2. The protocol handler maps the payload into the normalized model (event, span or metric point).
3. Rows are appended to the **write-ahead log** (`<data>/wal/<signal>/`) and fsynced before the
   request is acknowledged (group commit, at most every 10 ms).
4. The pipeline reads the WAL in order, builds batches of up to `TIDEMARK_BATCH_MAX_ROWS` rows or
   `TIDEMARK_BATCH_MAX_WAIT`, and inserts them with a deterministic deduplication token, so a
   replay after a crash never duplicates rows.
5. Rows ClickHouse permanently rejects go to dead-letter files in `<data>/deadletter/`; transient
   failures are retried while the WAL holds the data.
6. When the queue or the WAL is full, ingestion answers `503` (OTLP/gRPC `UNAVAILABLE`) and
   stores nothing from that request, so clients can retry safely.

### Storage

| Data | Where | Retention |
|---|---|---|
| Log events | ClickHouse `tidemark.events`, partitioned by day, ordered by stream, time, service, level | Default 30 days, per stream and per level overrides |
| Spans | `tidemark.spans` | Default 7 days, per stream override |
| Metric points | `tidemark.metric_points` | Default 30 days, per stream override |
| Field catalog | `tidemark.field_catalog` (autocomplete, mappings) | 30 days (fixed) |
| Metadata | `<data>/tidemark.db` (SQLite) or PostgreSQL | Until deleted; automatic pre-migration copies in `<data>/backups` (last 10) |
| Secrets at rest | `<data>/secret.key` (AES-256-GCM key for channel and import secrets) | Must be backed up with the metadata |
| WAL | `<data>/wal` | Deleted once committed; capped by `TIDEMARK_WAL_MAX_BYTES` (10 GiB) |

## Glossary

| Term | Meaning |
|---|---|
| Event | One structured log record. |
| Span | One unit of work in a distributed trace. |
| Metric point | One sample of a metric time series (gauge, sum, histogram, exponential histogram or summary). |
| Signal | A saved, named TQL filter that can be toggled in the explorer. |
| Stream | A named dataset (`^[a-z0-9][a-z0-9._-]{0,63}$`). Every event, span and metric point belongs to exactly one stream. Retention, purge, statistics and read access work per stream. |
| Connection | A named ingestion source with its own key (`tmk_…`) and restrictions, including the streams it may write to. |
| Connection key | The secret an ingestion client presents. Shown once. |
| API token | A user-owned bearer token (`tmt_…`) for `/api/v1`, scoped `read`, `write` or `admin`; it sees the same streams as its owner. |
| Team | A group of users that is granted stream names; a user's stream scope is the union of their teams' grants ([Users and access](users-access.md#teams)). |
| Stream scope | The set of streams a principal may read, computed on every request from the role, the teams and the *deny by default* setting. |
| Scope picker | The top-bar control that narrows Events, Traces, Metrics and dashboards to some of the visible streams and environments; it never widens access. |
| TQL | OpenTidemark Query Language: filters plus pipe stages (`stats`, `top`, `sort`, …). |
| Channel | A notification target of alert rules: webhook, Slack, Microsoft Teams or email. |
| WAL | Write-ahead log: acknowledged ingestion data on local disk before it reaches ClickHouse. |
| Dead letter | NDJSON file with rows ClickHouse permanently rejected. |
| Lease | A row in the metadata database that makes one node the leader for a singleton job (alerting, retention, stats, purge, migrations, imports). |
| Drain | The graceful shutdown phase: `/readyz` answers `503` for `TIDEMARK_DRAIN_SECONDS` before the listeners close. |
| Principal | The authenticated identity of an API request: a user session or an API token. |

OpenTidemark is an independent project, not affiliated with, endorsed or sponsored by the owners
of the third-party products named in this manual. See the
[trademark notice](integrations.md#trademark-notice).
