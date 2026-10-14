<!-- SPDX-License-Identifier: Apache-2.0 -->
# Ingestion

Connect applications and shippers to OpenTidemark: create connections and keys with protocol,
network and stream restrictions, then follow a tested recipe per protocol (Seq/CLEF, the
Elasticsearch `_bulk` API, OpenTelemetry®, syslog, native JSON), route data to streams and handle
backpressure, the write-ahead log, dead letters and ingestion errors.

The protocol and field-mapping reference is [Sending data](../ingestion.md). Product-specific
details (version notes, limitations) are in [Integrations](integrations.md).

## Endpoints at a glance

| Protocol | Dedicated port | Path on the HTTP listener (8080 / your proxy) | Connection protocol |
|---|---|---|---|
| Seq (CLEF, legacy JSON) | `:5341` `POST /api/events/raw`, `POST /ingest/clef` | same paths | `seq` |
| Elasticsearch `_bulk` ingestion API | `:9200` `POST /_bulk`, `POST /{index}/_bulk`, `/{index}/_doc` | `/es/_bulk`, `/es/{index}/_bulk`, … | `elastic` |
| OTLP/HTTP (protobuf or JSON) | `:4318` `POST /v1/logs`, `/v1/traces`, `/v1/metrics` | `/otlp/v1/logs`, … | `otlp` |
| OTLP/gRPC | `:4317` | — | `otlp` |
| Native JSON / NDJSON | — | `POST /api/v1/ingest/logs` | `native` |
| Syslog, Fluent forward | via the Collector (5514, 24224) | — | `otlp` (Collector's key) |

Behind a TLS proxy that forwards only 443 to 8080, every protocol except OTLP/gRPC can use the
single base URL `https://tidemark.example.com`.

## Connections and keys {#connections}

A connection is one ingestion source. Its key (`tmk_…`) is shown **once**.

| Setting (API field) | Default | Effect |
|---|---|---|
| `name` | required | 1–128 characters, unique (case-insensitive) |
| `description` | `""` | ≤ 1024 characters |
| `protocols` | all four | Subset of `seq`, `elastic`, `otlp`, `native`; other protocols get `403` |
| `defaultStream` | `default` | Stream used when the request names none |
| `allowedStreams` | `[]` (any) | Up to 100 stream names the connection may write to; must include `defaultStream` and a pinned stream. Data naming another stream is kept but written to `defaultStream` and counted in `tidemark_ingest_rerouted_total` |
| `allowedCidrs` | `[]` (any) | Up to 64 CIDRs or single addresses; other sources get `403` |
| `minimumLevel` | none | Events below are dropped and counted (`Verbose`…`Fatal`, any level alias); returned to Seq clients as `MinimumLevelAccepted` |
| `appliedProperties` | `{}` | Up to 64 properties merged into every event, overriding incoming keys. `@stream` or `_stream` **pins** the stream |
| `rateLimitEps` | `0` (unlimited) | Token bucket in items/s, burst = the rate; exceeding ⇒ `429` + `Retry-After` |
| `expiresAt` | none | RFC 3339 time in the future; afterwards `403` |
| `key` | generated | Optional admin-supplied key for migrations (20–256 printable ASCII, entropy-checked) |

### Create a connection in the UI

**Settings › Connections › New connection**. The wizard asks for *Name*, *Protocols*, *Default
stream* and *Allowed streams* (optional; must include the default stream), then *Restrictions*
(*Allowed source CIDRs*, *Minimum level*, *Rate limit (events/s)*, *Expires*), and finally shows
the key once with ready-to-paste *Snippets* for Serilog, NLog, OpenTelemetry environment
variables, Filebeat, Fluent Bit, Vector and Logstash. Copy the key into your secret store before
closing the dialog. The *Edit* action of a connection offers the same fields; the *Restrictions*
column lists the allowed streams.

### Create a connection with the API

```bash
curl -sS "$TM/api/v1/connections" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "name": "checkout-api prod",
  "description": "Serilog from the checkout service",
  "protocols": ["seq"],
  "defaultStream": "checkout",
  "allowedStreams": ["checkout", "checkout-dev"],
  "allowedCidrs": ["10.20.0.0/16", "10.30.4.17"],
  "minimumLevel": "Information",
  "appliedProperties": {"team": "payments", "@stream": "checkout"},
  "rateLimitEps": 5000,
  "expiresAt": "2027-12-31T00:00:00Z"
}' | tee connection.json | jq '{id, keyPrefix, protocols, allowedCidrs}'
jq -r .key connection.json > checkout-api.key && rm connection.json   # key is returned once
```

Other operations (admin): `GET /connections`, `GET/PATCH/DELETE /connections/{id}`,
`POST /connections/{id}/rotate` `{graceMinutes?, key?}`, `POST /connections/{id}/revoke`,
`GET /connections/{id}/snippets`. `PATCH` takes the same fields as `POST` except `key`.

Rotation and revocation are described in
[Security hardening](security-hardening.md#connection-keys).

### Where clients put the key

Checked in this order:

1. `X-Tidemark-Key: <key>`
2. `X-Seq-ApiKey: <key>` (Seq clients)
3. `Authorization: Bearer <key>`
4. `Authorization: ApiKey <base64(id:key)>` or `ApiKey <key>` (Elasticsearch clients; the id part is ignored)
5. `Authorization: Basic <base64(any-user:key)>` (password = key)
6. `?apiKey=<key>` (Seq endpoints only; logged as a warning, because URLs leak into proxy logs)

OTLP/gRPC: metadata `x-tidemark-key: <key>` or `authorization: Bearer <key>`.

## Recipes

Each recipe assumes a connection that allows the protocol shown. Replace `<INGEST_KEY>` with the
key, or better, read it from an environment variable or secret.

### Seq clients: Serilog (.NET) {#seq-clients-serilog-net}

Package `Serilog.Sinks.Seq`. The sink posts CLEF to `/api/events/raw`; use the base URL (port
5341 or the HTTP listener/proxy).

```csharp
Log.Logger = new LoggerConfiguration()
    .Enrich.WithProperty("Application", "checkout-api")
    .WriteTo.Seq("https://tidemark.example.com", apiKey: Environment.GetEnvironmentVariable("TIDEMARK_KEY"))
    .CreateLogger();
```

`appsettings.json` with `Serilog.Settings.Configuration`:

```json
{
  "Serilog": {
    "Using": ["Serilog.Sinks.Seq"],
    "WriteTo": [
      { "Name": "Seq", "Args": { "serverUrl": "https://tidemark.example.com", "apiKey": "<INGEST_KEY>" } }
    ]
  }
}
```

The response carries the connection's minimum level (`MinimumLevelAccepted`), so Serilog's
dynamic level control (`controlLevelSwitch`) follows the connection. `Application` becomes the
service (field promotion), message templates and properties are kept.

### Seq clients: NLog {#seq-clients-nlog}

Package `NLog.Targets.Seq`.

```xml
<nlog xmlns="http://www.nlog-project.org/schemas/NLog.xsd"
      xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <extensions><add assembly="NLog.Targets.Seq"/></extensions>
  <targets>
    <target xsi:type="BufferingWrapper" name="tidemark" bufferSize="1000" flushTimeout="2000">
      <target xsi:type="Seq" serverUrl="https://tidemark.example.com" apiKey="${environment:TIDEMARK_KEY}">
        <property name="Application" value="checkout-api" />
      </target>
    </target>
  </targets>
  <rules><logger name="*" minlevel="Info" writeTo="tidemark" /></rules>
</nlog>
```

### Seq API with curl (CLEF)

```bash
printf '%s\n' \
  '{"@t":"2026-10-13T20:41:00Z","@mt":"Order {OrderId} failed for {Customer}","@l":"Error","OrderId":42,"Customer":"acme","Application":"checkout-api"}' \
  '{"@t":"2026-10-13T20:41:01Z","@m":"cache warmed","Application":"checkout-api"}' |
curl -sS "$TM/api/events/raw" -H "X-Seq-ApiKey: $TM_KEY" \
  -H 'Content-Type: application/vnd.serilog.clef' --data-binary @-
# ⇒ 201 {"MinimumLevelAccepted":null}
```

### `_bulk` shippers: Filebeat {#bulk-shippers-filebeat}

```yaml
# filebeat.yml (Filebeat 8.x)
filebeat.inputs:
  - type: filestream
    id: app
    paths: ["/var/log/app/*.json"]
    parsers:
      - ndjson:
          target: ""
          overwrite_keys: true
output.elasticsearch:
  hosts: ["https://tidemark.example.com:9200"]   # or hosts: ["https://tidemark.example.com"] + path: "/es"
  api_key: "tidemark:${TIDEMARK_KEY}"             # id:key, the id is ignored
  index: "filebeat-app"                           # becomes the stream (date suffixes removed)
setup.ilm.enabled: false
setup.template.enabled: false
```

### `_bulk` shippers: Fluent Bit

```ini
[OUTPUT]
    Name               es
    Match              *
    Host               tidemark.example.com
    Port               9200
    HTTP_User          tidemark
    HTTP_Passwd        ${TIDEMARK_KEY}
    Index              fluentbit-app
    Suppress_Type_Name On
    tls                On
```

### `_bulk` shippers: Vector

```yaml
sinks:
  tidemark:
    type: elasticsearch
    inputs: ["app_logs"]
    endpoints: ["https://tidemark.example.com:9200"]
    api_version: v8
    mode: bulk
    bulk:
      index: "vector-app"
    auth:
      strategy: basic
      user: tidemark
      password: "${TIDEMARK_KEY}"
```

### Logstash {#logstash}

Logstash's `elasticsearch` output does not work with OpenTidemark (see
[Integrations › Logstash](integrations.md#logstash)); use the `http` output to the native JSON
endpoint:

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

### `_bulk` with curl

```bash
printf '%s\n' \
  '{"create":{"_index":"web-2026.10.13"}}' \
  '{"@timestamp":"2026-10-13T20:41:04Z","message":"GET /api/orders 500","log.level":"error","service.name":"web","http.status_code":500}' |
curl -sS "$TM/es/_bulk" -H "Authorization: ApiKey $(printf 'tidemark:%s' "$TM_KEY" | base64 -w0)" \
  -H 'Content-Type: application/x-ndjson' --data-binary @-
# ⇒ {"errors":false,"items":[{"create":{…,"result":"created","status":201}}],"took":…}
```

Only `index` and `create` actions are accepted; `update`/`delete` items fail with
`action_not_supported`. Template, ILM, pipeline and data-stream management calls are acknowledged
without effect.

### OpenTelemetry SDKs {#opentelemetry-sdks}

All OpenTelemetry® SDKs read the standard exporter variables:

```bash
export OTEL_SERVICE_NAME=checkout-api
export OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=prod,tidemark.stream=checkout
export OTEL_EXPORTER_OTLP_ENDPOINT=https://tidemark.example.com/otlp   # or https://tidemark.example.com:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=${TIDEMARK_KEY}
```

For gRPC use `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` and `OTEL_EXPORTER_OTLP_ENDPOINT=https://tidemark.example.com:4317`.

`service.name` becomes the service, `deployment.environment.name` (or `deployment.environment`)
the environment, `host.name` the host, and the resource attribute `tidemark.stream` selects the
stream (unless the connection pins one).

### OpenTelemetry Collector {#opentelemetry-collector}

```yaml
exporters:
  otlphttp/tidemark:
    endpoint: https://tidemark.example.com:4318      # or https://tidemark.example.com/otlp
    headers:
      X-Tidemark-Key: ${env:TIDEMARK_KEY}
    timeout: 30s
    retry_on_failure:
      enabled: true
    sending_queue:
      enabled: true
  otlp/tidemark:                                     # gRPC alternative
    endpoint: tidemark.example.com:4317
    headers:
      x-tidemark-key: ${env:TIDEMARK_KEY}
processors:
  batch:
    send_batch_size: 5000
    timeout: 1s
service:
  pipelines:
    logs:    {receivers: [otlp], processors: [batch], exporters: [otlphttp/tidemark]}
    traces:  {receivers: [otlp], processors: [batch], exporters: [otlphttp/tidemark]}
    metrics: {receivers: [otlp], processors: [batch], exporters: [otlphttp/tidemark]}
```

Keep `retry_on_failure` and `sending_queue` enabled: OpenTidemark's `503` and gRPC `UNAVAILABLE`
are retryable and nothing of a rejected request is stored. Set the exporter `timeout` above the
default of 5 s on slow disks: a request whose client gives up while its rows wait for the fsync is
still stored (the rows count as enqueued, never as dropped) and the request ends without an
acknowledgement, so the exporter's retry stores the same rows a second time, as with any
at-least-once delivery.

To route a Collector's data into a stream, set the resource attribute:

```yaml
processors:
  resource/stream:
    attributes:
      - key: tidemark.stream
        value: infra-metrics
        action: upsert
```

### Syslog and Fluent forward (Collector profile) {#syslog-and-fluent-forward-collector-profile}

OpenTidemark does not listen for syslog or Fluent forward itself. The Compose `collector` profile
runs an OpenTelemetry Collector (`deploy/otel-collector.yaml`) that receives RFC 5424 syslog on
5514/tcp+udp and Fluent forward on 24224, and exports over OTLP/HTTP with
`Authorization: Bearer ${TIDEMARK_COLLECTOR_KEY}`.

```bash
# .env: a dedicated key that only allows otlp (default: the bootstrap key)
echo 'COLLECTOR_INGEST_KEY=<INGEST_KEY>' >> .env
docker compose --profile collector up -d
logger --rfc5424 -n <docker-host> -P 5514 -T "test message from $(hostname)"   # util-linux logger, TCP
```

How syslog records look in OpenTidemark (verified):

| Syslog part | OpenTidemark |
|---|---|
| Full line | `message` (the raw RFC 5424 line) |
| Severity | `level` (e.g. `<11>` ⇒ Error) |
| Hostname, app name, proc id, msg id | properties `hostname`, `appname`, `proc_id`, `msg_id` |
| Message text | property `message` |
| Facility, priority | properties `facility`, `priority` |
| Service | not set; falls back to the stream name |

Query with `hostname:fw01 and appname:sshd`, not `host:` (the built-in `host` stays empty). To fill
`service` or route per device, add a `transform` or `resource` processor in your own copy of the
Collector configuration. Only `rfc5424` is configured; for RFC 3164 senders change
`protocol: rfc3164` in your copy.

### Native JSON

```bash
curl -sS "$TM/api/v1/ingest/logs" -H "X-Tidemark-Key: $TM_KEY" -H 'Content-Type: application/json' -d '[
  {"level":"error","message":"payment failed","service":"checkout","order_id":42},
  {"level":"info","msg":"payment retried","service":"checkout","@stream":"payments"}
]'
# ⇒ 202 {"accepted":2,"rejected":0,"errors":[]}
```

The body may be one object, an array of objects or NDJSON. Field mapping follows
[Sending data › Field mapping](../ingestion.md#field-mapping).

## Routing to streams {#routing-to-streams}

The stream of a record is decided in this order:

1. **Pinned by the connection**: `appliedProperties` contains `@stream` or `_stream`. Nothing the
   client sends can override it.
2. **Named by the request**:
    - CLEF / native JSON: property `@stream` or `_stream`;
    - `_bulk`: `_index` of the action line, else the `{index}` in the URL, lower-cased, data-stream
      prefixes `logs-`/`.ds-` kept, date suffixes `-YYYY.MM.DD`, `-YYYY.MM`, `-YYYYMMDD` removed;
    - OTLP: resource attribute `tidemark.stream`.
3. The connection's `defaultStream` (default `default`).

When the connection has `allowedStreams`, a stream named by the request that is not in the list
is replaced by `defaultStream`; the item is kept and counted in
`tidemark_ingest_rerouted_total{protocol, reason="stream_not_allowed"}`. The default stream and a
pinned stream must be in the list, so step 1 and step 3 always produce an allowed stream.

Names are normalised: lower-cased, invalid characters become `-`, empty becomes `default`.
Unknown streams are created on first write while fewer than `TIDEMARK_MAX_STREAMS` (200) exist;
once the limit is reached, data for a new stream lands in `default` (even when `default` is not
among a connection's allowed streams). Paused streams accept requests but drop the rows (counted
as `tidemark_pipeline_dropped_total{reason="stream_paused"}`).

Recommended layout: one stream per retention class and access boundary (for example
`payments-prod`, `payments-dev`, `infra-syslog`), not one per host. Streams lead the ClickHouse®
sort key, so filtering by stream is cheap.

## Batching, backpressure and durability

```text
request ─▶ auth ─▶ normalize ─▶ WAL append + fsync ─▶ 2xx ack
                                     │
                         (queue/WAL full ⇒ 503 Retry-After: 1, nothing stored)
                                     ▼
                    batcher: ≤ BATCH_MAX_ROWS rows or BATCH_MAX_WAIT ─▶ ClickHouse INSERT (dedup token)
                                     │ transient error ⇒ retry with capped backoff, data stays in WAL
                                     │ permanent row rejection ⇒ <data>/deadletter/<signal>-<ts>.ndjson
```

| Situation | Client sees | What to do |
|---|---|---|
| Queue (`TIDEMARK_QUEUE_MAX_ROWS`) or WAL (`TIDEMARK_WAL_MAX_BYTES`) full | `503` + `Retry-After: 1` (gRPC `UNAVAILABLE`); nothing stored | Retry with backoff (all mainstream shippers do); investigate ClickHouse |
| Connection rate limit | `429` + `Retry-After` | Raise `rateLimitEps` or spread load |
| Body too large | `413` | Lower client batch size (`TIDEMARK_MAX_BODY_BYTES` 10 MiB compressed, 100 MiB decompressed) |
| Invalid lines (CLEF, native) | Accepted lines stored; `400` only if **all** lines are invalid | Fix the producer; native responses list up to 20 `errors` |

- **WAL** (enabled by default): acknowledged data is on local disk before the `2xx`. After a crash,
  uncommitted records are replayed with their original deduplication token, so nothing is stored
  twice. If ClickHouse is down, data accumulates in the WAL until `TIDEMARK_WAL_MAX_BYTES`, then
  ingestion returns `503`. A corrupt record in the middle of a segment is skipped and counted as
  `wal_corrupt`; undecodable payloads as `wal_decode`.
- **Dead letters:** rows ClickHouse rejects permanently are written as NDJSON to
  `<data>/deadletter/<signal>-<timestamp>.ndjson` and counted in
  `tidemark_pipeline_deadlettered_total`. There is no built-in replay; inspect the file, fix the
  cause, and re-send the data from the source if needed.
- **Without WAL** (`TIDEMARK_WAL_ENABLED=false`) the in-memory queue is used; acknowledged rows can
  be lost on a crash. Do not disable it in production.

`tidemark doctor` shows per signal the WAL segments, bytes not yet in ClickHouse, torn tail bytes
and total size against the cap; it is read-only and safe while the server runs.

## Troubleshooting ingestion {#troubleshooting-ingestion}

| Status / symptom | Meaning | Check |
|---|---|---|
| `401 unauthorized: missing key` | No key found in any accepted location | Header name; `?apiKey=` only works on Seq endpoints |
| `401 unauthorized: unknown key` | Key not found (typo, other installation, rotated and grace ended) | Compare the first 8 characters with *Key prefix* in **Settings › Connections** |
| `403 forbidden: protocol <p> not allowed` | Key valid, protocol not allowed | Connection *Protocols* |
| `403 forbidden: source address not allowed` | Client IP not in `allowedCidrs` | Client IP as seen by OpenTidemark (`lastUsedIp`, access log); `TIDEMARK_TRUSTED_PROXIES` |
| `403 forbidden: key revoked` / expired | Key revoked or past `expiresAt` | Rotate or create a new connection |
| `403` for every key from one IP | 30 auth failures/min from that IP ⇒ blocked for 5 min | Fix the misconfigured client, wait 5 min |
| `413 payload_too_large` | Body over the compressed or decompressed limit | Client batch size |
| `429` + `Retry-After` | Connection rate limit | `rateLimitEps` |
| `503` + `Retry-After: 1` | Queue or WAL full | ClickHouse health, `tidemark doctor`, `tidemark_pipeline_queue_depth`, disk |
| `2xx` but no data | Stream paused; events below `minimumLevel`; timestamps outside the searched range; the reader's stream scope excludes the stream | **Streams** status; connection *Min level*; widen the time range (future timestamps > 7 days are clamped and marked `_tidemark.clock_skew=true`); **Settings › Users › Your account** shows which streams you see |
| `2xx` but the data is in the default stream | The named stream is not in the connection's `allowedStreams` | `tidemark_ingest_rerouted_total`; extend `allowedStreams` or fix the client |
| Logstash: `Could not connect to a compatible version of Elasticsearch` | The `elasticsearch` output's product check; OpenTidemark does not identify as Elasticsearch | Use the `http` output: [Integrations › Logstash](integrations.md#logstash) |

Useful signals while debugging:

```bash
# Requests rejected per protocol and reason (auth, rate_limited), and items rerouted to the default stream
curl -sS -H "Authorization: Bearer $METRICS_TOKEN" "$TM/metrics" | grep -E '^tidemark_ingest_(rejected_requests|rerouted)_total'
# Last use of a connection
curl -sS "$TM/api/v1/connections/<CONNECTION_ID>" -H "Authorization: Bearer $TM_TOKEN" | jq '{lastUsedAt, lastUsedIp, eventsReceived}'
# Ingestion and queue counters without Prometheus
curl -sS "$TM/api/v1/system" -H "Authorization: Bearer $TM_TOKEN" | jq '{queueDepth, counters}'
```
