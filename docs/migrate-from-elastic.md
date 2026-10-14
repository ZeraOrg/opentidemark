<!-- SPDX-License-Identifier: Apache-2.0 -->
# Migrating from Elasticsearch, OpenSearch and Kibana

Replace an Elasticsearch, OpenSearch or Kibana (ELK) logging stack with OpenTidemark: keep your
Beats, Fluent Bit, Vector and Logstash shippers, and import existing indices and saved searches.

This guide moves a logging setup built on Elasticsearch or OpenSearch (with Kibana or
OpenSearch Dashboards) to OpenTidemark. The ingestion endpoints are described in
[ingestion.md](ingestion.md), the query language in [query-language.md](query-language.md).

## 1. Point shippers at OpenTidemark

OpenTidemark speaks the Elasticsearch ingestion API on port **9200** (`_bulk`, `_doc`, `_create`,
and the version/health/template probes that Beats, Fluent Bit and Vector send).
Indices map to streams: the index name is lower-cased and a date suffix is removed
(`app-2026.10.13`, `app-2026.10`, `app-20261013` ⇒ `app`).

**Logstash** is the exception: its `elasticsearch` output only talks to servers that identify as
Elasticsearch, which OpenTidemark does not. Replace that output with Logstash's `http` output to
the native JSON endpoint, as shown in [Sending data › Logstash](ingestion.md#logstash); the
connection needs the native protocol.

To keep the shippers' credentials:

1. In OpenTidemark, open **Settings → Connections → New connection**, protocol **Elasticsearch**.
2. Paste the secret your shippers already send into **Key** (admin-supplied key: 20–256
   printable ASCII characters, no spaces, ≥ 80 bits of estimated entropy): for
   `Authorization: ApiKey <base64(id:key)>` that is the `key` part of the API key, for basic
   authentication the password, for `Authorization: Bearer` the token.
3. Change only the host name the shippers use.

## 2. Import historical documents

Import from the UI (**Settings → Import → Elasticsearch**) or from the command line:

```bash
export ES_PASSWORD='…'            # secrets come from environment variables only
tidemark import elastic \
  --url https://es.internal:9200 \
  --index 'logs-*' \
  --auth-type basic --username migrator --password-env ES_PASSWORD \
  --ca-file /etc/ssl/es-ca.pem \
  --from 2026-01-01T00:00:00Z
```

| Flag | Meaning |
|---|---|
| `--auth-type` | `none`, `basic` (`--username`, `--password-env`), `apiKey` (`--api-key-env`, the base64 `id:key` value) or `bearer` (`--token-env`) |
| `--ca-file` | PEM bundle trusted in addition to the system roots (TLS verification cannot be disabled) |
| `--time-field` | time field to sort and filter on (default `@timestamp`) |
| `--from`, `--to` | RFC 3339 range; without `--to` the range ends when the job is created |
| `--query-file` | a JSON query DSL object, ANDed with the range (e.g. `{"term":{"service":"api"}}`) |
| `--stream` | store everything in one stream instead of mapping each index |
| `--page-size` | documents per request (default 5,000, at most 10,000) |
| `--detach` | queue the job for the server instead of running it in the foreground |

The importer opens a point in time and pages with `search_after` (Elasticsearch 7.10+ and
8.x); when the cluster has no point-in-time API (OpenSearch, older versions) it falls back to
the scroll API. Documents are mapped exactly like live `_bulk` requests; the original `_id` is
kept as the `_id` property and the original timestamps are kept.

The import user needs the `read` index privilege on the imported indices. If the job is interrupted (`Ctrl+C`, a restart, a node
failure in a cluster), it resumes from its checkpoint; every page is written with a
deterministic deduplication token, so documents are never stored twice.

## 3. Import Kibana saved objects

Export saved objects in Kibana (**Stack Management → Saved objects → Export**, `.ndjson`) and
import the file in **Settings → Import → Kibana** or with:

```bash
tidemark import kibana export.ndjson
```

- **Saved searches** become signals. KQL is translated as it is (`field:value`, phrases,
  wildcards, ranges, `and`/`or`/`not`); Lucene's `_exists_:field` becomes `field:*`. Searches
  that use query DSL objects, active filter pills or unsupported syntax are imported
  **disabled** with a warning; fix and enable them in **Signals**.
- **Index patterns / data views** create one stream per pattern (`app-*` ⇒ `app`).
- **Dashboards, visualizations and other objects** are listed in the report as not converted;
  rebuild them with the dashboard builder or a template.

## 4. Check and switch off the cluster

- `tidemark import status <id>` (or the Import page) shows progress, documents that could not
  be decoded and warnings; `tidemark import cancel <id>` stops a job after its current page.
- Data older than a stream's retention is removed shortly after the import; raise the
  retention first if you need it.
- Once the shippers send to OpenTidemark and the history is imported, the cluster can be retired.
