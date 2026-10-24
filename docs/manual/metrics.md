<!-- SPDX-License-Identifier: Apache-2.0 -->
# Metrics

Bring metrics into OpenTidemark over OTLP from SDKs and the OpenTelemetry® Collector, collect
Prometheus-instrumented applications through the Collector's `prometheus` receiver, monitor
OpenTidemark itself with Prometheus® (`/metrics`, exported families, PromQL and alert rules), and
query metrics with TQL and the metrics browser.

## Metrics over OTLP

OpenTidemark accepts OpenTelemetry metrics over OTLP/gRPC (`:4317`) and OTLP/HTTP (`:4318`,
`/otlp/v1/metrics` on the HTTP listener), protobuf or JSON. Use a connection that allows `otlp`.

### Supported metric types

| OTLP type | Stored as (`kind`) | `value` | Other columns |
|---|---|---|---|
| Gauge | `gauge` | the point value (int or double) | — |
| Sum | `sum` | the point value | `temporality` (`cumulative`/`delta`), `is_monotonic` |
| Histogram | `histogram` | `sum / count` (mean) | `count`, `sum`, `min`, `max`, `bucket_bounds`, `bucket_counts`, temporality |
| Exponential histogram | `exp_histogram` | `sum / count` | `count`, `sum`, `min`, `max`; buckets converted to explicit bounds `base^i` |
| Summary | `summary` | `sum / count` | `count`, `sum`, temporality `cumulative`; quantiles as attributes `quantile.<q>` |

Resource `service.name` becomes the `service`; data point attributes become `attrs`; the resource
attribute `tidemark.stream` selects the stream. Temporality and monotonicity are preserved. Every
point also keeps its start time (`startTimeUnixNano`, the start of a cumulative count) in the
column `start_time`. The quantile attributes of a summary are values, not part of its series:
all points of one summary form one series (summary points stored by earlier versions count each point
as its own series and age out with retention).

### From an SDK

Set the standard variables (see [Ingestion › OpenTelemetry SDKs](ingestion.md#opentelemetry-sdks)):

```bash
export OTEL_SERVICE_NAME=checkout-api
export OTEL_RESOURCE_ATTRIBUTES=tidemark.stream=checkout
export OTEL_EXPORTER_OTLP_ENDPOINT=https://tidemark.example.com:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
export OTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=${TIDEMARK_KEY}
export OTEL_METRICS_EXPORTER=otlp
```

### From an OpenTelemetry Collector

```yaml
receivers:
  otlp:
    protocols:
      grpc: {endpoint: 0.0.0.0:4317}
      http: {endpoint: 0.0.0.0:4318}
processors:
  batch: {send_batch_size: 5000, timeout: 1s}
exporters:
  otlphttp/tidemark:
    endpoint: https://tidemark.example.com:4318
    headers:
      X-Tidemark-Key: ${env:TIDEMARK_KEY}
    timeout: 30s
service:
  pipelines:
    metrics: {receivers: [otlp], processors: [batch], exporters: [otlphttp/tidemark]}
```

## Prometheus-instrumented applications {#prometheus-apps}

Applications and exporters that expose Prometheus metrics (`/metrics` in text format) are
collected by an OpenTelemetry Collector with the `prometheus` receiver, which scrapes them like
Prometheus does and forwards the samples as OTLP metrics.

!!! note "Prometheus remote_write is not available"
    A native Prometheus `remote_write` receiver is planned (milestone M4) and does not exist yet.
    Use the Collector as shown here.

Full, tested configuration (`otel-collector-prometheus.yaml`), image
`otel/opentelemetry-collector-contrib:0.120.0` (the version pinned in `deploy/docker-compose.yml`):

```yaml
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: node                       # becomes service.name ⇒ OpenTidemark `service`
          scrape_interval: 15s
          static_configs:
            - targets: ["node-exporter:9100"]
        - job_name: checkout-api
          scrape_interval: 15s
          metrics_path: /metrics
          static_configs:
            - targets: ["checkout-1:8080", "checkout-2:8080"]

processors:
  memory_limiter:
    check_interval: 1s
    limit_mib: 256
  resource/stream:
    attributes:
      - key: tidemark.stream                   # all scraped metrics into one stream
        value: infra-metrics
        action: upsert
  batch:
    send_batch_size: 5000
    timeout: 1s

exporters:
  otlphttp/tidemark:
    endpoint: http://tidemark:4318             # https://… in production
    headers:
      X-Tidemark-Key: ${env:TIDEMARK_KEY}
    timeout: 30s
    retry_on_failure:
      enabled: true
    sending_queue:
      enabled: true

service:
  pipelines:
    metrics:
      receivers: [prometheus]
      processors: [memory_limiter, resource/stream, batch]
      exporters: [otlphttp/tidemark]
```

Run it:

```bash
docker run -d --name otel-prom --network <net-with-targets> \
  -e TIDEMARK_KEY="$(cat /run/secrets/tidemark_collector_key)" \
  -v "$PWD/otel-collector-prometheus.yaml:/etc/otelcol-contrib/config.yaml:ro" \
  otel/opentelemetry-collector-contrib:0.120.0 --config=/etc/otelcol-contrib/config.yaml
```

Verify:

```bash
curl -sS "$TM/api/v1/metrics/names?q=node_load" -H "Authorization: Bearer $TM_TOKEN"
# ⇒ [{"name":"node_load1","kind":"gauge","unit":"","description":"1m load average."}, …]
curl -sS "$TM/api/v1/query" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"q":"source metrics | name:node_load1 | stats avg(value) by service span 1m","from":"now-15m","to":"now"}'
```

Mapping notes:

- Prometheus counters arrive as cumulative monotonic `sum`s; use `rate(value)` in TQL (below).
- Gauges arrive as `gauge`; histograms as `histogram` (with buckets); summaries as `summary`.
- Labels become attributes; the receiver also adds `service.instance.id` and related resource
  attributes. Query labels with `label:value` (attributes are text; numeric comparisons convert).
- The scrape `job_name` becomes `service.name`, so `service:node` selects a job.

Collector-side alternatives (any receiver of the contrib distribution works the same way):
`hostmetrics` for host CPU/memory/disk without node-exporter, `kubeletstats` and `k8s_cluster`
on Kubernetes.

## Scraping OpenTidemark with Prometheus {#scraping-opentidemark-with-prometheus}

OpenTidemark exposes its own metrics in Prometheus text format on `GET /metrics`:

- **only on the HTTP listener** (`TIDEMARK_HTTP_ADDR`, default `:8080`), not on 5341, 9200 or 4318;
- **disabled by default**: set `TIDEMARK_METRICS_ENABLED=true`;
- optionally protected: with `TIDEMARK_METRICS_TOKEN` set, requests need
  `Authorization: Bearer <token>`; others get `401` with `WWW-Authenticate: Bearer realm="metrics"`.

```bash
# .env (Compose) or /etc/tidemark/tidemark.env (native); restart afterwards
TIDEMARK_METRICS_ENABLED=true
TIDEMARK_METRICS_TOKEN_FILE=/run/secrets/tidemark_metrics_token     # or TIDEMARK_METRICS_TOKEN=<TOKEN>
```

```bash
curl -sS -H "Authorization: Bearer $(cat metrics-token)" http://tidemark-host:8080/metrics | grep '^tidemark_'
```

The metrics token is a static shared secret, not an API token. Generate it with
`openssl rand -hex 32`. `/metrics` is not rate limited and not audited; restrict it at the
firewall or proxy to the Prometheus servers as well.

### `prometheus.yml`

=== "Single node"

    ```yaml
    scrape_configs:
      - job_name: tidemark
        metrics_path: /metrics
        scheme: http                       # https with built-in TLS or a proxy
        authorization:
          type: Bearer
          credentials_file: /etc/prometheus/tidemark-metrics-token
        static_configs:
          - targets: ["tidemark-host:8080"]
    ```

=== "TLS"

    ```yaml
    scrape_configs:
      - job_name: tidemark
        scheme: https
        authorization:
          type: Bearer
          credentials_file: /etc/prometheus/tidemark-metrics-token
        tls_config:
          ca_file: /etc/prometheus/ca.pem          # CA of the OpenTidemark (or proxy) certificate
          server_name: tidemark.example.com
        static_configs:
          - targets: ["tidemark.example.com:8080"]
    ```

=== "HA (one target per node)"

    ```yaml
    # Scrape every node directly, not through the load balancer: counters are per node and
    # a balanced scrape would mix them.
    scrape_configs:
      - job_name: tidemark
        authorization:
          type: Bearer
          credentials_file: /etc/prometheus/tidemark-metrics-token
        static_configs:
          - targets: ["tm-node1:8080", "tm-node2:8080", "tm-node3:8080"]
    ```

All nodes of an HA cluster must use the same `TIDEMARK_METRICS_TOKEN` (or one per node with
separate jobs).

Check the target is up:

```bash
curl -s 'http://prometheus:9090/api/v1/targets?state=active' | jq '.data.activeTargets[] | {scrapeUrl, health, lastError}'
curl -s 'http://prometheus:9090/api/v1/query?query=up{job="tidemark"}'
```

### Exported metric families

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `tidemark_ingest_items_total` | counter | `protocol` (`seq`, `elastic`, `otlp`, `native`), `source` (`logs`, `spans`, `metrics`) | Items accepted by ingestion |
| `tidemark_ingest_dropped_total` | counter | `protocol`, `reason` (`below_minimum_level`) | Items dropped by connection policy |
| `tidemark_ingest_rejected_requests_total` | counter | `protocol`, `reason` (`auth`, `rate_limited`) | Rejected ingestion requests (`401`/`403` and `429`) |
| `tidemark_ingest_rerouted_total` | counter | `protocol`, `reason` (`stream_not_allowed`) | Items written to the connection's default stream because the requested stream is not in its `allowedStreams` |
| `tidemark_pipeline_enqueued_total` | counter | `signal` (`events`, `spans`, `metrics`) | Rows enqueued (with WAL: appended to the WAL, including rows whose client gave up while waiting for the fsync) |
| `tidemark_pipeline_written_total` | counter | `signal` | Rows written to ClickHouse® |
| `tidemark_pipeline_dropped_total` | counter | `signal`, `reason` | Rows refused or lost: `backpressure` (WAL cap reached, failed write or full queue; the request was answered `503` and the client may retry), `stream_paused`, `wal_corrupt`, `wal_decode`, `deadletter_failed` |
| `tidemark_pipeline_deadlettered_total` | counter | `signal` | Rows written to dead-letter files |
| `tidemark_pipeline_write_errors_total` | counter | `signal` | Failed ClickHouse insert attempts |
| `tidemark_pipeline_queue_depth` | gauge | `signal` | Rows acknowledged but not yet written (with WAL: the WAL backlog) |
| `tidemark_wal_bytes` | gauge | `signal` | Bytes of the WAL segments on disk, read at scrape time; compare with `TIDEMARK_WAL_MAX_BYTES` (only with the WAL enabled) |
| `tidemark_update_available` | gauge | | `1` while a newer OpenTidemark release is known from the daily update check, else `0` |
| `go_*`, `process_*` | | | Go runtime and process collectors (memory, GC, goroutines, CPU, open file descriptors) |

Counter series with labels appear only after their first increment (for example
`tidemark_pipeline_write_errors_total` exists only after the first failed insert). Write alert
expressions that tolerate absent series.

There is no dedicated metric for ClickHouse reachability: a growing `tidemark_wal_bytes` or
`tidemark_pipeline_queue_depth` together with `tidemark_pipeline_write_errors_total` shows that
inserts fail; probe `/readyz` (blackbox) for reachability itself.

### Example PromQL

```promql
# Ingest rate per node and signal (rows/s written to ClickHouse)
sum by (instance, signal) (rate(tidemark_pipeline_written_total[5m]))

# Accepted items per protocol and source
sum by (instance, protocol, source) (rate(tidemark_ingest_items_total[5m]))

# Write lag: acknowledged rows not yet in ClickHouse, and the WAL's share of its cap (10 GiB default)
max by (instance, signal) (tidemark_pipeline_queue_depth)
sum by (instance) (tidemark_wal_bytes) / 10737418240

# Rejections by reason over the last hour
sum by (instance, signal, reason) (increase(tidemark_pipeline_dropped_total[1h]))
sum by (instance, protocol, reason) (increase(tidemark_ingest_rejected_requests_total[1h]))

# Success ratio of accepted vs written rows
sum(rate(tidemark_pipeline_written_total[15m])) / sum(rate(tidemark_pipeline_enqueued_total[15m]))
```

### Alert rules

`tidemark-rules.yml` (validated with `promtool check rules`):

```yaml
groups:
  - name: opentidemark
    rules:
      - alert: TidemarkScrapeDown
        expr: up{job="tidemark"} == 0
        for: 2m
        labels: {severity: critical}
        annotations:
          summary: "OpenTidemark node {{ $labels.instance }} is not answering /metrics"
      - alert: TidemarkIngestBackpressure
        expr: sum by (instance, signal) (rate(tidemark_pipeline_dropped_total{reason="backpressure"}[5m])) > 0
        for: 5m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }} rejects {{ $labels.signal }} with 503 (queue or WAL full)"
      - alert: TidemarkPipelineDataLoss
        expr: sum by (instance, signal, reason) (increase(tidemark_pipeline_dropped_total{reason=~"wal_corrupt|wal_decode|deadletter_failed"}[15m])) > 0
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} lost {{ $labels.signal }} rows ({{ $labels.reason }})"
      - alert: TidemarkDeadLetters
        expr: sum by (instance, signal) (increase(tidemark_pipeline_deadlettered_total[15m])) > 0
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }} wrote {{ $labels.signal }} rows to dead-letter files"
      - alert: TidemarkClickHouseWriteErrors
        expr: sum by (instance, signal) (rate(tidemark_pipeline_write_errors_total[5m])) > 0
        for: 5m
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }}: ClickHouse inserts for {{ $labels.signal }} keep failing"
      - alert: TidemarkBacklogGrowing
        expr: max by (instance, signal) (tidemark_pipeline_queue_depth) > 100000 and max by (instance, signal) (deriv(tidemark_pipeline_queue_depth[10m])) > 0
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }}: {{ $value }} {{ $labels.signal }} rows acknowledged but not yet in ClickHouse"
      - alert: TidemarkWALFilling
        expr: sum by (instance) (tidemark_wal_bytes) > 0.8 * 10737418240
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }}: the write-ahead log holds {{ $value | humanize1024 }}B, over 80 % of TIDEMARK_WAL_MAX_BYTES"
      - alert: TidemarkIngestStalled
        expr: sum by (instance) (rate(tidemark_pipeline_enqueued_total[10m])) > 0 and sum by (instance) (rate(tidemark_pipeline_written_total[10m])) == 0
        for: 10m
        labels: {severity: critical}
        annotations:
          summary: "{{ $labels.instance }} accepts data but writes nothing to ClickHouse"
      - alert: TidemarkIngestAuthFailures
        expr: sum by (instance, protocol) (rate(tidemark_ingest_rejected_requests_total{reason="auth"}[5m])) > 1
        for: 10m
        labels: {severity: warning}
        annotations:
          summary: "{{ $labels.instance }}: more than 1 rejected {{ $labels.protocol }} key per second"
```

Tune `100000` to your volume (the default queue capacity is `TIDEMARK_QUEUE_MAX_ROWS` = 200,000
per signal without WAL; with WAL the cap is `TIDEMARK_WAL_MAX_BYTES`, 10 GiB by default, which
`TidemarkWALFilling` assumes). Add a blackbox probe of `/readyz` for ClickHouse and metadata
reachability, and a node-exporter filesystem alert for the data directory (doctor's thresholds:
1 GiB, 10 %).

### Alternative: collect self-metrics into OpenTidemark

The Collector's `prometheus` receiver can scrape `/metrics` with the bearer token and store the
self-metrics in OpenTidemark itself (for the *Ingestion health* dashboard template or TQL alerts).
Keep an external Prometheus as well: a system cannot reliably alert on its own outage.

```yaml
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: tidemark-self
          scrape_interval: 15s
          authorization:
            type: Bearer
            credentials: ${env:TIDEMARK_METRICS_TOKEN}
          static_configs:
            - targets: ["tidemark:8080"]
```

## Querying metrics

### TQL

Start metrics queries with `source metrics`. Built-in fields: `@timestamp`, `name`, `service`,
`kind`, `unit`, `value`, `count`, `sum`, `min`, `max`, `stream`, `connection_id`; every other
name is an attribute (text). Queries read only the metrics of the caller's visible streams and
the scope picker's selection.

```text
# Average of a gauge per service, 1-minute buckets
source metrics | name:node_load1 | stats avg(value) by service span 1m

# Counter rate (per second) across series; must be the only aggregate of the stage
source metrics | name:node_cpu_seconds_total and mode:idle | stats rate(value) span 1m

# Histogram: mean latency per bucket from the per-series increase of sum and count
source metrics | name:http.server.request.duration | stats hist_mean() by service span 5m

# Histogram: 95th percentile per bucket, interpolated from the bucket increases
source metrics | name:http.server.request.duration | stats histogram_quantile(0.95) by service span auto

# Latest value of each series of a gauge
source metrics | name:queue.depth | stats last(value) by queue

# Which services send a metric
source metrics | name:orders.created | top 10 service
```

Semantics:

- `rate(value)` on cumulative monotonic sums takes, per series, the increase from each point to
  the next (also across bucket boundaries and from a point up to one span, at most 10 minutes,
  before the time range), adds the series up and divides by the bucket seconds. A counter reset
  (a new start time, or a value that went down) counts the new value; delta sums count as they
  are.
- `hist_mean()` reads histograms, exponential histograms and summaries. Per series it takes the
  difference between consecutive points (restarts are detected from the start time and from
  decreasing counts; delta temporality is used as is), reads up to one span (at most 10 minutes)
  before the range so the first bucket has a previous point (the first point of a series counts
  in full only when its start time is known and inside that range; otherwise it is only the
  starting value), and divides the summed increase of
  `sum` by that of `count`. A bucket in which nothing was recorded is `null`. It can share a
  `stats` stage only with other histogram aggregates.
- `histogram_quantile(q)` (`0 < q < 1`) uses the same per-series differences of the bucket
  counts (histograms and exponential histograms; summaries have no buckets), merges them per
  bucket bound and interpolates linearly inside the bucket that holds the quantile, like PromQL.
  Exact for series with the same bucket bounds, approximate when bounds differ (merged over
  all bounds). When a series changes its bounds without a restart (an exponential histogram
  rescaling), that point adds its count and sum but no buckets. The default column name is
  `histogram_quantile_0_95` for `q = 0.95`; name it with `as p95` instead. A quantile in the open-ended last bucket gives the highest finite bound.
- Percentiles over metrics (`p95(value)`) are percentiles of the stored values (for histograms the
  mean since the process started, per point), not histogram quantiles.
- Attributes are text: `status_code >= 500` converts with `toFloat64OrNull`.

### Metrics browser

**Metrics** (`/metrics` in the UI): search metric names (backed by `GET /api/v1/metrics/names?q=`,
up to 1,000 names of the selected time range), open a quick chart, and *Add to dashboard*.
The quick chart picks an aggregate for the metric kind: histograms and summaries their mean
(`hist_mean()`, histograms also p50/p95/p99), counters (monotonic sums, `monotonic` in the names
response) their per-second rate (`rate(value)`, button *rate/s*; `sum`, `avg`, `max`, `min` and
`p95` are a click away), gauges and up-down sums their average. Anything the browser shows can
be refined as a TQL query in the explorer or a dashboard panel.
