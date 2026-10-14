<!-- SPDX-License-Identifier: Apache-2.0 -->
# Security hardening

Harden an OpenTidemark installation before it takes production traffic: first sign-in and the
admin password, TLS with a reverse proxy or built in, trusted proxies and bind addresses,
connection keys and API tokens, roles and the audit log, security headers, secrets at rest,
webhook SSRF protection, rate limits and ClickHouse® isolation.

## First sign-in and the admin password {#admin-password}

On the first start with an empty metadata database OpenTidemark creates one administrator:

- user name `TIDEMARK_ADMIN_USERNAME` (default `admin`);
- password `TIDEMARK_ADMIN_PASSWORD` (default `admin`).

Both variables are read **only when no user exists**. Changing them later has no effect.

**Recommended:** set a strong `TIDEMARK_ADMIN_PASSWORD` before the first start, through a secret
file so it never appears in the environment of other processes:

=== "Docker Compose"

    ```bash
    # in the bundle directory or deploy/
    echo "TIDEMARK_ADMIN_PASSWORD=$(openssl rand -base64 30)" >> .env   # .env is 0600
    docker compose up -d
    grep '^TIDEMARK_ADMIN_PASSWORD=' .env    # read it once, store it in your vault, then delete the line
    ```

=== "Linux native"

    ```bash
    sudo install -m 0640 -o root -g tidemark /dev/null /etc/tidemark/admin_password
    openssl rand -base64 30 | sudo tee /etc/tidemark/admin_password >/dev/null
    echo 'TIDEMARK_ADMIN_PASSWORD_FILE=/etc/tidemark/admin_password' | sudo tee -a /etc/tidemark/tidemark.env
    sudo systemctl start tidemark
    # after the first successful sign-in:
    sudo sed -i '/^TIDEMARK_ADMIN_PASSWORD_FILE=/d' /etc/tidemark/tidemark.env
    sudo rm /etc/tidemark/admin_password
    ```

If you start with the default `admin`/`admin`, OpenTidemark logs a warning on every start, the UI
shows a red, non-dismissable banner, and `GET /api/v1/auth/me` returns
`"defaultCredentials": true`. Sign in and change the password at once (**Settings › Users ›
Your account**, or the *Change now* action of the banner). The UI prompts but does not block, so
treat the banner as a release blocker.

Password policy: at least 12 characters, at most 256, not equal to the user name, not one of the
10,000 most common passwords. Hashes are argon2id (m = 64 MiB, t = 3, p = 2). Five failed sign-ins
lock the account for 15 minutes (growing up to 24 h); an IP address may try 20 times in 5 minutes.

Lost admin password: `tidemark admin reset-password --username <name>` prints a random password
that must be changed at the next sign-in; see [Operations](operations.md#lost-admin-password).

## TLS {#tls}

Use HTTPS for anything beyond a local test. Choose one of two models.

| | Reverse proxy (recommended with Compose and HA) | Built-in TLS |
|---|---|---|
| Configure | Proxy terminates TLS; OpenTidemark listens on plain HTTP on a private address | `TIDEMARK_TLS_CERT_FILE` + `TIDEMARK_TLS_KEY_FILE` (PEM) |
| Scope | Per port, as configured in the proxy | **All** listeners (HTTP, Seq, `_bulk`, OTLP/HTTP, OTLP/gRPC), TLS 1.2+ |
| Certificate renewal | Proxy (ACME etc.) | Restart OpenTidemark after replacing the files |
| Container health check | Unchanged | Bundled check requests plain `http://127.0.0.1:8080/healthz` and fails; the Compose collector profile then does not start |

In both cases set:

```bash
TIDEMARK_PUBLIC_URL=https://tidemark.example.com     # Secure cookies + HSTS, links in alerts and snippets
```

When `TIDEMARK_PUBLIC_URL` is `https`, session cookies are `Secure` and every response carries
`Strict-Transport-Security: max-age=31536000`. Cookie-authenticated requests are accepted from
this origin or from the host the request was sent to, so set it to the exact origin users type
into the browser (a reverse proxy that rewrites the `Host` header relies on it).

### Reverse proxy requirements

- Pass `X-Forwarded-For` and set `TIDEMARK_TRUSTED_PROXIES` to the proxy's address (see
  [Trusted proxies](#trusted-proxies)).
- Do not buffer `/api/v1/tail` (Server-Sent Events, heartbeat every 15 s) and allow long reads.
- Allow request bodies of at least 1 GiB on the UI host if admins upload import files
  (Settings › Import); ingestion bodies are limited by OpenTidemark itself.
- OTLP/gRPC needs HTTP/2 end to end (h2c to the backend).
- Keep the security headers OpenTidemark sets; do not add CORS headers.
- Health-check `/readyz` (port 8080) so draining nodes leave rotation.

The repository ships complete three-node examples in `deploy/ha/lb/` (`haproxy.cfg`,
`nginx.conf`, `Caddyfile`). The single-node versions below follow the same settings.

=== "nginx"

    ```nginx
    # /etc/nginx/conf.d/tidemark.conf — OpenTidemark on 127.0.0.1 (TM_BIND_ADDR=127.0.0.1)
    upstream tidemark_ui        { server 127.0.0.1:8080; keepalive 32; }
    upstream tidemark_otlp_grpc { server 127.0.0.1:4317; }

    server {
        listen 443 ssl;
        http2 on;
        server_name tidemark.example.com;
        ssl_certificate     /etc/nginx/certs/tidemark.crt;
        ssl_certificate_key /etc/nginx/certs/tidemark.key;
        client_max_body_size 1100m;          # import uploads up to 1 GiB per file

        location / {
            proxy_pass http://tidemark_ui;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            proxy_set_header Host $host;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto https;
            proxy_buffering off;             # live tail (SSE)
            proxy_read_timeout 1h;
        }
    }

    # OTLP/gRPC with TLS from clients, h2c to OpenTidemark
    server {
        listen 4317 ssl;
        http2 on;
        ssl_certificate     /etc/nginx/certs/tidemark.crt;
        ssl_certificate_key /etc/nginx/certs/tidemark.key;
        client_max_body_size 0;
        location / {
            grpc_pass grpc://tidemark_otlp_grpc;
            grpc_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        }
    }
    ```

    With this layout every HTTP-based protocol uses `https://tidemark.example.com`: Seq clients
    the base URL, `_bulk` shippers `/es`, OTLP/HTTP `/otlp`. The connection snippets in the UI are
    generated in exactly this form from `TIDEMARK_PUBLIC_URL`.

=== "Caddy"

    ```caddyfile
    tidemark.example.com {
        request_body {
            max_size 1100MB
        }
        reverse_proxy 127.0.0.1:8080 {
            health_uri /readyz
            flush_interval -1            # live tail (SSE)
        }
    }

    tidemark.example.com:4317 {
        reverse_proxy h2c://127.0.0.1:4317
    }
    ```

=== "HAProxy"

    ```haproxy
    defaults
        mode http
        timeout connect 5s
        timeout client  60s
        timeout server  60s
        timeout tunnel  1h               # live tail (SSE)

    frontend ui
        bind :443 ssl crt /etc/haproxy/certs/tidemark.pem alpn h2,http/1.1
        http-request set-header X-Forwarded-Proto https
        option forwardfor
        default_backend tidemark_ui

    backend tidemark_ui
        option httpchk GET /readyz
        http-check expect status 200
        server tm1 127.0.0.1:8080 check inter 2s fall 2 rise 2

    frontend otlp_grpc
        bind :4317 ssl crt /etc/haproxy/certs/tidemark.pem alpn h2
        option forwardfor
        default_backend tidemark_otlp_grpc

    backend tidemark_otlp_grpc
        option httpchk GET /readyz
        server tm1 127.0.0.1:4317 proto h2 check port 8080 check-proto h1 inter 2s
    ```

### Built-in TLS

```bash
# /etc/tidemark/tidemark.env (native) or .env (Compose, with a volume override for the files)
TIDEMARK_TLS_CERT_FILE=/etc/tidemark/tls/tidemark.crt
TIDEMARK_TLS_KEY_FILE=/etc/tidemark/tls/tidemark.key
TIDEMARK_PUBLIC_URL=https://tidemark.example.com:8080
```

The files must be readable by the service account: `tidemark` (Linux), `NT SERVICE\Tidemark`
(Windows) or UID 65532 (container). With `ProtectSystem=strict`, keep them under `/etc/tidemark`.
In containers, check health with `tidemark healthcheck --url https://<name>:8080/healthz`; it only
passes when the container trusts the certificate and resolves the name.

## Trusted proxies {#trusted-proxies}

`TIDEMARK_TRUSTED_PROXIES` is a comma-separated list of CIDRs. When the TCP peer is in the list,
OpenTidemark walks `X-Forwarded-For` from right to left and uses the first address that is **not**
trusted as the client address. Otherwise the TCP peer is the client.

The client address is used for: per-connection `allowedCidrs`, rate limits (login, ingestion auth
failures), the audit log and the access log. Getting it wrong either blocks legitimate clients or
lets one client spend everyone's budget.

```bash
TIDEMARK_TRUSTED_PROXIES=10.0.10.5/32,10.0.10.6/32   # the proxy/LB addresses only, never 0.0.0.0/0
```

Never trust a range that untrusted clients can send from: they could then forge their source
address and bypass CIDR restrictions.

## Bind addresses and exposure

| Variable | Default | Hardening |
|---|---|---|
| `TIDEMARK_HTTP_ADDR` | `:8080` | `127.0.0.1:8080` behind a local proxy |
| `TIDEMARK_SEQ_ADDR` | `:5341` | `""` if no Seq clients (they can use 8080) |
| `TIDEMARK_ES_ADDR` | `:9200` | `""` if no `_bulk` shippers, or if they use `/es` on 8080 |
| `TIDEMARK_OTLP_HTTP_ADDR` | `:4318` | `""` if OTLP/HTTP clients use `/otlp` on 8080 |
| `TIDEMARK_OTLP_GRPC_ADDR` | `:4317` | `""` if no gRPC clients |
| `TM_BIND_ADDR` (Compose only) | `0.0.0.0` | `127.0.0.1` or a private interface address |

The Compose file always publishes all five ports; to stop publishing one, set `TM_BIND_ADDR` to
loopback and expose what you need through the proxy, or disable the listener inside the container
with the variable above.

`/healthz` and `/readyz` are unauthenticated and return only a status. `/metrics` is off by
default (see [Metrics](metrics.md#scraping-opentidemark-with-prometheus)).

## Connection keys {#connection-keys}

Every ingestion request must carry a connection key; there is no anonymous ingestion.

- Format `tmk_` + 40 base62 characters (≥ 238 bits), generated with a CSPRNG, stored as a
  SHA-256 hash plus an 8-character prefix, **shown once**. Logs show only `tmk_abcd…`.
- Per connection: allowed protocols (`seq`, `elastic`, `otlp`, `native`), allowed source CIDRs
  (up to 64), expiry, rate limit (events/s), minimum level, default stream, allowed streams (up
  to 100), applied properties (including a pinned stream), rotate with grace period, revoke.
- Lookups use an in-memory cache with a 60 s TTL (10 s in HA mode), invalidated on change.

Hygiene rules:

1. **One connection per application and environment.** Name it so the audit log is readable
   (`checkout-api prod`). Never share keys between teams.
2. **Least protocol:** allow only the protocol the client uses. A Serilog app needs `seq`; a
   Filebeat fleet `elastic`; an OpenTelemetry® Collector `otlp`.
3. **Restrict sources** with `allowedCidrs` (single addresses are accepted and stored as `/32`).
   Combine with `TIDEMARK_TRUSTED_PROXIES` when a proxy is in front.
4. **Limit the streams.** `allowedStreams` lists the streams a key may write to; data naming
   another stream lands in the connection's default stream instead (counted in
   `tidemark_ingest_rerouted_total`), so one team's key cannot write into another team's
   streams. For untrusted senders **pin the stream** with
   `appliedProperties: {"@stream": "<name>"}`, so nothing the client sends through `@stream`,
   `_index` or `tidemark.stream` is honoured at all.
5. **Set an expiry** for temporary keys (load tests, vendors).
6. **Retire the bootstrap key.** `TIDEMARK_BOOTSTRAP_INGEST_KEY` creates a connection
   `bootstrap` that accepts every protocol from everywhere. Revoke it (or restrict it) once real
   connections exist, and remove the variable from `.env`.
7. **Store keys as secrets** in the client (environment, secret store), never in source control.

Rotation without downtime:

```bash
# 1. Rotate: a new key is returned once; the old key keeps working for graceMinutes (0–1440)
curl -sS -X POST "$TM/api/v1/connections/<CONNECTION_ID>/rotate" \
  -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"graceMinutes": 60}' | jq -r .key > new-key.txt
# 2. Roll the new key out to all clients within the grace period.
# 3. Verify the old key is no longer used: lastUsedAt / lastUsedIp of the connection, and
#    tidemark_ingest_rejected_requests_total{reason="auth"} after the grace period ends.
```

Compromised key: `POST /api/v1/connections/<id>/revoke` takes effect at once (`403 key revoked`),
then create a new connection or rotate with `graceMinutes: 0`.

## API tokens {#api-tokens}

API tokens (`tmt_` + 40 characters) authenticate automation against `/api/v1` with
`Authorization: Bearer <token>`. A token belongs to a user and has a scope:

| Scope | Effective role |
|---|---|
| `read` | viewer |
| `write` | editor |
| `admin` | admin |

The effective role is the **lower** of the scope and the owner's current role, so demoting a user
also limits their tokens. Tokens are shown once, can expire (`expiresAt`), are limited to 100 per
user, and are deleted with `DELETE /api/v1/tokens/{id}`. Disabling a user revokes their tokens,
and so does a password reset by an admin or with `tidemark admin reset-password`. A user changing
their own password keeps their tokens.

Hygiene: one token per automation, the smallest scope that works, an expiry, stored in a secret
store. Bearer requests need no CSRF header. Details and examples in [Users and access](users-access.md#api-tokens).

## Roles

| Role | Can |
|---|---|
| viewer | Read everything that is not admin-only: search, traces, metrics, dashboards, alerts, channels (secrets masked), streams list and mappings, own API tokens |
| editor | viewer + create/change signals, dashboards and alert rules; export (`GET /export`) |
| admin | everything: users, connections and keys, streams, retention, indexed fields, purge, channels, settings, audit log, system, imports |

The last admin cannot be deleted or demoted. See [Users and access](users-access.md#roles).

Roles decide what a user may do; **teams** decide which streams' data a user may read. Every
data read is limited on the server to the caller's stream scope (admins: every stream; team
members: the union of their teams' grants; users in no team: every stream, or none with **Deny by
default**). API tokens see what their owner sees. Put sensitive data into its own stream and grant
it only to the teams that need it; the environment selection is a filter, not a boundary
([Users and access](users-access.md#teams)).

## Audit log

The audit log is append-only (database triggers reject updates and deletes). It records sign-ins
(success and failure), sign-outs, password changes and resets, user, team, token, connection,
stream, channel, alert and settings changes, purges, exports, imports and sampled ingestion
authentication failures, with actor (`user:<id>`, `token:<id>`, `system:…`), client IP, target and
details. Access changes are explicit: `team.members`, `team.streams` and `user.teams` record
`{added, removed}`, `settings.update` records `access.denyByDefault`, `alert.create|update` the
rule's `streams` and `connection.create|update` the `allowedStreams`. Keys and secrets are never
logged.

```bash
# Last 50 connection key rotations
curl -sS "$TM/api/v1/audit?action=connection.rotate&limit=50" -H "Authorization: Bearer $TM_TOKEN"
# Everything one token did yesterday
curl -sS "$TM/api/v1/audit?actor=token:<TOKEN_ID>&from=now-2d&to=now-1d" -H "Authorization: Bearer $TM_TOKEN"
```

UI: **Settings › Audit log** (admin). Ship the audit log off the host if you need tamper evidence
beyond the database triggers, for example with a scheduled export of `GET /audit` into a separate
stream or SIEM.

## Security headers and CSP

Every UI and API response carries:

```text
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  connect-src 'self'; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Permissions-Policy: camera=(), microphone=(), geolocation=()
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
X-Frame-Options: DENY
Strict-Transport-Security: max-age=31536000          # only when TIDEMARK_PUBLIC_URL is https
```

API responses also carry `Cache-Control: no-store` and `X-Request-Id`. There is no CORS on
`/api/v1`. Reverse proxies must pass these headers unchanged and must not add `'unsafe-inline'`
or third-party origins; the UI needs none (self-hosted fonts, no inline scripts or styles).

## Secrets at rest

| Secret | Protection |
|---|---|
| User passwords | argon2id PHC hashes |
| Sessions, connection keys, API tokens | SHA-256 hashes; raw value shown once |
| Channel secrets (webhook URL, HMAC secret, header values, Slack/Teams URLs), import credentials | AES-256-GCM with the key in `<data>/secret.key` (or `TIDEMARK_SECRET_KEY_FILE`); returned masked as `"••••"` |
| ClickHouse and PostgreSQL credentials, SMTP password | Configuration; use `<VAR>_FILE` with Docker/Kubernetes secrets |

- `secret.key` is created with mode 0600 on the first start (Windows: ACL limited to
  Administrators and SYSTEM). Back it up with the metadata: without it, channel and import secrets
  cannot be decrypted after a restore.
- In HA mode every node must have the **same** `secret.key`.
- These secrets can be given as `<VAR>_FILE` (for example
  `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD_FILE=/run/secrets/ch_writer`):
  `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD`, `TIDEMARK_CLICKHOUSE_READER_PASSWORD`,
  `TIDEMARK_ADMIN_PASSWORD`, `TIDEMARK_BOOTSTRAP_INGEST_KEY`, `TIDEMARK_SMTP_PASSWORD`,
  `TIDEMARK_METRICS_TOKEN` and `TIDEMARK_META_DSN`. Setting both forms is an error.

- In Docker Compose, `.env` is passed with `env_file`, so a `_FILE` variable must point to a path
  inside the container (mount the file with your own override file).
- Keep `.env` (0600), `/etc/tidemark/tidemark.env` (root:tidemark 0640) and the data directory
  private.

## Outbound requests and SSRF {#ssrf}

Alert notifications (webhook, Slack, Teams) use an SSRF-safe HTTP client:

- URLs must be `https`;
- addresses are checked **after DNS resolution, at dial time**: loopback, link-local, private
  (RFC 1918/4193), CGNAT, multicast and cloud metadata (169.254.169.254) are rejected;
- redirects are not followed; timeouts are 10 s per attempt;
- error messages never contain the target URL.

`TIDEMARK_ALLOW_PRIVATE_WEBHOOKS=true` lifts the address check and allows plain `http`. Set it only
when notification targets are on an internal network (an internal webhook relay, a self-hosted
chat), and prefer an internal HTTPS endpoint. Importers (Seq, Elasticsearch sources) always allow
private addresses because sources are usually internal; they never follow redirects and always
verify TLS.

## Rate limiting and resource limits

| Limit | Value | Effect |
|---|---|---|
| Sign-in per account | 5 failures ⇒ 15 min lock (exponential up to 24 h) | Generic error message |
| Sign-in per IP | 20 attempts / 5 min | Further attempts rejected |
| Ingestion auth failures per IP | 30 / min ⇒ temporary `403` for 5 min | Protects key brute force |
| API per session or token | 300 requests / min | `429 rate_limited` with `Retry-After` |
| Per connection | `rateLimitEps` (0 = unlimited) | `429` with `Retry-After` |
| Live tails | 20 per user | `429` |
| HTTP server | `ReadHeaderTimeout` 10 s, `ReadTimeout` 60 s, `WriteTimeout` 120 s (SSE exempt), `IdleTimeout` 120 s, headers 64 KiB | |
| Bodies | `TIDEMARK_MAX_BODY_BYTES`, `TIDEMARK_MAX_DECOMPRESSED_BYTES`; API bodies 1 MiB | `413` |

In HA mode every limit is enforced per node; the effective limit is limit × nodes.

## ClickHouse users and network isolation

OpenTidemark uses two ClickHouse users (`deploy/clickhouse/users.d/tidemark.xml`):

| User | Grants | Used for |
|---|---|---|
| `tidemark_writer` | `ALL ON tidemark.*`, `CREATE DATABASE`, `SELECT` on `system.parts`, `system.mutations`, `system.disks` (+ `CLUSTER` in HA) | Migrations, inserts, retention DDL, purge, indexed fields, backups |
| `tidemark_reader` | `SELECT ON tidemark.*`, profile `readonly=2`, 1 GiB memory, 60 s | Every user query |

Hardening:

- Generate both passwords (the installers write 256-bit hex secrets into `.env` / `tidemark.env`)
  and pass them with `from_env`; never reuse them.
- Narrow `<networks>` in `users.d/tidemark.xml` from `::/0` to the OpenTidemark hosts for native
  or HA ClickHouse.
- Keep ClickHouse off untrusted networks: Compose keeps it on the internal `backend` network;
  native ClickHouse listens on loopback by default (`config.d/tidemark.xml` does not set
  `listen_host`). If OpenTidemark runs elsewhere, add a private `listen_host` and a firewall rule
  for that host only, and consider `TIDEMARK_CLICKHOUSE_TLS=true`.
- `config.d/tidemark.xml` removes the MySQL and PostgreSQL protocol ports and disables crash
  reports to the ClickHouse developers.

## Users disabled, sessions and CSRF

- Sessions: 256-bit random token in cookie `tm_session` (`HttpOnly`, `SameSite=Strict`, `Secure`
  with an https public URL), stored hashed; idle timeout `TIDEMARK_SESSION_IDLE_TIMEOUT` (12 h),
  absolute `TIDEMARK_SESSION_ABSOLUTE_TIMEOUT` (168 h). A password change or disabling a user
  revokes all sessions.
- Cookie-authenticated non-GET requests need header `X-Tidemark-CSRF` equal to the `tm_csrf`
  cookie, plus an `Origin` (when sent) that matches `TIDEMARK_PUBLIC_URL` or the requested host.
  Bearer tokens are exempt.

## Hardening checklist

- [ ] Admin password set before first start or changed at first sign-in; banner gone.
- [ ] At least two named admins; shared `admin` account disabled or its password vaulted.
- [ ] `TIDEMARK_PUBLIC_URL` is the https origin; HSTS observed.
- [ ] TLS terminated (proxy or built-in) for UI, API and ingestion from untrusted networks.
- [ ] `TIDEMARK_TRUSTED_PROXIES` lists only proxy addresses.
- [ ] Unused listeners disabled; `TM_BIND_ADDR` set on Compose hosts.
- [ ] Firewall: ingestion ports only from sender networks; ClickHouse never exposed.
- [ ] Bootstrap connection revoked; one connection per app with protocol, CIDR, allowed streams
      and stream pinning.
- [ ] Teams grant each group only its streams; **Deny by default** on once every non-admin is in
      a team; sensitive data in dedicated streams.
- [ ] API tokens scoped and expiring; no personal admin tokens in CI.
- [ ] `/metrics` disabled or protected with `TIDEMARK_METRICS_TOKEN`.
- [ ] `TIDEMARK_ALLOW_PRIVATE_WEBHOOKS` left `false` unless required.
- [ ] Secrets supplied with `_FILE`; `.env` / `tidemark.env` / data directory private.
- [ ] `secret.key` backed up with the metadata (and identical on HA nodes).
- [ ] ClickHouse users' `<networks>` narrowed (native/HA); passwords unique.
- [ ] Audit log reviewed regularly or exported to a SIEM.
- [ ] `tidemark doctor` all `ok`.

See also the project's [security policy](../../SECURITY.md).
