<!-- SPDX-License-Identifier: Apache-2.0 -->
# OpenTidemark documentation

OpenTidemark is an open-source, self-hosted observability platform for logs, metrics and
distributed traces that accepts OpenTelemetry®, Seq and Elasticsearch `_bulk` shippers and stores
telemetry in ClickHouse®.

![OpenTidemark log explorer with histogram, signals, facets and the event stream](images/explorer.png)

Applications keep their existing clients and only change a URL and an API key: OpenTelemetry SDKs
and the Collector (OTLP gRPC and HTTP), Serilog and NLog through the Seq ingestion API,
Filebeat, Fluent Bit and Vector through Elasticsearch `_bulk`, and Logstash through its `http`
output. Everything is searched with one query language, from a quick
`level:error service:checkout*` to `stats` pipelines, and any result can become a dashboard panel
or an alert. OpenTidemark is a self-hosted alternative to Seq and to Elasticsearch/Kibana, runs on
Linux, Windows or Docker, and is licensed under Apache-2.0 with every feature included and no user
limits.

## Guides

- [Quick start](quickstart.md): run OpenTidemark and ClickHouse locally with Docker Compose and
  send a first event in about ten minutes.
- [Installation](installation.md): Docker Compose, native Linux (systemd) and Windows services,
  hardening, upgrades, backups and troubleshooting.
- [Sending data](ingestion.md): OpenTelemetry, Seq, Elasticsearch `_bulk` and native JSON,
  connection keys and field mapping.
- [Query language (TQL)](query-language.md): filters, operators and pipe stages for analytics.
- [Configuration](configuration.md): every setting with its default.
- [Migrating from Seq](migrate-from-seq.md) and
  [from Elasticsearch, OpenSearch and Kibana](migrate-from-elastic.md).

## Operations manual

The [operations manual](manual/index.md) is written for the engineers and automation agents who
run OpenTidemark in production: [deployment](manual/deploy.md) topologies and a production
checklist, [security hardening](manual/security-hardening.md), [users, tokens and
teams](manual/users-access.md), [ingestion recipes](manual/ingestion.md) per protocol and
shipper, [integrations](manual/integrations.md), [metrics](manual/metrics.md) and Prometheus®
monitoring, [streams and retention](manual/streams-retention.md), a
[TQL cookbook](manual/querying.md), [dashboards as code](manual/dashboards.md),
[alerting](manual/alerting.md), [day-2 operations](manual/operations.md) with an incident
runbook, the [automation API](manual/api.md) and a [guide for agents](manual/agents.md).

Every release attaches this site as an offline bundle (`tidemark_<version>_docs.zip`, with
search); agents and language models can read it as
[`llms.txt`](https://zeraorg.github.io/opentidemark/llms.txt) (index) and
[`llms-full.txt`](https://zeraorg.github.io/opentidemark/llms-full.txt) (full text).

## Project

- Source code, issues and the full specification:
  [github.com/ZeraOrg/opentidemark](https://github.com/ZeraOrg/opentidemark)
- Downloads: [releases](https://github.com/ZeraOrg/opentidemark/releases) and the container image
  `ghcr.io/zeraorg/tidemark`
- What changed: [changelog](https://github.com/ZeraOrg/opentidemark/blob/main/CHANGELOG.md)
- Security policy: [SECURITY.md](https://github.com/ZeraOrg/opentidemark/blob/main/SECURITY.md)
