<!-- SPDX-License-Identifier: Apache-2.0 -->
# Quick start: run OpenTidemark locally

Run OpenTidemark and ClickHouse on your own computer with Docker Compose, send a first log event
and search it in the web UI, in about ten minutes.

For servers and native installs see the [installation guide](installation.md).

## Before you start

- **Docker** with Compose v2: Docker Desktop on Windows or macOS, or Docker Engine with the
  Compose plugin on Linux. `docker compose version` must work.
- These free ports on your computer: **8080** (web UI and API), **5341** (Seq), **9200**
  (Elasticsearch `_bulk`), **4317** and **4318** (OpenTelemetry gRPC and HTTP).

## 1. Get the Docker bundle

Download `tidemark_<version>_docker.tar.gz` (or `.zip` on Windows) and `checksums.txt` from the
latest [release](https://github.com/ZeraOrg/opentidemark/releases) (every release after 0.1.0 has one).
The bundle holds the Compose file, pinned to that release's published image, the installers and
the ClickHouse configuration.

```bash
sha256sum -c checksums.txt --ignore-missing
tar -xzf tidemark_<version>_docker.tar.gz
cd tidemark_<version>_docker
```

```powershell
Get-FileHash .\tidemark_<version>_docker.zip -Algorithm SHA256   # compare with checksums.txt
Unblock-File .\tidemark_<version>_docker.zip
Expand-Archive .\tidemark_<version>_docker.zip -DestinationPath .
cd .\tidemark_<version>_docker
```

## 2. Create the settings file

The installer copies `.env.example` to `.env`, with random ClickHouse passwords and an ingest key.
It does not overwrite an existing `.env`.

```bash
./install.sh docker --no-start            # Linux / macOS
```

```powershell
.\install.ps1 -Mode Docker -NoStart       # Windows PowerShell
```

By default the ports are published on every network interface. For a local test, publish them
on your own computer only by adding this line to `.env`:

```
TM_BIND_ADDR=127.0.0.1
```

If colleagues will use it, set `TIDEMARK_PUBLIC_URL` in `.env` to the address they open in the
browser, for example `http://192.168.1.20:8080`; alert links and connection snippets use it.

## 3. Start the stack

```bash
docker compose up -d
```

Compose pulls the OpenTidemark and ClickHouse images on the first start. To try unreleased changes,
build the image from a source checkout instead (`git clone https://github.com/ZeraOrg/opentidemark`,
then `./install.sh docker` in its `deploy/` directory; see the
[installation guide](installation.md#4-docker-compose-linux-windows-macos)).

Wait until both containers show `(healthy)`:

```bash
docker compose ps
```

## 4. Open the web UI

1. Go to <http://localhost:8080>.
2. Sign in as **admin / admin**.
3. **Change the password right away** under Settings → Users. Until you do, the server logs a
   warning.

## 5. Send a first event

The first start creates a connection called `bootstrap`. It accepts every protocol, and its key is
the `TIDEMARK_BOOTSTRAP_INGEST_KEY` line in `.env`.

```bash
KEY=$(grep '^TIDEMARK_BOOTSTRAP_INGEST_KEY=' .env | cut -d= -f2)
curl -X POST 'http://localhost:5341/api/events/raw?clef' \
  -H "X-Seq-ApiKey: $KEY" -H 'Content-Type: application/vnd.serilog.clef' \
  --data-binary '{"@t":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'","@l":"Warning","@mt":"Hello from {Name}","Name":"quickstart"}'
```

```powershell
$key = ((Select-String '^TIDEMARK_BOOTSTRAP_INGEST_KEY=' .env).Line -split '=', 2)[1]
$body = '{"@t":"' + (Get-Date).ToUniversalTime().ToString('o') + '","@l":"Warning","@mt":"Hello from {Name}","Name":"quickstart"}'
Invoke-RestMethod 'http://localhost:5341/api/events/raw?clef' -Method Post -Body $body `
  -ContentType 'application/vnd.serilog.clef' -Headers @{ 'X-Seq-ApiKey' = $key }
```

A `201` response means the event was stored. Search for `Name:quickstart` in the explorer, and pick
a time range that includes the event's `@t` timestamp.

## 6. Connect your applications

Point your clients at OpenTidemark. Best practice is one connection (one API key) per application,
created under Connections in the UI.

| Client | Address | Key |
|---|---|---|
| Serilog / NLog (Seq sink) | `http://localhost:5341` | API key setting |
| Filebeat, Fluent Bit, Vector | `http://localhost:9200` | `Authorization: ApiKey <key>` or as the password |
| Logstash (`http` output, see [Sending data](ingestion.md#logstash)) | `http://localhost:8080/api/v1/ingest/logs` | `X-Tidemark-Key: <key>` |
| OpenTelemetry SDK or Collector | `http://localhost:4318` (HTTP), `localhost:4317` (gRPC) | `Authorization: Bearer <key>` |

The details for each protocol are in [Sending data](ingestion.md), and the search syntax is in the
[query language reference](query-language.md).

## Everyday commands

Run these in the bundle directory.

| Task | Command |
|---|---|
| Show status | `docker compose ps` |
| Follow OpenTidemark's logs | `docker compose logs -f tidemark` |
| Stop (data is kept) | `docker compose down` |
| Start again | `docker compose up -d` |
| Update to a newer release | unpack the new bundle, copy `.env` into it, then `docker compose pull && docker compose up -d` there |
| Remove everything, including data | `docker compose down -v` |

## Troubleshooting

- **`run install.sh docker to create .env`:** `.env` is missing. Run step 2 in the bundle
  directory.
- **`port is already allocated`:** another program uses one of the ports. Stop that program; the
  ports are fixed in `docker-compose.yml`.
- **`tidemark` stays `unhealthy` or restarts:** look at `docker compose logs tidemark`. ClickHouse
  must be healthy first (`docker compose logs clickhouse`).
- **`401` when sending events:** the key is wrong or missing. Copy it from `.env` again, or create a
  new connection in the UI.
- **Events don't show up in the explorer:** widen the time range, because the event's `@t`
  timestamp decides where it appears.
- **Starting over:** `docker compose down -v` deletes the data and the admin account. Your `.env`
  is kept, and the next start creates the admin account and the `bootstrap` connection again.

All settings are described in [Configuration](configuration.md).
