<!-- SPDX-License-Identifier: Apache-2.0 -->
# Migrating from Seq

This guide moves a Seq installation to Tidemark: applications keep sending logs unchanged,
existing events and signals are imported, and Seq can be switched off afterwards. The
Seq-compatible endpoints are described in [ingestion.md](ingestion.md), the query language in
[query-language.md](query-language.md).

## 1. Point applications at Tidemark

Tidemark accepts Seq's ingestion API on port **5341** (`POST /api/events/raw`, CLEF and the
compact JSON format), authenticated with the `X-Seq-ApiKey` header or the `?apiKey=` parameter.

To avoid touching every application's configuration, re-use the existing Seq API keys:

1. In Tidemark, open **Settings → Connections → New connection**, protocol **Seq**.
2. Paste the existing Seq API key into **Key** (admin-supplied key). The key must be
   20–256 printable ASCII characters without spaces and look random enough (≥ 80 bits of
   estimated entropy); Seq's generated keys qualify.
3. Repeat for every key. Each key becomes one connection; its target stream decides where
   the events land.

Then change only the host name (or DNS record) your applications use, for example
`http://seq.internal:5341` → `http://tidemark.internal:5341`.

## 2. Import historical events and signals

Create an API key in Seq with the **Read** permission. Import from the UI
(**Settings → Import → Seq**) or from the command line on a Tidemark server:

```bash
export SEQ_API_KEY='…'            # never pass secrets as command-line arguments
tidemark import seq \
  --url https://seq.internal \
  --api-key-env SEQ_API_KEY \
  --from 2026-01-01T00:00:00Z \
  --signals \
  --stream app
```

- `--from` / `--to` limit the time range (RFC 3339). Without `--to` the range ends at the time
  the job is created, so a resumed job reads exactly the same events.
- `--filter` takes a strict Seq filter expression, e.g. `@Level = 'Error'`.
- `--signals` also imports the shared signals; `--no-events` imports only signals.
- `--stream` stores every event in one stream; without it events go to `default`.
- The command runs the job in the foreground and prints progress every 5 seconds. `--detach`
  queues it for the server instead. `Ctrl+C` leaves the job resumable: start the server (or
  run `tidemark import list` to find it) and it continues from its checkpoint.

Seq returns events newest first, so the import also walks from the newest event in the range
to the oldest. Every page is written with a deterministic deduplication token, so an
interrupted and resumed job never stores an event twice.

### Signals

Seq filter expressions are translated to TQL: `@Level`, `@Message`, `@MessageTemplate`,
`@Exception`, `@EventType`, `@TraceId` and `@SpanId` map to Tidemark's built-in fields,
`X is not null` becomes `X:*`, and Seq's `=`, `<>`, `like`, `in […]`, `and`/`or`/`not` work
as they are. A signal whose filter cannot be translated is imported **disabled** with its
original text and a warning in the job report; edit it in **Signals** and enable it.

## 3. Import exported files instead

If the Seq server is no longer reachable, export events with
`seqcli search --json … > events.clef` (or use existing CLEF/NDJSON archives, `.gz` allowed)
and import the files:

```bash
tidemark import clef events-2026-*.clef.gz --stream app
```

Files can also be uploaded in **Settings → Import → Files** (at most 16 files of 1 GiB each per
job).

## 4. Check and switch off Seq

- `tidemark import status <id>` (or the Import page) shows rows imported, records that could
  not be decoded and warnings.
- Data older than a stream's retention is removed by ClickHouse shortly after the import; raise
  the stream's retention first if you need it.
- Once applications send to Tidemark and the history is imported, Seq can be stopped.
