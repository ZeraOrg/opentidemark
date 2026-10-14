<!-- SPDX-License-Identifier: Apache-2.0 -->
# Querying

A TQL cookbook for operators: incident triage, error rates, latency percentiles, top-N, trace
lookups and metrics, plus saved signals, live tail, exports and running queries from scripts.

The complete syntax is in the [query language reference](../query-language.md). This chapter
collects queries that answer operational questions. Every query works in the explorer
(**Events**), in dashboard panels, and through `POST /api/v1/query`.

## Essentials in one minute

```text
timeout                                   free text in message and exception (case-insensitive)
level:error service:checkout*             field:value (case-insensitive), wildcard, implicit AND
level >= warning                          level ordinal comparison
status_code >= 500 and elapsed > 250      numeric comparison on numeric properties
not exception:*                           field missing or empty
Service = 'payments-svc'                  Seq-style, case-sensitive (add ci for insensitive)
… | stats count() by service              aggregate ⇒ table
… | stats count() by level span 5m        aggregate over time ⇒ time series
source spans | …   /   source metrics | … switch the data source (default logs)
```

The time range is not part of the query: it comes from the time picker or the `from`/`to`
fields of the API (`now-15m`, `now-7d`, RFC 3339; `now+…` is not accepted). Events are always
newest first.

Neither is the set of streams: every query reads only the streams the caller may see
([Users and access](users-access.md#teams)), narrowed further by the **scope picker** in the top
bar (streams and, for logs, environments; `streams`/`environments` in `POST /query`). Inside
that scope, `stream:payments` and `connection_id:<id>` filter like any other built-in field.

## Incident triage

```text
# 1. What is failing right now? Errors by service over time
level >= error | stats count() by service span 1m

# 2. Which errors? Top message templates (event types) for one service
level >= error and service:checkout-api | top 10 event_type

# 3. Top exceptions across the fleet
level >= error and exception:* | stats count() by service, event_type | sort count desc | limit 20

# 4. Everything around one request id
RequestId:"0HN7…"

# 5. Errors that are new compared to normal noise: exclude known templates
level >= error and not event_type:(a1b2c3d4 or 0f9e8d7c)

# 6. Slow requests
elapsed > 1000 | stats count(), p95(elapsed) by service, route

# 7. One host or pod misbehaving?
level >= warning | stats count() by host | sort count desc | limit 10
```

Open an event and use *context* (events before and after from the same service and host) and the
trace link to jump to the trace waterfall.

## Error rates

```text
# Error ratio per service (two aggregates; compute the ratio in a table panel or client-side)
service:* | stats count() as total, count(exception) as with_exception by service

# Errors per second, 5-minute buckets
level >= error | stats rate() by service span 5m

# HTTP 5xx from spans
source spans | status:error | stats count() by service, name span 5m

# 5xx by route from structured logs
http.status_code >= 500 | stats count() by http.route | sort count desc
```

`rate()` is count per second over each bucket (or over the whole range without `span`).

## Latency percentiles

```text
# Span latency (duration is in milliseconds in results)
source spans | kind:server | stats p50(duration), p95(duration), p99(duration) by service span 5m

# Slowest operations
source spans | kind:server | stats p99(duration) by service, name | sort p99_duration desc | limit 10

# Spans slower than 2 s (units: ns, us, ms, s, m, h, d)
source spans | duration > 2s

# From log properties
elapsed:* | stats p95(elapsed), avg(elapsed) by service, route span 1m
```

Default aggregate names are `fn` or `fn_field` (`p95_duration`); rename with `as`.

## Top-N and cardinality

```text
level:error | top 10 service                    # = stats count() by service | sort count desc | limit 10
level:error | top 3 event_type by service       # top 3 per service
* | stats count_distinct(user.id) by service    # distinct users
* | stats count() by stream                     # volume per stream
* | stats count() by stream, level span 1h      # volume trend for capacity planning
* | stats count() by connection_id              # which connection sends the volume (ids from GET /connections)
connection_id:0199d2f4-3b1e-7c2a-9f00-5d6e7a8b9c0d | top 10 service   # what one key sends
```

## Traces

```text
@trace_id:4bf92f3577b34da6a3ce929d0e0e4736                  # all log events of a trace
source spans | trace_id:4bf92f3577b34da6a3ce929d0e0e4736    # its spans
source spans | status:error | stats count() by service, name
source spans | parent_span_id:"" | stats count() by service # root spans per service
```

The **Traces** screen searches traces with a spans filter and opens the waterfall
(`/traces/<traceId>`). API: `GET /api/v1/traces?q=status:error&from=now-1h&to=now` (summaries)
and `GET /api/v1/traces/<traceId>` (spans plus log events with that trace id).

## Metrics

```text
source metrics | name:node_load1 | stats avg(value) by service span 1m
source metrics | name:http.server.request.duration | stats sum(count) by service span 5m
source metrics | name:node_cpu_seconds_total and mode:idle | stats rate(value) span 1m
```

More in [Metrics › Querying metrics](metrics.md#querying-metrics).

## Signals

A signal is a saved filter that can be toggled in the explorer's left rail; active signals are
ANDed with the query. They suit recurring scopes ("Production", "Payments errors", "Exclude health
checks").

- Filter only (no `|` stages), for one source (`logs` default, `spans`, `metrics`).
- `shared: true` makes it visible to everyone; private signals are visible to the owner (and
  admins). Editors create; only the owner or an admin changes or deletes.
- At most 500 signals per user.

```bash
curl -sS "$TM/api/v1/signals" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Exclude health checks","tql":"not path:/healthz","color":"info","shared":true}'
```

Use signals in API queries with `"signals": ["<SIGNAL_ID>"]` in `POST /query`, `signals=<id>`
on `GET /tail`, `/histogram`, `/facets` and `/export`. Updating a signal (`PUT /signals/{id}`)
requires the `updatedAt` value you last read (optimistic concurrency).

UI: *Save as signal* next to the query bar.

## Live tail

The explorer's **Live** toggle streams new events matching the query (logs only, no stats). Under
the hood it is Server-Sent Events on `GET /api/v1/tail`:

```bash
curl -sS -N "$TM/api/v1/tail?q=level%3Aerror" -H "Authorization: Bearer $TM_TOKEN"
# : connected
# event: events
# data: [{"id":"…","timestamp":"…","ingestedAt":"…","level":"Error",…}]
```

- Polls every second on ingest time with a lag of `TIDEMARK_BATCH_MAX_WAIT` + 2 s.
- At most 50 events per second per subscriber; the excess is reported as `event: dropped`
  (data = count). Narrow the filter rather than tailing everything.
- Heartbeat every 15 s; at most 20 concurrent tails per user. Proxies must not buffer it.

## Running queries from scripts

```bash
curl -sS "$TM/api/v1/query" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"q":"level >= error | stats count() by service","from":"now-1h","to":"now"}' |
  jq -r '.rows[] | @tsv'
```

| Query shape | `kind` | Response |
|---|---|---|
| Filter only | `events` | `events[]` (newest first), `nextCursor` for the next page (pass as `cursor`), `limit` ≤ `TIDEMARK_QUERY_MAX_ROWS` (default page 200) |
| `stats` without `span` | `table` | `columns[]`, `rows[][]` |
| `stats … span` | `timeseries` | `span` (ms), `columns[]`, `series[{key, points[[tsMs, v…]]}]` |

Every response has `stats {elapsedMs, rowsRead, bytesRead, interpretedAsText}`. If the input does
not parse and contains no TQL operators, it is treated as one free-text phrase and
`interpretedAsText` is `true`.

Validate without running: `POST /api/v1/query/validate {"q": "…"}` ⇒
`{"ok":false,"error":{"message":"unknown level 'erorr'","pos":6,"len":5,"hint":"did you mean 'error'?"}}`.

### Exporting results

Editors can export events (not stats) as NDJSON or CSV, up to 100,000 events, audited:

```bash
curl -sS -o errors.ndjson -H "Authorization: Bearer $TM_TOKEN" \
  "$TM/api/v1/export?q=level%3Aerror&from=now-24h&to=now&format=ndjson"
curl -sS -o errors.csv -H "Authorization: Bearer $TM_TOKEN" \
  "$TM/api/v1/export?q=level%3Aerror&from=now-24h&to=now&format=csv"
```

CSV cells starting with `=`, `+`, `-`, `@`, tab or CR are prefixed with `'` to prevent formula
injection in spreadsheets.

## Performance tips

- Narrow the time range first; `stream:` and `service:` filters prune data early (`stream` leads
  the ClickHouse® sort key).
- Prefer `field:value` or `=` on specific fields over free text across all messages.
- Free text and `contains()` scan message and exception text (n-gram indexes help, but large
  ranges still read a lot).
- Index frequently filtered high-cardinality keys and query them with `=`
  ([Indexed fields](streams-retention.md#indexed-fields)).
- Queries time out after `TIDEMARK_QUERY_TIMEOUT` (30 s) and are limited to 1 GiB of ClickHouse
  memory.
