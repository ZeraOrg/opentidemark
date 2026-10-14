<!-- SPDX-License-Identifier: Apache-2.0 -->
# Operations

Run OpenTidemark day to day: backup and restore with the exact CLI syntax, `tidemark doctor`,
upgrades and migrations (including rolling upgrades in HA), monitoring, capacity and retention
planning, migrating from Seq and Elasticsearch, log locations per platform, and an incident
runbook.

## Running operator commands {#operator-commands}

Every command accepts the configuration variables and flags of the server
([Configuration](../configuration.md)), so it must run with the same configuration.

| Install | How to run `tidemark <command>` |
|---|---|
| Compose, server running | `docker compose exec tidemark tidemark <command>` |
| Compose, server stopped (`restore`) | `docker compose stop tidemark` then `docker compose run --rm --no-deps tidemark <command>` |
| Native Linux | `sudo -u tidemark tidemark <command> --env-file /etc/tidemark/tidemark.env` |
| Native Windows | `& "$env:ProgramFiles\Tidemark\tidemark.exe" <command>` (elevated PowerShell) |

| Command | Purpose |
|---|---|
| `tidemark version` | Print the version |
| `tidemark doctor [--json]` | Health checks; non-zero exit on any `fail`; safe while running |
| `tidemark backup --out <dir> [--meta-only]` | Metadata export (+ native SQLite copy) and ClickHouse® `BACKUP` |
| `tidemark restore --from <backup dir> [--force] [--with-clickhouse]` | Restore with the server stopped |
| `tidemark migrate [--status \| --dry-run]` | Apply, list or preview migrations |
| `tidemark admin reset-password --username <name> [--password-stdin]` | Reset a password |
| `tidemark import …` | Import from Seq, Elasticsearch/OpenSearch, Kibana saved objects, CLEF files |
| `tidemark healthcheck [--url URL]` | Exit 0 when the URL (default `http://127.0.0.1:8080/healthz`) answers 200 |
| `tidemark service install\|uninstall\|start\|stop` | Windows service management |

!!! tip "Git Bash on Windows"
    Git Bash rewrites arguments that look like POSIX paths (`/data/…` becomes `C:/Program
    Files/Git/data/…`) before they reach `docker exec`. Prefix such commands with
    `MSYS_NO_PATHCONV=1`, or use PowerShell.

## Backup and restore {#backup-and-restore}

A complete backup has three parts; copy all of them off the server:

1. the `tidemark backup` directory (`meta.json` with users, teams, connections, dashboards,
   alerts, settings and the audit log, the native SQLite copy, `manifest.json`);
2. the ClickHouse backup it names (`tidemark-<timestamp>` on ClickHouse's `backups` disk,
   `/var/lib/clickhouse/backups/` by default);
3. `secret.key` from the data directory (not part of the backup directory).

### Back up

=== "Docker Compose"

    ```bash
    docker compose exec tidemark tidemark backup --out /data/manual-backups
    # clickhouse: 5 tables backed up to disk "backups" as tidemark-20261013T204714Z
    # backup written to /data/manual-backups/tidemark-backup-20261013T204714Z
    docker compose cp tidemark:/data/manual-backups ./tidemark-backups
    docker compose cp clickhouse:/var/lib/clickhouse/backups ./clickhouse-backups
    docker compose cp tidemark:/data/secret.key ./tidemark-backups/secret.key
    ```

=== "Linux native"

    ```bash
    sudo install -d -m 0700 -o tidemark -g tidemark /var/backups/tidemark
    sudo -u tidemark tidemark backup --env-file /etc/tidemark/tidemark.env --out /var/backups/tidemark
    sudo cp /var/lib/tidemark/secret.key /var/backups/tidemark/
    # and the ClickHouse backup from the ClickHouse host: /var/lib/clickhouse/backups/tidemark-<stamp>
    ```

=== "Windows native"

    ```powershell
    & "$env:ProgramFiles\Tidemark\tidemark.exe" backup --out D:\Backups\tidemark
    Copy-Item "$env:ProgramData\Tidemark\secret.key" D:\Backups\tidemark\
    docker cp tidemark-clickhouse:/var/lib/clickhouse/backups D:\Backups\clickhouse
    ```

`manifest.json` (format `tidemark-backup/1`) records the version, node, metadata driver and schema
version, and the ClickHouse database, disk, backup name and tables. Without a `backups` disk in
ClickHouse a warning is recorded and only metadata is saved; with ClickHouse unreachable the
backup fails unless `--meta-only`.

Schedule it (Linux example, daily at 02:30, keep 14 days):

```ini
# /etc/systemd/system/tidemark-backup.service
[Service]
Type=oneshot
User=tidemark
ExecStart=/usr/local/bin/tidemark backup --env-file /etc/tidemark/tidemark.env --out /var/backups/tidemark
ExecStartPost=/usr/bin/find /var/backups/tidemark -maxdepth 1 -name 'tidemark-backup-*' -mtime +14 -exec rm -rf {} +
# /etc/systemd/system/tidemark-backup.timer
[Timer]
OnCalendar=*-*-* 02:30:00
Persistent=true
[Install]
WantedBy=timers.target
```

ClickHouse backups on the `backups` disk are not pruned by OpenTidemark; remove old
`tidemark-<stamp>` directories on the ClickHouse host after copying them off.

### Restore

Restore runs with the server **stopped**. It validates the manifest before changing anything.

```bash
# Compose
docker compose stop tidemark
docker compose run --rm --no-deps tidemark restore --from /data/manual-backups/tidemark-backup-<stamp> --force
docker compose up -d
```

- SQLite target with an existing database needs `--force`; the current database is first copied
  to `TIDEMARK_BACKUP_DIR` as `meta-pre-restore-<stamp>.db`.
- The restored metadata is migrated to the running binary's schema (downgrade protection applies).
- `--with-clickhouse` also restores the telemetry tables (`RESTORE TABLE … FROM Disk('backups',
  …)`); target tables must be absent or empty, so use it for a fresh ClickHouse. The ClickHouse
  backup directory must be on that server's `backups` disk.
- Put `secret.key` back into the data directory before starting, otherwise channel and import
  secrets cannot be decrypted.
- Moving from SQLite to PostgreSQL (HA): restore the backup into an empty, migrated PostgreSQL
  with `TIDEMARK_META_DRIVER=postgres` and `TIDEMARK_META_DSN` set.

## `tidemark doctor`

```text
[ok  ] config                 node 945d70580127, single node (sqlite), clickhouse cluster none, version 0.1.0
[ok  ] metadata               sqlite reachable
[ok  ] metadata schema        version 8, 0 pending (applied at next start or by `tidemark migrate`)
[ok  ] clickhouse ma-ch:9000  version 25.8.33.6
[ok  ] clock skew ma-ch:9000  2ms (leases need < 10s)
[ok  ] clickhouse schema ma-ch:9000 6 of 6 migrations applied
[ok  ] clickhouse reader      user tidemark_reader can connect
[ok  ] wal events             1 segments, 0 bytes not yet in clickhouse, 0 torn bytes (truncated at next start), pending batch false
[ok  ] wal size               24787912 of 10737418240 bytes (0.2%); full ⇒ ingestion backpressure
[ok  ] disk /data             951312 MiB free of 1031018 MiB
```

| Check | `warn` | `fail` |
|---|---|---|
| Metadata | pending migrations | unreachable; database requires a newer binary |
| ClickHouse (per address) | pending migrations | cannot connect; cluster not defined (cluster mode) |
| Reader user | | cannot connect |
| Clock skew vs ClickHouse / PostgreSQL | ≥ 2 s | ≥ 10 s |
| WAL | torn tail bytes; total ≥ 80 % of `TIDEMARK_WAL_MAX_BYTES` | |
| Disk of the data directory | < 10 % free | < 1 GiB free |

Use `doctor --json` in monitoring (exit code ≠ 0 on any `fail`).

## Upgrades and migrations

Upgrades keep data. Rules that make this safe:

- Migrations are additive (expand/contract); removing anything takes two releases.
- Before applying metadata migrations, OpenTidemark copies the metadata to
  `<data>/backups/meta-<fromVersion>-<stamp>.db` (SQLite `VACUUM INTO`; PostgreSQL: a JSON export).
  The last 10 are kept; if the copy fails, the start aborts.
- A binary older than the database's `min_compatible_binary` refuses to start (downgrade
  protection). Development builds skip the check.
- ClickHouse migrations run at startup with the writer user (in HA under the
  `clickhouse-migrations` lease; other nodes wait).

Procedure:

1. Read the `CHANGELOG.md` of the target version (before 1.0, minor versions may break APIs or
   configuration).
2. `tidemark backup` (and copy it off).
3. Preview: `tidemark migrate --status` and `tidemark migrate --dry-run` **with the new binary**
   (prints the SQL, changes nothing).
4. Upgrade per install method ([installation guide › Upgrading](../installation.md#upgrading)):
    - Docker bundle: new bundle directory, copy `.env`, `docker compose pull && docker compose up -d`.
    - Compose with a published image: set `TM_IMAGE=ghcr.io/zeraorg/tidemark:<version>`,
      `docker compose pull tidemark && docker compose up -d --no-build`.
    - Linux: `sudo systemctl stop tidemark && sudo ./deploy/install.sh native ./tidemark --start`.
    - Windows: `.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe -Start` (as Administrator).
5. `tidemark doctor`, check `/readyz`, ingest rate and alert evaluation.

### Rolling upgrade (HA)

```bash
# on each node in turn
sudo systemctl stop tidemark        # /readyz → 503, drains TIDEMARK_DRAIN_SECONDS, flushes the WAL
sudo ./deploy/install.sh native ./tidemark      # replaces binary and unit, keeps /etc/tidemark/tidemark.env
sudo systemctl start tidemark
sudo -u tidemark tidemark doctor --env-file /etc/tidemark/tidemark.env
# wait until the load balancer marks the node healthy, then continue with the next node
```

The first upgraded node migrates under the migration lock and takes the pre-migration backup
(PostgreSQL: JSON export); old nodes keep working because changes are additive.

## Monitoring OpenTidemark

- Prometheus® scrape of `/metrics` on every node, with the alert rules in
  [Metrics › Alert rules](metrics.md#alert-rules).
- Blackbox probe of `/readyz` (ClickHouse + metadata reachable; `503 {"status":"draining"}` during
  shutdown) and `/healthz` (process alive).
- `tidemark_wal_bytes` against `TIDEMARK_WAL_MAX_BYTES` and `tidemark_pipeline_queue_depth` for
  the backlog; `tidemark doctor --json` from cron for WAL, clock and disk.
- `GET /api/v1/system` (admin) for a quick look: version, uptime, `queueDepth`, cumulative
  `counters`, number of streams, ClickHouse version and disk free/total.
- The *Ingestion health* dashboard template (needs the self-metrics collected into OpenTidemark,
  see [Metrics](metrics.md#alternative-collect-self-metrics-into-opentidemark)).

## Capacity and retention planning

1. Measure: `GET /api/v1/streams` gives per stream and source `ratePer24h` (rows/s),
   `compressedBytes` and `rows`. Bytes per row = `compressedBytes / rows`.
2. Project per stream: `ratePer24h × 86,400 × retention days × bytes per row`. Sum over streams
   and sources, add 20–30 % for merges and parts in flight, plus the ClickHouse backups you keep
   on the same disk.
3. Compare with `GET /api/v1/system` → `clickhouse.disks[].freeBytes`.
4. Reduce: level rules for Verbose/Debug, shorter span retention, `minimumLevel` on noisy
   connections, purge of mistakes ([Streams and retention](streams-retention.md)).
5. The data directory needs room for the WAL cap (default 10 GiB) at all times.

## Migrating from Seq and Elasticsearch

Full guides: [Migrating from Seq](../migrate-from-seq.md) and
[Migrating from Elasticsearch, OpenSearch and Kibana](../migrate-from-elastic.md). Runbook:

1. Create the target streams and **extend retention** to cover the history you will import.
2. Cut clients over: change only the URL (Seq clients to port 5341 or the base URL, `_bulk`
   shippers to port 9200 or `/es`). To keep existing keys, create connections with an
   admin-supplied `key` (20–256 printable characters, entropy-checked).
3. Import history (jobs are resumable and deduplicated; at most 2 run per node):

    ```bash
    # Seq (API key read from an environment variable, never from a flag value)
    export SEQ_KEY=<SEQ_API_KEY>
    tidemark import seq --url https://seq.example.com --api-key-env SEQ_KEY \
      --from 2026-07-01T00:00:00Z --signals --stream seq-history

    # Elasticsearch / OpenSearch
    export ES_KEY=<ES_API_KEY>
    tidemark import elastic --url https://es.example.com:9200 --index 'logs-*' \
      --auth-type apiKey --api-key-env ES_KEY --from 2026-07-01T00:00:00Z

    # Kibana saved objects (saved searches ⇒ signals, index patterns ⇒ streams)
    tidemark import kibana export.ndjson

    # Follow
    tidemark import list
    tidemark import status <JOB_ID>
    ```

4. Check row counts and a few sample queries, then switch the old system off.

## Log locations

| Platform | OpenTidemark log | Notes |
|---|---|---|
| Docker Compose | `docker compose logs -f tidemark` | JSON lines on stderr |
| Linux native | `journalctl -u tidemark -f` | JSON lines |
| Windows native | Application event log, source `Tidemark` (start/stop reasons) | Full log only in the foreground: `Stop-Service Tidemark; & "$env:ProgramFiles\Tidemark\tidemark.exe" serve` |
| ClickHouse (Compose) | `docker compose logs clickhouse` | Level `warning` (config.d/tidemark.xml) |
| Dead letters | `<data>/deadletter/<signal>-<ts>.ndjson` | Rows ClickHouse rejected permanently |
| Pre-migration backups | `<data>/backups/meta-*.db` | Last 10 |

`TIDEMARK_LOG_LEVEL=debug` increases verbosity; every HTTP request is logged with method, path,
status, duration, client IP and request id (also returned as `X-Request-Id`).

## Incident runbook {#runbook}

### Ingest stopped

1. Client side: status codes (see [Ingestion › Troubleshooting](ingestion.md#troubleshooting-ingestion)).
   `401/403` ⇒ keys; `429` ⇒ connection rate limit; `503` ⇒ continue below.
2. `curl -s $TM/readyz` — `503 not ready` ⇒ ClickHouse or metadata unreachable ("ClickHouse down").
3. `tidemark doctor` — WAL bytes not yet in ClickHouse growing? disk full?
4. Metrics: `tidemark_pipeline_queue_depth` rising and `tidemark_pipeline_write_errors_total`
   increasing ⇒ ClickHouse rejects or is slow; check `docker compose logs clickhouse`.
5. Data accepted but invisible: stream paused (`stream_paused` drops), events below
   `minimumLevel`, wrong time range, the stream is outside the reader's scope (**Settings ›
   Users › Your account** shows what they see; the scope picker may also hide it), or the
   connection's `allowedStreams` rerouted the data to its default stream
   (`tidemark_ingest_rerouted_total`).

### UI slow

1. Which queries? Access log `duration_ms` for `/api/v1/query`, `/facets`, `/histogram`.
2. Narrow time ranges and add `stream:`/`service:` filters; avoid free text over weeks of data.
3. Many concurrent users or dashboards with short `refresh`: raise `refresh` to ≥ 1 min.
4. ClickHouse load: `SELECT query, elapsed FROM system.processes` as an admin user; queries are
   capped at `TIDEMARK_QUERY_TIMEOUT` and 1 GiB.
5. Recurrent filters on a high-cardinality key: [indexed field](streams-retention.md#indexed-fields).

### ClickHouse down

- Ingestion keeps acknowledging while the WAL has room (`TIDEMARK_WAL_MAX_BYTES`), then returns
  `503`. Nothing acknowledged is lost; it is written when ClickHouse is back (deduplicated).
- Queries, dashboards and alert evaluations fail (alert rules go to `error` after 3 failures).
- `/readyz` answers `503`; in HA the load balancer moves traffic, and `TIDEMARK_CLICKHOUSE_ADDR`
  lists all replicas for client failover.
- Fix ClickHouse (logs, disk, memory, permissions on `config.d`/`users.d`), then watch
  `tidemark_pipeline_queue_depth` drain to 0.

### Disk full

| Disk | Symptom | Action |
|---|---|---|
| Data directory | WAL cannot grow ⇒ `503`; SQLite writes fail; doctor `fail` | Free space (old `manual-backups`, dead letters, import uploads under `<data>/imports`), never delete `wal/` or `tidemark.db*` while running |
| ClickHouse | Inserts fail ⇒ write errors, WAL grows | Lower retention or purge (partition drops free space fastest: purge without stream and filter for whole days); remove old ClickHouse backups; extend the volume |

After freeing space, check `tidemark doctor` and that the backlog drains.

### Lost admin password {#lost-admin-password}

```bash
# Compose
docker compose exec tidemark tidemark admin reset-password --username admin
# Linux
sudo -u tidemark tidemark admin reset-password --username admin --env-file /etc/tidemark/tidemark.env
# Windows (elevated)
& "$env:ProgramFiles\Tidemark\tidemark.exe" admin reset-password --username admin
```

It prints a random 24-character password that must be changed at the next sign-in
(`mustChangePassword: true`). `--password-stdin` sets a given password instead (reads one line).
The reset is audited (`auth.password_reset`). API tokens of the user stay valid.

