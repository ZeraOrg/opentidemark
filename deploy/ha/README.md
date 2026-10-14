<!-- SPDX-License-Identifier: Apache-2.0 -->

# OpenTidemark high availability: 3-server reference setup

This directory implements the reference topology of SPEC §17.5. Single-node mode remains the
default; HA is enabled purely by configuration, using the same binary.

```
             clients (browsers, Serilog/NLog, Filebeat, OTel SDKs …)
                                  │
                    load balancer (lb/haproxy.cfg | lb/nginx.conf | lb/Caddyfile)
           ┌──────────────────────┼──────────────────────┐
      tm-node1                tm-node2                tm-node3
   tidemark + WAL          tidemark + WAL          tidemark + WAL
   clickhouse replica      clickhouse replica      clickhouse replica
   clickhouse keeper       clickhouse keeper       clickhouse keeper
           └──────────── external HA PostgreSQL (metadata) ───────┘
```

| Path | Purpose |
|---|---|
| `clickhouse/config.d/cluster.xml` | cluster `tidemark_cluster` (1 shard × 3 replicas), macros, Keeper client, `backups` disk, crash reports to the ClickHouse developers off |
| `clickhouse/config.d/keeper.xml` | embedded ClickHouse Keeper, Raft quorum of 3 |
| `clickhouse/users.d/tidemark.xml` | `tidemark_writer` (incl. `GRANT CLUSTER` for `ON CLUSTER` DDL) and read-only `tidemark_reader` |
| `nodes/clickhouse-nodeN.env` | per-node ClickHouse environment (replica name, Keeper id, host names, passwords) |
| `nodes/tidemark-nodeN.env` | per-node OpenTidemark configuration (`/etc/tidemark/tidemark.env`) |
| `lb/` | load balancer examples with `/readyz` health checks |

## Requirements

- Three Linux servers (`tm-node1..3`) that resolve each other's names, with clocks synchronised by
  NTP (leases use node clocks; `tidemark doctor` reports skew).
- PostgreSQL 14+ run highly available (managed service, Patroni, …). Create one database and one
  login role owning it, e.g. `CREATE ROLE tidemark LOGIN PASSWORD '…'; CREATE DATABASE tidemark
  OWNER tidemark;`.
- ClickHouse 24.8+ (25.8 tested) on every server.
- Ports open **between the servers only**: 9000 (native), 9009 (interserver), 9181 (Keeper client),
  9234 (Keeper Raft). Ports open to the load balancer: 8080, 5341, 9200, 4317, 4318.

## 1. ClickHouse and Keeper (every server)

1. Install ClickHouse, then copy `clickhouse/config.d/*.xml` to `/etc/clickhouse-server/config.d/`
   and `clickhouse/users.d/tidemark.xml` to `/etc/clickhouse-server/users.d/`.
2. Copy `nodes/clickhouse-nodeN.env` (N = this server) to `/etc/clickhouse-server/tidemark-ha.env`,
   mode `0640`, owner `root:clickhouse`. Replace both `generate-me` passwords with the **same**
   random values on all three servers (`openssl rand -hex 24`).
3. Load the file into the service:

   ```ini
   # /etc/systemd/system/clickhouse-server.service.d/tidemark-ha.conf
   [Service]
   EnvironmentFile=/etc/clickhouse-server/tidemark-ha.env
   ```

4. `systemctl daemon-reload && systemctl restart clickhouse-server` on all three, then check:
   `clickhouse-client -q "SELECT * FROM system.clusters WHERE cluster = 'tidemark_cluster'"` lists
   three replicas and `SELECT * FROM system.zookeeper WHERE path = '/'` answers (Keeper quorum).

OpenTidemark creates the database and tables itself (`ON CLUSTER`, `ReplicatedMergeTree`) the first
time a node starts; there is nothing to create by hand.

## 2. OpenTidemark (every server)

1. Install the binary as for a native install (`deploy/install.sh native`), but do not start it yet.
2. Copy `nodes/tidemark-nodeN.env` to `/etc/tidemark/tidemark.env` (`root:tidemark`, `0640`) and
   set the ClickHouse passwords from step 1, `TIDEMARK_PUBLIC_URL` and `TIDEMARK_TRUSTED_PROXIES`
   (the load balancer address).
3. Write the PostgreSQL DSN to `/etc/tidemark/meta-dsn` (`root:tidemark`, `0640`), for example
   `postgres://tidemark:<password>@pg.example.internal:5432/tidemark?sslmode=verify-full`.
4. Start the first node (`systemctl start tidemark`). It creates `/var/lib/tidemark/secret.key`,
   migrates PostgreSQL and (holding the `clickhouse-migrations` lease) ClickHouse. Copy
   `secret.key` to the other two nodes (same path, owner `tidemark`, mode `0600`) **before**
   starting them: every node must use the same key.
5. Start nodes 2 and 3. `tidemark doctor` on each node checks connectivity, schema compatibility,
   clock skew, the WAL and disk space.

## 3. Load balancer

Pick one of `lb/haproxy.cfg`, `lb/nginx.conf` or `lb/Caddyfile`. No sticky sessions are needed:
sessions, keys and settings live in PostgreSQL. Each node's `/readyz` returns `503` as soon as it
starts draining (SIGTERM / service stop), and the node keeps serving for `TIDEMARK_DRAIN_SECONDS`
so the load balancer removes it before it stops.

Rate limits are enforced per node: the effective limit is the configured limit × the number of
nodes behind the load balancer.

## Operations

- **Rolling upgrade:** upgrade one node at a time (`systemctl stop tidemark`, replace the binary,
  start). Migrations are additive, so the old nodes keep working; the first upgraded node migrates
  under the migration lock and takes a pre-migration backup.
- **Backups:** `tidemark backup --out /var/backups/tidemark` on any node saves the metadata
  (JSON export) and runs a ClickHouse `BACKUP` to the `backups` disk of the replica it is connected
  to; copy both off the servers.
- **Losing a server:** with quorum inserts (`TIDEMARK_CLICKHOUSE_INSERT_QUORUM=auto`) every
  acknowledged and flushed batch exists on at least two replicas. The lost node's unflushed WAL
  (bounded by `TIDEMARK_WAL_MAX_BYTES`) is the only data at risk. Two of three ClickHouse servers
  keep Keeper and inserts available.
- **Singleton work** (alert evaluation, stream statistics, purge, imports, ClickHouse migrations)
  runs on whichever node holds the corresponding lease in PostgreSQL; leases are renewed every
  10 s and expire after 30 s, so another node takes over within about 30 s of a failure.
