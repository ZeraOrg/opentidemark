<!-- SPDX-License-Identifier: Apache-2.0 -->
# Tidemark

**Open-source, self-hosted observability for logs, traces and metrics.** Tidemark offers a
familiar Kibana/Seq-style workflow, works with the logging tools you already use and stores
telemetry in ClickHouse. It is licensed under [Apache-2.0](LICENSE): every feature is included and
the number of users is not limited.

> **Status: early 0.x release.** Ingestion, search, the web UI, dashboards, alerting, stream
> management, high availability and the importers are implemented and tested. Expect rough edges
> and breaking changes between minor versions before 1.0 (your data is always kept on upgrade).
> See the [changelog](CHANGELOG.md) and the [releases](https://github.com/ZeraOrg/tidemark/releases).

## Features

- **Works with your existing clients.** Serilog and NLog (Seq protocol), Filebeat, Fluent Bit,
  Vector and Logstash (Elasticsearch `_bulk`), and **OpenTelemetry** (OTLP gRPC/HTTP) for logs,
  traces and metrics. Applications only change a URL and an API key.
- **Search that's easy and powerful.** Start with `level:error service:checkout*` and grow into
  `level >= warning | stats count() by service span 5m`.
- **A web UI for everything.** Log explorer with live tail, trace waterfall, metrics browser,
  dashboards with a visual panel builder, alerts, streams, connections and settings.
- **Dashboards and alerts.** Add query results to dashboards; alert on any query with webhook,
  Slack, Teams or email notifications.
- **Index management.** Streams, retention rules, storage stats, field mappings, indexed fields and
  purge.
- **Secure by default.** Every ingestion connection has its own API key; strict web security
  throughout (see [SECURITY.md](SECURITY.md)).
- **Durable and optionally highly available.** A write-ahead log keeps acknowledged data, upgrades
  never lose data, and several nodes can run behind a load balancer.
- **Easy migration.** `tidemark import` moves data and saved searches from Seq and
  Elasticsearch/OpenSearch.
- **Runs on Linux and Windows** as a single binary or with Docker Compose.

## Quick start

Tidemark needs a ClickHouse server. Pick one of these ways to install it:

### Docker Compose with the published image (Linux, macOS, Windows)

The image `ghcr.io/zeraorg/tidemark` is built for linux/amd64 and linux/arm64. The Compose
setup in `deploy/` runs it together with ClickHouse:

```bash
git clone --depth 1 --branch v0.1.0 https://github.com/ZeraOrg/tidemark
cd tidemark/deploy
./install.sh docker --no-start      # creates .env with random passwords and an ingest key
echo "TM_IMAGE=ghcr.io/zeraorg/tidemark:0.1.0" >> .env
docker compose pull tidemark
docker compose up -d --no-build
```

On Windows use `.\install.ps1 -Mode Docker -NoStart` in PowerShell, add the `TM_IMAGE` line to
`deploy\.env`, then run the same two `docker compose` commands.

### Release archive (native install)

Download the archive for your platform from the
[releases page](https://github.com/ZeraOrg/tidemark/releases) and check it against
`checksums.txt` (`sha256sum -c checksums.txt --ignore-missing`). Each archive contains the
`tidemark` binary with the web UI built in, the licence files and the installer files in
`deploy/`. ClickHouse is set up separately with the users from `deploy/clickhouse/users.d`.

```bash
tar -xzf tidemark_0.1.0_linux_amd64.tar.gz
cd tidemark_0.1.0_linux_amd64
sudo ./deploy/install.sh native ./tidemark --start   # systemd service, config in /etc/tidemark
```

On Windows unpack `tidemark_0.1.0_windows_amd64.zip` and run
`.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe -Start` as Administrator (a Windows
service; run ClickHouse with Docker Desktop).

### From source

```bash
git clone https://github.com/ZeraOrg/tidemark
cd tidemark/deploy
./install.sh docker                 # builds the image from source and starts the stack
```

On Windows use `.\install.ps1 -Mode Docker`. See [Building from source](#building-from-source)
for the binaries.

### First steps

Open <http://localhost:8080> and sign in with **admin / admin**. **Change the password right
away** (see [SECURITY.md](SECURITY.md)), then create a connection (one API key per application or
shipper) and point your clients at Tidemark ([Sending data](docs/ingestion.md)).

## Documentation

- [Query language (TQL)](docs/query-language.md)
- [Sending data (ingestion)](docs/ingestion.md)
- [Configuration](docs/configuration.md)
- [Migrating from Seq](docs/migrate-from-seq.md) and
  [from Elasticsearch/Kibana](docs/migrate-from-elastic.md)
- [Security policy and hardening](SECURITY.md)
- [Changelog](CHANGELOG.md)
- [Specification](SPEC.md) (design, architecture and every detail of the behaviour)

## Building from source

Go 1.26 and Node 22 are required.

```bash
make build    # web UI + bin/tidemark
make check    # gofmt, go vet, golangci-lint, tests with -race, govulncheck, web checks
make dist     # release archives + checksums.txt in dist/, version from the VERSION file
```

On Windows use `.\build.ps1 build`, `.\build.ps1 check` and `.\build.ps1 dist`.

## Releases

Releases follow [Semantic Versioning](https://semver.org); before 1.0, minor versions may contain
breaking changes. Each [release](https://github.com/ZeraOrg/tidemark/releases) has archives for
linux/amd64, linux/arm64 and windows/amd64, `checksums.txt` and build provenance attestations
(`gh attestation verify <file> --repo ZeraOrg/tidemark`), and the matching image
`ghcr.io/zeraorg/tidemark:<version>`. What changed is in [CHANGELOG.md](CHANGELOG.md).

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md): changes are accepted
under Apache-2.0 with a DCO sign-off (`git commit -s`).

## Licence

Copyright 2026 The Tidemark Authors. Licensed under the [Apache License, Version 2.0](LICENSE);
see [NOTICE](NOTICE), [LICENSING.md](LICENSING.md) and the generated
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
