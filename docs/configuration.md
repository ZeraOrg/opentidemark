<!-- SPDX-License-Identifier: Apache-2.0 -->
# Configuration

Tidemark is configured with environment variables. Every variable can also be passed as a
command-line flag named after it in kebab case without the prefix
(`TIDEMARK_HTTP_ADDR` ⇒ `--http-addr`). Precedence, lowest first: built-in defaults, the env file
(`TIDEMARK_ENV_FILE`), the process environment, flags. Invalid configuration stops the start with
a clear message; unknown `TIDEMARK_*` variables are ignored with a warning.

Secrets (marked *secret*) may instead be read from a file: set `<VARIABLE>_FILE` to its path, for
example `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD_FILE=/run/secrets/ch_writer` (Docker/Kubernetes
secrets). Setting both forms is an error.

`deploy/.env.example` lists every variable for Docker Compose.

## Variables

| Variable | Default | Description |
|---|---|---|
| `TIDEMARK_HTTP_ADDR` | `:8080` | Listener for the UI, `/api/v1`, native ingestion and the Seq-compatible endpoints. |
| `TIDEMARK_SEQ_ADDR` | `:5341` | Extra listener serving only the Seq endpoints (`""` disables it). |
| `TIDEMARK_ES_ADDR` | `:9200` | Elasticsearch-compatible listener (`""` disables it). |
| `TIDEMARK_OTLP_HTTP_ADDR` | `:4318` | OTLP/HTTP listener (`""` disables it). |
| `TIDEMARK_OTLP_GRPC_ADDR` | `:4317` | OTLP/gRPC listener (`""` disables it). |
| `TIDEMARK_TLS_CERT_FILE` | — | TLS certificate (PEM). When both certificate and key are set, **all** listeners use TLS 1.2+. |
| `TIDEMARK_TLS_KEY_FILE` | — | TLS private key (PEM). |
| `TIDEMARK_PUBLIC_URL` | `http://localhost:8080` | External URL, used in alert links and snippets; `https` makes cookies `Secure` and enables HSTS. |
| `TIDEMARK_DATA_DIR` | Linux `/var/lib/tidemark`, Windows `%ProgramData%\Tidemark`, container `/data` | Metadata database (SQLite), secret key and other local state. |
| `TIDEMARK_CLICKHOUSE_ADDR` | `localhost:9000` | ClickHouse native-protocol address(es), comma separated. |
| `TIDEMARK_CLICKHOUSE_DATABASE` | `tidemark` | ClickHouse database. |
| `TIDEMARK_CLICKHOUSE_WRITER_USER` | `tidemark_writer` | ClickHouse user for schema changes and inserts. |
| `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD` | — | Its password (*secret*). |
| `TIDEMARK_CLICKHOUSE_READER_USER` | `tidemark_reader` | Read-only (`readonly=2`) ClickHouse user for queries. |
| `TIDEMARK_CLICKHOUSE_READER_PASSWORD` | — | Its password (*secret*). |
| `TIDEMARK_CLICKHOUSE_TLS` | `false` | Use TLS for the ClickHouse connection. |
| `TIDEMARK_ADMIN_USERNAME` | `admin` | Admin account created on the first start (only when no users exist). |
| `TIDEMARK_ADMIN_PASSWORD` | `admin` | Its password (*secret*). While the default is in use Tidemark warns on every start and shows a banner; change it after the first login. |
| `TIDEMARK_BOOTSTRAP_INGEST_KEY` | — | Optional (*secret*): creates a connection "bootstrap" with this key on the first start. |
| `TIDEMARK_BATCH_MAX_ROWS` | `10000` | Maximum rows per ClickHouse insert. |
| `TIDEMARK_BATCH_MAX_WAIT` | `1s` | Maximum time before a batch is flushed. |
| `TIDEMARK_QUEUE_MAX_ROWS` | `200000` | Queue capacity per signal type; when full, ingestion answers `503` (backpressure). |
| `TIDEMARK_MAX_BODY_BYTES` | `10485760` | Limit for a (compressed) request body. |
| `TIDEMARK_MAX_DECOMPRESSED_BYTES` | `104857600` | Limit after decompression (protects against decompression bombs). |
| `TIDEMARK_QUERY_TIMEOUT` | `30s` | Execution time limit of a user query. |
| `TIDEMARK_QUERY_MAX_ROWS` | `10000` | Maximum rows returned by one query page or table. |
| `TIDEMARK_SESSION_IDLE_TIMEOUT` | `12h` | Session idle timeout. |
| `TIDEMARK_SESSION_ABSOLUTE_TIMEOUT` | `168h` | Maximum session lifetime. |
| `TIDEMARK_TRUSTED_PROXIES` | — | CIDRs of reverse proxies whose `X-Forwarded-For` is trusted. |
| `TIDEMARK_ALLOW_PRIVATE_WEBHOOKS` | `false` | Allow alert webhooks to private, loopback and link-local addresses (and plain `http`). |
| `TIDEMARK_SMTP_HOST` | — | SMTP server for e-mail notifications. |
| `TIDEMARK_SMTP_PORT` | `587` | SMTP port. |
| `TIDEMARK_SMTP_USERNAME` | — | SMTP user name. |
| `TIDEMARK_SMTP_PASSWORD` | — | SMTP password (*secret*). |
| `TIDEMARK_SMTP_FROM` | — | Sender address of notifications. |
| `TIDEMARK_SMTP_STARTTLS` | `true` | Require STARTTLS. |
| `TIDEMARK_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `TIDEMARK_MAX_STREAMS` | `200` | Maximum number of streams. |
| `TIDEMARK_METRICS_ENABLED` | `false` | Expose Prometheus metrics of Tidemark itself on `/metrics`. |
| `TIDEMARK_METRICS_TOKEN` | — | Optional bearer token required for `/metrics` (*secret*). |
| `TIDEMARK_SECRET_KEY_FILE` | `<data dir>/secret.key` | AES-256 key that encrypts notification secrets at rest; created on the first start. |
| `TIDEMARK_ENV_FILE` | `<data dir>/tidemark.env` | Env file read at start-up (when present) before the environment. |
| `TIDEMARK_NODE_ID` | host name (`tidemark` if unknown) | Unique name of this node (high availability, leases, write-ahead log); 1–64 characters of `A-Z a-z 0-9 . _ -`, starting with a letter or digit. |
| `TIDEMARK_META_DRIVER` | `sqlite` | Metadata database: `sqlite` (single node) or `postgres` (required for high availability). |
| `TIDEMARK_META_DSN` | — | PostgreSQL connection string when the driver is `postgres` (*secret*). |
| `TIDEMARK_CLICKHOUSE_CLUSTER` | — | ClickHouse cluster name; when set, the schema is created `ON CLUSTER` with replicated tables. |
| `TIDEMARK_CLICKHOUSE_INSERT_QUORUM` | `auto` | `auto` (majority of replicas in cluster mode, `1` otherwise) or a fixed number of replicas. |
| `TIDEMARK_WAL_ENABLED` | `true` | Write-ahead log: ingested data is on disk before the client gets its acknowledgement and is replayed after a crash. |
| `TIDEMARK_WAL_DIR` | `<data dir>/wal` | Directory of the write-ahead log. |
| `TIDEMARK_WAL_MAX_BYTES` | `10737418240` | Size cap of the write-ahead log for all signals (minimum 1 MiB); when full, ingestion answers `503` (backpressure). |
| `TIDEMARK_WAL_SYNC` | `batch` | `batch` (group commit, fsync before the acknowledgement) or `none` (testing only). |
| `TIDEMARK_BACKUP_DIR` | `<data dir>/backups` | Automatic backups of the metadata database taken before migrations (the last 10 are kept). |
| `TIDEMARK_DRAIN_SECONDS` | `10` | Graceful drain on shutdown, 0–600 seconds: `/readyz` answers `503` so the load balancer stops sending traffic before the listeners close. |
