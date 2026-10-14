<!-- SPDX-License-Identifier: Apache-2.0 -->
# API for automation

Automate OpenTidemark through its HTTP API: authentication with API tokens (and CSRF for cookie
sessions), conventions, error codes and the stream scope, the main endpoints with their required
role, and curl recipes for connections, teams, dashboards, alert rules, queries and exports.

## Authentication

| Method | Use | Header |
|---|---|---|
| API token (`tmt_…`) | Automation, CI, scripts | `Authorization: Bearer <token>` |
| Session cookie (`tm_session`) | The web UI | Cookie + `X-Tidemark-CSRF: <tm_csrf cookie>` on every non-GET request |

Create tokens as described in [Users and access › API tokens](users-access.md#api-tokens). Token
requests need no CSRF header. The effective role is the lower of the token scope (`read` viewer,
`write` editor, `admin` admin) and the owner's role; the token sees the same streams as its owner.

Connection keys (`tmk_…`) only work on ingestion endpoints, never on `/api/v1` (except
`POST /api/v1/ingest/logs`, which is an ingestion endpoint).

```bash
export TM=https://tidemark.example.com
export TM_TOKEN=$(cat ~/.config/tidemark/token)      # never inline tokens in scripts
curl -sS "$TM/api/v1/auth/me" -H "Authorization: Bearer $TM_TOKEN"
```

## Conventions

- JSON in and out, camelCase fields, RFC 3339 UTC timestamps. Request bodies are limited to 1 MiB
  (import uploads excepted).
- Time parameters `from`/`to`: RFC 3339 or relative `now`, `now-15m`, `now-7d` (default: last
  hour). `now+…` is rejected.
- **Stream scope:** every data read returns only the streams the caller may see
  ([Users and access](users-access.md#teams)). The selection parameters (`streams`,
  `environments`/`env`) narrow a request within that scope; a selection outside it returns no
  data, not an error. Objects outside the scope (events, traces, rules, stream fields) are `404`.
- Every response carries `X-Request-Id`; quote it when reporting problems. API responses are
  `Cache-Control: no-store`.
- Rate limit: 300 requests per minute per token or session ⇒ `429 rate_limited` with
  `Retry-After` (per node in HA).
- Optimistic concurrency: `PUT` of dashboards, alerts and channels needs the current `version`;
  signals need `updatedAt`. Mismatch ⇒ `409`.
- Errors: `{"error":{"code":"…","message":"…","details":{…}}}`.

| Code | HTTP | Typical cause |
|---|---|---|
| `bad_request` | 400 | Validation; `details.field` names the field (`doc.every`, `config.url`, `streams`, `allowedStreams`, `teamIds`, `userIds` …) |
| `invalid_query` | 400 | TQL error; `details` `{pos, len, hint}` (+ `panelId` for dashboard panels) |
| `unauthorized` | 401 | Missing, expired or revoked token |
| `forbidden` | 403 | Role too low, or CSRF missing on a cookie request |
| `not_found` | 404 | Unknown id, or an item the caller may not see (private, or outside the stream scope) |
| `conflict` | 409 | Stale `version`, duplicate name, channel in use, deleting `default` |
| `payload_too_large` | 413 | Body too large |
| `rate_limited` | 429 | API or tail limit |
| `backpressure` | 503 | Ingestion queue full |
| `internal` | 500 | Logged with the request id |

## Endpoints {#endpoints}

All paths are under `/api/v1`. Role = minimum role (`any` = any authenticated principal).

### Auth, users, tokens, teams

| Endpoint | Role | Purpose |
|---|---|---|
| `POST /auth/login` `{username, password}` | public | Session cookies |
| `POST /auth/logout` | any | End session |
| `GET /auth/me` | any | Who am I, and which streams I see (`allStreams`, `streams`, `teams`, `streamAccess`) |
| `POST /auth/password` `{currentPassword, newPassword}` | any | Change own password |
| `GET/POST /users`, `GET/PATCH/DELETE /users/{id}` | admin | Manage users (objects include `teams`) |
| `PUT /users/{id}/teams` `{teamIds}` | admin | Replace a user's teams |
| `GET/POST /tokens`, `DELETE /tokens/{id}` | any (own) | Manage own API tokens |
| `GET/POST /teams`, `GET/PATCH/DELETE /teams/{id}` | admin | Teams |
| `PUT /teams/{id}/members` `{userIds}`, `PUT /teams/{id}/streams` `{streams}` | admin | Replace a team's members or granted streams |
| `GET /teams/access` | admin | `{denyByDefault, usersWithoutTeam}` |

### Search and analytics

| Endpoint | Role | Purpose |
|---|---|---|
| `POST /query` `{q, from, to, limit?, cursor?, span?, signals?, streams?, environments?}` | viewer | Events, table or time series |
| `POST /query/validate` `{q}` | viewer | Check a query without running it |
| `GET /histogram?q&from&to&buckets&streams&env` | viewer | Volume histogram |
| `GET /facets?q&from&to&fields&top&streams&env` | viewer | Top values per field |
| `GET /fields?source&from&to&streams&env` | viewer | Field list |
| `GET /events/{id}`, `GET /events/{id}/context` | viewer | Event detail, surrounding events |
| `GET /tail?q&signals&streams&env` | viewer | Live tail (Server-Sent Events) |
| `GET /traces?q&from&to&limit&streams`, `GET /traces/{traceId}` | viewer | Trace search and detail |
| `GET /metrics/names?q&streams` | viewer | Metric names |
| `GET /export?q&from&to&format=ndjson\|csv&streams&env` | editor | Export up to 100,000 events (audited) |
| `GET /tql/complete?q&pos&source&streams&env` | viewer | Autocomplete |

`streams` and `env` are comma-separated and/or repeated (at most 100 valid stream names and 20
environments); `environments` filters **logs** only.

### Signals, dashboards, alerts, channels

| Endpoint | Role | Purpose |
|---|---|---|
| `GET /signals`, `GET /signals/{id}` | viewer | Saved filters |
| `POST /signals`, `PUT/DELETE /signals/{id}` | editor | Manage (owner or admin) |
| `GET /dashboards`, `GET /dashboards/{id}`, `GET /dashboards/{id}/export`, `GET /dashboards/templates` | viewer | Read |
| `POST /dashboards/{id}/panels/{panelId}/query` | viewer | Panel query with variables substituted |
| `POST /dashboards`, `POST /dashboards/import`, `PUT/DELETE /dashboards/{id}`, `POST /dashboards/{id}/duplicate` | editor | Manage |
| `GET /alerts`, `GET /alerts/{id}`, `GET /alerts/{id}/history` | viewer | Rules (with `streams`), state, history; only rules within the caller's scope |
| `POST /alerts`, `PUT/DELETE /alerts/{id}`, `POST /alerts/{id}/test`, `POST /alerts/{id}/mute` | editor | Manage rules |
| `GET /channels`, `GET /channels/{id}` | viewer | Channels (secrets masked) |
| `POST /channels`, `PUT/DELETE /channels/{id}`, `POST /channels/{id}/test` | admin | Manage channels |

### Connections, streams, administration

| Endpoint | Role | Purpose |
|---|---|---|
| `GET/POST /connections`, `GET/PATCH/DELETE /connections/{id}` | admin | Ingestion connections (with `allowedStreams`) |
| `POST /connections/{id}/rotate` `{graceMinutes?, key?}`, `POST /connections/{id}/revoke` | admin | Key lifecycle |
| `GET /connections/{id}/snippets` | admin | Client configurations |
| `GET /streams` | viewer | Visible streams with statistics |
| `POST /streams`, `PATCH/DELETE /streams/{name}` | admin | Manage streams (delete needs `{"confirm":"<name>"}`) |
| `GET /streams/{name}/fields`, `GET /indexed-fields` | viewer | Mappings (404 outside the scope), indexed fields |
| `POST /indexed-fields`, `DELETE /indexed-fields/{id}` | admin | Index a key |
| `POST /purge`, `GET /purge/{id}` | admin | Purge (dry run first) |
| `GET/PATCH /settings` | admin | `retention`, `access` (`{denyByDefault}`), `ui` |
| `GET /audit?from&to&actor&action&limit` | admin | Audit log |
| `GET /system` | admin | Version, uptime, queues, counters, ClickHouse® |
| `GET/POST /imports`, `GET /imports/{id}`, `POST /imports/{id}/cancel` | admin | Import jobs |

### Outside `/api/v1` (no API authentication)

| Endpoint | Purpose |
|---|---|
| `GET /healthz` | `200 {"status":"ok"}` while the process runs |
| `GET /readyz` | `200 {"status":"ready"}`; `503` when ClickHouse or metadata is unreachable, or while draining |
| `GET /metrics` | Prometheus® metrics; only with `TIDEMARK_METRICS_ENABLED=true`, bearer `TIDEMARK_METRICS_TOKEN` if set |
| Ingestion endpoints | Connection key, see [Ingestion](ingestion.md) |

## Recipes

### Create a connection and store its key

```bash
curl -sS --fail-with-body "$TM/api/v1/connections" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"billing-worker prod","protocols":["otlp"],"defaultStream":"billing","allowedStreams":["billing","billing-dev"],"allowedCidrs":["10.40.0.0/16"]}' |
  jq -r .key | vault kv put secret/tidemark/billing-worker key=-      # or your secret store
```

### Create a team and grant it streams

```bash
TEAM=$(curl -sS --fail-with-body "$TM/api/v1/teams" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"name":"Billing"}' | jq -r .id)
curl -sS --fail-with-body -X PUT "$TM/api/v1/teams/$TEAM/streams" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"streams":["billing","billing-dev"]}' | jq .streams
curl -sS --fail-with-body -X PUT "$TM/api/v1/teams/$TEAM/members" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"userIds":["<USER_ID>"]}' | jq '.members[].username'
```

### Create a dashboard from JSON in git

```bash
curl -sS --fail-with-body "$TM/api/v1/dashboards/import" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @dashboards/service-health.json | jq -r .id
```

Idempotent create-or-update: [Dashboards › Dashboards as code](dashboards.md#as-code).

### Create an alert rule

```bash
CHANNEL=$(curl -sS "$TM/api/v1/channels" -H "Authorization: Bearer $TM_TOKEN" | jq -r '.[] | select(.name=="ops-webhook") | .id')
jq -n --arg ch "$CHANNEL" '{name:"Checkout errors", enabled:true, streams:["checkout"], doc:{
  query:"service:checkout-api and level >= error | stats count()",
  condition:{op:">", threshold:25}, window:"5m", every:"1m", "for":"2m",
  severity:"critical", channels:[$ch], noData:"ok", groupBy:[]}}' |
curl -sS --fail-with-body "$TM/api/v1/alerts" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @- | jq '{id, state, streams}'
```

Omit `streams` (or send `null`) to read every stream; only callers who see every stream may do
that.

### Query

```bash
curl -sS "$TM/api/v1/query" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"q":"level >= error | stats count() by service","from":"now-1h","to":"now","streams":["checkout"]}' | jq -r '.rows[] | @tsv'
```

Paginate events with `nextCursor`:

```bash
CURSOR=null
while :; do
  PAGE=$(jq -n --argjson c "$CURSOR" '{q:"level:error", from:"now-24h", to:"now", limit:1000, cursor:$c}' |
         curl -sS "$TM/api/v1/query" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' --data-binary @-)
  echo "$PAGE" | jq -c '.events[]'
  CURSOR=$(echo "$PAGE" | jq -c '.nextCursor // null')     # JSON string or null
  [ "$CURSOR" = null ] && break
done
```

### Export

```bash
curl -sS --fail -o errors-$(date +%F).ndjson -H "Authorization: Bearer $TM_TOKEN" \
  "$TM/api/v1/export?q=level%3Aerror&from=now-24h&to=now&format=ndjson"
```

### Set retention and create a stream

```bash
curl -sS -X PATCH "$TM/api/v1/settings" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"retention":{"logsDays":30,"spansDays":7,"metricsDays":30}}'
curl -sS "$TM/api/v1/streams" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"billing","retentionDays":90}'
```
