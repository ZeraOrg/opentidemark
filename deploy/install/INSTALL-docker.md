<!-- SPDX-License-Identifier: Apache-2.0 -->
# Running OpenTidemark with Docker Compose

This bundle runs OpenTidemark and ClickHouse with Docker Compose on Linux, Windows (Docker Desktop
with the WSL2 backend) or macOS (for trying it out). It needs Docker with Compose 2.24 or newer.
`docker-compose.yml` uses the published image `ghcr.io/zeraorg/tidemark` of this release; nothing
is built. ClickHouse stays on an internal network and is never published to the host. The full
guide, with every option and troubleshooting, is
<https://github.com/ZeraOrg/opentidemark/blob/main/docs/installation.md>.

Replace `<version>` below with the version in the bundle name.

## 1. Verify the download

In the directory with the bundle and `checksums.txt` from the same release:

```bash
sha256sum -c checksums.txt --ignore-missing
gh attestation verify tidemark_<version>_docker.tar.gz --repo ZeraOrg/opentidemark          # optional, needs the GitHub CLI
gh attestation verify oci://ghcr.io/zeraorg/tidemark:<version> --repo ZeraOrg/opentidemark  # the image
```

On Windows, compare `Get-FileHash .\tidemark_<version>_docker.zip -Algorithm SHA256` with
`checksums.txt`.

## 2. Install and start

Linux and macOS:

```bash
tar -xzf tidemark_<version>_docker.tar.gz
cd tidemark_<version>_docker
./install.sh docker
```

Windows (PowerShell; unblocking the download first lets the script run under the default
execution policy):

```powershell
Unblock-File .\tidemark_<version>_docker.zip
Expand-Archive .\tidemark_<version>_docker.zip -DestinationPath .
cd .\tidemark_<version>_docker
.\install.ps1 -Mode Docker
```

The installer writes `.env` next to `docker-compose.yml` (random ClickHouse passwords and a
bootstrap ingest key; an existing `.env` is kept; on Linux mode `0600`) and runs
`docker compose up -d --build`, which pulls the images. Add `--no-start` (`-NoStart`) to only
create `.env`, for example to edit it first.

**Bind address:** the OpenTidemark ports (8080, 5341, 9200, 4317, 4318) are published on every
network interface. To keep them on this computer, or behind a reverse proxy on the same host,
add this line to `.env` before starting (or run `docker compose up -d` again afterwards):

```bash
TM_BIND_ADDR=127.0.0.1
```

If other people will use it, set `TIDEMARK_PUBLIC_URL` in `.env` to the address they open in the
browser, for example `http://192.168.1.20:8080`; alert links and connection snippets use it.

Check the stack with `docker compose ps` (both services `healthy`), then open
<http://localhost:8080>, sign in as **admin / admin** and **change the password right away**.
Create a connection (one API key per application) and point your clients at OpenTidemark:
<https://github.com/ZeraOrg/opentidemark/blob/main/docs/ingestion.md>.

## 3. Syslog and Fluent forward (optional)

The `collector` profile adds an OpenTelemetry Collector (`otel-collector.yaml`) that receives
syslog on 5514 tcp/udp and Fluent forward on 24224 and forwards it to OpenTidemark:

```bash
docker compose --profile collector up -d
```

Pass `--profile collector` to the other `docker compose` commands as well (for example `down`).

## 4. Everyday commands

Run these in the bundle directory.

| Task | Command |
|---|---|
| Status | `docker compose ps` |
| Logs | `docker compose logs -f tidemark` |
| Stop (data is kept) | `docker compose down` |
| Start | `docker compose up -d` |
| Operator command | `docker compose exec tidemark tidemark doctor` |

## 5. Upgrade

Read the changelog first (`CHANGELOG.md`). Unpack the new release's bundle, copy your `.env`
into its directory, and run there:

```bash
docker compose pull && docker compose up -d
```

The Compose project is always named `tidemark`, so in the new directory Compose replaces the
running containers with ones using the new image and keeps the data volumes (`tidemark_ch-data`,
`tidemark_tm-data`). If you set `TM_IMAGE` in `.env`, update or remove that line first.

## 6. Remove

```bash
docker compose down        # removes the containers, keeps the data volumes
docker compose down -v     # also DELETES all data (telemetry, users, dashboards, keys)
```
