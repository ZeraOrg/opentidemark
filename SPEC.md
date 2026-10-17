<!-- SPDX-License-Identifier: Apache-2.0 -->
# OpenTidemark — Product & Engineering Specification

> **Status:** Living document — the source of truth for OpenTidemark, published with the code.
> **Rule:** It must always be possible to recreate the entire public repository from this file
> alone. Maintainer-only development tooling (outside the public tree) is specified separately;
> together with that specification the maintainers' development repository can be recreated as
> well. Any change to behaviour, APIs, schema, configuration, layout, UI or tooling **must**
> update this spec in the same commit. Version history is in §21.

**Spec version:** 0.31.6 · **Last updated:** 2026-10-17

---

## Table of contents

1. Purpose & goals
2. Non-goals
3. Glossary
4. Architecture
5. Technology stack (pinned)
6. Repository layout
7. Configuration
8. Data model (ClickHouse + metadata)
9. Ingestion (protocols & field mapping)
10. OpenTidemark Query Language (TQL)
11. HTTP API
12. Streams & index management
13. Dashboards
14. Alerting
15. Security
16. Web UI
17. Platform support & deployment
18. Testing & quality gates
19. Licensing & repositories
20. Milestones & status
21. Changelog

---

## 1. Purpose & goals

OpenTidemark is a **self-hosted, open-source observability platform** for **logs, traces (spans)
and metrics**, intended as a low-cost alternative to commercial log platforms such as
Elasticsearch/Kibana and Seq, with comparable day-to-day quality. It is licensed under the **Apache
License 2.0** for everyone, including commercial use; every feature is available and the number
of users is not limited (§19). The source code and this specification are published in the
public repository `ZeraOrg/opentidemark` (§19.3).

**Name.** The product is called **OpenTidemark** (it was called Tidemark before SPEC 0.15.0).
Every prose and display reference uses that name: documentation, the web UI, release titles,
image labels, CLI and log messages. Technical identifiers keep `tidemark` so that existing
installs, scripts and clients keep working: the binary `tidemark` / `tidemark.exe` and its
commands; the `TIDEMARK_*` variables; the headers `X-Tidemark-Key`, `X-Tidemark-CSRF` and
`X-Tidemark-Signature`; the cookies `tm_session` and `tm_csrf` and the browser storage keys
`tm.*`; the Windows service `Tidemark` (account `NT SERVICE\Tidemark`, event log source
`Tidemark`, `%ProgramFiles%\Tidemark`, `%ProgramData%\Tidemark`, firewall rules
`Tidemark TCP <port>`); the Linux user and group `tidemark`, `/etc/tidemark`,
`/var/lib/tidemark` and the systemd unit `tidemark.service`; the image
`ghcr.io/zeraorg/tidemark`; the release archives `tidemark_<version>_…`; the ClickHouse database
`tidemark` and users `tidemark_writer` / `tidemark_reader`; the Prometheus metrics `tidemark_*`;
the OTLP attribute `tidemark.stream`; the webhook user agent `Tidemark-Alerting/1`; the
Seq-compatible `GET /api` reply `{"Product":"Tidemark",…}` (§9.2); and file names such as
`deploy/clickhouse/config.d/tidemark.xml`. The copyright line names "The Tidemark Authors".

**Repository.** The public repository is `ZeraOrg/opentidemark`
(<https://github.com/ZeraOrg/opentidemark>; it was `ZeraOrg/tidemark` up to release 0.2.0, and
GitHub redirects the old repository, clone and release URLs to it). The Go module path follows
the repository: `github.com/ZeraOrg/opentidemark`. The documentation site moved with it to
<https://zeraorg.github.io/opentidemark/>. The container image keeps its name
`ghcr.io/zeraorg/tidemark`, so existing installs, Compose files and `TM_IMAGE` settings keep
pulling the same image. Release provenance of 0.2.0 and earlier names the former repository
(§17.6).

Goals:

- **G1 — Drop-in compatibility.** Existing applications and shippers keep working by changing only a
  URL and an API key: Seq clients (CLEF), shippers using the Elasticsearch `_bulk` ingestion API
  (§9.3), and OpenTelemetry (OTLP gRPC + HTTP) for logs, traces and metrics.
- **G2 — Excellent search.** A query language that is easy to start with (KQL-style
  `field:value`, free text) and extensive enough for analytics (pipe stages: `stats`, `sort`,
  `top` …), with autocomplete and precise error messages.
- **G3 — Easy dashboards.** A visual panel builder; any query can be pinned to a dashboard.
- **G4 — Index management.** Streams with retention rules, storage statistics, field mappings,
  indexed fields and purge tools.
- **G5 — Secure by default.** Every ingestion connection authenticates with its own API key;
  strict web security (§15).
- **G6 — Cross-platform.** Runs on **Linux and Windows** (§17).
- **G8 — Never lose data on upgrade.** Upgrades never lose telemetry, metadata or the ability to
  log in (§8.5); acknowledged telemetry survives crashes (§9.8).
- **G9 — Optional redundancy.** OpenTidemark can run as N stateless nodes (e.g. 3 servers) behind a
  load balancer (§17.5).
- **G10 — Easy migration.** Built-in importers move existing data and saved searches from Seq and
  Elasticsearch/OpenSearch; existing clients only change their URL (§9.9).
- **G7 — Fast and frugal.** Columnar storage (ClickHouse) with compression; single-node setup
  handles at least 10k events/s on 4 vCPU / 8 GB RAM.

## 2. Non-goals (v1)

- Multi-tenant SaaS hosting, billing.
- Editions, licence keys, feature gating or user limits (§19).
- Being a full Elasticsearch API implementation (only the ingestion surface in §9.3).
- Machine-learning anomaly detection.
- Native ClickHouse on Windows (ClickHouse runs via Docker/WSL2 on Windows — §17).
- Running a highly-available PostgreSQL for the operator (HA mode expects an existing HA
  PostgreSQL such as Patroni or a managed service — §17.5).

## 3. Glossary

| Term | Meaning |
|---|---|
| **Event** | One structured log record. |
| **Span** | One unit of work in a distributed trace (OTel span). |
| **Metric point** | One sample of a metric time series. |
| **Signal** | A saved, named TQL filter that can be toggled in the explorer (Seq term). |
| **Stream** | A named logical dataset (≈ Elasticsearch index / data stream). Every event, span and metric point belongs to exactly one stream. |
| **Connection** | A named ingestion source with its own API key (e.g. "checkout-api prod"). |
| **TQL** | OpenTidemark Query Language (§10). |
| **Principal** | The authenticated identity of a request: a user session or an API key. |

---

## 4. Architecture

```
 Apps / shippers                         ┌──────────────────────── tidemark (Go, single binary) ─────────────────────────┐
 ─────────────────                       │                                                                                │
 Serilog/NLog (Seq) ──HTTP :5341────────▶│ ingest/seq ─┐                                                                  │
 Filebeat/FluentBit/Vector               │             │   ┌──────────┐   ┌───────────────┐   ┌──────────────────┐        │
   (ES _bulk) ───────HTTP :9200────────▶│ ingest/elastic ├─▶│ normalize │──▶│ pipeline       │──▶│ store/clickhouse │──┐     │
 OTel SDKs/Collector ─gRPC :4317────────▶│ ingest/otlp ─┤   │ + auth    │   │ (batch queues) │   │ (writer)         │  │     │
                     HTTP :4318────────▶│             │   └──────────┘   └───────────────┘   └──────────────────┘  │     │
 Logstash, any JSON ─HTTP :8080────────▶│ ingest/native┘                                                          │     │
                                         │                                                                          ▼     │
 Browser ───────────HTTPS :8080────────▶│ web (embedded SPA) + api (query, meta, auth) ── tql ── store/clickhouse (reader)│
                                         │ alerting (evaluator + notifiers) · retention manager · stats collector         │
                                         │ meta (SQLite: users, sessions, keys, connections, signals, dashboards, alerts) │
                                         └────────────────────────────────────────────────────────────────────────────────┘
 Syslog / Fluent forward ──▶ OTel Collector (optional sidecar) ──OTLP/HTTP──▶ :4318
                                                                                  ClickHouse (events, spans, metric_points)
```

Principles:

- **One binary** (`tidemark`) serves UI, API and all ingestion protocols. Optional OTel Collector
  sidecar only for protocols OpenTidemark does not speak natively (syslog, Fluent forward).
- **ClickHouse** stores telemetry. **SQLite** (pure Go, file in data dir) stores metadata.
- **Layered, dependency-injected design** (§6.2): handlers → services → repositories/ports;
  all wiring happens in one composition root (`internal/app`).
- All telemetry flows through a single normalized model (§8.1) so every protocol gets identical
  search, retention and alerting behaviour.

---

## 5. Technology stack (pinned)

Versions are minimums; lockfiles (`go.sum`, `web/package-lock.json`) pin exact versions.

### Backend
| Concern | Choice | License |
|---|---|---|
| Language | Go **1.27** toolchain (`go 1.26.0` as the minimum language version, required by `ncruces/go-sqlite3`; `toolchain go1.27.2`: the newest patch release, free of the stdlib advisories reported by govulncheck/Trivy) | BSD |
| HTTP | `net/http` (Go 1.22+ pattern routing), no framework | — |
| Logging | `log/slog` (JSON handler) | — |
| ClickHouse client | `github.com/ClickHouse/clickhouse-go/v2` (native protocol) | Apache-2.0 |
| Metadata DB (single node) | `github.com/ncruces/go-sqlite3` (SQLite compiled to WASM and translated to Go by `github.com/ncruces/go-sqlite3-wasm` — pure Go, no CGO → Windows friendly) | MIT (go-sqlite3-wasm: MIT-0, §19.1) |
| Metadata DB (HA) | PostgreSQL 15+ via `github.com/jackc/pgx/v5` | MIT |
| OTLP | `go.opentelemetry.io/proto/otlp`, `google.golang.org/grpc`, `google.golang.org/protobuf` | Apache-2.0 |
| Password hashing | `golang.org/x/crypto/argon2` (argon2id) | BSD |
| Windows service | `golang.org/x/sys/windows/svc` | BSD |
| UUID | `github.com/google/uuid` (UUIDv7) | BSD |
| Compression | `github.com/klauspost/compress` (gzip, zstd, snappy) | Apache-2.0/BSD |
| Self metrics | `github.com/prometheus/client_golang` | Apache-2.0 |
| CGO | **Disabled** (`CGO_ENABLED=0`) for all builds | — |

### Storage
| Component | Version | License |
|---|---|---|
| ClickHouse server | **25.8 LTS** (`clickhouse/clickhouse-server:25.8`) | Apache-2.0 |
| OTel Collector (optional) | `otel/opentelemetry-collector-contrib` (pinned tag in compose) | Apache-2.0 |

### Frontend (`web/`)
| Concern | Choice |
|---|---|
| Language | TypeScript 6 (`strict: true`, `noUncheckedIndexedAccess: true`) |
| Framework | React 19, React Router 7 |
| Build | Vite 8 (`--mode mock`: MSW-backed demo build for e2e and screenshots only; it reports the release in `VERSION`) |
| Data fetching | TanStack Query 5 |
| Charts | uPlot (time series/histogram) |
| Query editor | CodeMirror 6 with a TQL language mode + autocomplete |
| Grid layout (dashboards) | `react-grid-layout` |
| Styling | CSS Modules + CSS custom properties (design tokens, §16.2). No CSS-in-JS. |
| Tests | Vitest + Testing Library; Playwright for e2e |
| Lint/format | ESLint (typescript-eslint strict), Prettier |

Node **22 LTS** for builds. The built SPA is embedded in the Go binary via `go:embed`.

---

## 6. Repository layout

### 6.1 Tree

```
.
├── SPEC.md                    # this file (source of truth)
├── mkdocs.yml                 # documentation site (§17.4): MkDocs + Material, nav over docs/ and docs/manual/
├── mkdocs.offline.yml         # offline documentation bundle (§17.6): inherits mkdocs.yml, Material's offline plugin, local worker shim
├── docs-site/                 # documentation site support (§17.4): requirements.in + requirements.txt (pinned, hashed), hooks.py (top-level pages, docs/manual and docs/images only; front matter after the SPDX comment; meta descriptions; links outside docs/; logo and shim copy; llms.txt + llms-full.txt), overrides/main.html (Open Graph tags, colours), overrides/home.html (landing page styles), offline/iframe-worker.js (vendored MIT shim of the offline bundle, §17.6)
├── README.md                  # open-source README: status, why OpenTidemark, about (screenshots from docs/images), features, quick start (release archive, GHCR image, source), docs, building, releases, contributing, licence
├── VERSION                    # version of the current/next release, e.g. 0.1.0 (§17.6)
├── CHANGELOG.md               # user-facing changes, Keep a Changelog format; release notes source (§17.6)
├── LICENSE                    # Apache License 2.0, verbatim (§19.1)
├── NOTICE                     # Apache-style NOTICE: copyright + third-party pointer (§19.1)
├── LICENSING.md               # short licence note: Apache-2.0, notices, contributions, trademarks
├── THIRD_PARTY_NOTICES.md     # generated dependency licences
├── CONTRIBUTING.md            # inbound = outbound Apache-2.0 + DCO sign-off, workflow (§19.2)
├── SECURITY.md                # private reporting via the public repo + hardening summary
├── Makefile                   # Linux/macOS task runner
├── build.ps1                  # Windows task runner (same targets as Makefile)
├── .gitattributes             # * text=auto eol=lf ; *.ps1 eol=crlf
├── .gitignore                 # local tool state, build output (bin/, dist/, site/, __pycache__/, …), `make dev` state, deploy/.env, *.key
├── .dockerignore              # keeps .git, local tool state, build output (incl. site/) and deploy/.env out of the image context
├── .editorconfig
├── .golangci.yml
├── go.mod / go.sum            # module github.com/ZeraOrg/opentidemark
├── cmd/tidemark/main.go       # CLI entry: serve | service install/uninstall/start/stop (Windows) | admin reset-password | backup | restore | migrate | doctor | import | version | healthcheck (§17.2)
├── internal/
│   ├── app/                   # composition root: builds every dependency, starts/stops servers
│   ├── config/                # env + flag parsing, validation, OS-specific defaults
│   ├── model/                 # Event, Span, MetricPoint, Level, Stream name rules (no deps)
│   ├── normalize/             # map[string]any → model.Event heuristics (§9.6), template rendering
│   ├── ingest/
│   │   ├── seq/               # CLEF + legacy Seq JSON (§9.2)
│   │   ├── elastic/           # Elasticsearch `_bulk` ingestion API (§9.3)
│   │   ├── otlp/              # OTLP gRPC + HTTP (protobuf & JSON) logs/traces/metrics (§9.4)
│   │   ├── native/            # JSON / NDJSON (§9.5)
│   │   └── body.go            # shared: size limits, decompression with bomb protection
│   ├── pipeline/              # bounded queues + batchers (implements ingest sinks)
│   ├── wal/                   # write-ahead log (§9.8)
│   ├── importer/              # Seq / Elasticsearch / Kibana / CLEF importers (§9.9)
│   ├── cluster/               # node identity, leases, cache invalidation (§17.5)
│   ├── store/
│   │   ├── ports.go           # interfaces consumed by services (EventWriter, Querier, …)
│   │   └── clickhouse/        # implementation: schema migrations, writer, reader, TTL, stats
│   ├── tql/                   # lexer, parser (AST), semantic checker, SQL compiler (§10)
│   ├── meta/                  # SQLite/PostgreSQL repositories, migrations/{sqlite,postgres}, backups, testdata/upgrade fixtures
│   ├── auth/                  # passwords, sessions, API keys, principals, RBAC, rate limiting
│   ├── api/                   # HTTP handlers for /api/v1 (thin: decode → service → encode)
│   ├── service/               # application services (search, dashboards, streams, users …)
│   ├── alerting/              # evaluator, state machine, notifiers (webhook, slack, email, teams)
│   ├── retention/             # builds & applies TTL rules (§12.3)
│   ├── httpx/                 # middleware: security headers, logging, recovery, limits, CSRF, IP filters
│   ├── selfmetrics/           # Prometheus metrics for OpenTidemark itself
│   ├── testutil/              # shared test fakes and the TIDEMARK_TEST_CLICKHOUSE_* reader
│   ├── repotest/              # repository-wide tests: SPDX headers, Apache LICENSE text, CI ClickHouse grants = users.d,
│   │                          #   public docs = this spec (§18)
│   └── web/                   # go:embed of web/dist + SPA fallback handler
├── web/                       # React SPA (§16)
│   ├── src/{app,api,components,features/<feature>,lib,mocks,tql,styles,test}
│   │                          #   main.tsx = composition root; mocks/ = MSW handlers + demo data (mock mode only)
│   ├── scripts/               # check-csp.mjs (§15.7), ci-annotate.mjs, copy-public.mjs
│   └── e2e/                   # Playwright tests (screenshots in e2e/screenshots, untracked)
├── deploy/
│   ├── docker-compose.yml     # tidemark + clickhouse (+ optional otel-collector profile)
│   ├── docker-compose.dev.yml # `make dev` override: ClickHouse on a loopback port
│   ├── .env.example           # every TIDEMARK_* variable (test-enforced) + compose-only TM_* settings
│   ├── clickhouse/            # config.d: tidemark.xml (quiet logs, no MySQL/PostgreSQL ports, crash reports off, `backups` disk for `tidemark backup`; default listen address), docker-listen.xml (Compose only: listen on the container network) / users.d (writer + read-only reader users)
│   ├── ha/                    # 3-server HA reference (§17.5): README, clickhouse/{config.d (cluster, macros, Keeper, backups disk, crash reports off), users.d (+ GRANT CLUSTER)}, nodes/{clickhouse,tidemark}-nodeN.env, lb/{haproxy.cfg,nginx.conf,Caddyfile}
│   ├── otel-collector.yaml
│   ├── install.sh             # Linux installer (systemd unit)
│   ├── install.ps1            # Windows installer (service)
│   ├── install/               # INSTALL-{linux,windows,docker}.md: the INSTALL.md of each release archive (§17.6)
│   └── tidemark.service       # systemd unit
├── Dockerfile                 # multi-stage: node build → go build → distroless nonroot
├── scripts/                   # with-clickhouse.sh, with-clickhouse-cluster.sh (integration tests, §18), notices.tpl + licence-exceptions.{sh,txt} (§19.1), ci-annotate.sh, release-notes.sh (§17.6)
├── examples/                  # (not in the tree yet, §20) client configs: serilog, nlog, python, node, go, java, filebeat, fluent-bit, vector, logstash, otel-collector
├── docs/                      # user docs, derived from this spec (§18), published as the documentation site (§17.4):
│   ├── index.md               #   landing page of the documentation site (template home.html): what OpenTidemark is, capabilities with screenshots, trust points, getting started, links into the docs
│   ├── overview.md            #   documentation overview: guides, operations manual, project links
│   ├── images/                #   README screenshots (§19.3): explorer, event-detail-columns, trace-waterfall, dashboard, metrics, alerts, scope-picker, teams (.png); social-preview.png (1280×640 social preview of the repository and og:image of the documentation site, §17.4)
│   ├── quickstart.md          #   running locally with Docker Compose: install, first event, everyday commands (§17.2)
│   ├── installation.md        #   full installation guide per platform: Compose, native Linux/Windows + ClickHouse, macOS dev, hardening, HA pointer, operations, troubleshooting (§17)
│   ├── query-language.md      #   complete TQL reference (§10) with examples; its example block = §10.1 verbatim, every `tql` block is tested (§18)
│   ├── ingestion.md           #   protocols, keys, field mapping (§9.1–§9.7)
│   ├── configuration.md       #   every variable the binary knows, with defaults (§7)
│   ├── migrate-from-seq.md    #   moving from Seq with `tidemark import` (§9.9)
│   ├── migrate-from-elastic.md #  moving from Elasticsearch/Kibana with `tidemark import` (§9.9)
│   └── manual/                #   operations manual (§17.4), one chapter per file: index (overview, architecture, glossary), deploy, security-hardening, users-access, ingestion, integrations, metrics, streams-retention, querying, dashboards, alerting, operations, api, agents
└── .github/                   # workflows/ci.yml (Go on Linux + Windows, integration, notices, web incl. e2e; calls release.yml), security.yml (§17.4), docs.yml (documentation site, §17.4), release.yml (§17.6); dependabot.yml; ISSUE_TEMPLATE/ (bug_report.yml form: version, installation, platform, steps, expected/actual, `tidemark doctor --json`, logs; config.yml links private vulnerability reporting and the docs)
```

Maintainer-only development tooling (publishing automation, offline dependency snapshots, design
review material) lives in the maintainers' development repository outside this public tree; no
public file depends on it (§19.3).

### 6.2 Design rules (SOLID + DI)

- **Composition root:** only `internal/app` constructs concrete types. Everything else receives
  dependencies through constructors (`NewX(deps …) *X`). No package-level mutable state,
  no `init()` side effects (except `go:embed`).
- **Interfaces at the consumer** (ISP/DIP): a package declares the small interface it needs;
  implementations live elsewhere. Core ports:

```go
// internal/store/ports.go
type EventWriter  interface { WriteEvents(ctx context.Context, ev []model.Event) error }
type SpanWriter   interface { WriteSpans(ctx context.Context, sp []model.Span) error }
type MetricWriter interface { WriteMetrics(ctx context.Context, mp []model.MetricPoint) error }
type Querier      interface {                                                // §11.3, read-only user
    Run(ctx context.Context, q *tql.Compiled) (Result, error)                  // events page (+NextCursor) | table | timeseries
    Histogram(ctx context.Context, f Filter, span time.Duration) (Histogram, error)
    Newest(ctx context.Context, f Filter, n uint64) (Sample, error)            // facet sampling: total + n-th newest time
    Fields(ctx context.Context, scope model.StreamScope, src model.Source, r TimeRange) ([]FieldInfo, error)
    Event(ctx context.Context, scope model.StreamScope, id uuid.UUID) (model.Event, error) // outside scope ⇒ ErrNotFound
    Context(ctx context.Context, scope model.StreamScope, ev model.Event, before, after int) (older, newer []model.Event, err error)
    Tail(ctx context.Context, f Filter, after, until time.Time, limit int) ([]TailEvent, uint64, error)
    Trace(ctx context.Context, scope model.StreamScope, traceID string) ([]model.Span, []model.Event, error) // nothing visible ⇒ ErrNotFound
    Traces(ctx context.Context, f Filter, limit int) ([]TraceSummary, error)    // summaries over spans in f.Streams only
    MetricNames(ctx context.Context, scope model.StreamScope, substr string, r TimeRange, limit int) ([]MetricName, error)
}
// from tql.CompileWhere with Options.Streams = Streams (the WHERE already holds the stream condition)
type Filter struct { Source model.Source; Where string; Args []any; Range TimeRange; Streams model.StreamScope }
// Every read with an unset scope (parameter or Filter.Streams) fails with store.ErrNoScope (§15.10).
type StreamAdmin interface {                     // writer connection; internal/store/clickhouse/admin*.go
    Stats(ctx context.Context) ([]StreamStats, error)
    ApplyRetention(ctx context.Context, rules []retention.Rule) error
    Purge(ctx context.Context, p PurgeRequest) (PurgeJob, error)      // DryRun ⇒ RowsEstimate only
    AddIndexedField(ctx context.Context, f IndexedField) error
    DropIndexedField(ctx context.Context, f IndexedField) error
    Mappings(ctx context.Context, scope model.StreamScope, stream string) ([]FieldMapping, error) // outside scope ⇒ none
    DeleteStream(ctx context.Context, name string) error
}
// PurgeRequest{Stream ("" = all), Source, Where+Args (optional tql.CompileWhere condition), From, To, DryRun}
// PurgeJob{RowsEstimate, DroppedPartitions []string, LightweightDeleted bool}
// FieldMapping{Source, Key, Type string|number|mixed, Hits, FirstSeen, LastSeen}

// singleton jobs (alerting, stats) — consumer-side in internal/service and internal/alerting
type Leader interface { IsLeader(name string) bool } // cluster.Elector in both modes (§17.5); a single node wins every lease on its first round

// internal/ingest (consumed by protocol handlers; implemented by pipeline)
type EventSink  interface { EnqueueEvents(ctx context.Context, ev []model.Event) error }   // ErrBackpressure when full
type SpanSink   interface { EnqueueSpans(ctx context.Context, sp []model.Span) error }
type MetricSink interface { EnqueueMetrics(ctx context.Context, mp []model.MetricPoint) error }
type KeyAuthenticator interface {
    AuthenticateKey(ctx context.Context, raw string, proto auth.Protocol, remote netip.Addr) (auth.Connection, error)
}

// internal/alerting
type Notifier interface { Kind() string; Notify(ctx context.Context, ch meta.Channel, n Notification) error }
type Clock    interface { Now() time.Time }
```

- **SRP:** protocol handlers only parse/map; the pipeline only batches; the store only talks to
  ClickHouse; HTTP handlers contain no business logic.
- **OCP:** new protocols/notifiers/panel types are added by implementing an interface and
  registering it in `internal/app`, without editing existing implementations.
- **Errors:** wrap with `%w`; sentinel errors per package (`ErrNotFound`, `ErrBackpressure`,
  `ErrUnauthorized`, `ErrForbidden`, `tql.Error{Pos, Msg}`); API maps them to §11.1 codes.
- **Context** is the first parameter of any I/O function; all I/O has timeouts.

---

## 7. Configuration

Configuration comes from environment variables, overridable by CLI flags (`--http-addr` etc.,
kebab-case of the suffix). Invalid configuration aborts startup with a clear message.

| Variable | Default | Description |
|---|---|---|
| `TIDEMARK_HTTP_ADDR` | `:8080` | UI + `/api/v1` + native ingest + Seq-compatible endpoints |
| `TIDEMARK_SEQ_ADDR` | `:5341` | Extra listener serving the Seq endpoints only (`""` disables) |
| `TIDEMARK_ES_ADDR` | `:9200` | Listener for the Elasticsearch `_bulk` ingestion API (`""` disables) |
| `TIDEMARK_OTLP_HTTP_ADDR` | `:4318` | OTLP/HTTP (`""` disables) |
| `TIDEMARK_OTLP_GRPC_ADDR` | `:4317` | OTLP/gRPC (`""` disables) |
| `TIDEMARK_TLS_CERT_FILE` / `TIDEMARK_TLS_KEY_FILE` | — | If both set, **all** listeners use TLS (min TLS 1.2) |
| `TIDEMARK_PUBLIC_URL` | `http://localhost:8080` | The address users open in the browser; used in alert links, connection snippets and the cookie `Secure` decision (https ⇒ Secure) |
| `TIDEMARK_DATA_DIR` | Linux `/var/lib/tidemark`; Windows `%ProgramData%\Tidemark`; container `/data` | SQLite DB + secrets |
| `TIDEMARK_CLICKHOUSE_ADDR` | `localhost:9000` | Native protocol address(es), comma separated |
| `TIDEMARK_CLICKHOUSE_DATABASE` | `tidemark` | |
| `TIDEMARK_CLICKHOUSE_WRITER_USER` / `_PASSWORD` | `tidemark_writer` / — | DDL + inserts |
| `TIDEMARK_CLICKHOUSE_READER_USER` / `_PASSWORD` | `tidemark_reader` / — | `readonly=2` user for queries |
| `TIDEMARK_CLICKHOUSE_TLS` | `false` | TLS to ClickHouse |
| `TIDEMARK_ADMIN_USERNAME` | `admin` | Seeded on first start only (when no users exist) |
| `TIDEMARK_ADMIN_PASSWORD` | `admin` | Seeded on first start only; see §15.2 |
| `TIDEMARK_BOOTSTRAP_INGEST_KEY` | — | Optional: create a connection "bootstrap" with this key on first start |
| `TIDEMARK_BATCH_MAX_ROWS` | `10000` | Max rows per insert |
| `TIDEMARK_BATCH_MAX_WAIT` | `1s` | Max wait before flushing |
| `TIDEMARK_QUEUE_MAX_ROWS` | `200000` | Per-signal queue capacity; full ⇒ backpressure (§9.1) |
| `TIDEMARK_MAX_BODY_BYTES` | `10485760` | Compressed request body limit |
| `TIDEMARK_MAX_DECOMPRESSED_BYTES` | `104857600` | Decompressed limit (bomb protection) |
| `TIDEMARK_QUERY_TIMEOUT` | `30s` | ClickHouse `max_execution_time` for user queries |
| `TIDEMARK_QUERY_MAX_ROWS` | `10000` | Max rows returned by a single query page/table |
| `TIDEMARK_SESSION_IDLE_TIMEOUT` | `12h` | |
| `TIDEMARK_SESSION_ABSOLUTE_TIMEOUT` | `168h` | |
| `TIDEMARK_TRUSTED_PROXIES` | — | CIDRs whose `X-Forwarded-For` is trusted |
| `TIDEMARK_ALLOW_PRIVATE_WEBHOOKS` | `false` | Allow alert webhooks to private/loopback IPs (§15.6) |
| `TIDEMARK_SMTP_HOST` / `_PORT` / `_USERNAME` / `_PASSWORD` / `_FROM` / `_STARTTLS` | — / 587 / — / — / — / `true` | Email notifications |
| `TIDEMARK_LOG_LEVEL` | `info` | `debug|info|warn|error` |
| `TIDEMARK_MAX_STREAMS` | `200` | Hard cap on number of streams |
| `TIDEMARK_NODE_ID` | hostname (`tidemark` if unknown) | Unique node name (HA, leases, WAL); `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`: starts with a letter or digit (`.`, `_` or `-` first is rejected) |
| `TIDEMARK_META_DRIVER` | `sqlite` | `sqlite` (single node) or `postgres` (required for HA, §17.5) |
| `TIDEMARK_META_DSN` | — | PostgreSQL DSN when driver is `postgres` (supports `_FILE`) |
| `TIDEMARK_CLICKHOUSE_CLUSTER` | — | ClickHouse cluster name ⇒ replicated schema `ON CLUSTER` (§17.5); `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$` |
| `TIDEMARK_CLICKHOUSE_INSERT_QUORUM` | `auto` | `auto` = majority of replicas in cluster mode, `1` otherwise |
| `TIDEMARK_WAL_ENABLED` | `true` | Write-ahead log for ingestion (§9.8) |
| `TIDEMARK_WAL_DIR` | `<data>/wal` | |
| `TIDEMARK_WAL_MAX_BYTES` | `10737418240` | WAL size cap shared by all signals (min 1 MiB); when full ⇒ backpressure |
| `TIDEMARK_WAL_SYNC` | `batch` | `batch` (group commit ≤ 10 ms, fsync before ack) or `none` (testing only) |
| `TIDEMARK_BACKUP_DIR` | `<data>/backups` | Automatic pre-migration backups (§8.5) |
| `TIDEMARK_DRAIN_SECONDS` | `10` | Graceful drain period on shutdown, 0–600 (§17.5) |
| `TIDEMARK_METRICS_ENABLED` / `TIDEMARK_METRICS_TOKEN` | `false` / — | Expose `/metrics` (§11.6) |
| `TIDEMARK_SECRET_KEY_FILE` | `<data>/secret.key` | AES key for secrets at rest (§15.5) |
| `TIDEMARK_ENV_FILE` | `<data>/tidemark.env` | Optional env file read at startup (§17.3) |

Secrets may also be supplied as `<VAR>_FILE` pointing to a file (Docker/K8s secrets).
Unknown `TIDEMARK_*` variables are ignored with a startup warning (e.g. `TIDEMARK_LICENSE` left
over from pre-release builds that had editions, §19).

---

## 8. Data model

### 8.1 Normalized Go model (`internal/model`)

```go
type Level uint8 // ordinal, used for range comparisons
const (Verbose Level = iota; Debug; Information; Warning; Error; Fatal)

type Source string // "logs" | "spans" | "metrics"

type Event struct {
    ID              uuid.UUID      // UUIDv7 generated at ingest
    Timestamp       time.Time      // event time (UTC, ns)
    IngestedAt      time.Time
    Stream          string
    Level           Level
    Service, Environment, Host string
    MessageTemplate string         // may be empty
    Message         string         // rendered
    Exception       string
    EventType       string         // 8 hex chars: FNV-1a 32 of MessageTemplate (or Message if no template)
    TraceID, SpanID string         // lowercase hex, "" if none
    Protocol        string         // seq | elastic | otlp | native
    ConnectionID    string
    Properties      map[string]any // typed, may be nested
}

type Span struct {
    TraceID, SpanID, ParentSpanID string
    Stream, Service, Name, Kind string        // kind: internal|server|client|producer|consumer
    Start time.Time; Duration time.Duration
    StatusCode string; StatusMessage string   // unset|ok|error
    Attributes map[string]any; ResourceAttributes map[string]any
    Events []SpanEvent; Links []SpanLink
    ConnectionID string
}

type MetricPoint struct {
    Timestamp time.Time; Stream, Service, Name, Description, Unit string
    Kind string        // gauge|sum|histogram|exp_histogram|summary
    Temporality string // cumulative|delta|"" ; IsMonotonic bool
    Value float64      // gauge/sum value; for histograms = sum/count (mean)
    Count uint64; Sum float64; Min, Max float64
    BucketBounds []float64; BucketCounts []uint64
    Attributes map[string]any
    ConnectionID string
}
```

**Level parsing** (case-insensitive, trimmed) — `internal/model.ParseLevel`:

| Level | Accepted inputs |
|---|---|
| Verbose | verbose, trace, trc, vrb, finest, finer, `0` |
| Debug | debug, dbg, fine, `1`, `7` (syslog debug) |
| Information | information, info, inf, notice, `2`, `5`(syslog notice), `6` (syslog info) |
| Warning | warning, warn, wrn, `3`(n/a), `4` (syslog warning) |
| Error | error, err, eror, `3` (syslog err) |
| Fatal | fatal, critical, crit, ftl, emergency, emerg, alert, panic, `0`–`2` (syslog emerg/alert/crit) |

Numeric strings follow **syslog severity** (0 emerg … 7 debug) only when the field is
`syslog.severity` or `severity` with integer type; otherwise unknown ⇒ `Information`.
OTLP `severityNumber`: 1–4 Verbose, 5–8 Debug, 9–12 Information, 13–16 Warning, 17–20 Error,
21–24 Fatal; 0 ⇒ fall back to `severityText`.

**Stream names:** `^[a-z0-9][a-z0-9._-]{0,63}$`. Inputs are lowercased; invalid characters become
`-`; empty ⇒ `default`.

**Stream scope** (`model.StreamScope`, §15.10) — the set of streams a caller may read:
`AllStreams()`, `NoStreams()` or `OnlyStreams(names…)` (sorted, de-duplicated); methods `IsSet`,
`All`, `Empty`, `Names`, `Allows(name)`, `Intersect(other)` (unset when either side is unset),
`Subset(other)`. The zero value is *unset* and every reader rejects it (fail closed).

### 8.2 ClickHouse schema (`internal/store/clickhouse/migrations/*.sql`)

Migrations are numbered SQL files applied in order at startup by the writer user, recorded in
`tidemark.schema_migrations(version UInt32, applied_at DateTime)`. `{db}` is substituted.

```sql
-- 0001_events.sql
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
  props            Map(LowCardinality(String), String) CODEC(ZSTD(3)),  -- flattened, stringified (§8.3)
  props_num        Map(LowCardinality(String), Float64) CODEC(ZSTD(3)), -- numeric/bool-as-0/1 values only
  props_json       String CODEC(ZSTD(3)),                               -- original typed JSON
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

-- 0002_spans.sql
CREATE TABLE IF NOT EXISTS {db}.spans (
  start_time       DateTime64(9, 'UTC') CODEC(Delta, ZSTD(1)),
  ingested_at      DateTime64(3, 'UTC') DEFAULT now64(3),
  trace_id         String,
  span_id          String,
  parent_span_id   String,
  stream           LowCardinality(String),
  service          LowCardinality(String),
  name             LowCardinality(String),
  kind             LowCardinality(String),
  duration_ns      UInt64 CODEC(T64, ZSTD(1)),
  status_code      LowCardinality(String),
  status_message   String,
  connection_id    LowCardinality(String),
  attrs            Map(LowCardinality(String), String) CODEC(ZSTD(3)),
  attrs_num        Map(LowCardinality(String), Float64) CODEC(ZSTD(3)),
  resource_json    String CODEC(ZSTD(3)),
  events_json      String CODEC(ZSTD(3)),
  links_json       String CODEC(ZSTD(3)),
  INDEX idx_trace trace_id TYPE bloom_filter(0.001) GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(start_time)
ORDER BY (stream, service, name, start_time);

-- 0003_metrics.sql
CREATE TABLE IF NOT EXISTS {db}.metric_points (
  timestamp     DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
  stream        LowCardinality(String),
  service       LowCardinality(String),
  name          LowCardinality(String),
  description   String,
  unit          LowCardinality(String),
  kind          LowCardinality(String),
  temporality   LowCardinality(String),
  is_monotonic  Bool,
  value         Float64 CODEC(Gorilla, ZSTD(1)),
  count         UInt64,
  sum           Float64,
  min           Float64,
  max           Float64,
  bucket_bounds Array(Float64),
  bucket_counts Array(UInt64),
  attrs         Map(LowCardinality(String), String),
  series_id     UInt64,  -- cityHash64 of name + sorted attrs + service (computed by writer)
  connection_id LowCardinality(String)
) ENGINE = MergeTree
PARTITION BY toYYYYMMDD(timestamp)
ORDER BY (stream, name, series_id, timestamp);

-- 0004_field_catalog.sql : property keys seen per stream/hour, for autocomplete & mappings
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
```

```sql
-- 0006_dedup_window.sql : idempotent inserts (§8.5 item 7)
ALTER TABLE {db}.events        MODIFY SETTING non_replicated_deduplication_window = 10000;
ALTER TABLE {db}.spans         MODIFY SETTING non_replicated_deduplication_window = 10000;
ALTER TABLE {db}.metric_points MODIFY SETTING non_replicated_deduplication_window = 10000;
```

**Cluster rendering** (`TIDEMARK_CLICKHOUSE_CLUSTER`, §17.5; `clickhouse.RenderForCluster`): every
`CREATE DATABASE|TABLE|MATERIALIZED VIEW … IF NOT EXISTS` gets `ON CLUSTER '<cluster>'` after the
object name (the cluster name must match `^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`); engines
`MergeTree`, `ReplacingMergeTree(v)`, `AggregatingMergeTree`, `SummingMergeTree` become
`Replicated…MergeTree('/clickhouse/tables/{shard}/{database}/<table>', '{replica}'[, v])`
(also for `schema_migrations`, so every node sees the applied versions). `ALTER` statements are
not rewritten: metadata changes of replicated tables replicate through Keeper. Any other
`CREATE` form fails rendering. `Migrator.Plan()` returns the rendered statements (used by
`tidemark migrate --dry-run`).

Indexed fields (§12.4) are added at runtime as
`ALTER TABLE events ADD COLUMN IF NOT EXISTS f_<slug> String MATERIALIZED props['<key>']` plus a
`bloom_filter` skip index `idx_f_<slug>`; the compiler uses the column when present.

### 8.3 Property flattening rules (writer)

- Nested objects are flattened with `.`: `{"Request":{"Path":"/a"}}` ⇒ `props['Request.Path']='/a'`.
  Keys already containing `.` are kept as-is.
- Strings ⇒ as-is. Numbers ⇒ shortest decimal text in `props` **and** float in `props_num`.
  Booleans ⇒ `'true'|'false'` in `props` and `1|0` in `props_num`.
- Arrays ⇒ compact JSON text in `props` (not flattened).
- `null` ⇒ key omitted from both maps (still present in `props_json`).
- Max 512 keys per event and 32 KiB per value (truncated with suffix `…[truncated]`, and a
  `_tidemark.truncated=true` property is added).
- `props_json` keeps the original typed JSON (after promotion) for the detail view.

### 8.4 Metadata (SQLite or PostgreSQL, `internal/meta/migrations/{sqlite,postgres}/*.sql`)

Tables (all have `id TEXT PRIMARY KEY` = UUIDv7 unless noted, `created_at`, `updated_at` RFC3339):

| Table | Columns |
|---|---|
| `users` | username UNIQUE, display_name, password_hash (argon2id PHC string), role (`admin`/`editor`/`viewer`), must_change_password BOOL, failed_logins INT, locked_until, last_login_at, disabled BOOL |
| `sessions` | `id` = SHA-256(token) hex, user_id, created_at, last_seen_at, expires_at, ip, user_agent |
| `connections` | name UNIQUE, description, key_prefix (first 8 chars), key_hash (SHA-256 hex) UNIQUE, protocols (JSON array subset of `seq,elastic,otlp,native`), default_stream, allowed_cidrs (JSON array; empty ⇒ any), allowed_streams (JSON array of stream names the connection may write to; empty ⇒ any; migration 0008), minimum_level, applied_properties (JSON object), rate_limit_eps (0 = unlimited), expires_at, revoked_at, last_used_at, last_used_ip, events_received INT |
| `api_tokens` | user-owned tokens for the query API: name, user_id, key_prefix, key_hash, scopes (`read`/`write`/`admin`), expires_at, revoked_at, last_used_at |
| `signals` | name, tql, color, owner_id, shared BOOL, source, disabled BOOL (imported filters that could not be translated, §9.9; never applied) |
| `dashboards` | name, description, owner_id, shared BOOL, doc (JSON, §13.1), version INT |
| `alert_rules` | name, enabled, doc (JSON, §14.1), state, state_since, last_eval_at, last_value, last_error, eval_state (JSON per-group state §14.3, migration 0006), streams (JSON array of stream names the rule reads, `NULL` = all streams, migration 0007), version |
| `alert_history` | rule_id, at, from_state, to_state, value, message, group_key (canonical JSON labels, migration 0006) |
| `channels` | name, kind (`webhook`/`slack`/`teams`/`email`), config (JSON; secrets encrypted §15.5) |
| `streams` | name UNIQUE, description, retention_days, level_rules (JSON `[{"levels":["Verbose","Debug"],"days":3}]`), spans_retention_days, metrics_retention_days, paused BOOL |
| `teams` | name UNIQUE (case-insensitive), description — groups of users granted streams (§15.10, migration 0007) |
| `team_members` | no `id`: PRIMARY KEY (team_id, user_id), both cascading on delete of the team or user; index on user_id |
| `team_streams` | no `id`: PRIMARY KEY (team_id, stream) cascading on team delete; `stream` is a stream **name** (valid per §8.1, may be granted before data exists); index on stream |
| `indexed_fields` | source, key, column_name |
| `purge_jobs` | stream, source, tql, from, to, status, rows_estimate, requested_by, ClickHouse mutation id |
| `audit_log` | at, actor (user/token id), ip, action, target, details JSON — append-only |
| `settings` | key PRIMARY KEY, value JSON (default retention, UI defaults) |
| `leases` | `name` PRIMARY KEY, holder (node id), expires_at, updated_at — leader election for singleton jobs (§17.5) |
| `import_jobs` | kind (`elastic`/`seq`/`clef-file`/`kibana`), name, config JSON (secrets encrypted §15.5), status (`queued`/`running`/`completed`/`failed`/`cancelled`), checkpoint JSON, rows_imported, rows_total, errors, last_error, warnings (JSON array), cancel_requested BOOL, requested_by, started_at, finished_at |
| `schema_info` | single row: schema_version, min_compatible_binary (semver), last_migrated_by, last_migrated_at |

Both dialects have identical migration version numbers and pass one shared repository contract
test suite (`internal/meta/contract_test.go`, run against SQLite always and PostgreSQL when
`TIDEMARK_TEST_POSTGRES_DSN` is set; every PostgreSQL test runs in its own schema). Repositories use a small dialect interface (placeholders,
upsert, now()) — no ORM.

Dialect details (normative):
- Repository SQL is written once with `?` placeholders in the common subset of both dialects
  (`INSERT … ON CONFLICT … DO UPDATE/NOTHING`, `CAST(? AS TEXT)` where a parameter type must be
  inferred); the PostgreSQL dialect rewrites `?` to `$n`. Times are fixed-width RFC 3339 UTC text
  and booleans 0/1 integers in **both** dialects, so comparisons and scans are identical.
- Case-insensitive uniqueness (usernames, connection names) uses `COLLATE NOCASE` in SQLite and a
  unique index on `lower(col)` in PostgreSQL; lookups use `lower(col) = lower(?)`.
- The `audit_log` append-only rule is a `RAISE(ABORT)` trigger in SQLite and a PL/pgSQL trigger
  function in PostgreSQL. Unique violations (`UNIQUE constraint failed` / SQLSTATE 23505) map to
  `meta.ErrConflict`.
- Drivers: SQLite `ncruces/go-sqlite3` (`meta.Open(path)`), PostgreSQL `pgx/v5/stdlib`
  (`meta.OpenPostgres(dsn)`); `meta.Tx` wraps transactions with the same placeholder rewriting.
- Bookkeeping tables created by the migrator itself: `schema_migrations(version INTEGER PRIMARY KEY,
  applied_at)` and `schema_info(id = 1, schema_version, min_compatible_binary, last_migrated_by,
  last_migrated_at)`.
- Migration files are `NNNN_name.sql`; each starts with a header line
  `-- min_compatible_binary: X.Y.Z` (required; a unit test checks both dialects list the same
  versions, names and headers). Versions increase but may have gaps (e.g. `0006_alert_groups`
  follows `0002_ha_import`); pending = every known version not yet in `schema_migrations`.

### 8.5 Durability & upgrade safety (mandatory rules)

1. **Forward-only, additive migrations ("expand/contract").** A release may only add tables,
   columns (nullable or defaulted) and indexes. Removing or renaming anything requires two releases:
   release N stops using it, release N+2 may drop it. ClickHouse migrations may only use
   `CREATE … IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, `ADD INDEX IF NOT EXISTS`,
   `MODIFY TTL`, `MODIFY SETTING`; a unit test scans `migrations/*.sql` and fails on `DROP`,
   `RENAME`, `MODIFY COLUMN`, `TRUNCATE`, `DELETE` (also `DETACH`, `EXCHANGE`, `REPLACE
   PARTITION`, `CLEAR COLUMN`) and on any statement that is not one of the allowed forms
   (`internal/store/clickhouse/migrations_lint_test.go`).
2. **Transactional metadata migrations.** Each SQLite/PostgreSQL migration runs in one
   transaction together with its `schema_migrations` row and the `schema_info` update; failure ⇒
   rollback and refusal to start with a clear message. Migrations are guarded by a lock
   (PostgreSQL session advisory lock `0x746d6d6967` on a dedicated connection; SQLite: `BEGIN
   IMMEDIATE`, i.e. the database file lock, with the version re-checked inside each transaction)
   so only one node migrates.
3. **Automatic pre-migration backup.** Before applying pending metadata migrations, SQLite is copied
   with `VACUUM INTO '<backup>/meta-<fromVersion>-<UTC timestamp>.db'` (last 10 kept); with
   PostgreSQL a logical JSON export of all metadata tables is written instead. Startup aborts if
   the backup fails. `<fromVersion>` is the highest applied version as 4 digits, the timestamp
   `YYYYMMDDThhmmssZ`; a fresh (empty) database is not backed up. The JSON export format is
   `{"format":"tidemark-meta-export/1","driver","exportedAt","tables":{"<table>":[{column:value}]}}`
   over every metadata table; `meta.ImportJSON` restores it in one transaction into a migrated,
   empty database (bookkeeping tables are skipped). Retention counts every `meta-*` file in the
   backup directory, newest modification time first.
4. **Compatibility window.** Each metadata migration declares `min_compatible_binary`. A node
   refuses to start only if the database requires a newer binary than itself (downgrade
   protection); older nodes keep working during a rolling upgrade because changes are additive.
   `schema_info.min_compatible_binary` is the highest value declared by any applied migration.
   Versions compare as semver (`v` prefix and pre-release suffix ignored); development builds
   (`dev` or any non-semver version) skip the check. A database that already holds migrations
   unknown to this binary is accepted when the compatibility check passes.
5. **Logins survive upgrades.** Password hashes are PHC strings carrying their own parameters;
   verification always uses the stored parameters; on successful login a hash with outdated
   parameters (any of m, t, p, salt or key length different from the current ones) is
   transparently re-hashed; the update is conditional on the old hash (`UPDATE … WHERE
   password_hash = <old>`) and failure does not fail the login. Session, connection-key and token hash formats are
   versioned and old versions remain verifiable for ≥ 2 releases.
6. **Upgrade regression tests.** `internal/meta/testdata/upgrade/meta-<version>.db` fixtures (one
   per release) are migrated in CI; the test asserts the admin can log in, connection keys
   authenticate, dashboards/signals/alerts load and no row counts decrease. Each fixture has a
   `meta-<version>.json` manifest (test-only admin password, connection key, API token, expected
   row counts). `meta-0.4.0.db` predates the public releases (the file name is a pre-release
   build number): migration 0001 only, with an admin whose hash uses outdated argon2 parameters
   (m=19456,t=2,p=1), so the test also proves the transparent re-hash. `meta-0.1.0.db` is the
   same data migrated by release 0.1.0 (migrations 0001, 0002, 0006, i.e. before teams); after
   the upgrade no user belongs to a team, so existing users keep seeing every stream (§15.10).
   `meta-0.2.1.db` is `meta-0.1.0.db` migrated to the 0.2.1 schema (up to 0008) plus a team with
   a member and a granted stream and a connection with `allowedStreams`; the newest fixture may
   already be at head. Fixtures are opened by a binary of the version in `VERSION`, and every
   applied migration must be preceded by a backup.
7. **Idempotent telemetry writes.** Every insert batch carries `insert_deduplication_token`
   (= batch UUID; with the WAL, the token derived from the batch UUIDs of its records, §9.8); tables set `non_replicated_deduplication_window = 10000` (replicated tables use
   the built-in replicated deduplication), so retries and WAL replays never duplicate rows.
   The token travels in the context (`store.WithDedupToken(ctx, token)`, read by the ClickHouse
   writer, which also sets `insert_deduplicate = 1`); ClickHouse suffixes it per inserted part, so
   a batch spanning several partitions is deduplicated as a whole. Verified by the integration
   test `TestIntegrationReplayIsDeduplicated` (single node) and `TestIntegrationCluster`
   (3 replicas: the same token sent through a second replica stores nothing).
8. **Operator tooling.** `tidemark backup --out <dir>` (metadata export + ClickHouse
   `BACKUP TABLE … TO Disk('backups', …)` when a backup disk is configured), `tidemark restore`,
   `tidemark migrate --status|--dry-run`, `tidemark doctor` (connectivity, schema versions,
   WAL health, clock skew, disk space), `tidemark admin reset-password`.
   Implemented by `app.Ops` (composition root); every command also accepts the configuration
   flags/variables of §7 and works with both metadata drivers.
   - `backup --out <dir> [--meta-only]` creates `<dir>/tidemark-backup-<YYYYMMDDThhmmssZ>/` (0700)
     with `meta.json` (the JSON export of item 3, always), for SQLite also the `VACUUM INTO` copy
     `meta-native-<stamp>.db`, and `manifest.json`: `{"format":"tidemark-backup/1","version",
     "createdAt","nodeId","meta":{"driver","schemaVersion","export":"meta.json","nativeFile"?},
     "clickhouse"?:{"database","disk":"backups","name":"tidemark-<stamp>","tables":[…]},
     "warnings"?:[…]}`. ClickHouse: `BACKUP TABLE db.t1, TABLE db.t2 … TO Disk('backups',
     'tidemark-<stamp>')` over every `*MergeTree` table except `schema_migrations`; without a
     `backups` disk a warning is recorded and only metadata is saved; an unreachable ClickHouse
     is an error unless `--meta-only`. The disk is declared in the ClickHouse server config
     (`storage_configuration` + `<backups><allowed_disk>backups</allowed_disk></backups>`).
   - `restore --from <backup dir> [--force] [--with-clickhouse]` (server stopped): the manifest
     is validated (format, file names without paths, database/table/backup names against
     allow-list patterns, disk `backups`) before anything changes. SQLite target: an existing
     database requires `--force` and is first copied to `TIDEMARK_BACKUP_DIR` as
     `meta-pre-restore-<stamp>.db`; a backup taken from SQLite is restored from its native copy
     (integrity-checked), otherwise (`postgres` source) a fresh database is migrated and
     `meta.json` imported. PostgreSQL target: migrated, then `meta.json` imported, which requires
     an empty database (`ErrNotEmpty`) — this is also the documented SQLite → PostgreSQL (HA)
     move. The restored metadata is migrated to the running binary's schema (downgrade protection
     applies). `--with-clickhouse` runs `RESTORE TABLE src.t AS <db>.t … FROM Disk('backups',
     name)`; target tables must be absent or empty.
   - `migrate` applies pending metadata migrations (with the pre-migration backup) and ClickHouse
     migrations (under the `clickhouse-migrations` lease in HA mode); `--status` lists every
     version as applied/pending (plus applied versions unknown to this binary) and the
     `schema_info` compatibility; `--dry-run` additionally prints the SQL of pending migrations
     (ClickHouse statements rendered for cluster mode) and changes nothing (no database or
     bookkeeping table is created).
   - `doctor [--json]` prints one line (or a JSON array of `{name,status,detail}`) per check with
     status `ok`/`warn`/`fail` and exits non-zero when any check fails: config summary; metadata
     reachable, pending migrations (warn), binary compatibility (fail); per ClickHouse address:
     connect + version, applied migrations (pending ⇒ warn), cluster defined (cluster mode);
     reader user can connect; clock skew against ClickHouse `now64(3)` and PostgreSQL
     `clock_timestamp()` (warn ≥ 2 s, fail ≥ 10 s = the lease safety margin); per-signal WAL
     segments, bytes not yet in ClickHouse, torn tail bytes (warn), pending batch, and total WAL
     size vs `TIDEMARK_WAL_MAX_BYTES` (warn ≥ 80 %) — read-only (`wal.Inspect`), safe while the
     server runs; disk space of the data directory (fail < 1 GiB free, warn < 10 %).
   - `admin reset-password` uses the configured metadata driver.

---

## 9. Ingestion

### 9.1 Common behaviour

- **Authentication (mandatory).** Every ingestion request must present a **connection key**
  (§15.3). Accepted locations, in order: `X-Tidemark-Key` header; `X-Seq-ApiKey` header;
  `Authorization: Bearer <key>`; `Authorization: ApiKey <base64(id:key) or key>`;
  `Authorization: Basic <base64(any:key)>` (password = key); query `?apiKey=` (Seq only, logged as
  a warning since it can leak into proxy logs). Missing/invalid ⇒ `401`; protocol not allowed,
  IP not in `allowed_cidrs`, expired or revoked ⇒ `403`. Failures are rate limited per IP
  (§15.4) and audited (sampled).
- The connection supplies: `default_stream`, `minimum_level` (events below are dropped and
  counted), `applied_properties` (merged into every event, overriding incoming keys),
  `rate_limit_eps` (token bucket; exceeded ⇒ `429` with `Retry-After`).
- **Body handling:** `Content-Encoding` gzip/deflate/zstd/snappy(OTLP only) supported; compressed
  limit `TIDEMARK_MAX_BODY_BYTES` and decompressed limit `TIDEMARK_MAX_DECOMPRESSED_BYTES`
  ⇒ `413`.
- **Backpressure:** the pipeline queue is bounded; when full, handlers return `503` with
  `Retry-After: 1` (OTLP gRPC: `UNAVAILABLE`) and nothing from that request is enqueued.
- **Durability:** a request is acknowledged after enqueue. Batches failing to insert are retried
  with exponential backoff (100 ms → 30 s, max 10 attempts) then written as NDJSON to
  `<data>/deadletter/<signal>-<ts>.ndjson` and counted in self-metrics.
- **Stream selection:** explicit per-protocol value (ES `_index`, OTLP resource attribute
  `tidemark.stream`, native/CLEF property `@stream`/`_stream`) if the connection allows overriding
  (`applied_properties` can pin it) else `default_stream`. When the connection has
  `allowed_streams`, an explicit stream outside that list is replaced by `default_stream` (the
  item is kept) and counted in `tidemark_ingest_rerouted_total{protocol,reason="stream_not_allowed"}`;
  the default and a pinned stream must be in the list (§11.5). A stream that is new when the
  stream limit is reached still falls back to `default` as before (§12.1), which may lie outside
  the list. Paused streams drop data (counted).
- **Timestamps:** missing ⇒ ingest time. Timestamps more than 7 days in the future are clamped to
  ingest time and `_tidemark.clock_skew=true` is added.

### 9.2 Seq compatibility (`internal/ingest/seq`)

Served on `TIDEMARK_HTTP_ADDR` and `TIDEMARK_SEQ_ADDR`.

| Method & path | Behaviour |
|---|---|
| `POST /api/events/raw` | CLEF (NDJSON) when `Content-Type` is `application/vnd.serilog.clef` or query `?clef`, else legacy JSON `{"Events":[…]}`. |
| `POST /ingest/clef` | CLEF always. |
| `GET /api` | `{"Product":"Tidemark","Version":"<ver>"}` (clients probe it). |

Response: `201 Created`, body `{"MinimumLevelAccepted": <null|"Warning"…>}` (connection's
`minimum_level` so Serilog's dynamic level control works).

**CLEF mapping:** `@t` timestamp (RFC3339/ISO-8601) · `@m` message · `@mt` template · `@l` level
(default Information) · `@x` exception · `@i` event type (hex or number → hex) · `@tr` trace id ·
`@sp` span id · `@r` renderings (stored as property `@r` only if present) · keys starting `@@` ⇒
property with one `@` stripped · other keys ⇒ properties. Service/environment/host promotion per
§9.6.

**Legacy JSON:** each element `{Timestamp, Level, MessageTemplate, RenderedMessage?, Exception?,
Properties{}}` maps to the same fields.

Invalid lines: valid lines are accepted; if **all** lines are invalid ⇒ `400` with
`{"Error":"line N: <reason>"}`.

**Message rendering** (`normalize.Render`): tokens `{Name}`, `{@Name}`, `{$Name}`, `{Name:fmt}`,
`{Name,align}`, positional `{0}`; `{{`/`}}` escapes. Strings render without quotes, objects/arrays
as compact JSON, missing properties render the token verbatim. Format/alignment are ignored.
If `@m` is present it wins; otherwise rendered from template.

### 9.3 Elasticsearch `_bulk` ingestion API (`internal/ingest/elastic`)

Served on `TIDEMARK_ES_ADDR` (root paths) and under `/es/` on `TIDEMARK_HTTP_ADDR`.

| Method & path | Behaviour |
|---|---|
| `GET /` , `HEAD /` | `{"name":"tidemark","cluster_name":"tidemark","version":{"number":"8.11.0"}}`. No product header, tagline or build details: the endpoint does not identify itself as Elasticsearch. `version.number` is the API level of the implemented `_bulk` format; shippers that choose their request format from the major version (Vector's `api_version: auto`, Filebeat's version check) treat it as 8.x. |
| `POST /_bulk`, `PUT /_bulk`, `POST /{index}/_bulk` | NDJSON bulk. Actions `index` and `create` accepted; `update`/`delete` ⇒ per-item `400` `action_not_supported`. URL `{index}` is the default for lines without `_index`. |
| `POST|PUT /{index}/_doc[/{id}]`, `POST /{index}/_create/{id}` | Single document ⇒ `201` `{"_index","_id","result":"created","_version":1}`. |
| `GET /_license` | `{"license":{"status":"active","type":"basic"}}` |
| `GET /_xpack` | `{"features":{}}` |
| `GET /_cluster/health` | `{"status":"green"}` |
| `GET|PUT|HEAD|DELETE` on `/_index_template/*`, `/_template/*`, `/_ilm/*`, `/_ingest/pipeline/*`, `/_data_stream/*`, `/_component_template/*`, `PUT|HEAD /{index}` | `200 {"acknowledged":true}` (GET returns `{}`) — lets Filebeat and Vector start up without setup changes. |
| anything else | `404` ES-shaped error `{"error":{"type":"unsupported_operation","reason":"…"},"status":404}` |

Bulk response is ES-shaped:
`{"took":<ms>,"errors":<bool>,"items":[{"index":{"_index":"…","_id":"<uuid>","_version":1,"result":"created","status":201}}]}`.

**Clients:** Filebeat 8.15.3, Fluent Bit 3.1.9 and Vector 0.41.1 are verified. Clients that only
accept a server identifying as Elasticsearch — the product check of Logstash's `elasticsearch`
output (it requires the `X-Elastic-Product: Elasticsearch` header when the reported version is
≥ 7.14) and of the official Elasticsearch language clients — refuse the endpoint and are **not
supported**. Logstash sends with its `http` output (`format => "json_batch"`, header
`X-Tidemark-Key`) to the native endpoint (§9.5), verified with Logstash 8.15.3; the connection
snippet `logstash` (§11.5) renders that output.

**Stream from index:** `_index` lowercased, data-stream prefixes `logs-`/`.ds-` kept, trailing date
suffixes `-YYYY.MM.DD`, `-YYYY.MM`, `-YYYYMMDD` stripped (`filebeat-8.11.0-2026.10.13` ⇒
`filebeat-8.11.0`). Document mapping via §9.6.

### 9.4 OpenTelemetry (`internal/ingest/otlp`)

- **gRPC** on `TIDEMARK_OTLP_GRPC_ADDR`: `LogsService/Export`, `TraceService/Export`,
  `MetricsService/Export`. Key in metadata (`x-tidemark-key`, or `authorization: Bearer`).
  gzip supported. Max message size = `TIDEMARK_MAX_DECOMPRESSED_BYTES`.
- **HTTP** on `TIDEMARK_OTLP_HTTP_ADDR` and under `/otlp/` on `TIDEMARK_HTTP_ADDR`:
  `POST /v1/logs`, `/v1/traces`, `/v1/metrics`; `application/x-protobuf` and `application/json`
  (OTLP JSON: hex trace ids, lowerCamelCase). Response per OTLP spec (empty
  `Export*ServiceResponse`; partial success when rows were rejected).

**Logs:** time = `timeUnixNano` || `observedTimeUnixNano`; level per §8.1; body string ⇒ message;
body map ⇒ merged into properties, message from body `message`/`msg`; attributes ⇒ properties;
resource attributes ⇒ properties (same key space; record attributes win);
template from attribute `{OriginalFormat}` (MEL) or `message_template.text`; if a template exists
and message is empty ⇒ rendered. Exception = `exception.type: exception.message\n
exception.stacktrace`. `service.name` ⇒ service, `deployment.environment.name` /
`deployment.environment` ⇒ environment, `host.name` ⇒ host. Scope name ⇒ property
`otel.scope.name`.

**Traces:** one `model.Span` per span; kind/status mapped to lowercase names; durations
`end-start`; events/links JSON. Service from resource `service.name`.

**Metrics:** gauge & sum data points ⇒ `value` (int or double); histogram ⇒ count/sum/min/max/
buckets, `value = sum/count`; exponential histogram ⇒ count/sum/min/max (buckets converted to
explicit bounds `base^i`); summary ⇒ count/sum and `value = sum/count`, quantiles stored as attrs
`quantile.<q>`. Temporality and monotonicity preserved.

### 9.5 Native JSON (`internal/ingest/native`)

`POST /api/v1/ingest/logs` on `TIDEMARK_HTTP_ADDR`: body is a JSON object, a JSON array of
objects, or NDJSON. Each object mapped via §9.6 (CLEF `@` keys honoured).
Response `202 {"accepted":N,"rejected":M,"errors":[{"index":i,"reason":"…"}]}` (max 20 errors).
This is the supported Logstash path (§9.3): the `http` output's `json_batch` format posts one
array per batch, and its retries of `429`/`503` match §9.1.

### 9.6 Field promotion heuristics (`internal/normalize`)

Applied to flat/nested maps (ES, native, OTLP body maps). First match wins; dotted paths also
match nested objects. Promoted keys are **kept** in properties (so `Application = 'x'` still works)
except `@t/@m/@mt/@l/@x/@i/@tr/@sp`, `@timestamp`, `message`.

| Target | Keys (in order) |
|---|---|
| timestamp | `@t`, `@timestamp`, `timestamp`, `time`, `ts`, `Timestamp`, `date` (RFC3339, ISO-8601 without zone ⇒ UTC, epoch s/ms/µs/ns by magnitude) |
| message | `@m`, `message`, `msg`, `Message`, `RenderedMessage`, `log` (string), `text` |
| template | `@mt`, `MessageTemplate`, `message_template`, `{OriginalFormat}` |
| level | `@l`, `level`, `log.level`, `Level`, `severity`, `severity_text`, `levelname`, `log_level`, `loglevel`, `syslog.severity` |
| exception | `@x`, `exception`, `Exception`, `error.stack_trace`, `stack_trace`, `stacktrace`, `exc_info`, `error.message` (+ `error.type`) |
| service | `service.name`, `service`, `Service`, `ServiceName`, `Application`, `application`, `app`, `app.name`, `kubernetes.labels.app`, `kubernetes.container.name`, `container.name`, `logger_name` *(last resort: stream name)* |
| environment | `deployment.environment.name`, `deployment.environment`, `environment`, `Environment`, `env` |
| host | `host.name`, `host.hostname`, `hostname`, `host`(string), `MachineName`, `agent.hostname` |
| trace id | `@tr`, `trace.id`, `trace_id`, `traceId`, `TraceId` (normalized to 32 lowercase hex if possible) |
| span id | `@sp`, `span.id`, `span_id`, `spanId`, `SpanId` |

If no message is found, message = compact JSON of the original object (max 2 KiB).

### 9.7 Other protocols

- **Syslog (RFC 5424/3164, UDP/TCP 5514)** and **Fluent forward (24224)**: via optional OTel
  Collector profile in `deploy/docker-compose.yml` exporting OTLP/HTTP to OpenTidemark with a
  connection key. The shipped Collector configuration does not remap fields, so a syslog line
  arrives as the Collector's `syslog` receiver produces it (§9.4 mapping): `message` is the whole
  raw line, the syslog severity becomes the level, and the parsed parts are log attributes and
  therefore properties — `hostname`, `appname`, `proc_id`, `msg_id`, `message` (the message
  part), `facility`, `priority`, `version` (and `structured_data` when present). The built-in
  `host` stays empty and `service` falls back to the stream name; search with
  `hostname:web-01 appname:nginx`. Verified with `otel/opentelemetry-collector-contrib` 0.120.0.
- **Planned (M4):** Loki push API, Prometheus remote-write, GELF.

---

### 9.8 Write-ahead log (`internal/wal`)

- Enabled by default. Each ingestion request's normalized rows are appended to a local segmented
  WAL **before** the request is acknowledged (group commit: records from concurrent requests are
  fsynced together, at most every 10 ms).
- Segment files `<wal>/<signal>/<seq>.wal` (`<seq>` = 16 lower-case hex digits, segments roll at
  64 MiB; `<signal>` = `events`/`spans`/`metrics`), record = `len uint32 LE | crc32c uint32 LE |
  payload` (Castagnoli, over the payload; `len` 1 … 1 GiB). Payload v1 = `0x01 | batch UUIDv7
  (16 bytes) | JSON array of rows` (one record per acknowledged ingestion call; non-finite floats
  travel as the strings `"NaN"`, `"+Inf"`, `"-Inf"`). Torn/corrupt tail records are detected by
  CRC/length and truncated on startup (reported in the log). A corrupt record in the middle of a
  segment (bit rot) makes the reader skip to the next segment; the loss is counted as dropped
  (`wal_corrupt`), undecodable payloads as `wal_decode`.
- Group commit: a dedicated syncer fsyncs the active segment as soon as an appender is waiting and
  every fsync covers all records written before it; `Append` returns after that fsync. New
  segments also fsync their directory (Linux/macOS; NTFS journals directory entries). Files are
  never renamed and are closed before deletion, so the same code works on Windows.
- **Failed writes are undone.** When a record write fails part-way (e.g. the disk is full), the
  active segment is truncated back to the end of the last complete record before the error is
  returned (the request gets backpressure, nothing is acknowledged). If that truncation fails too,
  every later `Append` retries it first and fails until it succeeds, so no record is ever written
  behind a partial one — a later acknowledged record can never be hidden from the reader or cut
  off by the startup truncation.
- **Abandoned waits.** The request context only bounds the wait for the fsync: when the client
  goes away (cancelled request, gRPC deadline) while its record waits, the record is already
  written, the next group commit makes it durable and the pipeline stores it like any other. Such
  rows count as enqueued (`tidemark_pipeline_enqueued_total`, queue depth), never as dropped; the
  request ends with the context error (no acknowledgement — a client that retries may store the
  rows twice, as with any at-least-once delivery). `tidemark_pipeline_dropped_total{reason=
  "backpressure"}` counts only rows that were refused and not written: WAL size cap reached, a
  failed write, or a full in-memory queue (§9.1).
- The pipeline reads from the WAL in order, groups records into insert batches
  (`TIDEMARK_BATCH_MAX_ROWS` / `_MAX_WAIT`) and inserts them with a deterministic deduplication
  token: the UUID formed from the first 16 bytes of SHA-256 over the batch UUIDs of the batch's
  records. **Before** the insert, the checkpoint records the batch as pending
  (`{start, end, token}`); after it, the checkpoint advances to `end` and fully committed segments
  are deleted. On restart, a pending batch is re-sent with exactly its records (never records after
  `end`) and its original token, so ClickHouse deduplicates it.
- Checkpoint: two slot files `checkpoint.0`/`checkpoint.1` written alternately (`len | crc32c |
  JSON {seq, committed:{seg,off}, pending?}`, fsynced); the valid slot with the highest `seq` wins,
  so a torn checkpoint write never loses the previous one (no rename needed).
- On startup, uncommitted records are replayed. If ClickHouse is unavailable, data accumulates in
  the WAL until `TIDEMARK_WAL_MAX_BYTES`, then ingestion returns backpressure (§9.1).
- Rows ClickHouse permanently rejects go to the dead-letter files (§9.1); transient failures retry
  indefinitely with capped backoff while the WAL holds the data. Permanent = a row the client
  cannot encode (`clickhouse.ErrRowRejected`) or a server exception whose code is not transient;
  transient codes are 16, 47, 60, 81 (schema not yet migrated on this replica), 159, 164, 202,
  203, 209, 210, 236, 241, 242, 243 (not enough disk space: the data waits in the WAL until the
  operator frees space), 252, 285, 286, 319, 394, 425, 497, 516 and 999 (Keeper); network
  errors and timeouts are always transient (`clickhouse.IsPermanent`).
- With the WAL disabled the in-memory queue of §9.1 is used (acknowledged rows may be lost on crash).

### 9.9 Importing from Seq and Elasticsearch/OpenSearch (`internal/importer`)

Goal: move an existing installation to OpenTidemark with minimal effort.

**Client cut-over.** OpenTidemark listens on Seq's port 5341 and Elasticsearch's port 9200 with
compatible ingestion APIs (§9.2, §9.3), so applications only change the host name. Admins may
create a connection with an **admin-supplied key value** (≥ 20 characters, ≥ 80 bits estimated
entropy, unique) so existing Seq API keys / ES API-key secrets keep working unchanged.
Rules (`auth.ValidateSuppliedKey`): 20–256 printable ASCII characters `!`…`~` (no spaces); the
UI prefix of a supplied key is its first **4** characters, and the entropy estimate covers the
characters after them: each character adds `log2(pool)` (pool = sum of the classes present:
lower 26, upper 26, digits 10, symbols 32) unless it repeats the previous character, continues
a ±1 sequence (`abc`, `321`) or completes a character pair already seen (so `aaaa…`,
`123456…` and `aB3$aB3$…` are rejected). Uniqueness is checked against current keys and keys
still in a rotation grace period (`409 conflict`). Supplied keys are accepted on `POST
/connections` and `POST /connections/{id}/rotate` (field `key`); `PATCH` with `key` ⇒ 400.
The audit entry records `suppliedKey: true`, never the key.

**Importers** run as server-side jobs (Settings → Import, or CLI `tidemark import …`); progress,
errors and a resumable checkpoint are stored in `import_jobs`; one node runs each job (lease);
pages are inserted with deterministic deduplication tokens so a resumed job never duplicates data.
Original timestamps are kept; the UI warns when imported data is older than the target stream's
retention and offers to extend it first. Target throughput ≥ 20k events/s per job.

| Source | How | Mapping |
|---|---|---|
| **Elasticsearch / OpenSearch** (7.10+, 8.x, OpenSearch 1–2) | URL, auth (basic / API key / bearer), optional custom CA, index pattern, time field (default `@timestamp`), from/to, optional ES query DSL JSON. Uses point-in-time + `search_after` (scroll fallback when PIT unavailable), page size 5,000. | Documents ⇒ §9.6 heuristics (identical to live `_bulk`); index ⇒ stream via §9.3 rules or a fixed target stream. |
| **Kibana saved objects** (`.ndjson` export upload) | Saved searches ⇒ signals (KQL is a subset of TQL filters; unsupported syntax ⇒ signal imported disabled with a warning); index patterns/data views ⇒ streams. Dashboards are listed in the report as not converted. | — |
| **Seq (live API)** | URL + API key (needs read permission); uses Seq's HTTP events API (`GET /api/events?count&render=true&fromDateUtc&toDateUtc&filter&afterId`, header `X-Seq-ApiKey`); Seq returns events newest first, so the job pages newest → oldest with `afterId` (exclusive) inside the frozen range. | CLEF semantics (§9.2): template, properties, level, exception, trace/span ids. |
| **Seq signals** | Imported from the same Seq connection; Seq filter expressions are parsed as TQL (Seq-style operators are supported); untranslatable ones imported disabled with a warning. | — |
| **CLEF / NDJSON files** (e.g. from `seqcli search --json` or archives; `.gz` allowed) | CLI `tidemark import clef <files…> --stream x`, or UI upload (streamed, ≤ 1 GiB per file). | §9.2 / §9.6 |

Importer HTTP clients use the SSRF-safe client **with private addresses allowed** (sources are
usually internal), redirects disabled, TLS verification always on.

CLI: `tidemark import clef <files…> [--stream x]`, `tidemark import elastic --url U --index I
[--auth-type none|basic|apiKey|bearer] [--username u] [--password-env VAR] [--api-key-env VAR]
[--token-env VAR] [--ca-file f] [--time-field f] [--from t] [--to t] [--query-file f]
[--stream x] [--page-size n]`, `tidemark import seq --url U [--api-key-env VAR] [--from t]
[--to t] [--filter f] [--signals] [--no-events] [--stream x] [--page-size n]`, `tidemark import
kibana <export.ndjson>`, each with `[--name n]` and (not kibana) `[--detach]`; `tidemark import
list`, `tidemark import status <id>` (job as JSON, the API shape), `tidemark import cancel <id>`.
Flags not valid for the kind ⇒ error; other `--flags` are configuration flags (§7); times are
RFC 3339; the CA bundle and query files are ≤ 1 MiB. Secrets are read only from the environment
variables named by `*-env` flags (empty/unset ⇒ error), never from flag values. The CLI opens
the configured metadata store, creates the job as actor `system:cli` (clef-file jobs are pinned
to this node's `TIDEMARK_NODE_ID`, so a running server can take over a detached one) and, unless
`--detach`, competes for the job's lease under the holder id `<node id>/cli-<pid>`, runs the
job when it gets the lease (otherwise follows the node that runs it) and prints
`HH:MM:SS status rows N/total errors E` every 5 s and when the local run ends. SIGINT/SIGTERM
leave the job resumable. Exit status 1 when the job ends `failed` or `cancelled`. Operator
guides: `docs/migrate-from-seq.md`, `docs/migrate-from-elastic.md`.

**Jobs (normative).** Kinds `elastic`, `seq`, `clef-file`, `kibana`; statuses `queued` →
`running` → `completed` | `failed` | `cancelled`. Job names are trimmed, ≤ 128 characters
(default `Import <kind>`). Configurations are decoded strictly (unknown fields ⇒ 400, field
`config`; invalid values ⇒ field `config.<name>`); secrets (`auth.password`, `auth.apiKey`, `auth.token`, Seq `apiKey`) are sealed
with the secret box (§15.5) and never returned; `config` in responses is the stored config
without secrets and without server-side file paths. When `to` is absent it is frozen to the
creation time, so a resumed job re-reads identical pages.

| Config | Fields (defaults, limits) |
|---|---|
| `elastic` | `url` (≤ 2048, http/https), `auth {type: none\|basic\|apiKey\|bearer, username, password, apiKey, token}` (only the fields of the chosen type are kept), `caCert?` (PEM ≤ 64 KiB, added to the system roots), `index` (`^[A-Za-z0-9._*,:+-]{1,255}$`, not starting with `-`/`_`), `timeField` (`@timestamp`), `from?`, `to?` (`from < to`), `query?` (non-empty DSL object ≤ 64 KiB, ANDed as a `bool.filter`), `stream?`, `pageSize` (5,000; 1–10,000) |
| `seq` | `url`, `apiKey?`, `from?`, `to?`, `filter?` (≤ 8192), `stream?`, `pageSize` (1,000; 1–10,000), `importEvents` (true), `importSignals` (false); at least one of the two |
| `clef-file` | `stream?`; files are uploaded (1–16 per job, ≤ 1 GiB each, stored as `<data>/imports/<job id>/NN.ndjson`, mode 0600) or given on the CLI; the job is pinned to the node holding the files (`config.node`) |
| `kibana` | `{}`; one `.ndjson` export ≤ 64 MiB, processed synchronously when uploaded (the response is the finished job) |

Execution: every node runs an import runner that polls `import_jobs` every 5 s (and at once
after a local create) for `queued`/`running` jobs it may run (kibana never; clef-file only on
its node), registers the lease `import:<job id>` (§17.5) and runs at most **2** jobs at a time;
a node that does not get the lease within two renewal rounds (+1 s) frees the slot. A job whose
node dies stays `running` and is resumed from its checkpoint by the next lease holder; the lease
is released when the job ends. Each loop iteration: check `cancel_requested` (⇒ `cancelled`),
read the next page (transient source errors retried 6× with exponential backoff 1 s … 30 s;
HTTP 4xx except 408/429, oversized (> 512 MiB) and malformed responses are permanent ⇒ `failed`), write it, then
store checkpoint and counters in one update. Page token =
`"import-" + UUID(first 16 bytes of SHA-256("tidemark-import\0" + jobId + "\0" + checkpointBefore + "\0" + pageKey))`,
used as `insert_deduplication_token` (§8.5 item 7); the page key identifies the page content
(ES: SHA-256 of `index\0id\0` per hit; Seq: `<first id>..<last id>`; CLEF: the checkpoint after the page). Event ids are deterministic: UUIDv7 layout with the event timestamp (or now) in the
millisecond bits and the rest from SHA-256(`token\0i`). Events then pass `normalize.Finalize`
and the stream gate (paused streams drop, unknown streams are created); storage writes retry
10× unless the error is permanent. Undecodable records count as `errors` (job continues,
`lastError` kept); warnings are capped at 100 (+ "further warnings omitted"). Audit actions:
`import.create`, `import.cancel`, `import.done`, `import.failed`.

Sources: **Elasticsearch** opens a point in time (`POST /<index>/_pit?keep_alive=10m`) and
sorts by `timeField` asc + `_shard_doc` with `search_after`; if PIT is unavailable it uses
`/<index>/_search?scroll=10m` + `/_search/scroll`. The checkpoint stores the last time sort
value and how many documents carried it, so an expired context (404, reopened once per request) or a resumed scroll is
reopened with `timeField ≥ lastSort` skipping the documents already imported. `hits.total`
(`{value}` or a number) gives `rowsTotal`; the PIT/scroll is deleted when the job ends.
Documents map exactly like `_bulk` (`_id` kept as property). **Seq** imports signals first when
asked (`GET /api/signals?shared=true`; each filter of a signal is translated, combined with
`and`; a signal of the same owner and name is not created twice), then events (CLEF semantics of
the rendered event: message template tokens, properties, level, exception, trace/span ids).
**CLEF** reads files line by line (gzip detected by magic bytes, lines ≤ 16 MiB, 5,000 lines per
page; checkpoint `{file, offset, line}`). Filter translation: Seq built-ins map to TQL built-ins
(`@MessageTemplate` ⇒ `@template`, `@EventType` ⇒ `@event_type`, `@TraceId`/`@SpanId` ⇒
`@trace_id`/`@span_id`), `''` inside strings ⇒ `\'`, `X is not null` ⇒ `X:*`, `X is null` ⇒
`not X:*`; KQL is used as is with Lucene `_exists_:f` ⇒ `f:*`. A translation is accepted only if
it parses as a logs filter without stages and not as a free-text fallback; otherwise the signal
is imported **disabled** with its original text and a warning. Kibana objects of type
`index-pattern` (also data views) become one stream per comma-separated pattern (`*` and
trailing `-._` removed, then §9.3 naming; no fixed prefix ⇒ warning); `search` objects become
signals (query DSL objects or active filter pills ⇒ disabled with a warning); every other type
(dashboards, visualizations, …) is listed as a "not converted" warning; at most 10,000 objects.
The finished job reports `rowsImported` = signals + streams and `errors` = disabled signals.

## 10. OpenTidemark Query Language (TQL)

Design goals: **KQL-easy** for filters (`level:error service:checkout*`), familiar to Seq users
(`Level = 'Error'`), and **extensive** through pipe stages for analytics. The same TQL text drives
the explorer, dashboards and alerts.

### 10.1 Overview by example

The public reference `docs/query-language.md` repeats this block verbatim (§18).

```
timeout                                   # free text in message + exception (case-insensitive)
"connection refused"                      # phrase
level:error                               # field:value (case-insensitive for strings)
level >= warning                          # level ordinal comparison
service:checkout*  and not env:dev        # wildcard, boolean logic (implicit AND between terms)
status_code:(500 or 502 or 503)           # value groups
status_code >= 500 and elapsed > 250      # numeric comparison (uses numeric property values)
user.email:*                              # field exists
not exception:*                           # field missing / empty
path:/\/api\/v[12]\/orders/               # regex
Service = 'payments-svc' and StatusCode != 200   # Seq-style operators accepted too
@trace_id:4bf92f3577b34da6a3ce929d0e0e4736
level:error | stats count() by service | sort count desc
level:error | top 10 exception_type
* | stats count() by level span 5m       # time series
elapsed:* | stats p95(elapsed), avg(elapsed) by service, route span 1m
source spans | service:checkout-api and status:error | stats count() by name
source metrics | name:http.server.request.duration | stats avg(value) by service span 1m
```

### 10.2 Lexical rules

- Whitespace separates tokens. Keywords are case-insensitive: `and or not in like ci source stats
  by span auto sort asc desc limit fields where top as true false null`. Also `&&`, `||`, `!`.
- **Identifiers (fields):** `[A-Za-z_@][A-Za-z0-9_.\-@]*` (e.g. `Request.Path`, `@timestamp`,
  `http.status_code`) or backtick-quoted ``` `weird key` ```.
- **Strings:** `'…'` or `"…"`, backslash escapes `\\ \' \" \n \t \*`.
- **Numbers:** `-?\d+(\.\d+)?([eE][+-]?\d+)?`, optional duration suffix `ns|us|ms|s|m|h|d` in
  comparisons against `duration`/`@duration` (spans) only.
- **Bare values** (right of `:`): any run of non-space, non-`()` characters; `*` is a wildcard.
- **Regex:** `/…/` with `\/` escape (RE2 syntax, ClickHouse `match`).
- **Time literals** in `@timestamp` comparisons: `now` or `now` ± a whole number with one unit
  `s|m|h|d|w` and no spaces (`now-15m`, `now+1h`, `now-2w`; case-insensitive; at most 100 years),
  or a string in one of the layouts RFC 3339 (fractional seconds and offset allowed),
  `YYYY-MM-DDThh:mm:ss`, `YYYY-MM-DDThh:mm`, `YYYY-MM-DD hh:mm:ss`, `YYYY-MM-DD` (no zone ⇒ UTC).
  `now` is the end of the request range. After an operator a time with `:` must be quoted.
- Details (normative):
  - Outside value position a bare word is any run of characters other than whitespace and
    `()[],|:=<>'"` and backtick; `!` ends a word only before `=` or at its start, `&` only in `&&`.
    A word matching the identifier pattern is a field/keyword, a word matching the number pattern
    is a number, anything else (e.g. `4bf9…`, `/api`) is free text or a literal.
  - Right of `:` whitespace is skipped, then the value is one of: `(` group, a range operator
    `< <= > >=` followed by a value, a quoted string, a regex, or a bare value. Bare values also
    stop at `|` (quote values containing `|`). In bare values `\` escapes only `* ? ( ) | \` and
    space; other backslashes are literal (`C:\temp`).
  - A value starting with `/` is a regex only when its closing unescaped `/` is followed by
    whitespace, `)`, `|` or the end; otherwise it is a bare value (`path:/api/v1/orders`).
  - In value position an unescaped `*` (in bare **or** quoted values) makes a wildcard; `\*` is
    a literal star. A lone unquoted `*` means "exists". Quoted strings after comparison operators,
    in lists and as function arguments are never wildcards. Unknown string escapes are kept
    verbatim (`'\d+'` stays `\d+`).
  - `$name` (`\$[A-Za-z_][A-Za-z0-9_]*`) is a dashboard variable (§13.1); it must be substituted
    before Check/Compile.
  - Keywords are reserved only where the grammar expects them (`source` is a property elsewhere).

### 10.3 Grammar (EBNF)

```ebnf
query      = [ source_clause "|" ] filter { "|" stage } ;
source_clause = "source" ( "logs" | "spans" | "metrics" ) ;
filter     = "*" | or_expr | ε ;               (* empty or * = match all *)
or_expr    = and_expr { ("or" | "||") and_expr } ;
and_expr   = unary { [ "and" | "&&" ] unary } ;   (* adjacency = AND *)
unary      = ( "not" | "!" ) unary | primary ;
primary    = "(" or_expr ")" | field_term | compare | in_expr | func_call | free_text ;
field_term = field ":" value_expr ;
value_expr = value | "(" value { ("or"|"and") value } ")" | ( "<" | "<=" | ">" | ">=" ) value ;
value      = string | number | bare | regex | "*" | "true" | "false" | "null" ;
compare    = field op literal [ "ci" ] ;
op         = "=" | "==" | "!=" | "<>" | "<" | "<=" | ">" | ">=" | "like" | "not like" ;
in_expr    = field [ "not" ] "in" "[" literal { "," literal } "]" ;
func_call  = ( "contains" | "startswith" | "endswith" | "has" | "match" ) "(" field [ "," literal ] ")" ;
free_text  = string | bare ;                     (* a bare word not followed by ':' or op *)
stage      = stats | sort | limit | fields | where | top ;
stats      = "stats" agg { "," agg } [ "by" field { "," field } ] [ "span" duration ] ;
agg        = agg_fn "(" [ field ] ")" [ "as" ident ] ;
agg_fn     = "count" | "count_distinct" | "sum" | "avg" | "min" | "max" | "p50" | "p75" | "p90"
           | "p95" | "p99" | "rate" | "first" | "last" ;
sort       = "sort" ident [ "asc" | "desc" ] { "," ident [ "asc" | "desc" ] } ;
limit      = "limit" integer ;
fields     = "fields" field { "," field } ;   (* projection for event results *)
where      = "where" or_expr ;                (* after stats: filters on aggregate aliases ⇒ HAVING *)
top        = "top" integer field [ "by" field ] ;  (* = stats count() by field | sort count desc | limit n *)
```

Precedence: `not` > `and` (incl. adjacency) > `or`. Max query length 8 KiB; max nesting 32;
max 1,000 values in `in`/value groups; max 5 stages; max 10 `by` fields; max 20 aggregates per
stats; max 50 `fields`; max 10,000 time buckets per timeseries. Input must be valid UTF-8.

- `span` also accepts `auto` (dashboards, §13.1); explicit spans must be whole seconds ≥ 1 s.
- A value group desugars to field terms: `f:(a or b and c)` ≡ `f:a or (f:b and f:c)`.
- `has()` takes only a field; the other functions need a field and a string/number literal.
- **Stage order:** `where`* → at most one `stats`/`top` → `where`/`sort`/`limit` (each of `sort`,
  `limit`, `fields` at most once). `where` before stats is ANDed with the filter; after stats it
  becomes HAVING. `sort` requires stats/top (events are always newest first) and is not allowed
  after `top`. `limit` before stats, and `fields` together with stats/top, are errors.
- `top n f by g` ⇒ `stats count() by g, f`, ordered by count desc, limited to n rows **per value
  of g** (`LIMIT n BY g`); without `by`, limited to n rows.
- `sort` keys default to `asc`; sort keys and `as` names may be backtick-quoted. `limit` in a
  query without stats caps the events returned (never above the page size). After a source clause
  the filter is required: `source spans | * | stats …` (`source spans | stats …` reads `stats` as
  a filter).

### 10.4 Field resolution

Unprefixed names resolve first to a **built-in** of the active source (case-insensitive), else to a
property. `@`-prefixed names are always built-ins; `props.<key>` / `attrs.<key>` force a property.

| Source | Built-ins (aliases) ⇒ column |
|---|---|
| logs | `@timestamp` (`timestamp`,`@t`) ⇒ timestamp · `level` (`@level`,`@l`) ⇒ level_num/level · `message` (`@message`,`@m`,`msg`) ⇒ message · `template` (`@mt`,`messagetemplate`) ⇒ message_template · `exception` (`@x`) ⇒ exception · `service` · `environment` (`env`) · `host` · `stream` · `trace_id` (`traceid`,`@tr`) · `span_id` (`spanid`,`@sp`) · `event_type` (`@i`) · `protocol` · `connection_id` (`connectionid`) · `id` (`@id`) |
| spans | `@timestamp` ⇒ start_time · `trace_id` · `span_id` · `parent_span_id` · `service` · `name` · `kind` · `status` ⇒ status_code · `status_message` · `duration` (`@duration`) ⇒ duration_ns (ms in results) · `stream` · `connection_id` (`connectionid`) |
| metrics | `@timestamp` · `name` · `service` · `kind` · `unit` · `value` · `count` · `sum` · `min` · `max` · `stream` · `connection_id` (`connectionid`) |

`connection_id` is the id of the connection that ingested the row (the `connectionId` of events,
§11.5); ids are stored lower case, so equality compares the column directly.

- `@` + any canonical built-in name also resolves (`@service`, `@trace_id`); unknown `@names` are
  errors with a did-you-mean hint. Spans additionally accept `status_code` and `start_time`/`timestamp`.
- Properties: logs `props`/`props_num`; spans `attrs`/`attrs_num`; metrics `attrs` only (text;
  numeric use goes through `toFloat64OrNull`). Property keys are case-sensitive.
- Free text searches logs `message`+`exception`, spans `name`+`status_message`, metrics
  `name`+`description`.
- After stats, `where`/`sort` names resolve to aggregate aliases, then by-field names (as
  written, case-insensitive), then `@bucket` (sort only).

### 10.5 Semantics

- `field:value` — strings: case-insensitive equality (`lower(col) = lower(?)`); with `*`
  ⇒ `ILIKE` (`*`→`%`, `?`→`_`, `%`/`_` escaped); numbers ⇒ numeric equality using `props_num`
  **or** string equality on text (so `status:500` matches `500` stored as string);
  `true/false` ⇒ `'true'/'false'`; `null` ⇒ missing.
- `field:*` ⇒ exists and non-empty (`mapContains(props,k) AND props[k] != ''`; built-ins ⇒ `!= ''`).
- `field: >= 5` / `field >= 5` ⇒ numeric comparison on `props_num[k]` (missing ⇒ no match).
  `field >= 'abc'` ⇒ string comparison.
- `=`/`==`/`!=` are **case-sensitive** unless suffixed `ci` (Seq style); `:` is case-insensitive.
- `level` comparisons use the ordinal (`level >= warning` ⇒ `level_num >= 3`); level literals are
  parsed with `ParseLevel`; unknown level ⇒ query error with position.
- Free text ⇒ `(positionCaseInsensitiveUTF8(message, ?) > 0 OR positionCaseInsensitiveUTF8(exception, ?) > 0)`;
  adjacent free-text words are ANDed.
- `contains/startswith/endswith(f, 'x')` case-insensitive; `has(f)` ≡ `f:*`; `match(f, 're')` regex.
- `@timestamp` comparisons accept ISO strings and relative literals; they intersect with the
  request time range.
- **Stats:** `count()` ⇒ `count()`; `count_distinct(f)` ⇒ `uniq`; `sum/avg/min/max(f)` on numeric
  value; `pNN(f)` ⇒ `quantileTDigest(0.NN)`; `rate()` ⇒ count per second over the bucket (or whole
  range); for metrics, `rate(value)` on cumulative monotonic sums ⇒ per-series
  `deltaSum(value)/bucket_seconds` summed across series; `first/last(f)` ⇒ `argMin/argMax` by time.
  Default alias = `fn` or `fn_field` (`p95_elapsed`). `span <dur>` ⇒
  `toStartOfInterval(ts, INTERVAL n SECOND)` as `@bucket`; when `span` is omitted on a chart
  request the server picks `auto` (§11.3).
- **Result kinds:** no stats ⇒ `events`; stats without span ⇒ `table`; stats with span ⇒
  `timeseries`.
- Details (normative):
  - Numeric predicates never match a missing value and never yield NULL:
    `(mapContains(props_num, ?) AND props_num[?] op ?)`; text columns/attrs use
    `ifNull(toFloat64OrNull(x) op ?, 0)`. Hence `not x > 5` and `x != 5` match events without `x`.
  - `=` / `!=` with `null` ⇒ missing / exists. `in` and `like` are case-sensitive (`ci` ⇒
    `ILIKE`); `in` on a property uses `props_num` when every literal is a number.
  - `trace_id`, `span_id`, `parent_span_id` and `id` are stored lower case, so `:` lower-cases
    the argument instead of the column (index friendly); `id` equality is `id = toUUIDOrZero(?)`.
  - `level` with a wildcard/regex uses the `level` name column; ordinal comparisons use
    `level_num`. Level literals accept every `ParseLevel` input incl. ordinals `0`–`5`. After stats
    a `level` by-field is the name, so level predicates in HAVING become `IN (<names>)`.
  - `duration` literals without a unit are milliseconds (like results); with a unit they are
    converted to ns for `duration_ns`. A unit on any other field is an error.
  - `@timestamp` needs a comparison (`:` alone is an error) and is not allowed with `in`.
  - `?` is a wildcard only in a value that also contains an unescaped `*`; otherwise it is a
    literal character. `in` takes no `ci`.
  - `@bucket` is reserved as a result column name (also without `span`). Numeric aggregates of a
    text built-in or attribute convert with `toFloat64OrNull`; `avg(level)` uses the ordinal.
  - The request range is half-open `[from, to)`.
  - `rate()` is `count()/seconds` (bucket or whole range). Metrics `rate(value)` must be the only
    aggregate of its stats stage; per series: `deltaSumTimestamp(value, toUnixTimestamp64Milli(timestamp))`
    for cumulative points or `sum(value)` when `temporality = 'delta'`; summed across series and
    divided by the seconds. The increase between the last point of one bucket and the first point
    of the next is not counted (deltaSum is per bucket).
  - `count(f)` ⇒ `countIf(<f exists>)`; numeric aggregates of a property skip events without a
    numeric value; `sum/avg/min/max/pNN` of `@timestamp` or `id` are errors; `first/last(f)` ⇒
    `argMin/argMax(f, <time column>)`.

### 10.6 Compilation & safety

Public API of `internal/tql` (Go doc comments hold the details):

```go
func Parse(q string) (*Query, error)                                // syntax; free-text fallback below
func Check(q *Query) error                                          // resolution, types, stage rules (Compile runs it)
func Substitute(q *Query, vars map[string][]string) (*Query, error) // §13.1, on the AST; q unchanged
func Compile(q *Query, opts Options) (*Compiled, error)
func CompileWhere(q *Query, opts Options) (where string, args []any, err error) // histogram/facets/tail/purge
func Complete(q string, pos int, src model.Source, fields []FieldInfo) Completion // §10.7
func AutoSpan(rng time.Duration, target int) time.Duration           // §11.3 span list, ≤ target buckets
func BuiltinFields(src model.Source) []FieldInfo                     // §10.4 built-ins (GET /fields)
func (q *Query) String() string                                     // canonical TQL, minimal parentheses

type Options struct {
    Database      string                             // "" = unqualified; else ^[A-Za-z_][A-Za-z0-9_]{0,63}$
    From, To      time.Time                          // required: timeCol >= ? AND timeCol < ?
    Now           time.Time                          // for relative literals; zero ⇒ To
    Limit         int                                // events page size; 0 ⇒ 200; capped by MaxRows
    MaxRows       int                                // table/timeseries cap; 0 ⇒ 10,000
    Cursor        *Cursor                            // events only: rows strictly before (time, id)
    AutoSpan      time.Duration                      // for `span auto`; 0 ⇒ smallest §11.3 span giving ≤ 80 buckets (then whole days)
    IndexedFields map[model.Source]map[string]string // property key ⇒ column ^f_[a-z0-9_]{1,64}$ (§12.4)
    ExtraFilters  []*Query                           // signals: same source, no stages, ANDed
    Streams       model.StreamScope                  // required (§15.10): unset ⇒ ErrInvalidOptions
}
type Cursor   struct{ Timestamp time.Time; ID string } // ID = event UUID | span_id | series_id
type Compiled struct {
    Kind ResultKind; Source model.Source; SQL string; Args []any; Columns []Column
    Span time.Duration; Limit int; InterpretedAsText bool
}
type Column struct{ Name string; Type ColumnType } // time|string|number|bool|json|map|array|duration_ms
type Error  struct{ Pos, Len int; Msg, Hint string } // all query errors; option errors wrap ErrInvalidOptions
```

`ResultKind` is `events|table|timeseries`. The AST (`Query{Source, Filter Expr, Stages []Stage,
InterpretedAsText, Text}`, nodes `And Or Not MatchAll FieldMatch Compare In Func FreeText`,
stages `Stats Sort Limit Fields Where Top`) carries byte positions on every node.

- **SQL shapes.** Sources map to `events`/`timestamp`/`id`, `spans`/`start_time`/`span_id`,
  `metric_points`/`timestamp`/`series_id` (table / time column / keyset id).
  - WHERE: `time >= ? AND time < ? [AND stream IN (?, …) | AND 0] AND (<filter>) AND (<where before
    stats>) AND (<signal>)…`. The stream condition comes from `Options.Streams`: none for
    `AllStreams()`, `AND 0` for an empty scope (no rows, no error), else every name as a `?`
    argument (`stream` leads the sort key of every table, so it prunes by primary index).
  - events: `SELECT <event columns> FROM db.t WHERE … [AND (time, id) < (?, toUUIDOrZero(?)|?|toUInt64OrZero(?))]
    ORDER BY time DESC, id DESC LIMIT ?`. Spans expose `duration_ns / 1e6 AS duration_ms`.
    `fields a, b` selects `time, id, <a> AS f0, <b> AS f1` (columns named as written).
  - table/timeseries: `SELECT [toStartOfInterval(time, toIntervalSecond(?)) AS bucket,]
    <by> AS g0…, <agg> AS a0… FROM … WHERE … GROUP BY [bucket,] g… [HAVING …] ORDER BY …
    [LIMIT ? BY g_k] LIMIT ?`. Result columns are `@bucket`, the by-fields as written and the
    aggregate aliases. ORDER BY: `bucket ASC` first for timeseries, then `sort` keys, else
    (table) `a0 DESC`, then group keys ascending. Metrics `rate(value)` wraps an inner query
    grouped additionally by `series_id`.
  - Property access `props[?]` (key as parameter) or the indexed column `f_<slug>` for string
    use; numeric use keeps `props_num`.
- **Safety:** SQL text contains only constant fragments, built-in column names from the allow-list
  and validated option identifiers; every user value — including property keys, patterns, LIMIT
  values and bucket sizes — is a positional `?` argument. Tests assert no quote, comment or `;`
  from input ever reaches SQL text (§18).
- Errors are `tql.Error{Pos int, Len int, Msg string, Hint string}`; messages are human readable
  (e.g. *"unknown level 'erorr'"* with hint *"did you mean 'error'?"*).
- If parsing fails and the input contains no TQL operators/quotes/colons (none of
  `: = < > | ( ) [ ] ' "` backtick, `&&`, nor a `!` used as an operator), the whole trimmed input is
  one free-text phrase (Seq behaviour); `Query.InterpretedAsText` / API `"interpretedAsText": true`.
- Queries run as the read-only ClickHouse user with `max_execution_time`, `max_result_rows`,
  `max_memory_usage` (1 GiB default) and `readonly=2`, set **per query** by the reader
  (`internal/store/clickhouse/reader*.go`) plus a client deadline of `QUERY_TIMEOUT + 5 s`.
  `max_result_rows` (with `result_overflow_mode=break`) is the page/limit for events pages and the
  reader's fixed queries; it is `0` for stats statements and single-value queries because the
  setting also caps subqueries (metrics `rate`, facet sampling) — their own `LIMIT` bounds them.
- **Time arguments.** clickhouse-go's positional binding formats `time.Time` with second
  precision, so the reader binds every `time.Time` argument as UTC text
  `YYYY-MM-DD hh:mm:ss.fffffffff` (9 digits; 3 for metrics); ClickHouse converts it when
  comparing with the `DateTime64` column. Fixed reader queries use `toDateTime64(?, 9, 'UTC')`.
- **Reader result shaping.** Log events with the default columns are scanned into `model.Event`
  (typed properties from `props_json`, numbers kept exact); every other events-kind row (spans,
  metric points, `fields` projections) becomes a record keyed by column name. Values are made
  JSON-safe: UTC times, UUID text, NaN/±Inf ⇒ `null`, integers beyond ±2^53 ⇒ decimal strings,
  JSON columns ⇒ raw JSON. A full events page carries `NextCursor` = (time, id) of its last row
  (id = event UUID, `span_id`, or `series_id`). Timeseries rows are grouped into series by the
  `g0…` by-columns in order of first appearance; points are `[epoch ms, a0, a1…]`.

### 10.7 Autocomplete & help (served by API, rendered by UI)

`GET /api/v1/tql/complete?q=&pos=&source=` returns suggestions of kind `field` (from built-ins +
`field_catalog`), `value` (top 10 values via facets for the field left of `:`), `operator`,
`keyword`, `stage`, `function`, each with `label`, `insertText`, `detail`. The UI also shows a
syntax cheat-sheet popover (examples from §10.1).

`tql.Complete` produces the syntactic part: `Completion{From, To, Field, Suggestions}` where
`[From, To)` is the word to replace and `Field` is set in value position (after `field:` or a
comparison operator); the API then adds the field's top values (kind `value`). Built-ins come from
§10.4, properties from the caller's catalog (`FieldInfo{Name, Builtin, Numeric, Hits}`, by hits);
`level` values are suggested directly; sort keys are taken from the preceding stats stage. At most
50 suggestions, filtered by the typed prefix (case-insensitive).

---

## 11. HTTP API (`/api/v1`)

### 11.1 Conventions

- JSON (`application/json; charset=utf-8`), camelCase fields, RFC3339Nano UTC timestamps.
- Auth: session cookie (`tm_session`) **or** `Authorization: Bearer <api token>` (§8.4 `api_tokens`).
  Cookie-authenticated **mutating** requests require header `X-Tidemark-CSRF` equal to the
  `tm_csrf` cookie (double-submit, §15.2).
- Errors: `{"error":{"code":"<code>","message":"…","details":{…}}}`; codes:
  `bad_request`(400) `invalid_query`(400, details `{pos,len,hint}`) `unauthorized`(401)
  `forbidden`(403) `not_found`(404) `conflict`(409) `payload_too_large`(413)
  `rate_limited`(429) `backpressure`(503) `internal`(500, generic message, logged with request id).
- Every response carries `X-Request-Id`.
- Time parameters `from`/`to`: RFC3339 or relative (`now`, `now-15m`, `now-7d`); default last 1 h.
- Roles: **viewer** = read; **editor** = viewer + signals/dashboards/alerts CRUD; **admin** =
  everything incl. users, connections, streams, settings, audit log.

### 11.2 Auth & users

| Endpoint | Role | Notes |
|---|---|---|
| `POST /auth/login` `{username,password}` | public | Sets `tm_session` + `tm_csrf` cookies; returns `{user, mustChangePassword}`. Rate limited (§15.4). |
| `POST /auth/logout` | any | Deletes session server-side. |
| `GET /auth/me` | any | `{id,username,displayName,role,mustChangePassword,defaultCredentials,allStreams,streams,teams,streamAccess}`: the caller's stream access (§15.10) — `allStreams` (sees every stream), `streams` (the visible stream names when not all, else `[]`), `teams` (`[{id,name}]` by name) and `streamAccess` (`admin`, `teams`, `default-all` or `default-none`). |
| `POST /auth/password` `{currentPassword,newPassword}` | any | Policy §15.2; revokes other sessions. |
| `GET/POST /users`, `GET/PATCH/DELETE /users/{id}` | admin | Cannot delete/demote the last admin or the caller's own account. Deleting a user makes the deleting admin the owner of the user's dashboards and signals (private ones stay private) in the same transaction as the delete. The number of accounts is not limited. User objects of these routes include `teams:[{id,name}]` (by name). |
| `PUT /users/{id}/teams` `{teamIds}` | admin | Replaces the user's teams (unknown team ⇒ 400 `details.field = teamIds`, ≤ 500); returns the user with its teams. Audited `user.teams` `{added, removed}`. |
| `GET/POST /tokens`, `DELETE /tokens/{id}` | any (own) | Token value returned **once** on create. |

### 11.3 Search & analytics

| Endpoint | Description |
|---|---|
| `POST /query` | Body `{q, from, to, limit?(≤ QUERY_MAX_ROWS, default 200), cursor?, span?("auto"|dur), signals?[ids], streams?[names], environments?[values]}`. Active signals are ANDed with `q`'s filter. Response by kind: `events`: `{kind,events:[Event],nextCursor,stats}` (newest first; cursor = opaque base64 of `(timestamp,id)`); `table`: `{kind,columns:[{name,type}],rows:[[…]],stats}`; `timeseries`: `{kind,span,columns,series:[{key:{by…},points:[[tsMs,value…]]}],stats}`. `stats = {elapsedMs, rowsRead, bytesRead, interpretedAsText}`. |
| `POST /query/validate` `{q}` | `{ok, kind, error?}` — used for live squiggles. |
| `GET /histogram?q&from&to&buckets=80` | `{span, buckets:[{t, counts:{Verbose..Fatal}}]}` (logs); spans: counts by status. Span chosen from 1s,5s,10s,30s,1m,5m,10m,30m,1h,3h,6h,12h,1d so bucket count ≤ requested. |
| `GET /facets?q&from&to&fields=service,level&top=10` | `[{field, values:[{value,count}], otherCount}]`, computed over at most the newest 1,000,000 matching rows (flag `sampled`). |
| `GET /fields?source&from&to` | `[{name, builtin, numeric, hits}]` |
| `GET /events/{id}` | Full Event incl. typed `properties` from `props_json`. |
| `GET /events/{id}/context?before=20&after=20` | Surrounding events of the same service/host. |
| `GET /tail?q&signals` | **Server-Sent Events**: `event: events` with JSON arrays, polling ClickHouse every 1 s on `ingested_at`; heartbeat every 15 s; max 50 events/s per subscriber (excess ⇒ `event: dropped` count). Max 20 concurrent tails per user. |
| `GET /traces?q&from&to&limit=50` | Trace summaries `{traceId, rootService, rootName, start, durationMs, spanCount, errorCount, services[]}`. |
| `GET /traces/{traceId}` | `{spans:[…], events:[…]}` (events with matching trace id). |
| `GET /metrics/names?q` | `[{name, kind, unit, description}]` |
| `GET /export?q&from&to&format=ndjson|csv` | Streams up to 100,000 events. editor+. |
| `GET /tql/complete` | §10.7 |

All §11.3 routes require **viewer**, except `/export` (**editor**). Every route reads only the
streams of the caller's scope (§15.10). Details (normative):

- **Selection** — `POST /query` takes `streams` and `environments`; `GET /histogram`, `/facets`,
  `/fields`, `/tail`, `/traces`, `/metrics/names`, `/export` and `/tql/complete` take `streams`
  and `env` (comma-separated and/or repeated). At most 100 streams, each a valid stream name
  (§8.1), and at most 20 environments of 1…256 bytes (else 400 `bad_request`). The selected
  streams are intersected with the caller's scope, so a selection never widens access (a
  selection outside the scope returns no data, not an error); no selection means the whole
  scope. Environments add the filter `environment in [...]` to **logs** queries only (spans and
  metric points have no environment column); they are a filter, not an access boundary. The
  by-id routes (`/events/{id}`, `/context`, `/traces/{traceId}`) take no selection.

- **`POST /query`** — `limit` 1…`QUERY_MAX_ROWS` (0 ⇒ 200); `span` is `"auto"` or a Go duration of
  whole seconds (`"30s"`, `"5m"`) used for `span auto`. Response `span` is in **milliseconds**.
  `events` holds Event objects for log events (`{id,timestamp,ingestedAt?,stream,level,service,
  environment,host,messageTemplate,message,exception,eventType,traceId,spanId,protocol,
  connectionId,properties}`), and column-keyed objects plus `columns` for spans, metric points and
  `fields` projections. Signals must be visible to the caller and of the query's source (else 400).
- **`POST /query/validate`** `{q, from?, to?}` ⇒ `{ok, kind?, interpretedAsText, error?:{message,
  pos, len, hint?}}`; query errors are reported in the body with status 200.
- **`GET /histogram`** — `buckets` 1…1000 (default 80), optional `signals`; span = `tql.AutoSpan`.
  Every bucket of the range is present (zero filled, aligned to multiples of the span since the
  Unix epoch) with all keys: level names (logs), `unset/ok/error` (spans) or `count` (metrics).
- **`GET /facets`** — `fields` 1…20, `top` 1…100 (default 10), optional `signals`. Counted over rows
  where the field exists (`field:*`; `level` always), plus `where` stages before any stats; values
  are the top values (`top n field`), `otherCount` = rows having the field minus the listed counts.
  If more than 1,000,000 rows match, every facet of the request is computed from the time of the
  1,000,000th newest row onward and `sampled` is true.
- **`GET /fields`** — `source` (default logs): built-ins first, then property keys of
  `field_catalog` in the range by hits (metrics: attribute keys of `metric_points`); a property
  whose name equals a built-in (case-insensitive) is omitted.
- **`GET /events/{id}`** — id must be a UUID (400); an event outside the caller's scope is 404.
  **`/context`**: `before`/`after` 0…200 (default 20); events of the same service **and** host
  within ±24 h and in the caller's scope, `{before (oldest first), event, after}`.
- **`GET /tail`** — logs only and no stats (400), optional `signals`; ≤ 20 open tails per user
  (429 `rate_limited`). Every second the server reads events with `ingested_at` in
  `(previous, now − lag]`, lag = `BATCH_MAX_WAIT` + 2 s (rows carry the ingest time but appear
  after their batch insert), restricted to event timestamps in `[now − 1 h, now + 7 d + 1 h)`;
  at most 50 per poll (rest ⇒ `event: dropped`, data = count). Events carry `ingestedAt`. A
  failing poll sends `event: error` and ends the stream; streams also end when the server shuts
  down. The write deadline is lifted with `http.ResponseController`. The caller's scope is
  resolved again on every poll, so access changes apply to open tails.
- **`GET /traces`** — `q` is a spans filter (`source spans` implied; another explicit source ⇒
  400), `limit` 1…500 (default 50). Traces are ordered by their newest matching span; each summary
  covers the spans of the trace in the caller's scope within the range ± 1 h; root = earliest
  visible span without parent (else earliest visible span); `durationMs` = latest span end −
  earliest start; spans of other streams are not counted or named.
  **`/traces/{traceId}`** — 32 hex characters (any case); ≤ 10,000 spans by start time and
  ≤ 1,000 events with that trace id, both limited to the caller's scope; 404 when nothing
  visible exists. Span JSON: `{traceId, spanId,
  parentSpanId, stream, service, name, kind, start, durationMs, statusCode, statusMessage,
  attributes, resourceAttributes, events, links}` (numeric attributes typed again).
- **`GET /metrics/names`** — `q` case-insensitive substring (≤ 256 bytes), range default last
  hour, ≤ 1,000 names.
- **`GET /export`** — `format` `ndjson` (default) or `csv`, optional `signals`; events-kind queries
  only (stats ⇒ 400). Pages of `QUERY_MAX_ROWS` rows newest first, up to 100,000; audited
  (`export`, details q/from/to/format/signals/streams/environments) before streaming; `Content-Disposition:
  attachment`; the write deadline is extended to 10 min. CSV of log events: `timestamp, level,
  stream, service, environment, host, message, exception, trace_id, span_id, event_type, id,
  properties(JSON)`; other rows use the result columns. Cells starting with `= + - @`, tab or CR
  are prefixed with `'` (formula injection).
- **`GET /tql/complete`** — `pos` defaults to the end of `q`; `source` (default logs) unless `q`
  starts with a source clause; property names from `field_catalog` of the last 24 h; for the field
  in value position the top 10 values of the last hour (all rows) whose text starts with the typed
  prefix are prepended (kind `value`, `detail` = count, inserted bare when `^[A-Za-z0-9_.@-]+$`
  and not a keyword, else double-quoted with `\`, `"` and `*` escaped). Level values come from
  `tql.Complete`; fields that cannot be faceted yield no values.
- Errors: TQL errors ⇒ `invalid_query` (details `{pos,len,hint}`), store not found ⇒ `not_found`.

### 11.4 Signals, dashboards, alerts, channels

Standard REST: `GET/POST /signals`, `GET/PUT/DELETE /signals/{id}`; same for `/dashboards`,
`/alerts`, `/channels`. Dashboards also `POST /dashboards/import` (JSON doc), `GET
/dashboards/{id}/export`, `POST /dashboards/{id}/duplicate`, `GET /dashboards/templates`.
Alerts also `POST /alerts/{id}/test` (evaluate now, no notify), `POST /alerts/{id}/mute`
`{until}`, `GET /alerts/{id}/history`. Channels: `POST /channels/{id}/test`. `PUT` uses optimistic
concurrency via `version` (409 on mismatch).

**Signals** (`internal/service/signals.go`): body `{name, tql, color, shared, source}`; `name`
1–128 chars (trimmed); `source` logs (default) / spans / metrics; `tql` must check as a filter for
that source (no pipe stages; the source is implied when the text has no source clause); `color`
`^[A-Za-z0-9#-]{0,32}$` (token name or hex); at most 500 signals owned per user. GET (viewer)
lists the caller's own and shared signals; a private signal of someone else is 404 except for
admins. POST/PUT/DELETE require editor; only the owner or an admin may change or delete (403).
Signals have no `version` column, so `PUT` requires `updatedAt` (the value last read) as the
concurrency token: the update is `WHERE id = ? AND updated_at = ?`; mismatch ⇒ 409, missing ⇒ 400.

**Dashboards** (viewer reads, editor writes; `internal/service/dashboards.go`):

| Endpoint | Role | Notes |
|---|---|---|
| `GET /dashboards` | viewer | Own + shared, without `doc`. |
| `POST /dashboards` `{name, description?, shared?, doc}` | editor | Owner = caller; `201` with the dashboard. |
| `GET /dashboards/templates?service=` | viewer | `[{id, name, description, doc}]`; `service` (≤ 256 chars) becomes the default of each template's `service` variable. |
| `POST /dashboards/import` `{name, description?, doc}` | editor | Same format as export; creates a private dashboard of the caller. |
| `GET /dashboards/{id}` | viewer | Visible = owner or shared; otherwise 404. |
| `PUT /dashboards/{id}` `{name, description?, shared?, doc, version}` | editor | Owner, or admin for shared dashboards (else 403); stale `version` ⇒ 409. |
| `DELETE /dashboards/{id}` | editor | Same permission as `PUT`. |
| `GET /dashboards/{id}/export` | viewer | `{name, description, doc}` with `Content-Disposition: attachment; filename=dashboard-<name>.json`. |
| `POST /dashboards/{id}/duplicate` `{name?}` | editor | Private copy owned by the caller, default name `<name> (copy)`. |
| `POST /dashboards/{id}/panels/{panelId}/query` `{vars?:{name:[values]}}` | viewer | `{q}`: the panel query with variables substituted on the AST (§13.1), as canonical TQL for `POST /query`. Missing vars use defaults; unknown vars ⇒ 400. |

Dashboard JSON: `{id, name, description, ownerId, shared, version, doc, createdAt, updatedAt}`.
Invalid panel queries: a query that fails to parse, substitute or check (TQL error) ⇒ 400
`invalid_query` with `details` `{pos, len, hint, panelId}`; a valid query of the wrong kind for its
panel type (§13.1, e.g. a `timeseries` panel without `stats`) and every other document error ⇒
400 `bad_request` with `details.field` (e.g. `doc.panels[0].query`).

**Alerts and channels** (`internal/service/{alerts,channels}.go`):

| Endpoint | Role | Notes |
|---|---|---|
| `GET /alerts`, `GET /alerts/{id}` | viewer | `{id, name, enabled, doc, streams, state, stateSince, lastEvalAt, lastValue, lastError, groups:[{labels, state, since, value}], version, createdAt, updatedAt}` (`streams` `null` = every stream). Only rules whose streams lie within the caller's scope are listed; others are 404 on every `/alerts/{id}` route (§14.1). |
| `POST /alerts` `{name, enabled?, doc, streams?}`, `PUT /alerts/{id}` `{name, enabled?, doc, streams?, version}`, `DELETE /alerts/{id}` | editor | Channels in `doc.channels` must exist; invalid doc ⇒ 400 `details.field = doc.<field>` (or `invalid_query`); invalid `streams` ⇒ 400 `details.field = streams`. |
| `POST /alerts/{id}/test` | editor | Evaluates now without persisting or notifying: `{state, error?, samples:[{labels, value}], groups, transitions:[{labels, from, to, message}]}`. |
| `POST /alerts/{id}/mute` `{until: RFC3339 \| null}` | editor | Sets `doc.muteUntil` (future, ≤ 1 year; `null` unmutes); bumps `version`. |
| `GET /alerts/{id}/history?limit=` | viewer | Newest first, default 100, max 1000: `[{id, at, fromState, toState, value, message, group}]`. |
| `GET /channels`, `GET /channels/{id}` | viewer | `{id, name, kind, config, version, createdAt, updatedAt}` with secrets masked. |
| `POST /channels` `{name, kind, config}`, `PUT /channels/{id}` `{name, kind, config, version}`, `DELETE /channels/{id}` | admin | On `PUT`, a secret equal to `"••••"` keeps the stored value (same kind only). Deleting a channel used by a rule ⇒ 409. |
| `POST /channels/{id}/test` | admin | Sends one test notification (`state: "test"`, no retries): `{ok, error?}`. |

**Response shapes (exact, for the UI).** Same conventions as §11.5.

```ts
// GET /dashboards/templates
{ id: "service-overview"|"log-volume"|"http-red"|"ingestion-health", name: string, description: string,
  doc: DashboardDoc /* §13.1; `service` variable default = [service] when ?service= is given, else ["*"] */ }[]
// GET /dashboards → Dashboard[] without `doc`; other dashboard reads/writes → Dashboard
Dashboard = { id: string, name: string, description: string, ownerId: string, shared: boolean,
  version: number, doc?: DashboardDoc, createdAt: string, updatedAt: string }
// GET /alerts → Alert[]; GET/POST/PUT /alerts/{id}, POST /alerts/{id}/mute → Alert
Alert = { id: string, name: string, enabled: boolean, doc: RuleDoc /* §14.1 canonical */,
  state: "ok"|"pending"|"firing"|"error", stateSince: string?, lastEvalAt: string?, lastValue: number?,
  lastError: string /* "" when none */, groups: Group[], version: number, createdAt: string, updatedAt: string }
Group = { labels: {[field: string]: string} /* {} when ungrouped */, state: string, since: string, value: number? }
// POST /alerts/{id}/test → 200 (also when the query fails: `error` set, state from the failure policy)
{ state: string, error?: string, samples: {labels: {[f: string]: string}, value: number? /* null = no data */}[],
  groups: Group[], transitions: {labels: {[f: string]: string}, from: string, to: string, message: string}[] }
// GET /alerts/{id}/history
{ id: string, at: string, fromState: string, toState: string, value: number?, message: string,
  group: {[field: string]: string} }[]
// POST /channels/{id}/test → 200
{ ok: boolean, error?: string }
```

### 11.5 Connections, streams, admin

| Endpoint | Role |
|---|---|
| `GET/POST /connections`, `GET/PATCH/DELETE /connections/{id}`, `POST /connections/{id}/rotate` `{graceMinutes?, key?}` (new key, returned once; old key valid for optional grace period `graceMinutes` ≤ 1440), `POST /connections/{id}/revoke`. `POST` and `rotate` accept an optional admin-supplied `key` (§9.9); the response carries the key once in `key`. Connection objects carry `allowedStreams` (sorted; `[]` = any stream); a body may set it to at most 100 valid stream names, which must include `defaultStream` and a stream pinned in `appliedProperties` (else 400 `details.field = allowedStreams`); `connection.create`/`connection.update` audit it (§9.1) | admin |
| Connection bodies (`POST`, `PATCH`; on `PATCH` an absent field is unchanged): `name` (required on create, 1–128 characters after trimming, unique ⇒ else 409) · `description` (≤ 1024 bytes) · `protocols` (non-empty subset of `seq`, `elastic`, `otlp`, `native`; default all) · `defaultStream` (valid stream name §8.1; default `default`) · `allowedCidrs` (≤ 64 CIDRs or bare addresses, stored masked, an address as /32 or /128; `[]` = any source) · `allowedStreams` (above) · `minimumLevel` (a level name, `""` = none) · `appliedProperties` (object, ≤ 64 keys, merged into every event; `@stream`/`_stream` must be valid stream names and pin the stream §9.1) · `rateLimitEps` (0…1,000,000 events/s, 0 = unlimited) · `expiresAt` (RFC 3339 in the future, `null` clears) · `key` (create only, §9.9). Invalid values ⇒ 400 `bad_request` with `details.field`. Connection objects: `{id, name, description, keyPrefix, protocols, defaultStream, allowedCidrs, allowedStreams, minimumLevel (null = none), appliedProperties, rateLimitEps, expiresAt, revokedAt, rotationGraceUntil, lastUsedAt, lastUsedIp, eventsReceived, createdAt, updatedAt}` plus `key` once in create/rotate responses | admin |
| `GET /connections/{id}/snippets` → ready-to-paste configs (Serilog, NLog, OTel env vars, Filebeat, Fluent Bit, Vector, Logstash `http` output to the native endpoint §9.3) with host/port filled from `PUBLIC_URL` and key placeholder | admin |
| `GET /streams` (with stats §12.2), `POST /streams`, `PATCH /streams/{name}`, `DELETE /streams/{name}` (requires `{"confirm":"<name>"}`; drops data) | admin (GET: viewer) |
| `GET /streams/{name}/fields` (mappings: key, detected type, hits, indexed; 404 outside the caller's scope §15.10) | viewer |
| `GET /indexed-fields` | viewer |
| `POST /indexed-fields` `{source,key}`, `DELETE /indexed-fields/{id}` | admin |
| `POST /purge` `{stream?, source, q?, from, to, dryRun}` → estimate or job; `GET /purge/{id}` | admin |
| `GET /settings`, `PATCH /settings` | admin |
| `GET /teams`, `POST /teams`, `GET /teams/access`, `GET/PATCH/DELETE /teams/{id}`, `PUT /teams/{id}/members`, `PUT /teams/{id}/streams` (§15.10) | admin |
| `GET /audit?from&to&actor&action` | admin |
| `GET /system` (version, uptime, queue depth, ingest rates, ClickHouse version/disk) | admin |
| `GET /imports` (newest 100), `GET /imports/{id}`, `POST /imports` (JSON for `elastic`/`seq`; `multipart/form-data` for `clef-file`/`kibana`), `POST /imports/{id}/cancel` (§9.9) | admin |

Team bodies (§15.10): `GET /teams` returns `[{id, name, description, memberCount, streams,
createdAt, updatedAt}]` by name; `POST /teams` `{name, description?}` ⇒ 201 Team; `GET
/teams/{id}` and the `PUT` routes return a Team `{id, name, description, members:[{id, username,
displayName, role}], streams, createdAt, updatedAt}`; `PATCH /teams/{id}` `{name?, description?}`
⇒ Team; `DELETE /teams/{id}` ⇒ 204 (memberships and grants cascade); `PUT /teams/{id}/members`
`{userIds}` and `PUT /teams/{id}/streams` `{streams}` replace the set (duplicates ignored, unknown
user ⇒ 400 `details.field = userIds`). `GET /teams/access` ⇒ `{denyByDefault, usersWithoutTeam}`.
Limits: name 1…64 characters (trimmed), unique ignoring case (409 `conflict`); description ≤
1024 characters; at most 500 teams, 10,000 members and 1,000 streams per team; stream names
must be valid (§8.1) but need not exist yet. Audit: `team.create` `{name}`, `team.update`
`{name, previousName?}`, `team.delete` `{name, members (usernames), streams}`, `team.members`
and `team.streams` `{added, removed}`.

Stream bodies: `POST /streams` `{name, description?, retentionDays?, spansRetentionDays?,
metricsRetentionDays?, levelRules?:[{levels,days}], paused?}`; `PATCH` takes the same fields except
`name` (absent = unchanged; `null` retention = use the default). Responses `{id, name, description,
retentionDays, spansRetentionDays, metricsRetentionDays, levelRules, paused, createdAt, updatedAt}`;
`GET /streams` lists only the streams of the caller's scope (§15.10) and returns `{streams:[… + stats:{logs|spans|metrics:{rows, oldest, newest, ratePer5Min,
ratePer24h, compressedBytes, uncompressedBytes, compressionRatio, estimated, topServices}}],
statsUpdatedAt}`. Field mappings: `[{source, key, type, hits, firstSeen, lastSeen, indexed}]`.
Settings: `GET /settings` ⇒ `{retention: {logsDays, spansDays, metricsDays}, access: {denyByDefault},
ui: {…}}` with defaults applied (`denyByDefault` false); `PATCH /settings` takes any subset of
these keys (unknown key ⇒ 400; `access` must be exactly `{denyByDefault: true|false}`; `ui` a JSON
object ≤ 16 KiB) and returns the full settings; audited as `settings.update` with details
`{keys, "access.denyByDefault"?}`. Retention changes apply immediately, access changes with the
next request (§15.10).
Indexed fields: `{id, source, key, column, createdAt}`. Purge: `from`/`to` are required (§11.1
formats); a dry run returns `200 {dryRun:true, rowsEstimate}`, otherwise `202` with the job
`{id, stream, source, q, from, to, status (pending|running|done|failed), rowsEstimate, requestedBy,
droppedPartitions, createdAt, updatedAt}` (also returned by `GET /purge/{id}`). Errors: wrong
`confirm` ⇒ 400; deleting `default` or a full purge queue ⇒ 409; TQL errors ⇒ `invalid_query`.

**Response shapes (exact, for the UI).** `T?` = may be `null`; timestamps are RFC 3339 strings;
arrays and label maps are never `null` (empty instead).

```ts
// GET /streams
{ streams: Stream[], statsUpdatedAt: string? }            // null before the first collection
Stream = { id: string, name: string, description: string,
  retentionDays: number?, spansRetentionDays: number?, metricsRetentionDays: number?, // null = default
  levelRules: {levels: string[], days: number}[], paused: boolean, createdAt: string, updatedAt: string,
  stats?: { logs?: SourceStats, spans?: SourceStats, metrics?: SourceStats } } // only sources with rows;
                                                           // absent on POST/PATCH responses
SourceStats = { rows: number, oldest: string, newest: string, ratePer5Min: number, ratePer24h: number, // rows/s
  compressedBytes: number, uncompressedBytes: number, compressionRatio: number /* 0 when no bytes */,
  estimated: boolean, topServices: string[] /* ≤ 5, most frequent first */ }
// GET /streams/{name}/fields  (hits desc, then key; at most 2,000; firstSeen/lastSeen are catalog hours)
{ source: "logs"|"spans"|"metrics", key: string, type: "string"|"number"|"mixed", hits: number,
  firstSeen: string, lastSeen: string, indexed: boolean }[]
// GET /indexed-fields → IndexedField[]; POST /indexed-fields → 201 IndexedField
IndexedField = { id: string, source: "logs"|"spans"|"metrics", key: string, column: string, createdAt: string }
// POST /purge  (dryRun: true) → 200
{ dryRun: true, rowsEstimate: number }
// POST /purge (dryRun: false) → 202 PurgeJob;  GET /purge/{id} → 200 PurgeJob
PurgeJob = { id: string, stream: string /* "" = all */, source: string, q: string, from: string, to: string,
  status: "pending"|"running"|"done"|"failed", rowsEstimate: number, requestedBy: string,
  droppedPartitions: string /* comma-separated YYYYMMDD partition ids, "" when none */,
  createdAt: string, updatedAt: string }
// DELETE /streams/{name} {confirm}, DELETE /indexed-fields/{id} → 204 (no body)
```

Imports (§9.9). `POST /imports` with `application/json` takes `{kind: "elastic"|"seq", name?,
config}`; with `multipart/form-data` the **first** part must be `job` (the same JSON, kind
`clef-file` or `kibana`) followed only by `file` parts (clef-file: 1–16 files ≤ 1 GiB each;
kibana: exactly one ≤ 64 MiB). The upload route's body limit is 16 GiB + 1 MiB (routes may
declare a larger body limit than `MaxRequestBody`, `api.BodyLimiter`). Errors: invalid config ⇒
400 `bad_request` with `details.field` = `config.<name>`; a file over its limit ⇒ 413 `payload_too_large`;
cancelling a finished job ⇒ 409 `conflict`; unknown id ⇒ 404.

```ts
// GET /imports → ImportJob[] (newest first);  GET /imports/{id}, POST /imports/{id}/cancel → 200 ImportJob
// POST /imports → 201 ImportJob (kibana: already completed or failed)
ImportJob = { id: string, kind: "elastic"|"seq"|"clef-file"|"kibana", name: string,
  status: "queued"|"running"|"completed"|"failed"|"cancelled",
  config: ElasticConfig | SeqConfig | ClefConfig | KibanaConfig,   // secrets and paths removed
  rowsImported: number, rowsTotal: number /* 0 = unknown */, errors: number, lastError: string,
  warnings: string[], cancelRequested: boolean, requestedBy: string /* "user:<id>" | "system:cli" */,
  startedAt: string?, finishedAt: string?, createdAt: string, updatedAt: string }
ElasticConfig = { url: string, auth: { type: "none"|"basic"|"apiKey"|"bearer", username?: string },
  caCert?: string, index: string, timeField: string, from?: string, to?: string, query?: object,
  stream?: string, pageSize: number }
SeqConfig = { url: string, from?: string, to?: string, filter?: string, stream?: string, pageSize: number,
  importEvents?: boolean, importSignals?: boolean }
ClefConfig = { stream?: string, files: { name: string, size: number }[], node: string }
KibanaConfig = { file: { name: string, size: number } }
```

### 11.6 Operational endpoints (no auth, minimal output)

`GET /healthz` (process alive) · `GET /readyz` (ClickHouse + metadata DB reachable; `503
{"status":"draining"}` once a graceful drain started, §17.5) · `GET /metrics`
(Prometheus; **disabled unless** `TIDEMARK_METRICS_ENABLED=true`, optionally protected by
`TIDEMARK_METRICS_TOKEN`).

Self-metrics (`internal/selfmetrics`, own registry, plus the Go runtime and process collectors):

| Metric | Labels | Meaning |
|---|---|---|
| `tidemark_ingest_items_total` | `protocol`, `source` | Items accepted by ingestion |
| `tidemark_ingest_dropped_total` | `protocol`, `reason` | Items dropped by connection policy (e.g. below the minimum level) |
| `tidemark_ingest_rejected_requests_total` | `protocol`, `reason` | Rejected ingestion requests |
| `tidemark_ingest_rerouted_total` | `protocol`, `reason` | Items written to the default stream instead of the requested one (§9.1) |
| `tidemark_pipeline_enqueued_total` | `signal` | Rows enqueued (written to the WAL when it is enabled) |
| `tidemark_pipeline_written_total` | `signal` | Rows written to ClickHouse |
| `tidemark_pipeline_dropped_total` | `signal`, `reason` | Rows lost or refused: `backpressure`, `stream_paused`, `wal_corrupt`, `wal_decode`, `deadletter_failed` (§9.8) |
| `tidemark_pipeline_deadlettered_total` | `signal` | Rows written to dead-letter files |
| `tidemark_pipeline_write_errors_total` | `signal` | Failed insert attempts (ClickHouse unreachable or rejecting) |
| `tidemark_pipeline_queue_depth` | `signal` | Rows acknowledged but not yet in ClickHouse (WAL backlog) |
| `tidemark_wal_bytes` | `signal` | Bytes of the WAL segments on disk, read at scrape time (only with the WAL) |

---

## 12. Streams & index management

### 12.1 Concepts
Streams play the role of Elasticsearch indices / data streams and Seq retention scopes. They are
auto-created on first write (if under `TIDEMARK_MAX_STREAMS`) with the default retention.

### 12.2 Stats
A background collector (every 5 min, and on demand) computes per stream and source: row count,
oldest/newest timestamp, ingest rate (last 5 min / 24 h), compressed and uncompressed bytes
(`system.parts` totals apportioned by row share, flagged `estimated: true`), compression ratio,
top services. Cached in memory; served by `GET /streams`.
- One `SELECT stream, count(), min/max(time), countIf(<ingest> >= now-5m), countIf(<ingest> >= now-24h),
  topK(5)(service) … GROUP BY stream` per table; `<ingest>` is `ingested_at` (events, spans) or
  `timestamp` (metric_points). Rates are rows per second averaged over the window.
- Bytes: active-part totals per table from `system.parts`; stream share = `bytes × streamRows /
  Σ streamRows` (integer arithmetic, never exceeds the table total). Ratio = uncompressed /
  compressed.
- The periodic collector runs only on the node leading `stats` (§17.5); `GET /streams` refreshes
  on demand when the cache is older than 5 min. A failed collection keeps the previous values.

### 12.3 Retention (TTL rules)
- Settings: default `retention_days` (30 logs, 7 spans, 30 metrics); per stream overrides; per
  stream **level rules** (e.g. Verbose/Debug 3 days).
- `internal/retention` builds one TTL clause per table, applied with
  `ALTER TABLE … MODIFY TTL …` (runs on startup and on change, idempotent — skipped when unchanged):

```
TTL toDateTime(timestamp) + toIntervalDay(3)  DELETE WHERE stream = 'api' AND level_num IN (0,1),
    toDateTime(timestamp) + toIntervalDay(90) DELETE WHERE stream = 'api',
    toDateTime(timestamp) + toIntervalDay(30) DELETE WHERE stream NOT IN ('api')
```
Stream names in TTL are validated by the stream regex (§8.1) before inclusion — no parameters are
possible in DDL, so validation is mandatory and unit tested.

### 12.4 Field mappings & indexed fields
- Mappings page lists every property key with detected type (string/number/mixed), hits and
  first/last seen (from `field_catalog`).
- Admins can **index** a field: creates a materialized column + bloom-filter skip index (§8.2),
  then `MATERIALIZE INDEX` for existing parts (async). Max 50 indexed fields.
- Sources: logs ⇒ `events`/`props[...]`, spans ⇒ `spans`/`attrs[...]`, metrics ⇒
  `metric_points`/`attrs[...]`. DDL takes no parameters, so keys must match
  `^[A-Za-z0-9_.@:/\-]{1,128}$` (checked by the service and again by the store).
- Column: `f_` + slug, slug = key lower-cased, runs of non-`[a-z0-9]` ⇒ `_`, trimmed, max 64
  (`field` if empty); a taken column gets `_2`, `_3`, …. Index `idx_f_<slug>` =
  `bloom_filter(0.01) GRANULARITY 4`. Deleting drops index and column. A key may be indexed once
  per source (409 otherwise). Audited as `indexed_field.create|delete`.
- The compiler uses the columns through `tql.Options.IndexedFields`, which
  `service.IndexedFields.Columns` provides. The bloom filter is used by exact string comparisons
  (`key = "v"`, `key in ["a","b"]`); case-insensitive `key:v` (`lower(col) = lower(?)`) and
  numeric comparisons and `!=` read the column or `props_num` but cannot skip granules.

### 12.5 Purge
Delete by stream/time range/TQL filter. Dry run returns estimated rows. Whole-day, whole-stream-free
ranges drop partitions only when all rows in that partition match (no filter, no stream);
otherwise a lightweight `DELETE FROM … WHERE` is issued. Every purge is audited.
- `q` is filter-only (`where` stages allowed, nothing else); it is compiled with
  `tql.CompileWhere` for the purge source (a different explicit `source` is rejected) and the
  request range; the stream is ANDed as `stream = ?`.
- Without stream and filter, every UTC day entirely inside `[from, to)` is removed with
  `ALTER TABLE … DROP PARTITION ID '<YYYYMMDD>'`, then the rest with a lightweight `DELETE`.
- Requests are estimated (`count()`) first; real purges become `purge_jobs` rows (`pending`) and
  run one at a time, oldest first, on the node holding the `purge` lease (§17.5) ⇒ `running` ⇒
  `done|failed`. The queue is the set of `pending` rows (at most 16; a request that would exceed
  it is marked `failed` and answered 409), so a job requested on any node runs; the lease holder
  is woken by local requests and polls every 5 s for the others. `mutation_id` stores the dropped
  partition ids. When a node becomes the purge leader, jobs left `running` (by a crashed or
  deposed leader) are marked `failed`. Audit: `purge.request` (actor), `purge.done` /
  `purge.failed` (system).

### 12.6 Pause / resume
A paused stream rejects nothing at protocol level but drops its rows in the pipeline (counted in
`/system` and self-metrics).

### 12.7 Stream lifecycle (`service.StreamAdmin`)
- Create: name must already be valid (no normalization), the stream limit applies, retention days
  `1–36500`, at most 6 level rules (levels via `ParseLevel`, stored as canonical names, de-duplicated).
- Retention is re-applied (§12.3, idempotent) whenever an override or level rule changes; pause
  and resume take effect in the pipeline gate immediately.
- Delete: typed confirmation, the `default` stream cannot be deleted. The stream is paused, its
  rows are removed with lightweight `DELETE … WHERE stream = ?` from `events`, `spans`,
  `metric_points` and `field_catalog`, then the metadata row and every team grant of the name
  (`team_streams`, §15.10); new data re-creates the stream but not the grants. Streams cannot be
  renamed.
- Audit: `stream.create|update|delete` (`stream.delete` details `{revokedFromTeams: [team names]}`).

---

## 13. Dashboards

### 13.1 Document
```json
{
  "version": 1,
  "timeRange": {"from": "now-6h", "to": "now"},
  "refresh": "30s",
  "variables": [{"name": "service", "label": "Service", "type": "field-values",
                 "source": "logs", "field": "service", "multi": true, "default": ["*"]}],
  "panels": [{
    "id": "p1", "title": "Errors by service", "type": "timeseries",
    "grid": {"x": 0, "y": 0, "w": 6, "h": 4},
    "query": "level >= error and service:$service | stats count() by service span auto",
    "options": {"stack": true, "unit": "short", "legend": "bottom", "thresholds": [{"value": 100, "color": "danger"}]}
  }]
}
```
- Grid: 12 columns, row height 40 px. Panel types: `timeseries` (line/area/bars),
  `bar` (categorical), `stat` (single value + sparkline + delta vs previous range), `gauge`,
  `table`, `pie`, `logs` (live event list for a filter), `traces` (trace list), `markdown`.
- **Strict schema** (`service.ParseDashboardDoc`; unknown fields, trailing data or depth > 64 are
  errors; the stored document is the re-encoded canonical form):
  - `version` = 1 and `timeRange` with both `from` and `to` are **required** (a missing or
    invalid value ⇒ 400 `bad_request`, `details.field` = `doc.version`, `doc.timeRange.from` or
    `doc.timeRange.to`); `timeRange.from/to` = `now`, `now-<n><s|m|h|d|w>` or RFC 3339; `refresh`
    (optional) = `""`, `off` or `<n>s|m|h` between 5 s and 24 h; `variables` and `panels` may be
    omitted (empty).
  - `variables` ≤ 20: `name` `^[A-Za-z_][A-Za-z0-9_]{0,63}$` (unique), `label` ≤ 200, `type`
    `field-values` (needs `source` + `field`; values from facets), `custom` (needs `options`) or
    `text` (free input); `multi`; `default` (≤ 1 value unless `multi`). ≤ 1,000 values of ≤ 1,024
    bytes in `options`, `default` and request `vars`.
  - `panels` ≤ 100: `id` `^[A-Za-z0-9_-]{1,64}$` (unique), `title` ≤ 200, `type` from the list,
    `grid` with `0 ≤ x`, `1 ≤ w`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`, `1 ≤ h ≤ 100`; `query` (≤ 8 KiB)
    required except for `markdown` (which has none); `options` = `{stack, style
    (line|area|bars), unit (^[A-Za-z0-9_/%.-]{1,32}$), decimals (0–10), legend
    (bottom|right|hidden), thresholds (≤ 20 × {value, color token accent|success|warning|danger|info|chart1–8}),
    content (markdown only, ≤ 32 KiB)}`.
  - Every query must parse, substitute with the variable defaults (`tql.Substitute`) and pass
    `tql.Check`. Kinds: `timeseries`, `bar`, `stat`, `gauge`, `pie` need a `stats` or `top`
    stage; `logs` needs a logs query without stats; `traces` a spans query without stats;
    `table` accepts any query.
- Variables: `$name` substitution performed **on the AST** (values injected as literals, never
  string concatenation); multi-value ⇒ value group `(a or b)`; `*` ⇒ predicate removed.
  (`tql.Substitute`): a value that parses as a plain number becomes a number, anything else a
  string; `=` with several values ⇒ OR, `!=` ⇒ AND, other operators accept one value; `in [$v]`
  expands in place. An empty value list or one containing `*` removes the predicate: it is dropped
  from an `and`, makes an enclosing `or` match all, removes an enclosing `not`, and drops a `where`
  stage that becomes empty. Unknown variables are errors with position.

### 13.2 Builder (easy mode)
Panel editor has two tabs: **Builder** (source → filter (with autocomplete) → measure (count / avg
/ p95 … of field) → group by (≤3) → interval → visualization) which generates TQL, and **Query**
(raw TQL). Switching Builder→Query is lossless; Query→Builder works when the query fits the builder
shape, otherwise the Builder tab is read-only with a notice. Live preview updates on every change
(debounced 400 ms).

### 13.4 Dashboard assistant (planned — M4)
A "Describe a dashboard" box (dashboard list, and "Ask" in the panel editor) turns plain text such
as *"error rate and p95 latency per service for checkout, last 24h"* into panels.
- Provider-agnostic `internal/assistant` port: `type DashboardAssistant interface {
  Propose(ctx, prompt string, ctx SchemaContext) (Proposal, error) }`, with adapters for the
  Anthropic API (Claude), OpenAI-compatible endpoints (incl. GitHub Copilot/Azure OpenAI, local
  Ollama/LM Studio) and an MCP server mode so external coding agents can create dashboards through
  OpenTidemark tools (`list_fields`, `run_query`, `create_dashboard`).
- The model receives only schema context (field names/types, metric names, stream names, TQL
  grammar summary) — never event data — unless the admin opts in.
- Output is a §13.1 dashboard doc validated by the normal pipeline (TQL parse/check/compile, dry-run
  query); shown as a preview diff the user accepts or edits; nothing is saved automatically.
- Disabled by default; admin configures provider, model, endpoint and API key (encrypted §15.5);
  outbound calls via the SSRF-safe client with the configured endpoint allow-listed; usage audited.

### 13.3 Shortcuts
- "Add to dashboard" button on any explorer query result (choose dashboard + panel type).
- Templates (`GET /dashboards/templates`): *Service overview (OTel)*, *Log volume & errors*,
  *HTTP (RED metrics from spans)*, *Ingestion health* — instantiated with a chosen service.
  They are embedded JSON files `internal/service/templates/dashboards/{service-overview,log-volume,
  http-red,ingestion-health}.json` (`{id, name, description, doc}`), each with a multi-value
  `service` variable (default `*`). They are validated at startup, and a unit test compiles every
  panel query with the default and with concrete services.
- Import/export JSON; duplicate; per-panel "Open in explorer"; full-screen/TV mode with auto-cycle.

---

## 14. Alerting

### 14.1 Rule document
```json
{
  "description": "Checkout errors spike",
  "query": "service:checkout-api and level >= error | stats count()",
  "condition": {"op": ">", "threshold": 25},
  "window": "5m", "every": "1m", "for": "2m",
  "severity": "critical",
  "channels": ["<channel id>"],
  "noData": "ok",
  "groupBy": ["service"],
  "muteUntil": null
}
```
- Query must yield a single numeric column (optionally grouped by `groupBy` ⇒ one state per group).
- Evaluator: ticks every 10 s, runs due rules (bounded worker pool, 4), range `[now-window, now]`.
- States: `ok → pending (condition true, < for) → firing → ok (resolved)`; `error` when the query
  fails 3 times in a row. Notifications on `firing` and `resolved` (and `error`), de-duplicated;
  muted rules evaluate but do not notify. Every transition is written to `alert_history`.
- **Validation** (`alerting.ParseRule`, strict JSON, unknown fields rejected): `description` ≤ 1024;
  `condition.op` ∈ `> >= < <= == !=`, finite `threshold`; durations are Go duration strings:
  `window` 10 s–7 d, `every` 10 s–24 h, `for` 0–24 h; `severity` `info|warning|critical` (default
  `warning`); `noData` `ok` (no data = condition false, default) | `keep` (state unchanged) |
  `alerting` (no data = condition true); ≤ 20 unique `channels`. The query (≤ 8 KiB) must have
  exactly one `stats` stage (no `top`, no `span`) with one aggregate of numeric type, and its
  by-fields must equal `groupBy` in order; at most 1,000 groups are evaluated. The stored document
  is the canonical re-encoding (durations like `5m0s`).
- **Streams** (`alert_rules.streams`, outside the document so older binaries still parse it,
  §15.10): `null` (or absent) = the rule reads every stream, else 1…100 valid stream names
  (stored sorted, de-duplicated). `null` is accepted only from callers who see every stream; a
  list must lie within the editor's scope (else 400 `details.field = streams`). The evaluator
  reads the stored streams (`alerting.RuleScope`), so a rule keeps reading them after its author
  loses access. Rules of earlier versions have `null`. A caller sees, tests, mutes, updates,
  deletes and reads the history of a rule only when its streams lie within the caller's scope
  (a `null` rule only for callers who see every stream); otherwise the rule is not found.

### 14.2 Channels
- **webhook:** POST JSON `{rule:{id,name,severity}, state, previousState, value, threshold, group,
  at, url}`; optional HMAC-SHA256 signature header `X-Tidemark-Signature: sha256=<hex>` with a
  per-channel secret; custom headers.
- **slack:** incoming webhook, Block Kit message.
- **teams:** Workflows/incoming webhook, Adaptive Card.
- **email:** SMTP (STARTTLS required unless disabled), plain-text + HTML.
- Timeouts 10 s, 3 retries with backoff. Outbound URL restrictions §15.6.
- Config (`alerting.ChannelConfig`, strict JSON): webhook `{url, secret?, headers?}` (≤ 20
  headers, RFC 7230 names, no `Host`, `Content-Length`, `Content-Type`, `Transfer-Encoding`,
  `Connection`, `User-Agent`, `X-Tidemark-Signature`; values ≤ 1024 without CR/LF); slack and
  teams `{url}`; email `{to:[1–20 bare addresses]}` (SMTP server from `TIDEMARK_SMTP_*`, required
  to create email channels). URLs ≤ 2048 and checked with `httpx.ValidateURL`. Secrets (`url`,
  `secret`, header values) are sealed (§15.5) and masked as `"••••"`.
- Payloads: webhook also carries `message`; Slack `{text, blocks:[header "[STATE] name", section
  fields (Severity, State, Value, Condition, Group, At), section description/message, actions
  button "Open in OpenTidemark"]}` (mrkdwn `& < >` escaped); Teams `{type:"message",
  attachments:[{contentType:"application/vnd.microsoft.card.adaptive", content: AdaptiveCard 1.4
  with TextBlock title, FactSet, TextBlock text, Action.OpenUrl}]}`; email subject `[STATE] name`
  (Q-encoded, CR/LF removed), `multipart/alternative` text + HTML (html/template), quoted-printable.
- Delivery (`alerting.Dispatcher`): queue of 1,000 with 2 workers; per attempt timeout 10 s;
  1 attempt + 3 retries with backoff 1 s, 2 s, 4 s; HTTP 2xx = success, 408/429/5xx and network
  errors retry, other statuses and invalid configs fail permanently. Identical (rule, group,
  state) notifications within 1 minute are dropped. Error messages never include the target URL.
  HTTP notifiers use `httpx.SafeClient(TIDEMARK_ALLOW_PRIVATE_WEBHOOKS, 10s)`; email uses
  `net/smtp` (PLAIN auth only after STARTTLS or to localhost).
- Audit: `alert.create|update|delete|mute` (create and update include `streams`),
  `channel.create|update|delete|test`.

### 14.3 Evaluation & state machine (`internal/alerting`)
- Singleton: a tick does nothing unless `Leader.IsLeader("alerting")` (§17.5). A rule is due when
  enabled and `now ≥ lastEvalAt + every` (never evaluated ⇒ due). Each evaluation has a 60 s
  timeout; the query is `doc.Compile(now)` (indexed fields applied) run through `store.Querier.Run`
  with page limit 1,000.
- Per group (key = canonical JSON of the by-field labels, `""` when ungrouped) with value `v`
  (`nil` = no row, NULL or NaN ⇒ no data, resolved by `noData`):
  condition true: `ok|error → pending` (`for` > 0) or `→ firing` (`for` = 0); `pending → firing`
  once `now − since ≥ for`; `firing` stays. Condition false: any state `→ ok`, `ok` stays.
  Groups missing from the result are treated as no data; `ok` groups of grouped rules are
  forgotten.
- Failures: the 3rd consecutive failed evaluation (query error or invalid document) replaces all
  groups by one `error` group (single transition `<aggregate> → error`); further failures are
  silent. The next success restarts from `error` (grouped rules record `error → ok` for the rule
  and evaluate groups afresh).
- Persisted per rule: `eval_state` `{failures, groups:{key:{state, since, labels, value}}}`,
  `state` = aggregate (error > firing > pending > ok), `state_since` (aggregate changes),
  `last_eval_at`, `last_value` (largest group value), `last_error`. Every transition is a history
  row; transitions to `firing` or `error` and from `firing|error` to `ok` notify (unless muted).
- Notification fields: rule id/name/severity/description, state, previousState, value, condition
  op/threshold, group labels, time, link `<PUBLIC_URL>/alerts/<id>` and message.

---

## 15. Security

Target: **OWASP ASVS 4.0 Level 2** for the web app and API. Security rules are mandatory; any
deviation must be recorded in this section.

### 15.1 Transport
- Built-in TLS (min TLS 1.2, Go default modern cipher suites) on all listeners when cert/key set;
  production docs recommend TLS everywhere (directly or via reverse proxy).
- When `PUBLIC_URL` is https: cookies `Secure`, `Strict-Transport-Security: max-age=31536000`.
- ClickHouse is **never** published to the host in the default compose file (internal network).

### 15.2 Users, passwords & sessions
- First start with no users seeds **`admin` / `admin`** (role admin, `must_change_password=true`)
  unless `TIDEMARK_ADMIN_PASSWORD` is set. While the default password is in use: a startup WARN log
  every start, a persistent red banner in the UI, and `GET /auth/me` returns
  `defaultCredentials: true`. *(Testing-phase decision recorded per owner's request; the UI prompts
  to change it on first login but does not block.)*
- Any number of user accounts may exist; whether a user can log in depends only on the
  password, the lockout below and the `disabled` flag (no editions or licence keys, §19).
- Passwords: argon2id (m=64 MiB, t=3, p=2, 16-byte salt, 32-byte key), PHC string format.
  New passwords: ≥ 12 chars, not equal to username, not in a built-in list of the 10,000 most
  common passwords (embedded file), max 256 chars.
- Login throttling: 5 failures ⇒ account locked 15 min (exponential up to 24 h); per-IP limit
  20 attempts / 5 min. Identical error messages and constant-time-ish handling for unknown users
  (dummy hash) to prevent user enumeration.
- Sessions: 256-bit random token in `tm_session` cookie (`HttpOnly`, `SameSite=Strict`, `Path=/`,
  `Secure` per §15.1); stored as SHA-256 hash; idle and absolute timeouts (§7); rotated on login and
  privilege change; all sessions revoked on password change/disable. Disabling a user and a
  password set by an admin (`PATCH /users/{id}` with `password`) or by `tidemark admin
  reset-password` also revoke the user's API tokens (account recovery); a role change keeps them
  (§15.3 caps them at the current role) and so does the user's own password change.
- CSRF: double-submit token cookie `tm_csrf` (not HttpOnly) + header `X-Tidemark-CSRF`, compared
  in constant time, required for all cookie-authenticated non-GET requests; plus SameSite=Strict
  and, when the request carries an `Origin` header, an origin check: it must equal `PUBLIC_URL`
  (scheme and host) or have the host the request was sent to (same-origin), so an install opened
  by a LAN address or host name works without setting `PUBLIC_URL`.

### 15.3 Connection keys (ingestion) & API tokens
- **One key per connection**; ingestion without a valid key is impossible (no anonymous mode).
- Format: `tmk_` + 40 chars base62 (≥ 238 bits) for connections, `tmt_` + 40 for user tokens.
  Connections may instead use an admin-supplied key (migration, §9.9) subject to the length/entropy rules there.
  Generated with `crypto/rand`; stored as SHA-256 hash + 8-char prefix; shown once.
- Per-connection controls: allowed protocols, allowed source CIDRs, expiry, rate limit, minimum
  level, pinned stream/properties, revoke, rotate with grace period, last-used time/IP.
- Key lookup uses the hash (no timing oracle); an in-memory LRU cache (60 s TTL, invalidated on
  change) avoids DB hits per request.
- Keys are redacted in logs (`tmk_abcd…`).

### 15.4 Rate limiting & resource limits
- Per-IP token buckets: login (above), ingestion auth failures (30/min ⇒ temporary 403 for 5 min),
  API (300 req/min per session/token).
- HTTP server timeouts: `ReadHeaderTimeout 10s`, `ReadTimeout 60s`, `WriteTimeout 120s` (SSE
  exempt via `http.ResponseController`), `IdleTimeout 120s`, `MaxHeaderBytes 64 KiB`.
- Body and decompression limits §7; JSON depth limit 64; query limits §10.3/§10.6.

### 15.5 Data protection
- Channel secrets (webhook URLs, SMTP password, HMAC secrets) encrypted at rest with
  AES-256-GCM using a key in `<data>/secret.key` (created 0600 on first start; Windows: ACL limited
  to Administrators/SYSTEM) or `TIDEMARK_SECRET_KEY_FILE`.
  Format (`auth.SecretBox`): `enc:v1:` + base64url(12-byte random nonce ‖ ciphertext+tag), with
  the prefix as additional authenticated data; empty stays empty.
- Secrets never returned by the API after creation (masked as `"••••"`).
- Audit log for: login success/failure, logout, password change, user/connection/token/stream/
  channel/settings changes, purge, export; teams and access: `team.create|update|delete`,
  `team.members`, `team.streams`, `user.teams` (§11.5, §11.2), `user.delete` with the user's
  `teams`, `transferredTo` and the numbers of transferred `dashboards` and `signals`, `stream.delete` with `revokedFromTeams`, `settings.update` with
  `access.denyByDefault`, `alert.create|update` with `streams`, `connection.create|update`
  with `allowedStreams`.

### 15.6 Outbound requests (SSRF)
Webhook/Slack/Teams URLs must be `https` (http allowed only with `ALLOW_PRIVATE_WEBHOOKS`);
DNS-resolved addresses in loopback, link-local, private (RFC 1918/4193), CGNAT, multicast or
metadata ranges (169.254.169.254) are rejected at dial time (custom `net.Dialer.Control`) unless
`TIDEMARK_ALLOW_PRIVATE_WEBHOOKS=true`. No redirects followed.

### 15.7 Web hardening
Headers on every UI/API response: `Content-Security-Policy: default-src 'self'; script-src 'self';
style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; frame-ancestors
'none'; base-uri 'none'; form-action 'self'` · `X-Content-Type-Options: nosniff` ·
`Referrer-Policy: no-referrer` · `Permissions-Policy: camera=(), microphone=(), geolocation=()` ·
`Cross-Origin-Opener-Policy: same-origin` · `Cross-Origin-Resource-Policy: same-origin` ·
`X-Frame-Options: DENY` (the legacy equivalent of `frame-ancestors 'none'` for browsers without
CSP level 2) · `Strict-Transport-Security` when `PUBLIC_URL` is https (§15.1).
No CORS on `/api/v1` (same-origin only). The SPA never uses `dangerouslySetInnerHTML`; markdown
panels are rendered with a sanitizing renderer that disallows raw HTML. Fonts are self-hosted.
The bundle needs no CSP exception: no inline `<script>`/`<style>`, no style attributes in HTML, no
external URLs, fonts as woff/woff2 files (checked by `web/scripts/check-csp.mjs` on every build).
Libraries that inject CSS at runtime (CodeMirror via style-mod) are routed through a
constructable stylesheet (`document.adoptedStyleSheets`) in the cascade layer `codemirror`, so
their rules apply without `'unsafe-inline'` and the design CSS keeps precedence; React `style`
props are set through the CSSOM and are not affected by `style-src`.

### 15.8 Injection
SQL only via parameters (§10.6) or validated identifiers (allow-list / regex). ClickHouse query
user is `readonly=2` without DDL/INSERT grants; the writer user is used only by pipeline,
migrations and admin services.

### 15.9 Supply chain & runtime
- **Supply-chain gate:** OSV-Scanner (`github.com/google/osv-scanner/v2`, Apache-2.0, build
  tooling only, pinned `v2.6.0`) checks `go.mod` and `web/package-lock.json` against OSV, which
  includes the OpenSSF malicious-packages feed (`MAL-` advisories) besides known vulnerabilities
  (Go findings are filtered by call analysis). Any finding fails it. It runs in the local gate
  (`vuln`), weekly and on dependency changes (`security.yml`), and as job `supply-chain` of
  `release.yml`, which every publishing job depends on, so no release is published with a known
  vulnerable or malicious dependency (`TestSupplyChainScanPinned` keeps the four pins equal).
- CI runs `govulncheck`, `gosec` (via golangci-lint), `npm audit --audit-level=high`,
  Trivy image scan and the Go licence allow-list (§19.1); dependencies pinned via lockfiles;
  Dependabot enabled. Vulnerabilities are fixed by upgrading (modules, Go toolchain, base images),
  never by ignore lists.
- **7-day rule:** dependency updates (Go modules and toolchain, npm packages incl. transitive ones,
  e.g. `npm install --before=<date>`, GitHub Actions, base images, pinned tools) take the newest
  stable release that has been public for at least 7 days, so a freshly published compromised
  version is usually detected and withdrawn before it is used. A version already pinned newer
  (e.g. for a security fix) is never downgraded.
- Container: distroless `nonroot` (UID 65532), read-only root FS, `no-new-privileges`, all
  capabilities dropped; data only in `/data` volume.
- Linux service runs as dedicated `tidemark` user with systemd hardening (`ProtectSystem=strict`,
  `NoNewPrivileges`, `PrivateTmp`, …). Windows service runs as `NT SERVICE\Tidemark` virtual account.

### 15.10 Data access by stream
Every read of telemetry data is limited to the caller's **stream scope** (`model.StreamScope`,
§8.1), enforced on the server. Access is granted to **teams** (`meta.Teams`, tables `teams`,
`team_members`, `team_streams`, §8.4): a team has members and is granted stream names; grants
are by name and may precede the data. Deleting a user or a team cascades to its memberships
and grants; deleting a stream revokes its grants (§12.7). Teams are exported and restored with
the rest of the metadata (`tidemark backup`/`restore`, `meta.ExportJSON`). A binary older than
migration 0007 ignores these tables (downgrade caveat: it shows every stream to everyone).

**Resolution** (`service.Access`, consumers depend on `service.ScopeResolver`): roles decide what
a user may *do*, teams decide which streams' data they *see*. For a principal:
1. the user's own role is `admin` (`auth.Principal.UserRole`; also through an API token whose
   scope limits `Role`) ⇒ every stream (mode `admin`);
2. member of at least one team ⇒ the union of the teams' grants (mode `teams`; a team without
   grants adds nothing, so a member of only such teams sees no stream);
3. no team ⇒ every stream (mode `default-all`), or no stream when the setting
   `access.denyByDefault` is true (mode `default-none`; settings key `access`, default false,
   §11.5).

Admins manage teams through `/teams` and `PUT /users/{id}/teams` (§11.5, §11.2); `GET /auth/me`
reports the caller's access. API tokens inherit the scope of their user. On the write side a
connection can be limited to some streams (`allowedStreams`, §9.1), so a team's ingest key cannot
write into another team's streams. The scope is computed from the metadata on every
request (one or two small queries), so membership, grant and setting changes apply to the next
request on every node without cache invalidation. A request without a user fails
(`service.ErrNoPrincipal`).
- `tql.Options.Streams` is required; compiling without it fails (`ErrInvalidOptions`), so no
  query path can forget the scope. An empty scope compiles to `AND 0`: the request succeeds with
  no rows (never a 5xx). Stream names only ever reach SQL as `?` arguments.
- The store reads that are not compiled TQL take the scope as a parameter (`store.Querier`
  `Fields`, `Event`, `Context`, `Trace`, `MetricNames`; `StreamAdmin.Mappings`), filters carry it
  (`store.Filter.Streams`), and the ClickHouse reader adds the same condition (`1`, `0` or
  `stream IN (?, …)`) to every fixed query, including the trace summary and the trace detail
  (spans **and** events). An unset scope fails with `store.ErrNoScope` before any query runs.
- System callers use fixed scopes: purge (admin) reads all streams; compile-only validation
  (`POST /query/validate`, rule validation) uses the empty scope because it reads no data; the
  alert evaluator reads each rule's stored streams (§14.1).

**Enforced paths** (`service.Search` resolves the scope through `SearchDeps.Access` and narrows
it by the request's selection, §11.3): `POST /query` (also every dashboard panel, which runs
through it), `/histogram`, `/facets`, `/fields`, `/tql/complete` (field names and values),
`/events/{id}` and `/context` (an invisible event is 404), `/export` (an empty scope yields an
empty file), `/tail` (scope re-resolved on every poll), `/traces` (summaries of visible spans
only), `/traces/{traceId}` (visible spans and events; 404 when none), `/metrics/names`,
`GET /streams` (visible streams only) and `GET /streams/{name}/fields` (404 outside the scope).
Stream administration, purge, imports, audit and system routes are admin-only and see every
stream. A failing scope lookup fails the request; it never falls back to all streams.

**Accepted exposures** (documented, not data): the names of indexed fields (`GET
/indexed-fields`) and the text of shared signals are visible to every viewer; the environment
selection is not a security boundary — keep production data in its own stream.

---

## 16. Web UI

### 16.1 Information architecture & routes
| Route | Screen |
|---|---|
| `/login` | Login; change-password prompt when `mustChangePassword` |
| `/` | Redirect to `/events` |
| `/events` | **Log explorer** (§16.3) |
| `/events/:id` | Event permalink (explorer with detail open) |
| `/traces`, `/traces/:traceId` | Trace search, trace waterfall |
| `/metrics` | Metric browser (name search, quick chart, "add to dashboard") |
| `/dashboards`, `/dashboards/:id`, `/dashboards/:id/edit` | Dashboards list / view / edit |
| `/alerts`, `/alerts/:id`, `/alerts/new` | Alert rules, state, history; `:id` / `new` opens the editor drawer |
| `/streams`, `/streams/:name` | Index management: stats, retention, mappings, purge |
| `/settings/connections` | Connections & keys, snippets |
| `/settings/users`, `/settings/tokens`, `/settings/channels`, `/settings/audit`, `/settings/system` | Admin (`/settings/users` also holds the caller's own account / password change for every role, and shows which streams the caller sees §15.10) |
| `/settings/teams`, `/settings/teams/:id` | **Teams** (admin, §15.10): the deny-by-default switch and the teams table; a team's details, members and streams |
| `/settings/import` | Import jobs and new imports (§9.9); the nav entry appears only when `GET /imports` answers neither 404 nor 405 |
| any other path | "Not found" page inside the shell |

Global shell: collapsible left nav, top bar with the **scope picker** (streams and environment,
§16.6; on Events, Traces, Metrics and dashboard views), the global time picker (relative presets
+ absolute range, synced to URL), theme toggle (dark default, light, system), user menu. All
explorer state (q, from, to, streams, env, signals, selected event) is in the URL for shareable
links. A user who can see no stream (§15.10) gets, instead of those views, the empty state "You
don't have access to any streams yet · Ask an admin to add you to a team", and no query is sent.

### 16.2 Visual design system
**Normative:** `web/src/styles/tokens.css` is a **normative part of this specification**; every
colour, font, size, spacing, radius and shadow of the UI comes from its CSS custom properties
(`--tm-color-*`, `--tm-level-*`, `--tm-chart-*`, `--tm-font-*`, `--tm-text-*`, `--tm-space-*`,
`--tm-size-*`, `--tm-radius-*`, `--tm-shadow-*`), and changes to it are spec changes. Dark is
`:root` / `[data-theme="dark"]`, light is `[data-theme="light"]` on `<html>`; the `system` option
follows `prefers-color-scheme`. Components style only through `var(--tm-…)`.

**Principles.** Familiar workflow, own identity: query bar → time picker → volume histogram →
event stream → expandable properties → facets → saved filters, as Kibana/Seq users know it, but
OpenTidemark **must not** reuse their names, logos, icons, layouts or brand colour schemes. Dark
first and calm: a deep ink/slate base, colour reserved for meaning (levels, status, one accent),
quiet chrome. Dense but readable: 13 px UI text, 12 px monospace data, rows 28 px compact / 36 px
comfortable (a per-user choice). Monospace for anything that came from telemetry (timestamps,
levels, services, messages, properties, TQL), sans for chrome. Meaning is never colour alone:
every level, status and chart series also has a text label or legend.

**Typography.** IBM Plex Sans (chrome, 400/500/600) and JetBrains Mono (data and TQL,
400/500/600), both OFL, self-hosted (CSP `font-src 'self'`), fallbacks `"Segoe UI", system-ui,
sans-serif` and `"Cascadia Mono", ui-monospace, monospace`; tabular figures in numeric cells.
Scale 28 / 22 / 18 / 15 / 13 (default) / 12 / 11 / 10 px plus mono 12 px; column headers are
11 px 600 uppercase. TQL highlighting: fields in `info`, operators and parentheses `fg-muted`,
keywords `accent` 600, strings `warning`, numbers/durations/`$variables` chart colour 4,
aggregate functions chart colour 5, pipes `fg-subtle`, errors a wavy `danger` underline.

**Layout & shape.** 4 px spacing scale (2–64 px); top bar 48 px, left nav 208 px (56 collapsed),
explorer rail 264 px, drawers 480 px (alert editor 520), controls 32 px (small 26); dashboard grid
of 12 columns, row 40, gap 12. Radii 2 (bars) / 4 (chips, tags) / 6 (buttons, inputs, query bar)
/ 8–10 (cards, popovers) / pill (signal and status chips, toggles). Three elevation levels
(hover; popovers and menus; drawers, modals, autocomplete, toasts) and a focus ring of a 2 px
canvas gap plus a 2 px accent ring on every focusable element.

**Components.** Level chips are fixed-width 3-letter mono labels (`VRB DBG INF WRN ERR FTL`) on
tinted backgrounds, Fatal being the only filled chip, with the full name in `aria-label`; signal
chips are pills that turn accent-soft when on; status chips (OK, Pending, Firing as a solid danger
fill, Muted, Error, Info). Buttons: primary (accent fill), default, ghost, danger outline and
danger solid (final destructive confirmation only); icon-only buttons need an `aria-label`. The
query bar is a 36 px mono input with idle / focused (accent border + soft halo) / error (danger
border, squiggle, popover with "did you mean" fix) / free-text (warning border and notice)
states and an autocomplete popover listing kind, label and detail. Event rows are a grid of
timestamp · level chip · service · message (user-chosen columns before the message);
template property values are highlighted in accent on accent-soft; the selected row has an inset
accent bar and expands into an inline detail panel. Further patterns: facet rail with
proportional bars, level-stacked histogram with an accent brush, time picker popover with
relative presets and absolute range, toasts bottom-right (status-coloured left border; errors
persist), a non-dismissable danger banner for default credentials, right-anchored drawers that
become sheets below 900 px, icon + title + hint empty states, cards and dashboard panels on the
raised surface, and stat tiles with a mono value and sparkline.

**Brand & icons.** The logo mark is an abstract tide mark: a rounded square with a waterline wave
and a short vertical mark above it (accent tile with ink glyph on dark surfaces, ink tile with
accent glyph on light ones; `web/src/components/Logo.tsx`, `web/public/favicon.svg`); the wordmark
is "OpenTidemark" in IBM Plex Sans SemiBold. Icons: Lucide (ISC) via `lucide-react`, 16 px in
chrome, stroke 1.75, one icon set only.

Key tokens (all text/background pairs meet WCAG 2.1 AA in both themes; chart colours ≥ 3:1
against the surface):

| Role | Dark | Light |
|---|---|---|
| canvas / surface / raised / overlay | #0B0F14 / #11161D / #171E27 / #1D2631 | #F3F5F8 / #FFFFFF / #FFFFFF / #FFFFFF |
| border subtle / default / strong | #1C2530 / #27333F / #3A4859 | #E6EAEF / #D2D9E1 / #AAB5C2 |
| fg / muted / subtle | #E6EBF0 / #A3AFBC / #8593A1 | #121922 / #4A5666 / #65717F |
| accent "phosphor" / soft / on-accent | #B8F06A / #1D2A17 / #0B0F14 | #3F6B12 / #EAF6D9 / #FFFFFF |
| link | #8FD3FF | #0B63A8 |
| success / warning / danger / info | #5FD39A / #F3B945 / #FF7B70 / #6CC4FF | #1D7A4B / #8A5A00 / #B42318 / #0B63A8 |
| level verbose fg/bg | #97A4B2 / #1B222B | #5A6675 / #EEF1F4 |
| level debug | #AFBCFF / #1D2440 | #3F52B0 / #E8ECFC |
| level information | #6CC4FF / #0F2A40 | #0B63A8 / #E3F1FC |
| level warning | #F3B945 / #352810 | #8A5A00 / #FFF2D6 |
| level error | #FF7B70 / #3F1A1B | #B42318 / #FDE8E6 |
| level fatal (filled chip) | #FFE6EE / #8C1538 | #FFFFFF / #9B1140 |
| chart 1–8 | #B8F06A #6CC4FF #F3B945 #C3A8FF #FF8FB4 #5FD39A #FF9D66 #97A4B2 | #5C9418 #1A7FD0 #C47F00 #7A55D6 #D63D72 #1D8F5C #D9641C #6B7A8A |

Breakpoints and keyboard behaviour are in §16.5 and §16.6.

### 16.3 Log explorer
- Query bar (CodeMirror, TQL highlighting, autocomplete §10.7 narrowed to the selected streams,
  inline error squiggle + message, history ↑/↓, `Ctrl/Cmd+Enter` run, `?` cheat-sheet), scope
  picker and time picker, **Live** toggle (SSE tail), "Save as signal", "Add to dashboard",
  export. Every request (query, histogram, facets, fields, tail, export, completion) carries the
  selected streams and environments (§11.3).
- Histogram (stacked by level, brush to zoom time range).
- Left rail: signals (toggle chips, grouped), field facets with top values and counts
  (click = include, alt-click = exclude), field list with search.
- Event stream: virtualized list (≥ 100k rows smooth), columns timestamp · level chip · service ·
  rendered message with property values highlighted from the template; user-chosen columns
  before the message: any property, or the built-ins `environment`, `host`, `trace_id`,
  `span_id` and `event_type` (a built-in name always means the built-in field); each column
  header has a *Remove column* × button; the Columns dialog lists the shown columns in order
  (move up / down, remove — a shown field stays listed even when the range has no such field)
  and the other fields to add, with a filter above 12; shown columns are 140 px wide, shrink
  to 72 px when space runs out and then the list (header and rows together) scrolls sideways;
  saved per browser in localStorage (`tm.columns.logs`); infinite scroll via cursor.
- Event detail (inline expand): properties table with actions *filter =*, *exclude*, a labelled
  *Show in column* / *Hide column* toggle (properties and the built-ins above), *copy*; exception with collapsible stack trace; trace/span links → trace view; *context* (events
  before/after); raw JSON toggle; permalink.
- When query kind is `table`/`timeseries`, the stream area shows a table/chart with "Add to
  dashboard".
- An empty result shows *No events match* with a *Last 24 hours* action. When the streams list
  (`GET /streams`, cached) shows no log rows in any stream the caller can see, it shows **No data
  yet** instead: admins get a hint about connection keys and the installers' `bootstrap`
  connection (`TIDEMARK_BOOTSTRAP_INGEST_KEY`), a link to `/settings/connections` and an external
  link to the ingestion documentation (new tab, `noopener noreferrer`, nothing is loaded from it);
  other roles get *Ask an administrator to connect a log source.*

### 16.4 Other screens
Trace waterfall (spans by service colour, critical path, errored spans highlighted, span detail
drawer, linked logs; a span whose parent is in a stream the caller cannot see is drawn as a root
with the tag "parent span not visible", also in the span drawer); dashboards (grid, variables
bar, panel editor §13.2; panels and variable values read through the scope picker's selection);
alerts list + editor drawer with live preview chart & threshold line and a **Streams** field
(§14.1: "All streams" for callers who see every stream, else a required choice among the visible
streams; a new rule defaults to the editor's whole scope; the field feeds the query bar's
completions); streams (stats cards, retention editor, level rules, mappings, indexed fields,
purge with dry run & typed confirmation); connections (table with edit / rotate / revoke /
delete; create wizard: name → protocols → stream & **allowed streams** (§9.1: optional, must
include the default stream, else the step cannot continue) → restrictions → key shown once +
copyable snippets; an edit dialog with the same fields; the restrictions column lists the
allowed streams); users (Teams column; the edit dialog assigns teams through
`PUT /users/{id}/teams` when they changed; the account card states what the caller sees: every
stream as admin, the streams of their teams, every stream while in no team, or none); API
tokens (note that a token sees the same streams as its owner); **Teams** (§15.10): an access
card with the deny-by-default switch, its consequence spelled out, the number of users in no team
and a confirmation before turning it on while such users exist; a teams table (name, description,
members, streams with "none · members see nothing" for a team without grants, updated); "New
team" dialog (name 1–64, description); the team page with three cards that save on their own and
show *Unsaved changes* / *Saved* — Details (rename, description, "Delete team" with typed
confirmation), Members (searchable multi-select of users) and Streams (multi-select of known
streams plus typed names validated against §8.1 and marked "no data yet").

### 16.5 Accessibility & responsiveness
WCAG 2.1 AA contrast in both themes; full keyboard navigation (`/` focuses query bar, `j/k`
moves between events, `Enter` expands); visible focus rings; level conveyed by text + colour;
works from 360 px wide (explorer collapses rails into sheets).

### 16.6 Implementation decisions
- **Composition root** `web/src/main.tsx` builds the services once (API client, live-tail
  transport, preference store, series palette, clock) and provides them by context; components
  use only those interfaces, so tests and mock mode substitute fakes. Server state lives in
  TanStack Query; per-browser preferences in `localStorage` keys `tm.theme`, `tm.density`,
  `tm.history`, `tm.columns.logs`, `tm.nav.collapsed`, `tm.passwordPrompt`, `tm.scope` (every
  access guarded).
- **Scope picker** (`web/src/app/ScopePicker.tsx`, `scope.ts`): the selection lives in the URL
  (`streams`, `env`, comma lists; capped at the §11.3 limits) and the last applied choice in
  `tm.scope`, which is re-applied when a scoped route is opened without either parameter (so
  the choice follows from Events to Traces and survives a reload; "Reset" stores the empty
  choice). The picker offers only the caller's visible streams (`me.streams`, or every name of
  `GET /streams` for `allStreams`), a filter input above 8 streams, "All my streams", and the
  environments of the last 24 hours from the `environment` facet of the selected streams
  (marked *logs only*); a selection from a link is narrowed to the visible streams and never
  widens access. A caller with exactly one visible stream sees a static chip. Below 600 px the
  trigger is an icon button with a count badge that opens the panel as a bottom sheet.
  Accessibility: `aria-haspopup="dialog"`, `aria-expanded`, a label naming the current selection,
  `fieldset`/`legend` groups, arrow keys between rows, Escape closes and returns focus to the
  trigger; no new global hotkey.
- **Multi-select** (`web/src/components/MultiSelect.tsx`): chips with remove buttons, a
  checklist popover (filter above 8 items, arrow keys, Escape returns focus) and an optional
  validated free-text entry; used for team members, team and connection streams, user teams and
  alert streams.
- **API client:** JSON in/out, `credentials: same-origin`, header `X-Tidemark-CSRF` = cookie
  `tm_csrf` on every non-GET/HEAD/OPTIONS request, 30 s timeout (uploads 1 h), errors as
  `ApiError{status, code, message, details}` with client codes `network`, `timeout`, `http_<n>`;
  a 401 outside the login/me probe clears the session and routes to `/login?next=<path>` (only
  same-origin paths are accepted as `next`). `invalid_query` byte offsets (`pos`/`len`) and
  completion ranges are converted between UTF-8 bytes and UTF-16 code units.
- **Session:** the default-credentials banner (`me.defaultCredentials`) cannot be dismissed and
  links to the account form; after a sign-in with `mustChangePassword` the login page offers the
  change ("Not now" allowed, §15.2) and a toast repeats the prompt once per sign-in.
- **Explorer:** query bar = CodeMirror with token classes from a tolerant client tokenizer (the
  server stays the parser), squiggle + popover with "did you mean" fix from `invalid_query`
  details, completion from `GET /tql/complete`, history ↑/↓; Live = `EventSource` on `/tail`
  (events / dropped / error events); stream rows virtualized with `@tanstack/react-virtual`;
  event-detail row actions are transparent until hover or keyboard focus (always visible on
  touch devices, with 28 px targets) so they stay in the tab order; they float over the end of
  the row's first line with a fade on their left edge so a long value reads as cut off, not
  missing; phones (event cards without columns) do not offer *Show in column* and hide the
  hover hint.
- **Shortcuts:** `/` focuses the query bar, `?` opens the shortcut help, `g` then
  `e|t|m|d|a|s` navigates, `j`/`k` move between events and `Enter` expands; all are ignored while
  typing in a control or while a modal is open.
- **Alerts:** with the editor drawer open the rules table keeps only state, rule, query and
  condition (the condition is never clipped); recent transitions are one grid row per entry
  (wrapping text grows the row, rows never overlap). The preview chart (rule query with `span <every>` over the last 3 h) draws lines when the
  rule is grouped, a dashed threshold line, and shades buckets where any group meets the
  condition.
- **Import page** follows the `/imports` API (§9.9, §11.5): JSON body `{kind, name?, config}` for
  `elastic`/`seq`; multipart with the `job` part first and one `file` part per file for
  `clef-file` (1–16 files ≤ 1 GiB) and `kibana` (one file ≤ 64 MiB); progress =
  `rowsImported / rowsTotal` when `rowsTotal > 0`; warns when the chosen range starts before the
  target stream's retention.
- **Responsive:** below 600 px the nav becomes a bottom tab bar, rails and drawers become sheets,
  the time picker shows its short label; layouts are verified at 390 px.
- **Mock mode** (`vite build --mode mock`, never in the production bundle — enforced by the CSP
  check): MSW serves deterministic demo data shaped exactly like §11 for Playwright e2e tests and
  design-review screenshots, including teams, the deny-by-default setting, every seeded user as
  a login (password ≥ 12 characters) and the §15.10 scope on every search path (events, spans
  and trace summaries filtered by stream, by-id routes 404 outside the scope).

---

## 17. Platform support & deployment

### 17.1 Supported platforms
| Platform | OpenTidemark binary | ClickHouse |
|---|---|---|
| Linux x86-64 / arm64 | native (systemd) or Docker | native packages or Docker |
| Windows 10/11, Server 2019+ x86-64 | native (`tidemark.exe`, Windows service) or Docker Desktop | Docker Desktop (WSL2 backend) |
| macOS (dev only) | native | Docker |

All code paths must be OS-neutral: `filepath` for paths, no shelling out, no CGO, no
Unix-only syscalls outside `_linux.go`/`_windows.go` files. CI builds and tests on
`ubuntu-24.04` **and** `windows-latest` (§17.4).

### 17.2 Docker Compose (default, Linux & Windows)
`deploy/docker-compose.yml`: services `clickhouse` (25.8, volume `ch-data`, server settings from
`deploy/clickhouse/config.d/tidemark.xml` and `docker-listen.xml`, users from
`deploy/clickhouse/users.d/tidemark.xml` with passwords from `.env`), `tidemark` (image built from
`Dockerfile`, ports 8080, 5341, 9200, 4317, 4318; volume `tm-data`; depends_on clickhouse healthy),
optional profile `collector` (OTel Collector: syslog 5514 tcp/udp, fluent forward 24224).
`deploy/.env.example` documents all variables; `install.sh`/`install.ps1` generate random
ClickHouse passwords into `.env` on first run.

Normative details:
- ClickHouse server settings: `config.d/tidemark.xml` (shared with native installs, §17.3) sets
  the log level `warning` on the console, removes `mysql_port` and `postgresql_port`, disables
  ClickHouse's crash reports (`<send_crash_reports><enabled>false</enabled></send_crash_reports>`,
  the 25.8 setting) and declares the `backups` disk; it does **not** set `listen_host`, so a
  native server keeps ClickHouse's loopback default. `config.d/docker-listen.xml` (mounted only by
  this Compose file) adds `<listen_host>0.0.0.0</listen_host>` so the `tidemark` container
  reaches ClickHouse over `backend`; it is not part of the native archives (§17.6).
- Networks: `backend` is `internal: true` (ClickHouse only, never published, §15.1); `tidemark` is
  on `backend` + `frontend`; the collector only on `frontend` and exports to `http://tidemark:4318`
  with `Authorization: Bearer $COLLECTOR_INGEST_KEY` (default: `TIDEMARK_BOOTSTRAP_INGEST_KEY`).
- `tidemark` receives every variable of `.env` (`env_file`, optional) plus the fixed
  `TIDEMARK_CLICKHOUSE_ADDR=clickhouse:9000` and `TIDEMARK_DATA_DIR=/data`; the ClickHouse
  passwords are required (`${VAR:?}`). Compose-only settings: `TM_IMAGE` (default `tidemark:local`,
  built from `../Dockerfile` with `TM_VERSION`), `TM_BIND_ADDR` (default `0.0.0.0`) for all
  published ports. Container hardening per §15.9 (`read_only`, tmpfs `/tmp`, `no-new-privileges`,
  `cap_drop: [ALL]`) for `tidemark` and the collector.
- Health checks: ClickHouse `clickhouse-client … SELECT 1` as the reader; OpenTidemark runs
  **`tidemark healthcheck [--url URL]`** (the distroless image has no shell): GET the URL (default
  `http://127.0.0.1:8080/healthz`, http/https only, 5 s timeout) and exit 0 only on 200. The
  Dockerfile declares the same `HEALTHCHECK`. With built-in TLS pass `--url https://…`.
- `.env.example`: placeholders `generate-me` (replaced by 256-bit hex secrets) and a commented
  `#TIDEMARK_BOOTSTRAP_INGEST_KEY=` line (replaced by a generated `tmk_` key, §15.3); every
  `TIDEMARK_*` variable of §7 must appear (commented or not) and no unknown one (unit test).
  `install.sh docker` / `install.ps1 -Mode Docker` write `.env` (0600 / ACL: current user,
  Administrators, SYSTEM) only when absent, then `docker compose up -d --build` (skip with
  `--no-start` / `-NoStart`).
- `deploy/docker-compose.dev.yml` (used only by `make dev`) publishes ClickHouse on
  `127.0.0.1:${TIDEMARK_DEV_CLICKHOUSE_PORT:-19000}`.
- Dockerfile: `node:24-bookworm-slim` builds `web/` (skipped without `package.json`),
  `golang:1.27.2-bookworm` (same Go release as the `toolchain` directive, §5) builds with `CGO_ENABLED=0` and copies `LICENSE`, `NOTICE`,
  `THIRD_PARTY_NOTICES.md` to `/licenses/`, runtime `gcr.io/distroless/static-debian12:nonroot`
  (UID 65532, `/data` volume, `TIDEMARK_DATA_DIR=/data`, entrypoint `tidemark`, cmd `serve`).
  Both build stages run on the build platform (`FROM --platform=$BUILDPLATFORM`) and the Go
  stage cross-compiles with `GOOS=$TARGETOS GOARCH=$TARGETARCH` (BuildKit's automatic build
  arguments), so the multi-arch release image (§17.6) needs no emulation for npm or the Go
  compiler; build argument `VERSION` (default `dev`) is stamped with `-X main.version`.
- Published image (§17.6): `ghcr.io/zeraorg/tidemark:<version>` (also `<major>.<minor>` and
  `latest` for releases without a pre-release suffix), `linux/amd64` + `linux/arm64`. Compose
  uses it when `.env` sets `TM_IMAGE` to it and the stack is started without building
  (`docker compose pull tidemark`, then `docker compose up -d --no-build`); in a source tree
  `install.sh docker` / `install.ps1 -Mode Docker` build the image. The Docker bundle of a
  release (§17.6) holds a copy of this Compose file without the `build:` block whose image
  defaults to `ghcr.io/zeraorg/tidemark:<version>`, so the same installers pull instead of
  building there (`docker compose up -d --build` with nothing to build pulls missing images).

### 17.3 Native installs
- **Linux:** `sudo install.sh native <binary> [--start]` creates user `tidemark`,
  `/var/lib/tidemark` (0750), `/etc/tidemark/tidemark.env` (root:tidemark 0640, from
  `.env.example` with generated secrets and `TIDEMARK_CLICKHOUSE_ADDR=localhost:9000`, only when
  absent), installs the binary to `/usr/local/bin/tidemark` and `deploy/tidemark.service`
  (hardened: `ProtectSystem=strict`, `NoNewPrivileges`, `PrivateTmp`, `SystemCallFilter=@system-service`,
  no capabilities; `MemoryDenyWriteExecute` is **not** set: it was incompatible with the earlier
  run-time-compiled SQLite build and has not yet been verified with the translated one).
- **Windows:** `install.ps1 -Mode Native -Binary <tidemark.exe> [-OpenFirewall] [-Start]` (as
  Administrator) copies `tidemark.exe` to `%ProgramFiles%\Tidemark`, writes config to
  `%ProgramData%\Tidemark\tidemark.env` (same rules as Linux), runs `tidemark.exe service install`
  (service name `Tidemark`, account `NT SERVICE\Tidemark`, delayed auto start, recovery: restart
  on failure), restricts `%ProgramData%\Tidemark` to Administrators, SYSTEM and the service
  account, and opens inbound TCP 8080, 5341, 9200, 4317, 4318 only if `-OpenFirewall` is passed.
- ClickHouse for native installs is set up separately (native packages or Docker) with the users of
  `deploy/clickhouse/users.d` and the passwords from `tidemark.env`. The users read their
  passwords from the ClickHouse server's environment (`from_env`), so `docs/installation.md`
  passes them in as follows. Native packages use a file with the two password lines, loaded by a
  `clickhouse-server` systemd drop-in (`EnvironmentFile=`), the same mechanism as `deploy/ha/`. A
  container uses `-e` with the native port published on `127.0.0.1:9000` only, which is also the
  Windows setup (Docker Desktop; the image's own configuration listens on all container
  interfaces, so `docker-listen.xml` is not needed there). `config.d/tidemark.xml` leaves the
  listen address at ClickHouse's default, so a native ClickHouse on the same host listens on
  loopback only; the guide explains how to add a private `listen_host` (and a firewall rule) when
  OpenTidemark runs on another host.
- `tidemark` reads `TIDEMARK_ENV_FILE` (default `<data>/tidemark.env` if present) before env vars.

### 17.4 Build
- `make build` / `.\build.ps1 build`: `npm ci && npm run build` in `web/`, then
  `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" ./cmd/tidemark`.
  `$VERSION` defaults to `git describe --tags --always --dirty` (or `dev`) for development builds;
  release archives take it from the `VERSION` file instead (`dist`, §17.6).
- `Makefile` and `build.ps1` expose the same targets: `build`, `web` (copies `web/dist` into
  `internal/web/assets/dist`), `cross` (the three release targets into `bin/`), `dist` (release
  archives and `checksums.txt` into `dist/`, §17.6), `checksums` (rewrites `dist/checksums.txt`),
  `docs` (`mkdocs build --strict` into `site/`), `docs-offline` (the offline documentation
  bundle `dist/tidemark_<version>_docs.zip`, then `checksums`, §17.6), `test` (`-race` on
  Linux/macOS; plain on Windows, where the race detector needs CGO), `test-integration` (Makefile:
  through `scripts/with-clickhouse.sh`, §18; build.ps1 needs `TIDEMARK_TEST_CLICKHOUSE_ADDR`),
  `lint` (gofmt, `go vet` with and without the `integration` tag, golangci-lint for both tags and
  for the other OS), `vuln` (govulncheck and OSV-Scanner, §15.9), `web-check`, `check` (= lint + test + vuln + web-check), `licences`
  (Go licence allow-list, §19.1), `notices` (runs `licences` first), `dev`, `clean` (also removes
  `dist/`), `help`. Every
  web step is skipped while `web/package.json` does not exist. `web-check` = `npm ci`, `lint`,
  `format:check`, `typecheck`, `test:coverage`, `build`, `check:csp`, `npm audit --audit-level=high`.
- Workflow guard: every job of `ci.yml` and `security.yml` (and the automatic path of
  `release.yml`, §17.6) runs automatically only in the main public repository `ZeraOrg/opentidemark`
  (`github.repository == 'ZeraOrg/opentidemark'`; GitHub compares strings case-insensitively) and
  elsewhere (forks, the maintainers' development repository) only on `workflow_dispatch`, so
  those repositories get at most runs whose jobs are all skipped (no runner time). GitHub has no
  workflow-level condition, so every job carries the guard. Every job sets `timeout-minutes`;
  `actions/setup-go` caches modules and build output, `actions/setup-node` caches npm
  (`cache: npm`, `web/package-lock.json`).
- CI (`.github/workflows/ci.yml`; push to main, pull requests, `workflow_dispatch`; concurrency
  `ci-<ref>`, cancelling superseded pull-request runs only, so a run of `main` always finishes):
  - job `go` on `ubuntu-24.04` and `windows-latest`: gofmt, `go vet` and golangci-lint for both
    tags, unit tests (`-race` on Linux), and on Linux govulncheck and a `linux/arm64` build (the
    one release target the two native jobs do not compile);
  - job `integration` on Linux with a `clickhouse/clickhouse-server:25.8` service container whose
    access-management admin creates `tidemark_writer` / `tidemark_reader` through SQL with exactly
    the grants of `deploy/clickhouse/users.d` (enforced by `internal/repotest`);
  - job `notices`: `make notices` (which runs the Go licence allow-list `make licences` first)
    must leave `THIRD_PARTY_NOTICES.md` unchanged;
  - job `web`: every web gate (`npm ci`, lint, format, typecheck, unit tests with coverage,
    production build, CSP check, the Go embed check with `web/dist` copied to
    `internal/web/assets/dist` (`go build ./...`, `go test ./internal/web/... ./internal/repotest/`),
    `npm audit --audit-level=high`, the mock build and Playwright e2e on Chromium); each check
    runs even after an earlier failure (`!cancelled()`), so one run reports all of them, and
    failures are published as check-run annotations (`web/scripts/ci-annotate.mjs`); screenshots
    and the Playwright report are uploaded as artifact `playwright` (14 days);
  - job `release` (needs the four jobs above; only for a push to `main` of the main public
    repository) calls `release.yml` (§17.6).
- `security.yml` (weekly schedule, `workflow_dispatch`, and pushes to main touching `go.mod`,
  `go.sum`, `web/package.json`, `web/package-lock.json`, `Dockerfile` or the workflow; not on
  pull requests; concurrency `security-<ref>`): govulncheck, OSV-Scanner (§15.9), `npm audit --audit-level=high`, and a
  Trivy scan (HIGH/CRITICAL, fixed only) of the image built from the Dockerfile.
  `.github/dependabot.yml`: gomod, npm (`/web`), GitHub Actions, Docker, **weekly on Monday**,
  all four in **one** pull request (multi-ecosystem group `dependencies`, `patterns: ["*"]`), so
  a week of dependency updates costs a single CI run. Updates follow the 7-day rule of §15.9
  when they are applied. The npm entry ignores `typescript >= 6.1.0` while typescript-eslint
  supports only TypeScript < 6.1 (the update would fail on the peer dependency every week);
  remove the rule when typescript-eslint supports TypeScript 7. It also ignores `msw >= 3.0.0`
  (Vitest's mocker needs msw 2; remove the rule when Vitest supports msw 3) and major updates of
  `@types/node`, and the Docker entry ignores major updates of the `node` image, so both stay on
  the Node LTS line of the build (Node 24) until it is moved deliberately.
- **Documentation site** (`mkdocs.yml`, `docs-site/`, `.github/workflows/docs.yml`): MkDocs
  `1.6.1` (BSD-2-Clause) with Material for MkDocs `9.7.7` (MIT), every transitive dependency
  pinned with hashes in `docs-site/requirements.txt` (generated from `docs-site/requirements.in`
  with pip-tools; all under licences of the §19.1 allow-list; build tooling only, never shipped),
  builds `docs/` into `site/` (untracked) for <https://zeraorg.github.io/opentidemark/>. Site name
  OpenTidemark, keyword-rich `site_description`, repository and edit links, navigation: home
  (`docs/index.md`, the landing page), documentation overview (`docs/overview.md`), quick start, installation, sending data, query language, configuration,
  migration from Seq and from Elasticsearch/Kibana, and the section **Operations manual** with
  the fourteen chapters of `docs/manual/` in this order: Overview (`index.md`), Deployment,
  Security hardening, Users and access, Ingestion, Integrations, Metrics, Streams and retention,
  Querying, Dashboards, Alerting, Operations, API for automation, Guide for agents
  (`internal/repotest`, `TestManualPagesInNav`, fails when a chapter is missing from the
  navigation). The manual is written for operators and automation agents from this
  specification and the user docs, with procedures verified against a test installation; it
  links to the user docs instead of repeating them, names third-party products referentially
  with ® at the first prominent OpenTelemetry, Prometheus and ClickHouse mention of a page
  (§19.1), and contains no maintainer notes. Only the top-level pages, `docs/manual/` and
  `docs/images/` are part of the site (`docs-site/hooks.py` drops other subdirectories of
  `docs/`; `exclude_docs` cannot, because its patterns also match the theme's own files).
  Markdown extensions: admonitions, attribute lists (explicit heading ids `{#id}`), tables, toc
  permalinks, `pymdownx.highlight`, `pymdownx.superfences`, `pymdownx.tabbed` (`alternate_style`,
  content tabs such as Linux / Windows / Docker) and `pymdownx.tasklist` (`custom_checkbox`,
  checklists). Material features: instant navigation,
  sections, back-to-top, footer, followed table of contents, search with suggestions,
  highlighting and sharing, code copy, linked content tabs (`content.tabs.link`); light/dark
  palette following the system with the colours
  of `web/src/styles/tokens.css`; `font: false` (no web-font requests); no third-party origin is
  requested by any page. MkDocs writes
  `sitemap.xml`. `docs-site/hooks.py` sets each page's meta description to its summary paragraph
  (the first paragraph after the H1, §19.3) unless the page sets `description` in front matter,
  resolves relative links against the page and rewrites only those that leave `docs/`
  (`../SECURITY.md` from a top-level page, `../../deploy/ha/README.md` from the manual) to the
  file on GitHub (`main`), so they work both on GitHub and on the site, copies
  `web/public/favicon.svg` into the site as logo and favicon, and writes two plain-text files
  for agents and language models to the site root: `llms.txt` (an index with the title, summary
  paragraph and address of every page, grouped by navigation section) and `llms-full.txt` (the
  Markdown of every page in navigation order, HTML comments removed, relative links made
  absolute, each page preceded by a `Source:` line); the manual's agent guide names them.
  `docs-site/overrides/main.html` adds Open Graph and Twitter card tags (title,
  description, canonical URL, and as image `docs/images/social-preview.png` with a large-image
  card) and the colours. `docs-site/overrides/home.html` (extends `main.html`) is the template of
  the landing page `docs/index.md` (front matter `template: home.html`, navigation and table of
  contents hidden): inline styles only, on the token colours in both palettes, no scripts, and
  without the edit and source buttons. The landing page's text, links and screenshots stay
  ordinary Markdown (classes through attribute lists), so its summary paragraph is the meta
  description and it appears in `llms.txt` and `llms-full.txt` like any page: a hero with the
  summary, calls to action (documentation overview, quick start, source) and the 0.x status, the
  explorer screenshot, then supported clients, capabilities with screenshots, the trust points of
  the README, the three Docker Compose steps of the quick start, and links into the
  documentation. Because the SPDX comment is a page's first line, `hooks.py` reads front matter
  that follows it (MkDocs reads front matter only at the start of a file) into the page's
  metadata and removes it from the page, `llms.txt` and `llms-full.txt`. The social preview (1280×640 PNG, under 1 MB) shows the logo mark, the
  name, the tagline "Self-hosted logs, metrics & traces", a compatibility line and a cropped
  explorer screenshot of the mock build on the dark token colours with the bundled IBM Plex Sans,
  key content inside the central safe area; it was rendered from an HTML page with Playwright
  and is also uploaded by hand as the public repository's social preview (GitHub has no API for
  it). Link, anchor and omitted-file validation is set to
  `warn`, so `mkdocs build --strict` fails on any broken link or anchor.
  Workflow `docs.yml` (pushes to `main` and pull requests touching `docs/**`, `docs-site/**`,
  `mkdocs.yml`, `web/public/favicon.svg` or the workflow, and `workflow_dispatch`; concurrency
  `docs-<ref>`, cancelling superseded pull-request runs only): job `build` (guard as above,
  `timeout-minutes: 10`; Python 3.13 with the pip cache keyed on `docs-site/requirements.txt`;
  `pip install --require-hashes`; `mkdocs build --strict` through `scripts/ci-annotate.sh`; the
  Pages artifact of `site/`) and job `deploy` (needs `build`; only in `ZeraOrg/opentidemark` for
  `refs/heads/main` on push or `workflow_dispatch`; `timeout-minutes: 10`; permissions
  `pages: write`, `id-token: write`; environment `github-pages`). The repository's Pages source
  must be "GitHub Actions".
- Workflows avoid redundant runs: no per-branch or per-PR runs of expensive jobs (Windows, image
  build) without need; contributors verify locally (`make check`, `make test-integration`)
  before pushing.
- Pinned CI versions: actions `actions/checkout@v7`, `actions/setup-go@v7` (reads the `toolchain`
  directive, so CI uses exactly the Go release of §5), `actions/setup-node@v7`,
  `golangci/golangci-lint-action@v9`, `actions/upload-artifact@v7`, `actions/download-artifact@v8`
  (all on the Node 24 runtime), and for releases (§17.6) `actions/attest-build-provenance@v4`,
  `docker/setup-qemu-action@v4`, `docker/setup-buildx-action@v4`, `docker/login-action@v4`,
  `docker/build-push-action@v7`, and for the documentation site `actions/setup-python@v7`,
  `actions/upload-pages-artifact@v5`, `actions/deploy-pages@v5`; golangci-lint `v2.14.0`,
  govulncheck `v1.9.0`, OSV-Scanner `v2.6.0`, go-licenses `v2.0.1` (Makefile/build.ps1), Trivy `0.75.0`
  (`aquasec/trivy` image). Linux jobs run on `ubuntu-24.04` rather than `ubuntu-latest`, so the
  runner image changes (e.g. the announced move of `ubuntu-latest` to Ubuntu 26) only through a
  reviewed commit.
- `scripts/ci-annotate.sh "<title>" -- <cmd> [args…]` wraps CI steps whose output matters: it
  prints the output and, when the command fails, also publishes its last 9 × 4000 bytes (split on
  line boundaries, ANSI colours removed) as warning annotations `<title> failed (exit N) i/n`, so
  failures can be read from check-run annotations without log access (warning level because
  setup-go's problem matcher may use up the ten error annotations a step may have). It exits with
  the command's status.
- An untracked `vendor/` directory (in `.gitignore`, e.g. from `go mod vendor`) may be used with
  `GOFLAGS=-mod=vendor` for offline builds; `main` never contains `vendor/`. Maintainer tooling
  for offline dependency snapshots and for publishing is not part of the public tree (§19.3).

---

### 17.5 High availability (optional, e.g. 3 servers)

Single-node remains the default. HA mode is enabled by configuration only — same binary.

**Topology (reference: 3 servers, `deploy/ha/`)**: each server runs one OpenTidemark node, one
ClickHouse replica and one ClickHouse Keeper (Raft quorum of 3). Metadata lives in an external
HA PostgreSQL (`TIDEMARK_META_DRIVER=postgres`). A load balancer (HAProxy/nginx/Caddy examples in
`deploy/ha/`) spreads HTTP and OTLP traffic across nodes; no sticky sessions are needed.

**Rules that make nodes stateless & interchangeable**
- All shared state is in PostgreSQL (users, sessions, keys, dashboards, alerts, jobs, settings)
  or ClickHouse (telemetry). Node-local state is limited to the WAL, caches and rate limiters.
- `TIDEMARK_SECRET_KEY_FILE` must contain the same key on every node (installer copies it).
- ClickHouse cluster mode (`TIDEMARK_CLICKHOUSE_CLUSTER`): migrations run `ON CLUSTER` and use
  `ReplicatedMergeTree('/clickhouse/tables/{shard}/{database}/<table>', '{replica}')` (one shard,
  N replicas); inserts use `insert_quorum` (majority) so an acknowledged batch exists on ≥ 2 of 3
  replicas; `TIDEMARK_CLICKHOUSE_ADDR` lists all replicas (client fails over in order, local first).
  `TIDEMARK_CLICKHOUSE_INSERT_QUORUM=auto` sends `insert_quorum='auto'` (ClickHouse majority);
  a number sends that replica count (1–64); single-node mode sends no quorum. The writer user
  needs `GRANT CLUSTER ON *.*` for `ON CLUSTER` DDL (`deploy/ha/clickhouse/users.d/`).
- **Leader election** for singleton work (alert evaluation, retention TTL apply, stats collection,
  ClickHouse migrations, import jobs, purge): lease rows in `leases` (30 s lease, renewed every
  10 s, conditional UPDATE so exactly one holder). Losing the lease stops the work immediately.
  Implementation (`internal/cluster`): `Leader interface { IsLeader(name string) bool }` is what
  singleton workers consume (`App.Leader()`); the `Elector` renews every registered lease each
  round (`TryAcquire(name, node, now, ttl)` succeeds when the lease is free, expired or already
  ours) and treats a lease as held only until `last successful renewal + TTL − renew interval`, so
  a node stops a full interval before anyone else can take over; a failed renewal keeps that local
  window, a refused one drops it. `Elector.Lead(ctx, name, fn)` runs `fn` while leading and
  cancels its context when the lease is lost. Held leases are released on shutdown. Lease names:
  `alerting`, `retention`, `stats`, `clickhouse-migrations`, `purge`, `import:<job id>`.
  ClickHouse migrations at startup: in HA mode the node that gets the `clickhouse-migrations`
  lease (10 min TTL) migrates; the others poll every 2 s until nothing is pending.
  Lease times come from the node clocks, so clocks must be NTP-synchronised (`tidemark doctor`
  reports skew).
- Caches: connection-key cache TTL is 10 s in HA mode; changes also publish a PostgreSQL
  `NOTIFY tidemark_invalidate` (payload = topic, e.g. `connections`) that nodes `LISTEN` to for
  immediate invalidation. The listener uses a dedicated pgx connection and reconnects with
  backoff (1 s → 30 s); after every (re)connect all local caches are invalidated, because
  notifications sent while disconnected are lost. Single-node mode uses an in-process bus.
- Rate limits are enforced per node (documented: effective limit = limit × nodes behind the LB).
- Live tail, histograms and queries read from any replica; sessions are valid on every node.
- **Graceful drain:** on SIGTERM / service stop, `/readyz` returns 503 immediately, the node keeps
  serving for `TIDEMARK_DRAIN_SECONDS` (default 10) so the LB removes it, then stops listeners and
  flushes the WAL to ClickHouse.
- **Rolling upgrades:** upgrade one node at a time; additive migrations (§8.5) keep old nodes
  working; the first upgraded node migrates under the migration lock.
- A node whose WAL holds data for a failed ClickHouse keeps it until a replica is reachable; a
  permanently lost node loses only its unflushed WAL (bounded by `TIDEMARK_WAL_MAX_BYTES`; the
  backlog is visible as `tidemark_wal_bytes` and `tidemark_pipeline_queue_depth`, failing inserts
  as `tidemark_pipeline_write_errors_total` and `/readyz`, §11.6, so it can be alerted on). With
  quorum inserts, no acknowledged *and flushed* data is lost when one server fails.

### 17.6 Releases & versioning

**Versioning.**
- `VERSION` (repository root) holds the version of the current or next release as one line:
  [SemVer 2.0](https://semver.org/spec/v2.0.0.html) without a `v` prefix and without build
  metadata, `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$` (e.g. `0.1.0`,
  `0.2.0-rc.1`). A version with a pre-release suffix is published as a pre-release. Releases are
  tagged `v<VERSION>`. Before 1.0 a minor version may contain breaking changes to APIs or
  configuration (the data and upgrade rules of §8.5 always hold); patch versions only fix bugs.
  `VERSION` is exempt from the SPDX header rule (§19.1).
- `CHANGELOG.md` follows [Keep a Changelog 1.1](https://keepachangelog.com/en/1.1.0/): a short
  intro, `## [Unreleased]` first, then one `## [<version>] - <YYYY-MM-DD>` section per release,
  newest first, with `### Added` / `Changed` / `Deprecated` / `Removed` / `Fixed` / `Security`
  lists written for users, and link references at the end. Every user-visible change adds its
  entry under `[Unreleased]` in the same commit.
- **Making a release:** one commit sets `VERSION` to the new version and moves the
  `[Unreleased]` entries into a new dated section for it (`[Unreleased]` stays, empty). The
  release happens automatically once that commit is on `main` of the public repository and `ci`
  succeeded for it; nothing else is needed. `internal/repotest` (`TestVersionAndChangelog`) fails
  unless `VERSION` matches the pattern, `CHANGELOG.md` starts with `[Unreleased]`, every release
  heading is `[<semver>] - <valid date>` and the section of `VERSION` exists and is not empty, so
  the release workflow cannot fail on a missing section.
- `scripts/release-notes.sh <version> [<changelog>]` prints the body of that section (leading and
  trailing blank lines and the link references removed), followed by an `## Install` section with
  the version substituted: a table asset → platform → quick commands (Docker bundle,
  both Linux archives, the Windows archive, `docker pull ghcr.io/zeraorg/tidemark:<version>`,
  the offline documentation bundle `tidemark_<version>_docs.zip`), a
  pointer to the `INSTALL.md` of each archive, to `docs/installation.md` at tag
  `v<version>` and to the documentation site, and the verification commands (`sha256sum -c`, `gh attestation verify` for an
  archive and the image). It fails with a clear message when the section is missing or empty; it
  is the release notes source.

**Archives (`make dist` / `.\build.ps1 dist`).** Both read the version from `VERSION` only (never
from git, so the same commit gives the same archives locally and in CI), fail unless it matches
the pattern, run `web`, recreate `dist/` and build for each of `linux/amd64`, `linux/arm64` and
`windows/amd64` with `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<version>"`
(`tidemark version` prints `tidemark <version>`), the web UI embedded. Each archive holds one
directory `tidemark_<version>_<os>_<arch>/` with the binary (`tidemark` / `tidemark.exe`),
`INSTALL.md` (copied from `deploy/install/INSTALL-<os>.md`) and the files of the shared lists
(`DIST_FILES` in the Makefile, `$DistFiles` in build.ps1; kept equal and existing by
`TestDistFileListsMatch`): `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`,
`README.md`, `CHANGELOG.md`, `deploy/.env.example`, `deploy/clickhouse/config.d/tidemark.xml`,
`deploy/clickhouse/users.d/tidemark.xml`, plus for Linux `deploy/install.sh` and
`deploy/tidemark.service`, for Windows `deploy/install.ps1` (so `sudo ./deploy/install.sh native
./tidemark` and `.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe` work from the unpacked
archive). Linux archives are `tidemark_<version>_linux_<arch>.tar.gz` (file modes kept), the
Windows archive `tidemark_<version>_windows_amd64.zip`.

The **Docker bundle** `tidemark_<version>_docker.tar.gz` and `.zip` (both: the zip for Windows
users, the tar for file modes) holds `tidemark_<version>_docker/` with the files of
`DIST_FILES_DOCKER` / `$DistFilesDocker` (kept equal and existing by `TestDistFileListsMatch`)
with a leading `deploy/` removed — `LICENSE`, `NOTICE`, `README.md`, `CHANGELOG.md`,
`.env.example`, `install.sh`, `install.ps1`, `otel-collector.yaml`,
`clickhouse/config.d/{tidemark,docker-listen}.xml`, `clickhouse/users.d/tidemark.xml` — plus
`INSTALL.md` (from `deploy/install/INSTALL-docker.md`) and `docker-compose.yml` generated from
`deploy/docker-compose.yml`: the `build:` block of the `tidemark` service removed, its image
line `image: ${TM_IMAGE:-tidemark:local}` replaced by
`image: ${TM_IMAGE:-ghcr.io/zeraorg/tidemark:<version>}` (`DIST_IMAGE` / `$DistImage`) and
`deploy/.env` in the hint texts replaced by `.env` (Makefile: `sed`; build.ps1:
`Convert-DistCompose`; the same rules in `TestDistDockerCompose`). `dist` fails when the
generated file still contains a `build:` key or lacks the pinned image line. The files sit at
the top level, so `install.sh` / `install.ps1` (which use their own directory) work unchanged.

The **offline documentation bundle** `tidemark_<version>_docs.zip` holds
`tidemark_<version>_docs/`, the documentation site (§17.4) built from `mkdocs.offline.yml`, which
inherits `mkdocs.yml` (`INHERIT`) and adds Material's `offline` plugin: no directory URLs, the
search index embedded as `search/search_index.js`, so the site works from `file://` with
working search, and the same `llms.txt` / `llms-full.txt` with relative addresses. It restates
the theme features without `navigation.instant` (on `file://` Material skips instant
navigation but completes its document stream with the feature on, so copy buttons, table
scrolling and the followed table of contents would not mount). The plugin would load the iframe-worker shim (which replaces `Worker` on `file://`,
where browsers refuse workers) from a CDN; instead `extra.polyfills` names
`assets/javascripts/iframe-worker.js` and `docs-site/hooks.py` copies the vendored, unmodified
`docs-site/offline/iframe-worker.js` (iframe-worker 1.0.4, MIT, notice in its header, §19.1)
there, so the bundle makes no network request. `make docs-offline` / `.\build.ps1 docs-offline`
(the MkDocs requirements installed, §17.4) build it into `dist/` (`mkdocs build --strict -f
mkdocs.offline.yml -d dist/tidemark_<version>_docs`, zipped with `zip -qrX` / `tar.exe -a`, the
directory removed) and then rewrite `checksums.txt` (`make checksums` / `.\build.ps1
checksums`), so the bundle is listed next to the archives. The online site never uses the
offline configuration.

`deploy/install/INSTALL-{linux,windows,docker}.md` are version-independent and use only absolute
`https://` links (`TestInstallDocs`); they cover verifying the download (checksum and
`gh attestation verify`), installing (Linux: `sudo ./deploy/install.sh native ./tidemark` and a
short ClickHouse setup; Windows: `Unblock-File` or `-ExecutionPolicy Bypass`, Administrator,
the SmartScreen note for the unsigned `tidemark.exe`, ClickHouse in Docker Desktop; Docker:
`./install.sh docker` / `.\install.ps1 -Mode Docker`, `TM_BIND_ADDR=127.0.0.1`, the collector
profile), service management, upgrade and uninstall, matching the installers and
`docs/installation.md`.

Tar archives are written with `--owner=0 --group=0 --numeric-owner` (Makefile, GNU tar; `TAR`
selects another tar, e.g. `gtar` on macOS) or `--uid 0 --gid 0 --numeric-owner` (build.ps1). The
Makefile packs with `tar -czf` and `zip -qrX`; build.ps1 with `%SystemRoot%\System32\tar.exe`
(the bsdtar of Windows 10+, `-a`; called by path because a GNU tar earlier in `PATH`, e.g. from
Git Bash, cannot write zip). The staging directories are removed; `dist/checksums.txt` lists
`<sha256>  <archive>` for every archive (sha256sum format, LF line endings). Release archives
are built on Linux by the workflow below.

**`.github/workflows/release.yml`** (one file for every repository; the main public repository
`ZeraOrg/opentidemark` is recognised by `github.repository == 'ZeraOrg/opentidemark'` in expressions and
the workflow env `PUBLIC`):
- Triggers: `workflow_call` (from the `release` job of `ci.yml`, which runs only after the `go`,
  `integration`, `notices` and `web` jobs succeeded for a push to `main` of the main public
  repository, §17.4) and `workflow_dispatch`. Calling it from ci instead of a `workflow_run`
  trigger means no release run exists in other repositories unless started by hand, and the
  release waits for the whole gate including the web e2e. Workflow permissions `{}`; every job
  declares its own (the calling job grants `contents: write`, `packages: write`,
  `id-token: write`, `attestations: write`, `actions: read`); `run` steps use `bash`
  (`-eo pipefail`); every job sets `timeout-minutes`.
- Job `prepare` ("decide", `contents: read`, `actions: read`) runs for `workflow_dispatch`, or in
  the main public repository for a push to `main` (the ci call; pull-request ci runs never
  release). Its first step uses REST calls only (no checkout): it reads `VERSION` at the source
  commit (`github.sha`) and fails on a non-semver value. In the main public
  repository it ends with a `::notice::` and `proceed=false` when the tag `v<version>` already
  exists (the cheap, normal case of every push that does not bump `VERSION`), when a manual run
  is not on `main`, when the commit is no longer the tip of `main` (the newer commit releases
  itself) or, for a manual run, when the jobs of the latest `push` ci run for it other than its
  `release / …` jobs did not all succeed (so a manual run can recover a failed release). Outputs: `version`,
  `tag`, `sha`, `prerelease` (version contains `-`), `name` (`v<version>`; elsewhere
  `v<version> (private test build <7-hex sha>)`), `created` (commit date), `image-tags`
  (`ghcr.io/zeraorg/tidemark:<version>`, plus `:<major>.<minor>` and `:latest` unless
  pre-release) and `notes` (checkout, then `scripts/release-notes.sh`; a missing section fails
  the run with an error annotation).
- Job `supply-chain` ("supply chain", `contents: read`; needs `prepare`): checks out the source
  commit and runs OSV-Scanner on `go.mod` and `web/package-lock.json` (§15.9); a finding fails
  it and with it every later job, so nothing is built, tagged or pushed.
- Job `build` ("archives", needs `prepare` and `supply-chain`, `contents: read`, `id-token: write`, `attestations: write`): checkout
  of the source commit, setup-go (`go-version-file`), setup-node 24 (npm cache), `make dist`
  through `scripts/ci-annotate.sh`, then setup-python 3.13 (pip cache keyed on
  `docs-site/requirements.txt`), `pip install --require-hashes` and `make docs-offline` (the
  documentation bundle above, which rewrites `checksums.txt`); in the main public repository a
  build provenance attestation of every archive and the bundle
  (`actions/attest-build-provenance`, `subject-checksums: dist/checksums.txt`); uploads `dist/`
  as artifact `dist` (7 days). `internal/repotest` (`TestReleaseDocsBundle`) checks that the
  workflow, both task runners, `mkdocs.offline.yml`, the hook and the release notes keep the
  bundle.
- Job `image` ("container image", needs `build`; `contents: read`, `packages: write`,
  `id-token: write`, `attestations: write`; concurrency `release-image-<version>`, no cancel):
  `docker/build-push-action` with the Dockerfile, build argument `VERSION=<version>`, the
  `image-tags` and OCI labels `org.opencontainers.image.{title=OpenTidemark, description, version,
  revision=<sha>, created, source=url=https://github.com/ZeraOrg/opentidemark, licenses=Apache-2.0}`.
  Before that, in every repository, a `linux/amd64` build of the same Dockerfile is loaded into
  the runner's Docker (`load: true`) and scanned with Trivy (`aquasec/trivy:<TRIVY_VERSION>`,
  the version of `security.yml`, kept equal by `TestReleaseScanUsesSecurityTrivy`;
  HIGH/CRITICAL, fixed only, `--exit-code 1`), so a vulnerable image fails the run before any
  push, tag or release. In the main public repository: QEMU and Buildx set up, login to `ghcr.io` with the workflow
  token, platforms `linux/amd64,linux/arm64`, push, then a provenance attestation of the image
  digest pushed to the registry (`push-to-registry: true`, `create-storage-record: false`); a
  failed build/push adds an error annotation explaining the package prerequisites (organisation
  allows Actions to create packages, an existing `tidemark` package grants this repository write
  access, a new package must be made public once). Elsewhere: Buildx only, `linux/amd64`, no
  push, no login.
- Job `release` ("GitHub release", needs `build` and `image`; `contents: write`; concurrency
  `release-<version>`, no cancel): downloads `dist`, writes the notes (passed through an environment
  variable, never inlined into the script). In the main public repository it checks the tag again (a
  parallel run may have released meanwhile, then it ends with a notice) and runs `gh release create
  v<version> dist/* --target <sha> --title "OpenTidemark v<version>" --notes-file …
  --prerelease=<true|false>`, which uploads the assets before publishing and creates the lightweight
  tag on the released commit. Elsewhere it looks up a draft release with the same `name` (all
  pages): absent ⇒ create a draft (`tag_name v<version>`, `target_commitish <sha>`, body = notes; a
  draft creates no tag); present ⇒ update its target and body and delete its assets; then upload
  every file of `dist/` to it. The run summary links the release.
- Because the release job runs last, a failed build or image push leaves no tag behind, so the
  next successful ci run of `main` (or a manual run) retries the same version.

**Verifying a release (users).** `sha256sum -c checksums.txt` (PowerShell: `Get-FileHash`);
`gh attestation verify <archive> --repo ZeraOrg/opentidemark`; `gh attestation verify
oci://ghcr.io/zeraorg/tidemark:<version> --repo ZeraOrg/opentidemark`. Releases 0.2.0 and earlier
were attested under the former repository name (§1) and verify with `--repo ZeraOrg/tidemark`.

## 18. Testing & quality gates

| Layer | Requirement |
|---|---|
| Go unit | `go test -race ./...` (Linux), `go test ./...` (Windows). Table-driven. Coverage ≥ 85% for `internal/tql`, `internal/normalize`, `internal/ingest/...`, `internal/auth`, `internal/retention`; ≥ 70% overall. |
| TQL | Golden tests: queries in `internal/tql/testdata/*.tql` (`%cursor`/`%streams a,b|none`/`%where` directives) ⇒ `*.golden` (canonical form, SQL + args + columns, or error with position and caret); regenerate with `go test ./internal/tql -run TestGolden -update`. Every §10.1 example is read from `docs/query-language.md` (`TestDocExamplesParseAndCompile`) and must parse and compile, and so must every query line of the reference's `tql` code blocks (`TestDocQueriesParseAndCompile`, at least 150 queries; queries meant to fail use `text` blocks); `internal/repotest` (`TestTQLDocExamplesMatchSpec`) asserts that block equals the §10.1 block of this file. Fuzz test `FuzzParse` (no panics, positioned errors, stable canonical round trip, no user text in SQL); found inputs are kept in `testdata/fuzz`. SQL-injection table (quotes, backticks, `--`, `;`, unicode, keys with `'`) and a max-size pathological-input timing test. |
| Ingestion | Fixture bodies in `testdata/` captured from real clients: Serilog Seq sink (CLEF), NLog Seq target (legacy JSON), Filebeat/Fluent Bit/Vector/Logstash `_bulk`, OTLP JSON + protobuf logs/traces/metrics. |
| Integration | Build tag `integration`; needs `TIDEMARK_TEST_CLICKHOUSE_ADDR` (+ `TIDEMARK_TEST_CLICKHOUSE_{DATABASE,WRITER_USER,WRITER_PASSWORD,READER_USER,READER_PASSWORD}`). `scripts/with-clickhouse.sh <cmd>` starts a throw-away local server (binary from `$CLICKHOUSE_BIN`, `/opt/clickhouse/clickhouse` or `PATH`; temp dir; free loopback port; users from `deploy/clickhouse/users.d` with random passwords; a `backups` disk in the temp dir), exports those variables, runs the command and stops the server; with the variable already set it just runs the command. Packages run with `-p 1` because they share the `tidemark` database. Covers migrations, the reader against real data (`internal/store/clickhouse`), and end to end (`internal/app`): connection keys created via the API, rejection without a key / with a wrong-protocol key, Seq, ES `_bulk`, OTLP HTTP (JSON + protobuf) and native fixtures, then `POST /query` (events + cursor paging, table, timeseries), histogram, facets, fields, event by id + context, trace + trace search, metric names, export and live tail; stream access (`TestIntegrationStreamAccess`: logs, spans and metrics in two streams; a team member of one stream sees none of the other on every §15.10 path — queries incl. `stats count() by stream`, histogram, facets, fields, completion, event by id and context, CSV and NDJSON export, tail, trace search, a cross-stream trace, metric names and queries, streams list and fields; the selection never widens the scope; environments filter logs only; alert rules must name streams within the editor's scope, are hidden outside it and keep their streams after the author leaves the team; a team-less user sees every stream until deny-by-default is on and then sees nothing, without errors; an event naming a stream outside its connection's allowed streams lands in the default stream); retention TTL DDL. CI uses a ClickHouse service container (Linux job). Also covers insert deduplication of a replayed WAL batch and of a replayed import job (`tidemark import clef` of 12,345 lines, then the whole job re-run from an empty checkpoint: the row count is unchanged), address failover, operator commands (backup/restore/doctor) and the importers against fake sources. The replicated-schema test additionally needs `TIDEMARK_TEST_CLICKHOUSE_CLUSTER` and `TIDEMARK_TEST_CLICKHOUSE_CLUSTER_ADDRS` (a 1-shard, 3-replica cluster with Keeper, e.g. `deploy/ha/clickhouse/`); `scripts/with-clickhouse-cluster.sh <cmd>` starts such a cluster on loopback (three servers, each embedding Keeper with IPv6 disabled, free ports, users from `deploy/ha/clickhouse/users.d`, cluster `tidemark_cluster`), exports `TIDEMARK_TEST_CLICKHOUSE_{ADDR,CLUSTER,CLUSTER_ADDRS,WRITER_PASSWORD,READER_PASSWORD}` and removes it afterwards; PostgreSQL contract, bus and restore tests need `TIDEMARK_TEST_POSTGRES_DSN`. |
| Security | Tests for auth on every route (table of routes × roles), CSRF, rate limits, key restrictions (protocol, CIDR, expiry, revoke), SSRF dialer, headers, SQL-injection attempts through TQL (fuzz corpus). |
| Web | `tsc --noEmit`, ESLint (0 warnings), Prettier, Vitest with coverage ≥ 80 % statements/lines/functions and ≥ 70 % branches for `src/tql/**`, `src/lib/**` and `src/api/client.ts` (TQL highlighter, template renderer, builder↔TQL round-trip, client CSRF/errors/offsets, components; the scope helpers and `useDataScope` URL ↔ state round trip, the scope picker with one, many and no streams and its keyboard flow, the Teams pages and the endpoint parameters of the selection and team routes), production build + static CSP check, `npm audit --audit-level=high`, Playwright smoke against the mock build served with the production CSP (login + default-credentials banner, query, autocomplete, detail, analytics + add to dashboard, signal save, traces, metrics, dashboard create/edit, alerts drawer with the streams field, streams purge dry run, connection create with allowed streams, settings pages, the scope picker, an admin creating a team and granting a stream to a viewer who then sees only that stream with a static chip and an orphaned span, deny by default leading to the empty state without queries, light theme, keyboard, 390 px with the scope sheet); every e2e test fails on a CSP violation or page error and writes design-review screenshots. |
| Static | `gofmt`, `go vet`, `golangci-lint` (errcheck, govet, staticcheck, gosec, revive, gocritic, bodyclose, contextcheck, nilerr), `govulncheck`, OSV-Scanner (known vulnerabilities and malicious packages, §15.9). |
| Docs | `docs/query-language.md`, `docs/ingestion.md` and `docs/configuration.md` are derived from §10, §9 and §7 and updated with them in the same commit. `docs/configuration.md` lists exactly the variables of `internal/config` with their defaults (`TestConfigurationDocMatchesVariables`) and only §7 variables of this file (`internal/repotest`, `TestConfigurationDocMatchesSpec`); the §10.1 examples as above. Every chapter of `docs/manual/` is in the site navigation (`TestManualPagesInNav`, §17.4); `mkdocs build --strict` (both configurations) passes without warnings before a docs change is committed. |
| Release | `internal/repotest`: `TestVersionAndChangelog` (`VERSION` format, `CHANGELOG.md` shape and a non-empty section for `VERSION`), `TestDistFileListsMatch` (Makefile and build.ps1 archive file lists, Docker bundle list included, equal and existing), `TestDistDockerCompose` (the bundle rewrite of `deploy/docker-compose.yml` leaves no `build:` key, pins the release image and removes only the build block; Makefile and build.ps1 use the same image), `TestInstallDocs` (every `INSTALL-*.md` exists and links only to absolute `https://` URLs), `TestReleaseDocsBundle` (the offline documentation bundle stays in the workflow, the task runners, `mkdocs.offline.yml`, the hook and the release notes) and `TestReleaseScanUsesSecurityTrivy` (§17.6). `make dist` is run and its archives inspected (contents, `tidemark version`) before changing the release tooling. |
| Regression | Every bug fix adds a failing test first. No test may be deleted or weakened to make CI pass without a spec-recorded reason. |

Definition of done for any change: spec updated · tests added/updated · `make check` (or
`.\build.ps1 check`) green on Linux and Windows CI · no new lint findings.

---

## 19. Licensing & repositories

### 19.1 Licence, notices and dependency policy
- `LICENSE` — the **Apache License, Version 2.0**, verbatim (the text of
  <https://www.apache.org/licenses/LICENSE-2.0.txt>; `internal/repotest` checks its SHA-256
  `cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30`). OpenTidemark is open source
  for every use, commercial included; there are **no editions, licence keys or user limits**
  (removed before the first public release: `TIDEMARK_LICENSE` is now an unknown variable that only
  produces the startup warning of §7, and a `license.key` row left in `settings` by a pre-release
  build is ignored).
- `NOTICE` — Apache-style: `OpenTidemark`, `Copyright 2026 The Tidemark Authors (zeraOrg, …)`, a
  line that the product is licensed under Apache-2.0, and a pointer to `THIRD_PARTY_NOTICES.md`. It
  must ship with every distribution (repository, release archives, container image at `/licenses/`,
  §17.2), as Apache-2.0 §4(d) requires.
- `LICENSING.md` — short plain-language note: Apache-2.0, keep LICENSE/NOTICE when
  redistributing, third-party software, contributions (§19.2), trademarks.
- **Trademark notice policy.** The trademark notice lives in `LICENSING.md` § Trademarks and names
  the owners of the third-party marks the project mentions (Elasticsearch, Kibana, Logstash and
  Beats — elasticsearch B.V.; OpenTelemetry and Prometheus — The Linux Foundation; OpenSearch —
  LF Projects, LLC; ClickHouse — ClickHouse, Inc.; Seq — Datalust Pty Ltd), states that
  OpenTidemark is independent and not affiliated with, endorsed or sponsored by them, that
  compatibility statements describe protocol interoperability only, and that the Elasticsearch
  endpoint implements only the `_bulk` ingestion API and does not identify itself as
  Elasticsearch (§9.3). The README licence section and the documentation site footer
  (`mkdocs.yml` `copyright`, "Trademarks") link to it; `NOTICE` stays limited to the Apache-2.0
  §4(d) attribution. Third-party names are used only referentially ("accepts the Elasticsearch
  `_bulk` ingestion API", "familiar to Seq and Kibana users", "alternative to Seq and to
  Elasticsearch/Kibana"), never in product, package or domain names, without logos and without
  claims of endorsement or of compatibility beyond what is implemented; ® marks the first
  prominent mention of OpenTelemetry, Prometheus and ClickHouse in the README and on the
  documentation home page. A new third-party name in public text is added to the notice.
- `THIRD_PARTY_NOTICES.md` — licences of bundled dependencies; generated by
  `make notices` (`go-licenses report` + `license-checker` for `web/`) and checked in CI; go-licenses
  analyses the `linux` build on every host (build.ps1 runs it with `GOOS=linux`), so the file is
  the same whether it is generated on Linux or Windows.
- **Dependency policy.** Shipped code may only depend on permissive licences that are compatible
  with distributing OpenTidemark under Apache-2.0 and impose no conditions beyond keeping notices:
  MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, and MPL-2.0 for unmodified files (its
  file-level copyleft applies only to the MPL files themselves, which are used as is). Copyleft
  licences (GPL/AGPL/LGPL/SSPL and similar) are rejected because they would force the combined
  work under other terms, and unknown licences fail closed. CI fails on anything else.
- Go allow-list (`make licences` / `.\build.ps1 licences`, run by `notices` and by ci's `notices` job):
  `go-licenses check ./cmd/... --ignore <own module> --ignore <each reviewed exception> --allowed_licenses=Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0`
  (go-licenses v2). `THIRD_PARTY_NOTICES.md` lists one row per licence file and detected licence
  (`go-licenses report`, one row per library directory that has its own licence file), followed by
  one row per reviewed exception. Every package must have a licence file whose detected licences are all on the
  list; a missing or unrecognised licence fails the check, so unknown licences cannot slip through.
  Standard-library packages are recognised by their `GOROOT` path, so the targets pass
  `GOROOT=$(go env GOROOT)` explicitly (with go-licenses v1.6.0 and a toolchain selected through
  `GOTOOLCHAIN`, the standard library was reported as "does not have module info" and the check
  failed). The web allow-list (`license-checker --onlyAllow`) additionally accepts 0BSD, CC0-1.0,
  BlueOak-1.0.0, Python-2.0 and CC-BY-4.0 (data/assets), all permissive and Apache-2.0-compatible.
- Reviewed licence exceptions (`scripts/licence-exceptions.txt`: `<module> <SPDX id> <SHA-256 of
  the module's LICENSE>`) cover modules whose permissive licence go-licenses cannot classify.
  `scripts/licence-exceptions.sh modules|verify|rows` (and the same logic in `build.ps1`) lists
  them for `--ignore`, fails `licences` unless each LICENSE file still has the reviewed hash (a
  relicence needs a new review) or when the module is no longer a dependency, and prints their
  notices rows `| <module> | <version> | <SPDX id> (reviewed exception) |`. Current exceptions:
  `github.com/segmentio/asm` (from v1.2.1, required by `ClickHouse/ch-go`) is under **MIT-0**
  (MIT No Attribution): the MIT text without the condition to keep the notice, so it is more
  permissive than MIT; the classifier of go-licenses v2.0.1 has no MIT-0 template and reports "did
  not find license". `github.com/ncruces/go-sqlite3-wasm/v6` (from v6.3.35304, required by
  `ncruces/go-sqlite3` from v0.35) is under **MIT-0** for the same reason and holds the SQLite
  build of the metadata driver: SQLite compiled to WASM and translated to Go; SQLite is in the
  public domain and the WASI libc linked into it is under permissive licences, so no conditions
  beyond the module's notice apply. Adding a
  licence to the list or an exception requires a spec change with its justification.
- Every source file starts with: `// SPDX-License-Identifier: Apache-2.0`
  (`#` / `--` / `/* */` / `<!-- -->` / `{{/* */}}` comment styles as appropriate) within its first
  5 lines (after a shebang, doctype or XML declaration). Exempt: the untracked `vendor/` snapshot
  (§17.4), `LICENSE`, `NOTICE`, `go.sum`, generated `THIRD_PARTY_NOTICES.md`, `VERSION` (a bare
  version number read by tools, §17.6), the vendored `docs-site/offline/iframe-worker.js`
  (unmodified third-party code under MIT with its own notice in the header; shipped only in the
  offline documentation bundle, §17.6, and named in `LICENSING.md`), data files that
  cannot hold comments (JSON, `testdata/`, the common password list, binary assets) and build
  output (`dist/`, `bin/`, `site/`, and the untracked web outputs `dist-mock/`, `mock-public/`,
  `coverage/`, `test-results/`, `playwright-report/`). Enforced by `internal/repotest` (`TestSPDXHeaders`, `TestLicenseIsApache2`).

### 19.2 Contributions
- **Inbound = outbound:** contributions are licensed under Apache-2.0 (licence §5); there is no
  CLA and no relicensing grant.
- **DCO:** every contributed commit carries `Signed-off-by: Name <email>` certifying the Developer
  Certificate of Origin 1.1 (`git commit -s`), as described in `CONTRIBUTING.md`.
- Pull requests are opened on the public repository. A maintainer applies an accepted change to
  the maintainers' development repository (keeping author and sign-off, adding the spec update if
  the contribution did not include it), and it reaches the public repository with the next sync
  (§19.3), whose message credits the sign-offs as `Co-authored-by` trailers. Contributors update
  this spec (and the derived `docs/`) together with behaviour changes.
- Vulnerabilities are reported privately through GitHub security advisories of the public
  repository (`SECURITY.md`).

### 19.3 Public repository and publishing
- **Public repository** `ZeraOrg/opentidemark` holds the source code and this specification. It is
  published from the maintainers' development repository as **one squashed commit per sync**,
  subject `Sync from private <first 12 hex digits of the source commit>`, after the exported tree
  passed a standalone check (gofmt, `go vet` with and without `integration`, `go test -race`);
  the full `ci` (and, for dependency changes, `security`) then runs on the public repository for
  every sync commit. The commit carries `Co-authored-by` trailers for the
  `Signed-off-by` lines of the changes it contains. Its `main` only receives these sync commits,
  as fast-forwards (never forced); pull requests are reviewed on the public repository and
  applied by the maintainers (§19.2), then closed with a link to the sync commit.
- **The public tree is standalone:** `go build`, `go vet` (with and without `integration`) and
  `go test ./...` pass with exactly the files of §6.1, and `ci.yml`, `security.yml`, `release.yml`,
  `docs.yml` and `dependabot.yml` run there unchanged. Maintainer-only development tooling (publishing
  automation, offline dependency snapshots, design review material) is kept outside the public
  tree, is specified separately and is never referenced by public files.
- **Docs:** `docs/query-language.md`, `docs/ingestion.md` and `docs/configuration.md` are the user
  docs derived from §10, §9 and §7 (§18); the operations manual `docs/manual/` is derived from
  this specification as a whole (§17.4). Every user doc and manual chapter in `docs/` starts
  with an H1 followed by a one-sentence summary paragraph naming its topic (the page description
  of the documentation site and its entry in `llms.txt`). `docs/` is published by `docs.yml` as
  the documentation site <https://zeraorg.github.io/opentidemark/> (§17.4), and every release
  attaches it as the offline bundle `tidemark_<version>_docs.zip` (§17.6). Code comments cite
  this file as `SPEC §n`.
- The public `README.md` is written for an open-source project (a descriptive title and a first
  paragraph that name what the product is and the protocols and tools it works with, status, why
  OpenTidemark (only claims that hold for OpenTidemark itself), about with screenshots, features,
  quick start with the install options, docs, building, releases and changelog, contributing,
  licence) and must stay true: every change that affects a statement in it updates it in the same
  commit; `CONTRIBUTING.md` describes the contribution workflow.
- **Releases** are made in the public repository only, by its release workflow (§17.6): tags
  `v<version>`, GitHub releases with the archives, `checksums.txt` and provenance attestations,
  and the image `ghcr.io/zeraorg/tidemark`. The maintainers' development repository never
  publishes releases or images.

## 20. Milestones & status

| Milestone | Scope | Status |
|---|---|---|
| **M0** | Spec, contributor rules, repo scaffolding, CI | ✅ done |
| **M1** | Logs end-to-end: model, normalize, Seq + ES + native + OTLP logs ingestion, connections & keys, pipeline, ClickHouse schema, TQL (filters + stats), query/histogram/facets/fields/tail API, auth & users, security middleware, explorer UI, compose + native installers | ✅ implemented (open items below) |
| **M2** | OTLP traces & metrics, trace UI, metrics browser, dashboards (+builder, templates, variables), alerting + channels | ✅ implemented (open items below) |
| **M3** | Streams/index management UI (stats, retention, level rules, mappings, indexed fields, purge), audit UI, export | ✅ implemented (open items below) |
| **M3b** | Durability & HA: WAL, PostgreSQL metadata driver, leases, ClickHouse cluster mode, backups/doctor, upgrade tests, importers (Elasticsearch/OpenSearch, Kibana saved searches, Seq API + signals, CLEF files) | ✅ implemented (open items below) |
| **M4** | Loki push, Prometheus remote-write, GELF, OIDC SSO, Helm chart, tiered storage (S3), AI dashboard assistant (§13.4) | 📝 planned |

"Implemented" means the code exists and passes the gates of §18 (unit, golden, fuzz, integration
against a real ClickHouse, web unit tests and Playwright against the mock build). Known open
items of M0–M3b:

- **No end-to-end smoke test with real clients or Docker yet:** the ingestion tests use fixture
  bodies and the integration tests run the binary's code in-process against ClickHouse; nothing
  yet starts the Docker Compose stack or the released binaries and sends data from real Serilog,
  NLog, Filebeat, Fluent Bit, Vector, Logstash or OpenTelemetry clients.
- **`examples/`** (client configurations, §6.1) is not in the tree yet; `docs/ingestion.md`
  describes the endpoints, keys and headers clients need meanwhile.
- **Performance target not measured:** G7 (≥ 10k events/s on 4 vCPU / 8 GB) has no benchmark or
  load test yet.
- **UI polish:** chart legends belong in the panel header; the autocomplete list must stay
  anchored to the cursor of the query editor; the result count row on narrow (mobile) screens.

---

## 21. Changelog

| Version | Date | Change |
|---|---|---|
| 0.31.6 | 2026-10-17 | WAL (§9.8): a record write that fails part-way (disk full) is undone by truncating the active segment back to the last complete record; until that succeeds every append fails. Before, the next acknowledged record was written behind the partial bytes, so the reader skipped it as corrupt and startup truncated it (data loss). |
| 0.31.5 | 2026-10-16 | TQL reference (§6.1, §10, §18): `docs/query-language.md` documents every part of the language with examples (filters, operators, functions, fields, levels, time and duration literals, sources, stages, aggregates, time series, results, dashboard variables, lexical rules, autocomplete, examples by task, errors, pitfalls, limits, grammar, cheat sheet); every query in its `tql` blocks is tested (`TestDocQueriesParseAndCompile`). §10 now states existing behaviour it left out: keywords `like`, `ci`, `auto`; `now±N` with units `s m h d w` and the accepted time layouts; `sort` defaults to `asc`; backtick names for sort keys and aliases; `limit` without stats; the filter required after a source clause; `?` as a wildcard only with `*`; no `ci` after `in`; `@bucket` reserved; numeric aggregates of text fields. |
| 0.31.4 | 2026-10-15 | Documentation site (§17.4): the home page `docs/index.md` becomes a landing page (template `docs-site/overrides/home.html`: hero, supported clients, capabilities with screenshots, trust points, getting started, documentation links); the former home page moves to `docs/overview.md` ("Documentation overview", second in the navigation); `hooks.py` reads front matter that follows the SPDX comment. |
| 0.31.3 | 2026-10-15 | Dependabot ignores `msw >= 3.0.0` and major updates of `@types/node` and the `node` image (§17.4), so the weekly update no longer fails on held-back majors; Playwright 1.64.0. |
| 0.31.2 | 2026-10-14 | Dependabot ignores `typescript >= 6.1.0` (§17.4) until typescript-eslint supports TypeScript 7, so the weekly npm update no longer fails on the peer dependency. |
| 0.31.1 | 2026-10-14 | Notices are host-independent (§19.1): build.ps1 builds go-licenses for the host and runs its analysis with `GOOS=linux` like the Makefile and CI, so Linux-only dependencies (`prometheus/procfs`) are no longer dropped when the file is regenerated on Windows. |
| 0.31.0 | 2026-10-14 | Supply-chain gate (§15.9, §17.4, §17.6, §18): OSV-Scanner v2.6.0 checks `go.mod` and `web/package-lock.json` against OSV incl. the OpenSSF malicious-packages feed in the local `vuln` target, in `security.yml` and as `release.yml` job `supply-chain`, which every publishing job needs; `TestSupplyChainScanPinned` keeps the version equal in Makefile, build.ps1 and both workflows and checks the dependency. |
| 0.30.3 | 2026-10-14 | Web dependencies refreshed under the 7-day rule (§5, §15.9): React 19, React Router 7 (fixes GHSA-wrjc-x8rr-h8h6 and GHSA-337j-9hxr-rhxg), TypeScript 6, Vite 8, Vitest 5, ESLint 10, react-grid-layout 2 (v2 API with `useContainerWidth`), lucide-react 1, TanStack Query 5.104, jsdom 30; held back: TypeScript 7 (typescript-eslint supports < 6.1) and msw 3 (Vitest's mocker needs msw 2). eslint-plugin-react-hooks 7 keeps its React Compiler rules except `refs`, `set-state-in-effect` and `incompatible-library`, which only flag deliberate patterns (the app does not use the compiler). |
| 0.30.2 | 2026-10-14 | Dependabot weekly in one pull request (§17.4): `multi-ecosystem-groups: dependencies` (Monday) covers gomod, npm, GitHub Actions and Docker instead of one monthly PR per ecosystem; §15.9 states the 7-day rule for applying dependency updates. |
| 0.30.1 | 2026-10-14 | Node 24 (current LTS) for the web build in CI, release, security and the Dockerfile (`node:24-bookworm-slim`, §17.2, §17.4, §17.6); `engines` stays `>=22`. GitHub Actions already reference their newest majors; Trivy stays v0.75.0 (v0.76.0 is newer than the 7-day rule). |
| 0.30.0 | 2026-10-14 | Go dependencies refreshed (§5, §17.2, §19.1): toolchain go1.27.2 (Dockerfile `golang:1.27.2-bookworm`), `go 1.26.0` stays the minimum; `ncruces/go-sqlite3` v0.35.6 (no `embed` import; SQLite now translated to Go in `ncruces/go-sqlite3-wasm/v6`, wazero dropped) with the hash-pinned MIT-0 licence exception for go-sqlite3-wasm; `shopspring/decimal` 1.5.0, `pierrec/lz4` 4.1.33, `jackc/puddle` 2.2.3, OpenTelemetry 1.47.0, `go-faster/errors` 0.8.0. Rule for updates: the newest release that has been public for at least 7 days; versions already pinned newer for security fixes stay. |
| 0.29.7 | 2026-10-14 | "No data yet" in the log explorer (§16.3): when no visible stream holds log rows, the empty result points admins at connection keys, the `bootstrap` connection, Settings → Connections and the ingestion docs, and tells other roles to ask an administrator; `ButtonLink` renders a router link styled as a button (`EmptyResults.test.tsx`). |
| 0.29.6 | 2026-10-14 | Upgrade fixture for the 0.2.x schema (§8.5 item 6): `meta-0.2.1.db` (teams, team members and streams, connection `allowedStreams`) joins `TestUpgradeFixtures`; fixtures are opened with the release in `VERSION` and the newest may already be at head; tests use 0.2.1 instead of pre-release binary versions, and §7, §8.5 and §19.1 no longer name pre-release builds as if they were releases. |
| 0.29.5 | 2026-10-14 | Issue templates (§6.1): `.github/ISSUE_TEMPLATE/bug_report.yml` asks testers for version, installation, platform, steps, expected/actual behaviour, `tidemark doctor --json` and logs (secrets removed); `config.yml` links private vulnerability reporting and the documentation; CONTRIBUTING.md gains "Reporting bugs". |
| 0.29.4 | 2026-10-14 | Deleting a user transfers their objects (§11.2, §15.5): `DELETE /users/{id}` makes the deleting admin the owner of the user's dashboards and signals in the same transaction (`meta.Users.Delete(id, heirID)` returns the counts), so private ones no longer become unreachable; `user.delete` audits `transferredTo`, `dashboards` and `signals` (`contractUserDeleteTransfer`, `TestUserDeleteTransfersObjects`). |
| 0.29.3 | 2026-10-14 | Password resets revoke API tokens (§15.2): a password set by an admin (`PATCH /users/{id}` with `password`) or by `tidemark admin reset-password` now revokes the user's API tokens as well as the sessions, as the reset's code comment already promised; a role change and the user's own password change keep them (`TestPasswordResetRevokesTokens`). |
| 0.29.2 | 2026-10-14 | CSRF origin check accepts the requested host (§7, §15.2): an `Origin` header must equal `PUBLIC_URL` or have the host the request was sent to, so users who open an install by its LAN address or host name can save changes without `TIDEMARK_PUBLIC_URL` (before, every cookie-authenticated write from such an address failed while `PUBLIC_URL` kept its `localhost` default); the double-submit token and `SameSite=Strict` are unchanged (`TestCSRF`). `TIDEMARK_PUBLIC_URL` is described as the browser address; quickstart, `INSTALL-docker.md` and README recommend setting it for shared installs. |
| 0.29.1 | 2026-10-14 | A full ClickHouse disk is transient (§9.8): exception 243 (NOT_ENOUGH_SPACE) joins the transient codes, so inserts retry from the WAL until space is freed instead of dead-lettering the batch (`TestIsPermanent`). |
| 0.29.0 | 2026-10-14 | Repository renamed (§1, §6.1, §17.4, §17.6, §19.3): the public repository is `ZeraOrg/opentidemark` (old URLs redirect) and the Go module path is `github.com/ZeraOrg/opentidemark` (go.mod, imports, goimports `local-prefixes`, `MODULE` / `$Module`); every workflow guard compares `github.repository` with `ZeraOrg/opentidemark`; the image's OCI `source`/`url` labels, `mkdocs.yml` (`site_url` <https://zeraorg.github.io/opentidemark/>, `repo_url`, `repo_name`), `docs-site/hooks.py`, the release notes, the install guides, README, docs and `NOTICE` use the new URLs; releases 0.2.0 and earlier verify with `--repo ZeraOrg/tidemark`. Unchanged: the image `ghcr.io/zeraorg/tidemark`, the binary, archive names and every other technical identifier of §1. |
| 0.28.0 | 2026-10-14 | Operations manual and offline documentation (§6.1, §17.4, §17.6, §18, §19.1, §19.3): `docs/manual/` (fourteen chapters for operators and agents, written from this specification and verified against a test installation, including teams and stream access, `allowedStreams`, `tidemark_ingest_rerouted_total`, `tidemark_wal_bytes`, `connection_id`) joins the site as the navigation section "Operations manual" (`TestManualPagesInNav`); `mkdocs.yml` adds `pymdownx.tabbed`, `pymdownx.tasklist` and `content.tabs.link`; `docs-site/hooks.py` includes `docs/manual/`, resolves relative links against the page before rewriting those that leave `docs/` to GitHub, and writes `llms.txt` and `llms-full.txt`; `mkdocs.offline.yml` (Material's offline plugin, vendored iframe-worker shim instead of a CDN, SPDX-exempt) builds the offline bundle `tidemark_<version>_docs.zip` with `make docs-offline` / `.\build.ps1 docs-offline` (new targets `docs`, `docs-offline`, `checksums`), which the release workflow attaches, attests and lists in `checksums.txt` and the release notes (`TestReleaseDocsBundle`); `site/` and `__pycache__/` are build output skipped by the SPDX check; README and docs home link the manual, the bundle and the llms files; `LICENSING.md` names the bundle's third-party code. |
| 0.27.2 | 2026-10-14 | README screenshots refreshed from the mock build after the stream-access work; two new ones, `scope-picker.png` and `teams.png`, join the About table (§6.1). |
| 0.27.1 | 2026-10-14 | Explorer columns and row actions reviewed (§16.3, §16.6): the Columns dialog shows the chosen columns in order with move / remove and the other fields with a filter; shown columns shrink to 72 px before the list scrolls sideways with its header; the detail row actions anchor to the first line with a fade, get 28 px touch targets and are not offered on phones. |
| 0.27.0 | 2026-10-14 | Stream access in the web UI (§16.1, §16.3, §16.4, §16.6, §18): the scope picker (streams and environment, URL `streams`/`env`, remembered in `tm.scope`, static chip for one visible stream, bottom sheet on phones), every search request carries the selection, the empty state for users without streams, Settings → Teams (`/settings/teams`, `/settings/teams/:id`: deny-by-default card, teams table, team details / members / streams cards with their own save state), Teams column and assignment on the Users page, the account card's access summary, the tokens note, the alert editor's Streams field, allowed streams and an edit dialog for connections, "parent span not visible" in the trace waterfall; the mock server implements teams and the §15.10 scope; Vitest and Playwright coverage. |
| 0.26.0 | 2026-10-14 | TQL built-in `connection_id` (alias `connectionid`) for logs, spans and metrics (§10.4), mapped to the existing `connection_id` columns (stored lower case); golden cases added. |
| 0.25.0 | 2026-10-14 | Self-metrics listed (§11.6) and a new gauge `tidemark_wal_bytes{signal}` (size of each signal's WAL on disk, read at scrape time; `selfmetrics.Metrics.WALBytes`, registered when the WAL is opened). §17.5 names the metrics that make a WAL backlog and failing inserts visible instead of an unspecified "alerted via self-metrics". |
| 0.24.3 | 2026-10-14 | Abandoned WAL waits (§9.8): rows whose client went away while waiting for the fsync are already written and stored, so they count as enqueued instead of `dropped{reason="backpressure"}` and the request fails with the context error; `pipeline.Journal` documents that `Append` returns the context error only after writing. |
| 0.24.2 | 2026-10-14 | Corrections found while writing the operations manual (spec matches the code): §15.7 lists `X-Frame-Options: DENY` and HSTS among the response headers; §13.1 says `version` and `timeRange.from/to` are required (`variables`, `panels`, `refresh` optional); §11.4 separates TQL errors in panel queries (`invalid_query` with `pos/len/hint/panelId`) from wrong-kind queries and other document errors (`bad_request` with `details.field`); §11.5 documents the connection body fields, their limits and the connection object; §9.7 documents how syslog lines from the bundled Collector are mapped (raw line as message, parsed parts as properties `hostname`, `appname`, …, empty host). User docs: the Windows service is queried with `sc.exe qc Tidemark`; the header list and the syslog mapping. |
| 0.24.1 | 2026-10-14 | Trademark notice policy (§19.1): `LICENSING.md` § Trademarks names the owners of the third-party marks, states independence and that compatibility means protocol interoperability only (the Elasticsearch endpoint implements only `_bulk` and does not identify as Elasticsearch); linked from the README licence section and the documentation site footer (`mkdocs.yml` `copyright`), not from `NOTICE`. Referential wording (§1, §6.1, §7, §9.3): "Elasticsearch-compatible" ⇒ "the Elasticsearch `_bulk` ingestion API" (§9.3 renamed), "Kibana/Elastic" ⇒ "Elasticsearch/Kibana"; README and docs home use "familiar to Seq and Kibana users" and ® at the first prominent OpenTelemetry and ClickHouse mention. |
| 0.24.0 | 2026-10-14 | The Elasticsearch endpoint no longer identifies itself as Elasticsearch (§4, §9.3, §9.5, §11.5): no `X-Elastic-Product` header, no tagline; cluster info reduced to `name`, `cluster_name` and `version.number` (`8.11.0`, the implemented API level). Logstash's `elasticsearch` output (product check) and the official Elasticsearch clients are not supported; Logstash uses its `http` output to the native endpoint, and the `logstash` connection snippet renders that. Verified with Filebeat 8.15.3, Fluent Bit 3.1.9, Vector 0.41.1 (`_bulk`) and Logstash 8.15.3 (`http`). |
| 0.23.0 | 2026-10-13 | Connection allowed streams (§8.4, §9.1, §11.5, §15.5, §15.10, §18): meta migration 0008 adds `connections.allowed_streams` (JSON array, `[]` = any); API field `allowedStreams` (≤ 100 valid names, must include the default and any pinned stream, audited); at ingest an explicit stream outside the list goes to the default stream and is counted in `tidemark_ingest_rerouted_total{protocol,reason}` (new `ingest.Observer.Rerouted`); the stream access integration test covers a rerouted event. |
| 0.22.0 | 2026-10-13 | Teams API (§11.2, §11.5, §15.5, §15.10): admin routes `GET/POST /teams`, `GET /teams/access` (`{denyByDefault, usersWithoutTeam}`), `GET/PATCH/DELETE /teams/{id}`, `PUT /teams/{id}/members`, `PUT /teams/{id}/streams`, `PUT /users/{id}/teams` with limits and audit (`team.*`, `user.teams`; `user.delete` records the user's teams); `/users` objects include `teams`; `GET /auth/me` adds `allStreams`, `streams`, `teams`, `streamAccess`; route × role table extended; the stream access integration test manages its team through the API. |
| 0.21.0 | 2026-10-13 | Alert rules read chosen streams (§11.4, §14.1, §14.2, §15.10, §18): `streams` on the alert API (`null` = every stream, only for callers who see every stream; else a non-empty subset of the editor's scope), stored in `alert_rules.streams`; the evaluator reads the stored streams (`alerting.RuleScope`); rules outside the caller's scope are hidden and 404; `alert.create`/`alert.update` audit the streams; `service.NewAlerts` takes `AlertsDeps`. |
| 0.20.0 | 2026-10-13 | Data access by stream enforced on every search path (§11.3, §11.5, §15.10, §18): `service.Search` and `StreamAdmin` resolve the caller's scope (`SearchDeps.Access`, `StreamAdminDeps.Access`); selection `streams`/`environments` (`POST /query`) and `streams`/`env` (GET search routes; ≤ 100 stream names, ≤ 20 environments of 1…256 bytes) intersected with the scope, environments filtering logs only; events, context, traces and stream fields outside the scope are 404; trace summaries cover visible spans only; the tail re-resolves the scope every poll; `GET /streams` lists visible streams; the export audit records the selection; integration test `TestIntegrationStreamAccess`. |
| 0.19.0 | 2026-10-13 | Stream access resolution (§11.5, §15.10): `auth.Principal.UserRole` (the user's own role; tokens keep `Role` limited by scope); `service.Access` resolves a principal's scope and mode (`admin`, `teams`, `default-all`, `default-none`) from the user's role, teams and the new setting `access.denyByDefault` (default false), on every request; `GET/PATCH /settings` include `access` (strict `{denyByDefault}`, audited); the upgrade test proves upgraded users see every stream. |
| 0.18.0 | 2026-10-13 | Teams in the metadata (§8.4, §8.5, §12.7, §15.10): migration 0007 adds `teams` (case-insensitive unique name), `team_members` and `team_streams` (cascading on team/user delete) and the nullable `alert_rules.streams`; repository `meta.Teams` (CRUD, member/stream/user-team replacement with added/removed sets, per-user stream union, memberships, team-less user count, grant revocation); teams are part of the JSON export; deleting a stream revokes its grants (audited as `revokedFromTeams`); upgrade fixture `meta-0.1.0.db` (release 0.1.0 schema) proves existing users have no team after the upgrade. |
| 0.17.1 | 2026-10-13 | Store reads take the stream scope (§6.2, §15.10): `store.Querier` `Fields`/`Event`/`Context`/`Trace`/`MetricNames` and `StreamAdmin.Mappings` gain a `model.StreamScope` parameter, `store.Filter` carries `Streams`; the ClickHouse reader adds the stream condition to every fixed query (trace summaries count visible spans only) and fails with `store.ErrNoScope` when the scope is unset. Services still pass all streams; no behaviour change yet. |
| 0.17.0 | 2026-10-13 | Stream scope groundwork (§8.1, §10.6, §15.10 new, §18): `model.StreamScope` (all / none / named streams, fail-closed zero value); `tql.Options.Streams` is required and compiles to `AND stream IN (?, …)` (or `AND 0` when empty) after the time range; golden directive `%streams`; purge uses all streams and compile-only validation the empty scope. No behaviour change yet. |
| 0.16.2 | 2026-10-13 | Editorial: the 0.14.0 row contained a backspace control character instead of `` in `.uild.ps1`. |
| 0.16.1 | 2026-10-13 | Social preview image `docs/images/social-preview.png` (1280×640: logo, name, tagline, compatibility line, cropped explorer screenshot, dark token colours; §6.1, §17.4); the documentation site uses it as `og:image` / `twitter:image` with a large-image card. |
| 0.16.0 | 2026-10-13 | Documentation site (§6.1, §17.4, §19.3): `mkdocs.yml` (MkDocs 1.6.1 + Material for MkDocs 9.7.7, hashed requirements in `docs-site/`), `docs-site/hooks.py` (only top-level pages and `docs/images/`, meta descriptions from the summary paragraphs, links outside `docs/` to GitHub, logo from the web favicon), `docs-site/overrides/main.html` (Open Graph tags, token colours), new home page `docs/index.md`, public workflow `docs.yml` building with `--strict` and deploying to GitHub Pages (<https://zeraorg.github.io/tidemark/>) from `main` of `ZeraOrg/tidemark`; `site/` ignored; README links the site. The installation guide's contents link to section 3 follows the heading renamed in 0.15.0. |
| 0.15.1 | 2026-10-13 | Search-friendly wording (§19.3): the README has a descriptive title ("OpenTidemark — open-source, self-hosted log management, metrics and distributed tracing") and a first paragraph naming OpenTelemetry (OTLP), Seq/Serilog, Elasticsearch `_bulk`, ClickHouse and the Seq and Kibana/Elastic alternatives; every user doc starts with an H1 and a one-sentence summary paragraph. |
| 0.15.0 | 2026-10-13 | Product renamed to **OpenTidemark** (owner decision): new "Name" paragraph in §1 listing the technical identifiers that keep `tidemark`; every prose and display reference uses the new name (docs, README "Why OpenTidemark", UI wordmark, page title, login page, system card and connection snippets §16.2, Windows service display name "OpenTidemark observability" and its stop message, alert notification texts "Open in OpenTidemark" and the test notification §14.2, Elasticsearch "not supported" messages §9.3, release titles `OpenTidemark v<version>` and the OCI title label §17.6, NOTICE heading §19.1); identifiers, the `GET /api` product reply and the copyright holder are unchanged; README screenshots retaken. |
| 0.14.0 | 2026-10-13 | Ready-to-use release bundles (§6.1, §17.2, §17.6, §18): `make dist` / `.\build.ps1 dist` also build the Docker bundle `tidemark_<version>_docker.tar.gz` and `.zip` (Compose file without `build:` pinned to `ghcr.io/zeraorg/tidemark:<version>`, `.env.example`, installers, collector config, ClickHouse config incl. `docker-listen.xml`, `INSTALL.md`, licence files, README, changelog; `dist` fails on a remaining `build:` or a missing pin); every archive carries an `INSTALL.md` from `deploy/install/INSTALL-{linux,windows,docker}.md`; tar archives with owner/group 0; build.ps1 calls Windows' own `tar.exe` by path and reads the Compose file as UTF-8; `scripts/release-notes.sh` appends a version-substituted Install section; `release.yml` scans a linux/amd64 build with the pinned Trivy before pushing, tagging or releasing; new tests `TestDistDockerCompose`, `TestInstallDocs`, `TestReleaseScanUsesSecurityTrivy`. |
| 0.13.4 | 2026-10-13 | Workflows streamlined (§6.1, §17.4, §17.6, §19.1, §19.3): `web.yml` removed, its full gate (incl. Playwright e2e and the Go embed check) is ci job `web`; every ci and security job runs automatically only in `ZeraOrg/tidemark` (elsewhere only on `workflow_dispatch`) and has `timeout-minutes` and Go/npm caches; govulncheck in ci on Linux only; the cross-compile step reduced to `linux/arm64`; `security.yml` runs weekly, by hand and on dependency/Dockerfile pushes, without the duplicate licence job (ci `notices` runs `make licences`); `release.yml` is called by ci (`workflow_call`) after a green push to `main` instead of a `workflow_run` trigger, a manual run checks ci's gate jobs; the `ui/screenshots` publishing is gone. |
| 0.13.3 | 2026-10-13 | README "Why Tidemark" section (§6.1, §19.3): why the project exists, backed only by claims that hold for Tidemark itself (no outbound connections of its own, no third-party web assets, secrets at rest, Apache-2.0 without editions, release checksums and attestations, own Linux/Windows hardware or Docker). |
| 0.13.2 | 2026-10-13 | ClickHouse hardening (§6.1, §17.2, §17.3): `deploy/clickhouse/config.d/tidemark.xml`, which also ships in the native archives, no longer sets `listen_host 0.0.0.0` (a native server keeps the loopback default); the container listen address moved to the Compose-only `config.d/docker-listen.xml`. `tidemark.xml` and the HA `cluster.xml` disable ClickHouse crash reports (`send_crash_reports`). |
| 0.13.1 | 2026-10-13 | README "About" section with screenshots in `docs/images/` (§6.1, §19.3); the images are the e2e screenshots of the mock build halved to 1440×900 and saved as 256-colour PNGs. The mock build reports the release from `VERSION` (§5). |
| 0.13.0 | 2026-10-13 | Explorer columns (§16.3): the event detail's icon-only *pin column* became a labelled *Show in column* / *Hide column* toggle; built-in fields `environment`, `host`, `trace_id`, `span_id`, `event_type` can be shown as columns too (also in the Columns dialog); column headers get a *Remove column* × button. |
| 0.12.2 | 2026-10-13 | User doc `docs/installation.md` (§6.1): installation guide for every platform of §17.1 (install-method table, requirements and ports, release archives/image/source, Compose with `.env`, `TM_*` settings, collector profile and health checks, native Linux with `install.sh native` and native or Docker ClickHouse, native Windows service with ClickHouse in Docker Desktop on `127.0.0.1:9000`, macOS development, first sign-in, TLS/reverse proxy and exposure, HA pointer, operator commands, backups, upgrades, uninstalling, troubleshooting); §17.3 records how the ClickHouse passwords reach a separately run ClickHouse; README and `docs/quickstart.md` link the guide. |
| 0.12.1 | 2026-10-13 | User doc `docs/quickstart.md` (§6.1): running Tidemark locally with Docker Compose (`.env`, `TM_BIND_ADDR=127.0.0.1`, published image or local build, first sign-in, a first CLEF event, client addresses, everyday commands, troubleshooting); README links it and notes `TM_BIND_ADDR`. |
| 0.12.0 | 2026-10-13 | Releases (new §17.6): `VERSION` (0.1.0, SemVer without `v`, SPDX-exempt §19.1) and `CHANGELOG.md` (Keep a Changelog; `[Unreleased]` + dated sections) define releases; `make dist` / `.\build.ps1 dist` build the archives `tidemark_<version>_{linux_amd64,linux_arm64}.tar.gz` / `_windows_amd64.zip` (binary stamped with the version, licence files, README, CHANGELOG, installer files) and `checksums.txt` into `dist/`; `scripts/release-notes.sh`; `.github/workflows/release.yml` releases automatically in the public repository after a green `ci` on `main` when the tag `v<VERSION>` does not exist (provenance attestations, multi-arch image `ghcr.io/zeraorg/tidemark` with `<version>`, `<major>.<minor>`, `latest` and OCI labels, tag + GitHub release from the changelog section, pre-releases marked) and builds draft test releases elsewhere on demand; repository tests `TestVersionAndChangelog`, `TestDistFileListsMatch` (§18). Dockerfile cross-compiles on the build platform for multi-arch images (§17.2); published image usage with Compose (§17.2); `.dockerignore` excludes `dist`; release action versions pinned (§17.4); README content rules and releases in §19.3; §6.1 tree. |
| 0.11.3 | 2026-10-13 | §20 status brought up to date: M0–M3b implemented, with the known open items listed (no end-to-end smoke test with real clients or Docker, `examples/` not yet in the tree, G7 performance target not measured, UI polish items); M4 stays planned; §6.1 marks `examples/` as not yet present. |
| 0.11.2 | 2026-10-13 | `TIDEMARK_NODE_ID` must start with a letter or digit (§7): the startup error and `docs/configuration.md` now state that rule (both claimed any of `[A-Za-z0-9._-]` was allowed first), pinned by a regression test. |
| 0.11.1 | 2026-10-13 | Go module path is `github.com/ZeraOrg/tidemark` (the public repository; GitHub's canonical spelling of the organisation); example image `ghcr.io/zeraorg/tidemark` (§6.1, §17). |
| 0.11.0 | 2026-10-13 | SPEC.md is public; private development notes split out: the spec is published with the code and may be read by public tests (the §10.1 and §7 doc consistency tests are public, §18); maintainer-only development tooling is specified separately (§6.1, §17.4, §19.3); §16.2 declares `web/src/styles/tokens.css` normative and describes the visual design in prose; §19.3 rewritten as "Public repository and publishing"; changelog rows about maintainer tooling reworded. |
| 0.10.6 | 2026-10-13 | Maintainer tooling (not part of the public tree): publishing credentials. |
| 0.10.5 | 2026-10-13 | Maintainer tooling (not part of the public tree): publishing credentials. |
| 0.10.4 | 2026-10-13 | Windows go job and automatic `web.yml` runs only in the main public repository; elsewhere ci skips pull requests and `web.yml` runs on demand (§17.4). |
| 0.10.3 | 2026-10-13 | Fewer CI runs: Windows CI only on pushes, security not on PRs, Dependabot monthly and grouped per ecosystem; workflows avoid redundant runs (§17.4); maintainer tooling (not part of the public tree) adjusted. |
| 0.10.2 | 2026-10-13 | Web UI landed on the Apache-2.0 tree: CodeMirror styles applied under the unchanged CSP through a constructable stylesheet in cascade layer `codemirror` (§15.7); implementation decisions recorded (new §16.6: client, session, explorer, shortcuts, alerts drawer layout, Import page on the §11.5 `ImportJob` / `POST /imports` contract of 0.7.8, responsive, mock mode); `/alerts/new`, `/settings/import` (shown unless `GET /imports` is 404/405) and the not-found route (§16.1); `web-check` adds Prettier, coverage and the CSP check, `web.yml` described, artifact actions v7/v8 (§17.4); web test gate with coverage thresholds and the e2e list (§18); maintainer tooling (not part of the public tree) for offline web dependencies; `web/` files carry `Apache-2.0`, `web/package.json` licence `Apache-2.0`, no licence page, edition footer or user-limit notice in the UI. |
| 0.10.1 | 2026-10-13 | Merged main (0.10.0: Apache-2.0, editions removed, public export) into the M3b branch: M3b files carry the Apache-2.0 SPDX header; the user-limit login policy and `TIDEMARK_LICENSE` are gone from the M3b wiring; `docs/migrate-from-seq.md` and `docs/migrate-from-elastic.md` are public user docs (§6.1, §19.3). |
| 0.10.0 | 2026-10-13 | Open source under Apache-2.0 (owner decision): verbatim `LICENSE` (hash-checked), Apache-style `NOTICE`, every SPDX header `Apache-2.0`, contributions inbound = outbound with DCO sign-off, dependency policy re-justified for Apache-2.0 (§1, §2, §6.1, §19.1, §19.2). Public repository (new §19.3): `zeraOrg/tidemark` receives one squashed `Sync from private <sha>` commit per green `main` from the maintainers' development repository (maintainer tooling, not part of the public tree, checks and verifies the exported tree standalone); user docs `docs/{query-language,ingestion,configuration}.md` became the test input, kept consistent with this spec by tests (§6.1, §10.1, §17.4, §18). Editions removed: no licence keys, user limit, `internal/license`, `cmd/tmlicense`, `/license` routes, `license_required` (402), `TIDEMARK_LICENSE` or licence settings page; unlimited users (§6.1, §7, §11.1, §11.2, §15.2, §16.1, §19). |
| 0.9.3 | 2026-10-13 | `tidemark import` CLI (§9.9: kinds and flags, secrets from environment variables only, `system:cli` actor, lease holder `<node id>/cli-<pid>`, foreground run/follow with progress, `--detach`, list/status/cancel, exit status); migration guides `docs/migrate-from-seq.md` and `docs/migrate-from-elastic.md` (§6.1); import-replay deduplication integration test (§18). |
| 0.9.2 | 2026-10-13 | Merged main (0.9.x: Go 1.26, repaired CI, offline `vendor/` support) into the M3b branch (0.7.x rows below are the M3b changes made before this merge); `github.com/jackc/pgx/v5` re-added to the updated module set for the PostgreSQL metadata driver (§5, §19.1). |
| 0.9.1 | 2026-10-13 | Offline development: an untracked `vendor/` is skipped by the SPDX check (§19.1) and supported with `GOFLAGS=-mod=vendor` (§17.4); maintainer tooling (not part of the public tree) for offline dependency snapshots. |
| 0.9.0 | 2026-10-13 | CI repaired: Go 1.26 (`toolchain go1.26.9`) with current `golang.org/x/{crypto,net,sys,text}`, gRPC and protobuf modules because govulncheck and Trivy reported reachable `x/net/http2` and Go stdlib advisories (§5, §17.2); Go licence check is an allow-list on go-licenses v2 with an explicit `GOROOT`, new `licences` target (§17.4, §19.1); actions on Node 24 majors, Linux runners pinned to `ubuntu-24.04`, pinned tool versions, `scripts/ci-annotate.sh`, maintainer tooling (not part of the public tree) for module updates (§6.1, §15.9, §17.1, §17.4); Dependabot module updates folded in (clickhouse-go 2.49, klauspost/compress 1.20, client_golang 1.25, otlp 1.11) with the hash-pinned licence exception for `segmentio/asm` (MIT-0) (§19.1); gosec findings of golangci-lint v2.14 resolved (§18); Trivy 0.58.0 → 0.75.0 so the image scan uses a current scanner (§17.4). |
| 0.8.0 | 2026-10-13 | Web UI (§16): React SPA per §5 with the routes of §16.1 — shell (nav, URL-synced time picker, theme toggle dark/light/system, user menu), log explorer (CodeMirror TQL, histogram brush, facets and signals, virtualized stream with template highlighting, inline detail, live tail, table/timeseries results with Add to dashboard), traces and waterfall, metrics, dashboards (grid, variables, Builder↔Query, templates, import/export), alerts with editor drawer, streams, connections wizard, users, tokens, channels, audit, system; Vitest + Playwright gates and the MSW mock build (§17.4, §18). |
| 0.7.8 | 2026-10-13 | Importers implemented (§9.9): job kinds/statuses, config fields and limits, sealed secrets, frozen ranges, runner (5 s poll, ≤ 2 jobs per node, `import:<id>` lease, resume on another node), retry/permanence rules, page tokens and deterministic event ids, ES PIT/scroll resume via last sort value, Seq newest-first `afterId` paging (corrected) and signal import, CLEF checkpoints, Seq/KQL filter translation, Kibana mapping; `/imports` API with multipart uploads, per-route body limits (`api.BodyLimiter`) and exact `ImportJob` shapes (§11.5); `cluster.Elector.Release` for per-job leases. |
| 0.7.7 | 2026-10-13 | HA reference deployment `deploy/ha/` (README, per-node env files, ClickHouse cluster/Keeper/users config, HAProxy/nginx/Caddy) in the tree (§6.1); `scripts/with-clickhouse-cluster.sh` runs the replicated-schema test against a local 3-replica cluster (§18). |
| 0.7.6 | 2026-10-13 | Merged main (0.5.x query layer, packaging/CI, 0.6.x dashboards/alerting/streams) into the M3b branch: meta migration `0006_alert_groups` exists in both dialect directories (version gaps are allowed, §8.4); the lease `Elector` replaces the single-node leader in wiring; purge jobs are queued in `purge_jobs` (not in node memory) and run by the `purge` lease holder, which polls every 5 s (§12.5); `tidemark healthcheck` kept alongside the operator commands; `deploy/clickhouse/config.d` and `scripts/with-clickhouse.sh` declare the `backups` disk (the ops integration test skips its ClickHouse part on servers without one); new HA/WAL/backup variables documented in `deploy/.env.example`. |
| 0.7.5 | 2026-10-13 | Operator commands (§8.5 item 8): backup directory layout and manifest, restore rules (validation first, safety copy, native vs JSON path, SQLite → PostgreSQL move, ClickHouse RESTORE), migrate apply/status/dry-run without side effects, doctor checks and thresholds; `admin reset-password` honours the metadata driver (§6.1). |
| 0.7.4 | 2026-10-13 | Admin-supplied connection keys (§9.9, §11.5): normative alphabet/length rules, 4-character display prefix, entropy estimator (classes, runs, sequences, repeated pairs), uniqueness incl. grace-period keys (409), accepted on create and rotate only. |
| 0.7.3 | 2026-10-13 | HA runtime (§17.5): `internal/cluster` Elector/Leader semantics (validity window TTL − renew interval, `Lead`, lease names), ClickHouse migration lease at startup, LISTEN/NOTIFY bus with reconnect invalidation, 10 s HA key-cache TTL, graceful drain with `/readyz` 503 (§11.6); `TIDEMARK_DRAIN_SECONDS` and validation patterns for node id / cluster name (§7). |
| 0.7.2 | 2026-10-13 | Ingestion WAL implemented (§9.8): record/payload v1 and segment naming, group-commit syncer, two-slot CRC checkpoint with pending batch, deterministic insert token = UUID(SHA-256 of record batch UUIDs), replay bounded to the pending range, corrupt-record skipping with drop reasons `wal_corrupt`/`wal_decode` (§8.5 item 7 amended). |
| 0.7.1 | 2026-10-13 | ClickHouse durability & cluster mode: migration 0006 sets `non_replicated_deduplication_window`; inserts carry `insert_deduplication_token` via `store.WithDedupToken`; migration lint test; `ON CLUSTER` + `Replicated*MergeTree` rendering rules (§8.2); `insert_quorum` values; transient vs permanent ClickHouse error classification (§9.8); cluster integration test variables (§18). |
| 0.7.0 | 2026-10-13 | Metadata durability (M3b): migrations split into `migrations/{sqlite,postgres}` with `min_compatible_binary` headers; transactional, locked migrations with `schema_info` downgrade protection; pre-migration backups (`VACUUM INTO` / JSON export) and `meta.ImportJSON`; PostgreSQL driver (pgx v5 stdlib) behind a dialect layer with normative portability rules (§8.4); migration 0002 adds `leases`, `import_jobs`, `signals.disabled`; transparent conditional password re-hash; upgrade fixture `meta-0.4.0.db` + manifest (§8.5). |
| 0.6.4 | 2026-10-13 | Merged query layer (0.5.x) and dashboards/alerting/streams (0.6.x) branches; alert evaluator wired to the ClickHouse reader. |
| 0.6.3 | 2026-10-13 | Backend B completion: exact response shapes for streams/stats, field mappings, indexed fields, purge (dry run and job, `droppedPartitions` format), dashboards/templates, alerts, `/alerts/{id}/test`, history and channel test (§11.4, §11.5; the stream-bodies paragraph moved below the §11.5 table); which TQL comparisons can use an indexed field's bloom filter (§12.4). Integration tests on real ClickHouse cover TTL, indexed fields, purge, stream stats and alert evaluation. |
| 0.6.2 | 2026-10-13 | Alerting implemented: rule validation, `noData` policies, per-group state machine and evaluator details (§14.1, new §14.3), channel configs, payloads, delivery and audit (§14.2), alert/channel API bodies and roles (§11.4: channels viewer read, admin write), `alert_rules.eval_state` + `alert_history.group_key` (meta migration 0006). |
| 0.6.1 | 2026-10-13 | Dashboards implemented: strict document schema and limits (§13.1), API details incl. `POST /dashboards/{id}/panels/{panelId}/query` for server-side variable substitution and `?service=` on templates (§11.4), embedded templates (§13.3); JSON data files exempt from SPDX headers (§19.1). |
| 0.6.0 | 2026-10-13 | Streams & index management implemented (§12.2–12.7, §11.5 bodies, `GET /indexed-fields`); `StreamAdmin` port reshaped (`Mappings`, `DeleteStream`, purge request/result, `DropIndexedField` by field); `Leader` interface for singleton jobs; secret box format (§15.5). |
| 0.5.5 | 2026-10-13 | CI: `ci.yml` jobs (Linux + Windows go job, integration job whose ClickHouse users are created by SQL mirroring users.d — enforced by `internal/repotest`, notices, guarded web job), `security.yml` jobs and `dependabot.yml` (§6.1, §17.4). |
| 0.5.4 | 2026-10-13 | Packaging: `.gitignore`/`.dockerignore`, `scripts/`, `docker-compose.dev.yml`, `.env.example` added to the tree (§6.1); compose networks, `TM_*` settings, hardening, `tidemark healthcheck` command and health checks, `.env.example` rules, Dockerfile stages (§17.2); installer modes and Windows data-dir ACL (§17.3); Makefile/build.ps1 target list (§17.4). |
| 0.5.3 | 2026-10-13 | SPDX header rule details (first 5 lines, comment styles, exemptions) enforced by the new `internal/repotest` package (§19.1); Markdown, `go.mod` and editor/git config files now carry the header. |
| 0.5.2 | 2026-10-13 | Search API implemented (§11.3 details: parameter limits, response shapes, span in ms, zero-filled histograms, facet existence filter / otherCount / shared sample cutoff, tail lag and window, trace summaries, export paging, CSV layout, formula-injection guard and write deadline, completion values); signals CRUD with the `updatedAt` concurrency token and ownership rules (§11.4); too many tails ⇒ 429 |
| 0.5.1 | 2026-10-13 | Query layer: `store.Querier` reshaped around `store.Filter` (`Histogram`, `Newest`, `Context`, `Tail`, `Traces`, `MetricNames`; facets are composed by the service) (§6.2); per-query reader settings, `max_result_rows` only for single-level statements, full-precision time arguments, result shaping (§10.6); `tql.AutoSpan`, `tql.BuiltinFields`; integration tests need `scripts/with-clickhouse.sh` or `TIDEMARK_TEST_CLICKHOUSE_ADDR` and run packages sequentially (`-p 1`, shared database) (§18). |
| 0.5.0 | 2026-10-13 | Static checks (§18): golangci-lint must be clean for Linux, Windows and the `integration` tag; gocritic `rangeValCopy` disabled like `hugeParam`; `//nolint` only with a reason. Existing findings fixed. |
| 0.4.1 | 2026-10-13 | Planned feature: AI dashboard assistant (§13.4, M4). |
| 0.4.0 | 2026-10-13 | Owner requirements: optional HA (§17.5: PostgreSQL metadata, ClickHouse replication, leases, drain, rolling upgrades); durability & upgrade safety (§8.5); ingestion WAL (§9.8); importers from Seq/Elasticsearch/OpenSearch/Kibana/CLEF and admin-supplied migration keys (§9.9); approved UI design & tokens (§16.2); SQLite driver is `ncruces/go-sqlite3` (§5). |
| 0.3.0 | 2026-10-13 | TQL implemented: final `internal/tql` API (§10.6: `Options`, `Compiled`, `Cursor`, `CompileWhere`, `Substitute`, `Complete`/`Completion`); normative lexical, stage-order, resolution and semantic details (§10.2–10.5); SQL shapes; extra limits (aggregates, fields, buckets); test layout (§18). |
| 0.2.0 | 2026-10-13 | Licensing: PolyForm Noncommercial 1.0.0; Free (single-user) vs licensed multi-user editions (§19). |
| 0.1.0 | 2026-10-13 | Initial specification. |
