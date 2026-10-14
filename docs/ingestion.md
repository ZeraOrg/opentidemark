<!-- SPDX-License-Identifier: Apache-2.0 -->
# Sending data to OpenTidemark

Send logs, traces and metrics to OpenTidemark with OpenTelemetry (OTLP), Serilog and NLog through
the Seq API, Elasticsearch `_bulk` shippers such as Filebeat, Fluent Bit and Vector, Logstash's
`http` output, or plain JSON.

OpenTidemark speaks the protocols your applications and shippers already use. Existing clients only
change the URL and the API key.

| Protocol | Clients | Endpoint (default ports, see [configuration](configuration.md)) |
|---|---|---|
| Seq (CLEF and legacy JSON) | Serilog Seq sink, NLog Seq target, `seqcli` | `:5341` and `:8080`: `POST /api/events/raw`, `POST /ingest/clef` |
| Elasticsearch `_bulk` | Filebeat, Fluent Bit, Vector | `:9200` (root paths) and `:8080/es/` |
| OpenTelemetry (OTLP) logs, traces, metrics | OTel SDKs and Collector | gRPC `:4317`; HTTP `:4318` and `:8080/otlp/` (`/v1/logs`, `/v1/traces`, `/v1/metrics`) |
| Native JSON | Logstash (`http` output), anything that can POST JSON | `:8080`: `POST /api/v1/ingest/logs` |

Syslog (RFC 5424/3164) and Fluent forward are supported through the optional OpenTelemetry
Collector profile of `deploy/docker-compose.yml` (`docker compose --profile collector up -d`),
which forwards to OpenTidemark over OTLP/HTTP. A syslog event's message is the whole raw line and
its level comes from the syslog severity; the parsed parts are properties: `hostname`, `appname`,
`proc_id`, `msg_id`, `message` (the message part), `facility`, `priority` and `version`. The
`host` field stays empty and `service` is the stream name, so search syslog with properties, for
example `hostname:web-01 appname:nginx`.

## Connections and API keys

Every request must carry the key of a **connection**; there is no anonymous ingestion. Create one
connection per application or shipper (the bootstrap key from `deploy/.env` works for a first
test). Each connection can restrict protocols, source IP ranges (CIDR), expiry and rate, set a
default stream, a minimum level, and properties that are added to every event. Keys can be
rotated (with a grace period) and revoked.

The key is accepted in these places, checked in order:

1. `X-Tidemark-Key: <key>`
2. `X-Seq-ApiKey: <key>` (Seq clients)
3. `Authorization: Bearer <key>`
4. `Authorization: ApiKey <key>` or `ApiKey <base64(id:key)>` (Elasticsearch `_bulk` shippers)
5. `Authorization: Basic <base64(any-user:key)>` (the password is the key)
6. `?apiKey=<key>` in the URL (Seq endpoints only; logged as a warning because URLs end up in
   proxy logs)

For OTLP/gRPC send the key as metadata `x-tidemark-key` or `authorization: Bearer <key>`.

A missing or unknown key is answered with `401`; a key that is not allowed for the protocol or
source address, expired or revoked gets `403`. Repeated failures from one address are rate
limited.

## Common behaviour

- **Compression:** `Content-Encoding` gzip, deflate and zstd (snappy for OTLP). Bodies are limited
  before and after decompression (`413` when exceeded).
- **Backpressure:** when the ingestion queue is full OpenTidemark answers `503` with `Retry-After:
  1` (OTLP/gRPC: `UNAVAILABLE`) and nothing of that request is stored, so clients can retry safely.
- **Rate limit:** a connection's rate limit answers `429` with `Retry-After`.
- **Streams:** data goes to the connection's default stream unless the request names one (ES
  `_index`, OTLP resource attribute `tidemark.stream`, native/CLEF property `@stream` or
  `_stream`) and the connection does not pin it. A connection can be limited to some streams
  (*allowed streams*); a request naming another stream is written to the default stream
  instead and counted in `tidemark_ingest_rerouted_total`. Data sent to a paused stream is
  dropped.
- **Timestamps:** a missing timestamp becomes the ingest time; timestamps more than 7 days in the
  future are replaced by the ingest time and marked with `_tidemark.clock_skew=true`.
- **Levels:** events below the connection's minimum level are dropped.

## Seq

`POST /api/events/raw` accepts CLEF (newline-delimited JSON) when the content type is
`application/vnd.serilog.clef` or the URL has `?clef`, otherwise Seq's legacy JSON
(`{"Events":[…]}`). `POST /ingest/clef` always expects CLEF. The response is `201` with
`{"MinimumLevelAccepted": …}`, so Serilog's dynamic level control follows the connection's
minimum level. Invalid lines are skipped; only when every line is invalid the request fails with
`400`.

CLEF fields: `@t` timestamp, `@m` message, `@mt` message template, `@l` level (default
Information), `@x` exception, `@i` event type, `@tr` / `@sp` trace and span id. Other keys become
properties (`@@key` becomes the property `@key`). Messages are rendered from the template when
`@m` is missing.

## Elasticsearch

OpenTidemark answers the requests that shippers make at start-up (cluster info, `_license`,
templates, ILM, pipelines, data streams) so they run without setup changes, and accepts
documents through:

- `POST|PUT /_bulk`, `POST /{index}/_bulk` — NDJSON with `index` and `create` actions
  (`update`/`delete` are rejected per item);
- `POST|PUT /{index}/_doc[/{id}]`, `POST /{index}/_create/{id}` — single documents.

The index becomes the stream: lower-cased, with date suffixes such as `-2026.10.13`, `-2026.10`
or `-20261013` removed (`filebeat-8.11.0-2026.10.13` ⇒ `filebeat-8.11.0`).

The endpoint implements only this ingestion API and does not identify itself as Elasticsearch:
the cluster info is `{"name":"tidemark","cluster_name":"tidemark","version":{"number":"8.11.0"}}`,
where the version is the API level shippers use to choose their request format. Filebeat 8.15,
Fluent Bit 3.1 and Vector 0.41 have been tested. Clients that require a server identifying as
Elasticsearch (the product check of Logstash's `elasticsearch` output and of the official
Elasticsearch language clients) refuse the endpoint and are not supported; for Logstash use the
`http` output described [below](#logstash).

## OpenTelemetry

Logs, traces and metrics are accepted over gRPC and HTTP (protobuf or JSON). Resource attributes
`service.name`, `deployment.environment(.name)` and `host.name` become the service, environment
and host. Log bodies that are maps are merged into the properties. Gauges, sums, histograms,
exponential histograms and summaries are stored with their temporality.

Collector example:

```yaml
exporters:
  otlphttp/tidemark:
    endpoint: https://tidemark.example.com:4318
    headers:
      X-Tidemark-Key: ${env:TIDEMARK_KEY}
```

## Native JSON

`POST /api/v1/ingest/logs` takes a JSON object, an array of objects or NDJSON and answers `202`
with `{"accepted": n, "rejected": m, "errors": [{"index": i, "reason": "…"}]}` (at most 20
errors).

```bash
curl -H "X-Tidemark-Key: $TIDEMARK_KEY" -H 'Content-Type: application/json' \
  -d '{"level":"error","message":"payment failed","service":"checkout","order_id":42}' \
  http://localhost:8080/api/v1/ingest/logs
```

## Logstash

Logstash's `elasticsearch` output stops at start-up with *Could not connect to a compatible version
of Elasticsearch*, because it only talks to servers that identify as Elasticsearch. Send with
Logstash's `http` output to the [native JSON](#native-json) endpoint instead. The connection must
allow the native protocol.

```ruby
filter {
  # Optional: choose the stream; without it the connection's default stream is used.
  mutate { add_field => { "_stream" => "logstash-app" } }
}
output {
  http {
    url => "https://tidemark.example.com:8080/api/v1/ingest/logs"
    http_method => "post"
    format => "json_batch"
    headers => { "X-Tidemark-Key" => "${TIDEMARK_KEY}" }
  }
}
```

`format => "json_batch"` sends each batch as one JSON array. Logstash's `@timestamp`, `message`,
a `level` or `log.level` field and `host.name` become the timestamp, message, level and host
(see [field mapping](#field-mapping)); the other fields are kept as properties. The output retries
`429` and `503` answers, so backpressure loses nothing. This configuration has been tested with
Logstash 8.15.

## Field mapping

Documents from Elasticsearch shippers, native JSON and OTLP body maps are mapped with these keys
(first match wins; dotted names also match nested objects). The original keys stay available as
properties, so `Application = 'x'` still works.

| Field | Keys, in order |
|---|---|
| timestamp | `@t`, `@timestamp`, `timestamp`, `time`, `ts`, `Timestamp`, `date` (RFC 3339, ISO 8601 without zone = UTC, epoch seconds/ms/µs/ns) |
| message | `@m`, `message`, `msg`, `Message`, `RenderedMessage`, `log`, `text` |
| template | `@mt`, `MessageTemplate`, `message_template`, `{OriginalFormat}` |
| level | `@l`, `level`, `log.level`, `Level`, `severity`, `severity_text`, `levelname`, `log_level`, `loglevel`, `syslog.severity` |
| exception | `@x`, `exception`, `Exception`, `error.stack_trace`, `stack_trace`, `stacktrace`, `exc_info`, `error.message` (+ `error.type`) |
| service | `service.name`, `service`, `Service`, `ServiceName`, `Application`, `application`, `app`, `app.name`, `kubernetes.labels.app`, `kubernetes.container.name`, `container.name`, `logger_name` (else the stream name) |
| environment | `deployment.environment.name`, `deployment.environment`, `environment`, `Environment`, `env` |
| host | `host.name`, `host.hostname`, `hostname`, `host`, `MachineName`, `agent.hostname` |
| trace id | `@tr`, `trace.id`, `trace_id`, `traceId`, `TraceId` |
| span id | `@sp`, `span.id`, `span_id`, `spanId`, `SpanId` |

Without a message, the message is the compact JSON of the original object (at most 2 KiB).
