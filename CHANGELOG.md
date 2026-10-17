<!-- SPDX-License-Identifier: Apache-2.0 -->
# Changelog

All notable changes to OpenTidemark are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and OpenTidemark uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html). Before 1.0, minor versions may
contain breaking changes; upgrades never lose data (see the upgrade rules in `SPEC.md`).

Release archives, checksums and container images are on the
[releases page](https://github.com/ZeraOrg/opentidemark/releases).

## [Unreleased]

### Changed

- Documentation site: the home page is now a landing page with an overview of OpenTidemark
  (capabilities with screenshots, why self-host it, getting started); the former home page with
  every guide is the new "Documentation overview" page.
- Documentation: the query language (TQL) reference now covers every part of the language, with
  readable examples by task, an error guide, common pitfalls and a cheat sheet; every example is tested.

### Added

- `TIDEMARK_DEADLETTER_MAX_BYTES` (default 1 GiB): the dead-letter folder no longer grows without
  bound; the oldest files are deleted to make room for new ones.
- `TIDEMARK_WAL_MIN_FREE_BYTES` (default 1 GiB): ingestion pauses with `503` before the
  write-ahead log fills the disk, and resumes by itself once space is freed. `0` turns it off.
- `TIDEMARK_WAL_EXIT_ON_FAILURE` (default `true`): when the disk fails to save the write-ahead log,
  OpenTidemark stops cleanly so Docker, systemd or the Windows service restarts it, and the log is
  checked and replayed on start. `/healthz` now answers `503 {"status":"failed"}` in that state.
- Windows service: it now also restarts when OpenTidemark stops because of a fatal error, not only
  after a crash. Existing installations get this when the service is registered again: stop it,
  run `tidemark.exe service uninstall`, then run `install.ps1` again (`tidemark.env` is kept).

### Fixed

- Ingestion: when the disk filled up during a write-ahead-log write, events acknowledged
  afterwards could be lost (skipped as corrupt, or cut off at the next start). A failed write is
  now undone before anything else is written.

## [0.3.0] - 2026-10-14

### Added

- Log explorer: a fresh instance without data shows "No data yet" and points admins at
  connection keys, Settings → Connections and the ingestion docs (other users are told to ask an
  administrator).

### Changed

- Built with Go 1.27.2; Go dependencies updated (among them the SQLite driver, which no longer
  compiles SQLite at run time).
- Web UI dependencies updated (React 19, React Router 7, Vite 8 and others); this also fixes two
  React Router advisories (open redirect, constructor injection).

### Fixed

- When ClickHouse runs out of disk space, ingested data now waits in the write-ahead log and is
  inserted once space is freed, instead of being moved to the dead-letter files.
- Users who open OpenTidemark by its network address or host name (for example
  `http://192.168.1.20:8080`) can now save changes; before, every save failed with a CSRF error
  unless `TIDEMARK_PUBLIC_URL` was set to that address.
- Deleting a user no longer leaves their private dashboards and signals unreachable: they now
  belong to the admin who deleted the user.
- Install docs: the README's "From source" steps enter the right folder (`opentidemark/deploy`),
  the native-install example uses the current archive names and the Windows steps unblock the
  download first; the quick start's first test event uses the current time, so it shows up in the
  default time range.

### Security

- Every release is now checked for known vulnerabilities and malicious packages in its Go and npm
  dependencies (OSV-Scanner with the OpenSSF malicious-packages feed); a finding stops the release.
- Resetting a user's password (by an admin or with `tidemark admin reset-password`) now also
  revokes that user's API tokens, so a recovered account cannot still be used through an old
  token.

## [0.2.1] - 2026-10-14

### Changed

- The repository moved to [github.com/ZeraOrg/opentidemark](https://github.com/ZeraOrg/opentidemark)
  (old links redirect); the Go module path changed accordingly to
  `github.com/ZeraOrg/opentidemark`; the container image name `ghcr.io/zeraorg/tidemark` is
  unchanged. The documentation site is now <https://zeraorg.github.io/opentidemark/>. Release
  attestations of 0.2.0 and earlier verify with `--repo ZeraOrg/tidemark`.

## [0.2.0] - 2026-10-14

### Added

- **Operations manual** in the documentation (`docs/manual/`, section "Operations manual" on
  the site): deployment topologies and a production checklist, security hardening, users, roles,
  tokens and teams, ingestion recipes per protocol and shipper, integrations, metrics and
  Prometheus monitoring with ready-made alert rules, streams and retention, a TQL cookbook,
  dashboards as code, alerting with the webhook signature, day-2 operations and an incident
  runbook, the automation API and a guide for automation agents.
- Every release attaches the documentation as an offline bundle, `tidemark_<version>_docs.zip`,
  that works from `file://` with search (`make docs-offline` builds it locally).
- The documentation site publishes `llms.txt` (an index of every page) and `llms-full.txt` (the
  full text) for agents and language models; the offline bundle contains them too.
- Query language: the built-in field `connection_id` filters and groups logs, spans and metrics
  by the connection that sent them.
- New metric `tidemark_wal_bytes{signal}`: the size of the write-ahead log on disk, so a
  growing backlog (for example while ClickHouse is down) can be alerted on.
- A trademark notice in `LICENSING.md`, linked from the README and the documentation site
  footer, names the owners of the third-party products OpenTidemark works with.
- **Teams**: admins group users into teams and grant each team the streams it may see
  (`/api/v1/teams`, `PUT /api/v1/users/{id}/teams`). `GET /api/v1/auth/me` reports which
  streams the signed-in user can see, and user objects list their teams.
- **Data access by stream**: searches, histograms, facets, field lists, events, traces,
  metrics, exports, the live tail and the streams list show only the streams a user may see.
  Admins see every stream, members of teams see the streams granted to their teams, and users
  in no team see every stream unless *deny by default* is turned on in the settings. Search
  requests can be narrowed to some streams (`streams`) and, for logs, environments (`env`).
- Web UI: **Settings → Teams** manages teams, their members and streams, and the deny-by-default
  setting; the Users page shows and assigns teams, and your account page tells you which streams
  you can see.
- Web UI: a **scope picker** in the top bar of Events, Traces, Metrics and dashboards narrows
  the view to some of your streams and, for logs, to environments; the choice is in the URL and
  remembered. Users without any stream get a clear empty state instead of empty results.
- Web UI: alert rules can be limited to streams in the rule editor; connections get an
  "Allowed streams" field and an edit dialog; a trace shows "parent span not visible" when the
  parent is in a stream you cannot see.
- Connections can be limited to **allowed streams**: data that names another stream is written
  to the connection's default stream and counted in `tidemark_ingest_rerouted_total`.
- Alert rules can be limited to chosen streams. Users who see only some streams must choose
  streams they can see, and they only see alert rules over those streams. Existing rules keep
  reading every stream.
- **Documentation website** at <https://zeraorg.github.io/tidemark/> with search, built from the
  guides in `docs/`.
- Log explorer: **Show in column** in the event detail adds any field to the event list as a
  column, now including built-in fields such as host, environment and trace ID. Remove a column
  with the × in its header. The Columns dialog lists the shown columns in order (move, remove)
  and the other fields with a filter; with many columns the list scrolls sideways instead of
  squeezing the message.
- A step-by-step guide for running OpenTidemark locally with Docker Compose (`docs/quickstart.md`).
- A detailed installation guide for every supported platform (`docs/installation.md`). It covers
  Docker Compose, native Linux (systemd) and Windows (service) installs with ClickHouse, macOS for
  development, hardening, upgrades, backups and troubleshooting.
- **Ready-to-use Docker bundle** in every release: `tidemark_<version>_docker.tar.gz` (and
  `.zip`) contains a Compose file that runs the published image of that release, the installers,
  the ClickHouse configuration and an `INSTALL.md`. Unpack it and run `./install.sh docker`
  (`.\install.ps1 -Mode Docker` on Windows); no source checkout or image build is needed.
- Every release archive now contains an `INSTALL.md` with the installation steps for its
  platform (verification, installing, ClickHouse, service management, upgrade, uninstall).
- The release notes list the assets with quick install commands and how to verify them.

### Changed

- The Elasticsearch endpoint no longer identifies itself as Elasticsearch: the
  `X-Elastic-Product` header and the tagline are gone, and the cluster info only reports the
  name and the API version. Filebeat, Fluent Bit and Vector keep working. Logstash's
  `elasticsearch` output refuses such a server; send from Logstash with its `http` output to the
  native JSON endpoint instead (see "Logstash" in `docs/ingestion.md`). The Logstash connection
  snippet now shows that configuration.
- The project is now called OpenTidemark; commands, settings, the image and the API are
  unchanged.

### Fixed

- `tidemark_pipeline_dropped_total{reason="backpressure"}` no longer counts events whose client
  disconnected or timed out while they were being written to the write-ahead log. Those events
  are stored, so they were never lost; they now count as enqueued.

### Security

- The ClickHouse configuration in the release archives (`deploy/clickhouse/config.d/tidemark.xml`)
  no longer makes ClickHouse listen on every network interface. A native ClickHouse install that
  uses it now keeps ClickHouse's default loopback-only listen address; the Docker Compose stack
  gets the container listen address from a separate Compose-only file (`docker-listen.xml`). If
  you deleted the `listen_host` line by hand before, there is nothing to change.
- The shipped ClickHouse configuration (single server and high availability) switches off
  ClickHouse's crash reports to the ClickHouse developers.
- The container image is scanned for known vulnerabilities (HIGH and CRITICAL with an available
  fix) before a release is published; a finding stops the release.

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

[Unreleased]: https://github.com/ZeraOrg/opentidemark/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/ZeraOrg/opentidemark/releases/tag/v0.3.0
[0.2.1]: https://github.com/ZeraOrg/opentidemark/releases/tag/v0.2.1
[0.2.0]: https://github.com/ZeraOrg/opentidemark/releases/tag/v0.2.0
[0.1.0]: https://github.com/ZeraOrg/opentidemark/releases/tag/v0.1.0
