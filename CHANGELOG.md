<!-- SPDX-License-Identifier: Apache-2.0 -->
# Changelog

All notable changes to Tidemark are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Tidemark uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor versions may
contain breaking changes; upgrades never lose data (see the upgrade rules in `SPEC.md`).

Release archives, checksums and container images are on the
[releases page](https://github.com/ZeraOrg/tidemark/releases).

## [Unreleased]

## [0.1.0] - 2026-10-13

First public release.

### Added

- **Ingestion with the clients you already use:** Seq (CLEF and legacy JSON, for Serilog and
  NLog), the Elasticsearch `_bulk` API (Filebeat, Fluent Bit, Vector, Logstash), OpenTelemetry
  OTLP over gRPC and HTTP (protobuf and JSON) for logs, traces and metrics, and native JSON /
  NDJSON. Syslog and Fluent forward through the optional OpenTelemetry Collector profile.
- **Connections with per-connection API keys:** every application or shipper gets its own key,
  optionally limited by protocol, network (CIDR) and expiry; keys can be rotated and revoked.
- **Tidemark Query Language (TQL):** free text and `field:value` filters to start with, comparison
  and boolean operators, and pipe stages such as `stats`, `sort` and `top` for analytics, with
  autocomplete and precise error messages.
- **Web UI:** log explorer with histogram, facets, signals, live tail and event details; trace
  search and waterfall; metrics browser; dashboards with a visual panel builder, variables and
  templates; alert rules with history; dark and light themes.
- **Alerting** on any query, with webhook, Slack, Microsoft Teams and email notifications.
- **Streams and retention:** streams with retention rules, storage statistics, field mappings,
  indexed fields, purge (with dry run) and pause/resume.
- **Users, roles, API tokens and an audit log.**
- **Durability:** a write-ahead log so acknowledged data survives crashes, idempotent inserts,
  and upgrade safety with automatic pre-migration backups and downgrade protection.
- **Optional high availability:** several stateless nodes behind a load balancer with
  PostgreSQL metadata, ClickHouse replication and leader election (reference setup in
  `deploy/ha/`).
- **Importers** for Elasticsearch/OpenSearch, Kibana saved searches, the Seq API (including
  signals) and CLEF files (`tidemark import`).
- **Operator commands:** `tidemark backup`, `restore`, `migrate`, `doctor` and
  `admin reset-password`.
- **Runs on Linux and Windows:** single static binaries for linux/amd64, linux/arm64 and
  windows/amd64 (systemd unit and Windows service installers), and a multi-arch container image
  with a Docker Compose setup.

[Unreleased]: https://github.com/ZeraOrg/tidemark/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ZeraOrg/tidemark/releases/tag/v0.1.0
