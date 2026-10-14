<!-- SPDX-License-Identifier: Apache-2.0 -->
# Deployment

Choose a topology for OpenTidemark (single-node Docker Compose, native Linux or Windows service,
or a three-node high-availability cluster) and prepare the host with a production checklist for
sizing, disks, time synchronisation, TLS, backups and firewalling.

The step-by-step installation is in the [installation guide](../installation.md); this chapter
helps you decide what to install and what to check before the system takes production traffic.

## Choosing a topology

| Topology | Components | Choose it when | Limits |
|---|---|---|---|
| **Single node, Docker Compose** (default) | `tidemark` + `clickhouse` containers, optional `otel-collector` profile | You want the smallest operational footprint on Linux, or Windows with Docker Desktop. ClickHouse® stays on an internal network. | One host is a single point of failure. Host ports in the Compose file are fixed (move the bind address with `TM_BIND_ADDR`). |
| **Single node, native Linux** | `tidemark` systemd service + ClickHouse from packages (or Docker) | You want systemd hardening, host-level monitoring and package-managed ClickHouse, or you already run ClickHouse. | Same single point of failure. You maintain ClickHouse configuration yourself. |
| **Single node, native Windows** | `Tidemark` Windows service + ClickHouse in Docker Desktop (WSL2) | Windows-only estates. | ClickHouse has no native Windows build; Docker Desktop must be running before the service can reach ClickHouse. |
| **High availability** (3 servers) | 3 × (OpenTidemark node + ClickHouse replica + Keeper), external HA PostgreSQL, load balancer | You need to survive the loss of one server without losing acknowledged and flushed data, and to upgrade without downtime. | Requires an existing HA PostgreSQL, NTP everywhere, Linux servers. Rate limits are per node. |
| macOS | Native binary + ClickHouse in Docker | Development and evaluation only. | **Not supported for production.** |

```text
Single node (Compose)                          High availability (deploy/ha/)
┌────────────── host ──────────────┐           clients
│ frontend net      backend net    │              │
│ ┌──────────┐     ┌────────────┐  │        load balancer (HAProxy | nginx | Caddy, checks /readyz)
│ │ tidemark │────▶│ clickhouse │  │       ┌──────┼──────┐
│ └──────────┘     └────────────┘  │     node1  node2  node3   tidemark + WAL
│  :8080 :5341 :9200 :4317 :4318   │     CH r1  CH r2  CH r3   ClickHouse replicas (1 shard)
│  (otel-collector :5514 :24224)   │     KP 1   KP 2   KP 3    Keeper (Raft quorum of 3)
└──────────────────────────────────┘       └──── HA PostgreSQL (metadata) ────┘
```

Install instructions per topology:

- Docker Compose: [installation guide, section 4](../installation.md#4-docker-compose-linux-windows-macos)
- Native Linux: [section 5](../installation.md#5-native-install-on-linux-systemd)
- Native Windows: [section 6](../installation.md#6-native-install-on-windows-service)
- High availability: [section 9](../installation.md#9-high-availability) and
  [`deploy/ha/README.md`](../../deploy/ha/README.md)

### Why HA is more than "three copies"

- **Nodes are stateless** except for the WAL, caches and rate limiters. All shared state is in
  PostgreSQL (users, sessions, keys, dashboards, alerts, jobs, settings) or ClickHouse.
- **ClickHouse cluster mode** (`TIDEMARK_CLICKHOUSE_CLUSTER`) creates replicated tables
  `ON CLUSTER`, and inserts use a quorum (`TIDEMARK_CLICKHOUSE_INSERT_QUORUM=auto` = majority), so
  an acknowledged *and flushed* batch exists on at least two of three replicas.
- **Singleton work** (alert evaluation, retention, stats, ClickHouse migrations, purge, imports)
  runs on the node that holds the matching lease (30 s lease, renewed every 10 s). A failed node is
  replaced within about 30 s.
- **Graceful drain:** on SIGTERM or service stop, `/readyz` answers `503` immediately and the node
  keeps serving for `TIDEMARK_DRAIN_SECONDS` (default 10) so the load balancer removes it, then it
  flushes its WAL.
- A permanently lost node loses only its **unflushed** WAL (at most `TIDEMARK_WAL_MAX_BYTES`).

## Production checklist

Work through every item before the first production client is connected.

### Sizing

OpenTidemark does not publish measured sizing figures. The design goal is at least 10,000
events/s on one node with 4 vCPU and 8 GB RAM; it has not been benchmarked. Plan from your own
volume:

- [ ] Estimate daily ingest (events/s × average event size) per signal and multiply by the
      retention days of each stream (defaults: logs 30, spans 7, metrics 30). ClickHouse
      compresses the data; the actual ratio per stream is shown in **Streams** (column *Ratio*) and
      in `GET /api/v1/streams` (`compressionRatio`) once data arrives. Re-plan after a week of real
      traffic.
- [ ] Leave headroom for user queries. Each query runs as the read-only ClickHouse user with a
      memory limit of 1 GiB and `TIDEMARK_QUERY_TIMEOUT` (default 30 s);
      `deploy/clickhouse/users.d/tidemark.xml` caps that user at 1 GiB and 60 s.
- [ ] Keep the ingestion limits at their defaults unless clients need more:
      `TIDEMARK_MAX_BODY_BYTES` 10 MiB (compressed), `TIDEMARK_MAX_DECOMPRESSED_BYTES` 100 MiB,
      `TIDEMARK_QUEUE_MAX_ROWS` 200,000 per signal, `TIDEMARK_BATCH_MAX_ROWS` 10,000.

### Disks

- [ ] **Data directory** (`/var/lib/tidemark`, `%ProgramData%\Tidemark`, `/data` in the container)
      on a local, durable disk with fsync honoured. It holds the WAL, the SQLite metadata,
      `secret.key`, dead-letter files, import uploads and the pre-migration backups.
- [ ] Free space in the data directory ≥ `TIDEMARK_WAL_MAX_BYTES` (default 10 GiB) plus the
      metadata and its 10 backup copies. The WAL grows when ClickHouse is slow or down; when it is
      full, ingestion answers `503`.
- [ ] `tidemark doctor` fails below 1 GiB free and warns below 10 % free in the data directory;
      monitor the same thresholds.
- [ ] ClickHouse data (`/var/lib/clickhouse`, Compose volume `ch-data`) on its own volume, sized
      from the retention estimate above. The `backups` disk is `/var/lib/clickhouse/backups/` on
      the same volume by default; move it or copy the backups off the server (see
      [Operations](operations.md#backup-and-restore)).

### Time synchronisation

- [ ] NTP (chrony, systemd-timesyncd, w32time) on every OpenTidemark and ClickHouse host. Event
      timestamps more than 7 days in the future are clamped to ingest time, and in HA mode leases
      depend on node clocks.
- [ ] `tidemark doctor` compares the node clock with ClickHouse (and PostgreSQL): warn ≥ 2 s,
      fail ≥ 10 s.

### TLS

- [ ] HTTPS for the UI and API, either a reverse proxy (recommended with Compose) or built-in TLS
      (`TIDEMARK_TLS_CERT_FILE` + `TIDEMARK_TLS_KEY_FILE`, all listeners, TLS 1.2+).
- [ ] `TIDEMARK_PUBLIC_URL=https://…` (secure cookies, HSTS, correct links in alerts and snippets).
- [ ] `TIDEMARK_TRUSTED_PROXIES` set to the proxy addresses when a proxy is used.
- [ ] TLS for ingestion from untrusted networks (proxy the ingestion ports or use built-in TLS).
- [ ] `TIDEMARK_CLICKHOUSE_TLS=true` when ClickHouse is on another host.

Details and proxy configurations: [Security hardening](security-hardening.md#tls).

### Backups

- [ ] A scheduled `tidemark backup --out <dir>` (metadata + ClickHouse `BACKUP`), copied off the
      host together with the ClickHouse backup it names and `secret.key`.
- [ ] A restore rehearsal on a scratch host (`tidemark restore --from … --force`).
- [ ] With PostgreSQL (HA): PostgreSQL's own backups in addition.

Procedures: [Operations › Backup and restore](operations.md#backup-and-restore).

### Firewall and exposure

- [ ] Open only the ports clients use. UI/API (8080 or the proxy's 443) to users; ingestion ports
      only from the networks applications send from.
- [ ] ClickHouse (9000, 8123, 9009) never reachable from untrusted networks. In Compose it is on
      the internal `backend` network and not published. In HA, open 9000, 9009, 9181 and 9234
      **between the servers only**.
- [ ] With Compose on a multi-homed or internet-facing host, set `TM_BIND_ADDR` (for example
      `127.0.0.1` behind a local proxy). The default `0.0.0.0` publishes every port on every
      interface.
- [ ] Windows: `install.ps1 -OpenFirewall` opens 8080, 5341, 9200, 4317 and 4318 for everyone;
      restrict the rules' remote addresses afterwards, or create your own rules.
- [ ] `/metrics` disabled, or enabled with `TIDEMARK_METRICS_TOKEN` (see [Metrics](metrics.md#scraping-opentidemark-with-prometheus)).

### Before go-live

- [ ] Admin password changed (no red banner), additional admins named, `admin` reviewed.
- [ ] Bootstrap connection revoked or restricted once real connections exist.
- [ ] Teams created and streams granted when more than one team shares the installation;
      **Deny by default** turned on once every non-admin is in a team
      ([Users and access](users-access.md#teams)).
- [ ] Default retention set (**Streams** › *Default retention*, or `PATCH /api/v1/settings`).
- [ ] Self-monitoring in place: Prometheus® scrape, alert rules, a dashboard from the *Ingestion
      health* template.
- [ ] `tidemark doctor` all `ok`.

## Platform notes

| Platform | Data directory | Config file | Logs |
|---|---|---|---|
| Docker Compose | volume `tm-data` → `/data` | `.env` next to `docker-compose.yml` | `docker compose logs tidemark` |
| Linux native | `/var/lib/tidemark` | `/etc/tidemark/tidemark.env` | `journalctl -u tidemark` (JSON lines) |
| Windows native | `%ProgramData%\Tidemark` | `%ProgramData%\Tidemark\tidemark.env` | Application event log, source `Tidemark` (stop reasons only); run in the foreground for full logs |

The container runs as UID 65532 with a read-only root filesystem, `no-new-privileges` and all
capabilities dropped. The Linux unit runs as user `tidemark` with `ProtectSystem=strict`,
`NoNewPrivileges`, `PrivateTmp` and `SystemCallFilter=@system-service`. The Windows service runs
as the virtual account `NT SERVICE\Tidemark`.
