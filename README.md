<!-- SPDX-License-Identifier: Apache-2.0 -->
# Tidemark

**Open-source, self-hosted observability for logs, traces and metrics.** Tidemark offers a
familiar Kibana/Seq-style workflow, works with the logging tools you already use and stores
telemetry in ClickHouse. It is licensed under [Apache-2.0](LICENSE): every feature is included and
the number of users is not limited.

> 🚧 **Status: under active development.** Ingestion, search, dashboards, alerting and stream
> management work through the HTTP API; the web UI is being built.

## Features

- **Works with your existing clients.** Serilog and NLog (Seq protocol), Filebeat, Fluent Bit,
  Vector and Logstash (Elasticsearch `_bulk`), and **OpenTelemetry** (OTLP gRPC/HTTP) for logs,
  traces and metrics. Applications only change a URL and an API key.
- **Search that's easy and powerful.** Start with `level:error service:checkout*` and grow into
  `level >= warning | stats count() by service span 5m`.
- **Dashboards and alerts.** Pin any query to a dashboard; alert on any query with webhook, Slack,
  Teams or email notifications.
- **Index management.** Streams, retention rules, storage stats, field mappings, indexed fields and
  purge.
- **Secure by default.** Every ingestion connection has its own API key; strict web security
  throughout (see [SECURITY.md](SECURITY.md)).
- **Runs on Linux and Windows** as a single binary or with Docker Compose.

## Quick start (Docker, Linux or macOS)

```bash
git clone https://github.com/zeraOrg/tidemark
cd tidemark/deploy
./install.sh docker
```

On Windows use `.\install.ps1 -Mode Docker` in PowerShell. The installer creates `deploy/.env`
with random ClickHouse passwords and a bootstrap ingest key, then runs `docker compose up -d`.

Open <http://localhost:8080> and sign in with **admin / admin**. **Change the password right
away** (see [SECURITY.md](SECURITY.md)), then create a connection (one API key per application or
shipper) and point your clients at Tidemark.

Native installs (systemd or a Windows service) are described in `deploy/install.sh` and
`deploy/install.ps1`.

## Documentation

- [Query language (TQL)](docs/query-language.md)
- [Sending data (ingestion)](docs/ingestion.md)
- [Configuration](docs/configuration.md)
- [Security policy and hardening](SECURITY.md)

## Building from source

Go 1.26 and (for the web UI) Node 22 are required.

```bash
make build    # web UI (when present) + bin/tidemark
make check    # gofmt, go vet, golangci-lint, tests with -race, govulncheck, web checks
```

On Windows use `.\build.ps1 build` and `.\build.ps1 check`.

## Contributing

Contributions are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md): changes are accepted
under Apache-2.0 with a DCO sign-off (`git commit -s`).

## Licence

Copyright 2026 The Tidemark Authors. Licensed under the [Apache License, Version 2.0](LICENSE);
see [NOTICE](NOTICE), [LICENSING.md](LICENSING.md) and the generated
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
