<!-- SPDX-License-Identifier: Apache-2.0 -->
# Installing and running OpenTidemark

Install the self-hosted OpenTidemark log management platform and its ClickHouse storage with Docker
Compose or as a native service on Linux (systemd) or Windows, then harden, upgrade and back it up.

This guide covers every supported way to install OpenTidemark, from a single Docker Compose stack to
a native service on Linux or Windows. It also covers first sign-in, hardening, upgrades, backups
and troubleshooting. To try OpenTidemark on your own computer in ten minutes, use the
[local quick start](quickstart.md) instead.

OpenTidemark is one program, `tidemark`, with the web UI built in. It stores telemetry in
**ClickHouse**, which runs separately, either next to it in Docker or as a native ClickHouse
server. Users, dashboards, alerts and connections live in a small SQLite database in OpenTidemark's
data directory. In the optional high-availability mode they live in PostgreSQL instead.

- [1. Choosing an install method](#1-choosing-an-install-method)
- [2. Requirements](#2-requirements)
- [3. Getting OpenTidemark](#3-getting-opentidemark)
- [4. Docker Compose (Linux, Windows, macOS)](#4-docker-compose-linux-windows-macos)
- [5. Native install on Linux (systemd)](#5-native-install-on-linux-systemd)
- [6. Native install on Windows (service)](#6-native-install-on-windows-service)
- [7. macOS (development only)](#7-macos-development-only)
- [8. First sign-in and hardening](#8-first-sign-in-and-hardening)
- [9. High availability](#9-high-availability)
- [10. Operations](#10-operations)
- [11. Troubleshooting](#11-troubleshooting)

## 1. Choosing an install method

| Platform | Method | When to use it |
|---|---|---|
| Linux x86-64 / arm64 | [Docker Compose](#4-docker-compose-linux-windows-macos) | The default. OpenTidemark and ClickHouse in containers, with ClickHouse kept on an internal network. |
| Linux x86-64 / arm64 | [Native (systemd)](#5-native-install-on-linux-systemd), with ClickHouse from native packages or Docker | You want OpenTidemark as a hardened systemd service, or you already run ClickHouse. Also the basis for [high availability](#9-high-availability). |
| Windows 10/11, Server 2019+ (x86-64) | [Docker Compose](#4-docker-compose-linux-windows-macos) on Docker Desktop (WSL2 backend) | The simplest way to run OpenTidemark on Windows. |
| Windows 10/11, Server 2019+ (x86-64) | [Native Windows service](#6-native-install-on-windows-service), with ClickHouse in Docker Desktop | You want `tidemark.exe` as a Windows service. ClickHouse still runs in Docker Desktop. |
| macOS | [Development only](#7-macos-development-only): native binary, ClickHouse in Docker | Developing or trying OpenTidemark. **macOS is not supported for production.** |

ClickHouse has no native Windows build, so on Windows it always runs in Docker Desktop.

## 2. Requirements

### Software

- **Docker** (Compose method, and ClickHouse on Windows or macOS): Docker Engine with the Compose
  v2 plugin on Linux, Docker Desktop with the WSL2 backend on Windows, or Docker Desktop on macOS.
  `docker compose version` must work. The Compose file uses the `required` option of `env_file`,
  so Compose 2.24 or newer is needed.
- **ClickHouse 25.8.** The Compose file runs `clickhouse/clickhouse-server:25.8`, and the tests run
  against 25.8. For native installs use the same release line.
- **systemd** for the native Linux install (`install.sh native` refuses to run without it).
- **Administrator rights** for the native Windows install.
- **Go 1.26 and Node 22** only if you build from source.

### Ports

| Port | Used by | Purpose |
|---|---|---|
| 8080/tcp | OpenTidemark | Web UI, `/api/v1`, native JSON ingestion (also Logstash), Seq endpoints, Elasticsearch under `/es/`, OTLP/HTTP under `/otlp/`, `/healthz`, `/readyz` |
| 5341/tcp | OpenTidemark | Seq endpoints (Serilog, NLog, `seqcli`) |
| 9200/tcp | OpenTidemark | Elasticsearch `_bulk` (Filebeat, Fluent Bit, Vector) |
| 4317/tcp | OpenTidemark | OpenTelemetry OTLP/gRPC |
| 4318/tcp | OpenTidemark | OpenTelemetry OTLP/HTTP |
| 5514/tcp+udp | OTel Collector (optional `collector` profile) | Syslog (RFC 5424) |
| 24224/tcp | OTel Collector (optional `collector` profile) | Fluent forward |
| 9000/tcp | ClickHouse | Native protocol that OpenTidemark uses. Never expose it to untrusted networks. |

Every OpenTidemark listener can be moved or switched off with the `TIDEMARK_*_ADDR` variables
(see [Configuration](configuration.md)). A native ClickHouse server also listens on 8123 (HTTP)
and 9009 (inter-server) by default. The high-availability setup needs more ClickHouse ports
between the servers (see [section 9](#9-high-availability)).

### Disk and memory

OpenTidemark does not publish measured sizing figures yet. The design goal of at least 10,000
events/s on one node with 4 vCPU and 8 GB RAM has not been benchmarked. Plan capacity from your
own data volume and retention. These limits are fixed or configured:

- **Write-ahead log:** ingested data is written to `<data dir>/wal` before it is acknowledged. It
  may grow to `TIDEMARK_WAL_MAX_BYTES` (default 10 GiB) when ClickHouse is slow or unreachable,
  so keep that much free space in the data directory. When the cap is reached, ingestion answers
  `503` (backpressure).
- **Free space checks:** `tidemark doctor` fails when the data directory has less than 1 GiB free
  and warns below 10 %.
- **Metadata backups:** before each metadata migration, OpenTidemark keeps a copy in
  `<data dir>/backups` (the last 10 are kept).
- **ClickHouse:** the read-only query user is limited to 1 GiB of memory and 60 s per query
  (`deploy/clickhouse/users.d/tidemark.xml`). Telemetry storage is ClickHouse's own volume
  (`ch-data` in Compose).

## 3. Getting OpenTidemark

There are four ways to get OpenTidemark. Each
[release](https://github.com/ZeraOrg/opentidemark/releases) provides all of them for the same version
(the Docker bundle from the release after 0.1.0 on).

### Release archives

Each release has `tidemark_<version>_linux_amd64.tar.gz`, `tidemark_<version>_linux_arm64.tar.gz`,
`tidemark_<version>_windows_amd64.zip` and `checksums.txt`. An archive holds one directory
`tidemark_<version>_<os>_<arch>/` containing:

- the binary (`tidemark` or `tidemark.exe`) with the web UI built in;
- `INSTALL.md`, the short installation steps for that platform;
- `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, `README.md` and `CHANGELOG.md`;
- `deploy/.env.example`, `deploy/clickhouse/config.d/tidemark.xml` and
  `deploy/clickhouse/users.d/tidemark.xml`;
- on Linux also `deploy/install.sh` and `deploy/tidemark.service`, and on Windows
  `deploy/install.ps1`.

The native archives don't contain the Docker Compose files; for Compose, use the Docker bundle
(below) or the source tree. Tar archives record owner and group 0 for every file.

Verify the download before you unpack it:

```bash
sha256sum -c checksums.txt --ignore-missing
gh attestation verify tidemark_<version>_linux_amd64.tar.gz --repo ZeraOrg/opentidemark   # optional, needs the GitHub CLI
```

```powershell
Get-FileHash .\tidemark_<version>_windows_amd64.zip -Algorithm SHA256   # compare with checksums.txt
```

Releases 0.2.0 and earlier were published before the repository was renamed from
`ZeraOrg/tidemark`; verify their attestations with `--repo ZeraOrg/tidemark`.

`tidemark version` prints the version of an unpacked binary.

### Docker bundle

`tidemark_<version>_docker.tar.gz` and `tidemark_<version>_docker.zip` hold one directory
`tidemark_<version>_docker/` with everything Compose needs to run the published image, and
nothing to build:

- `docker-compose.yml`, the same file as `deploy/docker-compose.yml` of the source tree but
  without the `build:` section, with the `tidemark` image pinned to
  `ghcr.io/zeraorg/tidemark:<version>` (`TM_IMAGE` in `.env` still overrides it);
- `.env.example`, `install.sh`, `install.ps1` and `otel-collector.yaml`;
- `clickhouse/config.d/tidemark.xml`, `clickhouse/config.d/docker-listen.xml` and
  `clickhouse/users.d/tidemark.xml`;
- `INSTALL.md`, `LICENSE`, `NOTICE`, `README.md` and `CHANGELOG.md`.

Work in that directory as described in [section 4](#4-docker-compose-linux-windows-macos); the
installers use the directory they are in.

### Container image

`ghcr.io/zeraorg/tidemark:<version>` is built for `linux/amd64` and `linux/arm64`. Releases without
a pre-release suffix are also tagged `<major>.<minor>` and `latest`. The image is distroless and
runs as the non-root user 65532, with its data in the `/data` volume. You can verify its
provenance with `gh attestation verify oci://ghcr.io/zeraorg/tidemark:<version> --repo ZeraOrg/opentidemark`.

### Source

```bash
git clone --depth 1 --branch v<version> https://github.com/ZeraOrg/opentidemark
```

Leave out `--depth 1 --branch v<version>` to get the latest development state. To build binaries
see [Building from source](../README.md#building-from-source). The Compose setup can build the
image from the checkout for you.

## 4. Docker Compose (Linux, Windows, macOS)

`deploy/docker-compose.yml` runs two services:

- **`clickhouse`** (`clickhouse/clickhouse-server:25.8`), with its data in the volume `ch-data`.
  It is only on the internal `backend` network, so it is **never published to the host**.
- **`tidemark`**, with its data in the volume `tm-data`. It publishes ports 8080, 5341, 9200,
  4317 and 4318 and starts once ClickHouse is healthy.

The `tidemark` container runs with a read-only root filesystem, `no-new-privileges` and all
capabilities dropped. macOS is fine for trying the stack, but it is not supported for production.

### 4.1 Create `.env`

Work in the directory of the unpacked Docker bundle (section 3), or in the `deploy/` directory of a
source checkout. Both have the same layout; the bundle's Compose file runs the release image, the
checkout's builds the image from source by default.

```bash
./install.sh docker --no-start            # Linux, macOS
```

```powershell
.\install.ps1 -Mode Docker -NoStart       # Windows PowerShell
```

The installer copies `.env.example` to `.env` and changes three values:

- `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD` and `TIDEMARK_CLICKHOUSE_READER_PASSWORD` get random
  256-bit hex secrets.
- `TIDEMARK_BOOTSTRAP_INGEST_KEY` gets a generated connection key (`tmk_` followed by 40
  characters).

`install.sh` creates the file with mode `0600`. `install.ps1` gives access only to the current
user, Administrators and SYSTEM. An existing `.env` is never overwritten. Without the `--no-start`
or `-NoStart` switch, the installer goes on to run `docker compose up -d --build` (section 4.3).

ClickHouse and OpenTidemark both read the passwords from `.env`. Compose refuses to start with
`run install.sh docker to create deploy/.env` (in the bundle: `… create .env`) when they are missing. Every other `TIDEMARK_*`
line in `.env` is passed to the OpenTidemark container (all variables are described in
[Configuration](configuration.md)). Two values are fixed by the Compose file:
`TIDEMARK_CLICKHOUSE_ADDR=clickhouse:9000` and `TIDEMARK_DATA_DIR=/data`.

### 4.2 Compose-only settings

These lines in `.env` are read by Compose, not by OpenTidemark:

| Setting | Default | Meaning |
|---|---|---|
| `TM_BIND_ADDR` | `0.0.0.0` | Host address that every published port binds to. Use `127.0.0.1` for a local test, or when a reverse proxy on the same host is the only way in. |
| `TM_IMAGE` | bundle: `ghcr.io/zeraorg/tidemark:<version>`; checkout: `tidemark:local` | Image for the `tidemark` service, for example another `ghcr.io/zeraorg/tidemark:<version>`. |
| `TM_VERSION` | `dev` | Version stamped into an image you build yourself (source checkout only). |
| `COLLECTOR_INGEST_KEY` | `TIDEMARK_BOOTSTRAP_INGEST_KEY` | Connection key used by the optional collector. Any key that allows the `otlp` protocol works. |

By default the ports are published on **every network interface**. Set `TM_BIND_ADDR` on hosts
that are reachable from networks you don't trust.

### 4.3 Start the stack

- **Docker bundle:** `docker compose up -d` (the installer already did this unless you passed
  `--no-start`). Compose pulls the pinned release image; nothing is built.

In a source checkout, use one of these two options:

- **Published image** (no build):

  ```bash
  echo "TM_IMAGE=ghcr.io/zeraorg/tidemark:<version>" >> .env
  docker compose pull tidemark
  docker compose up -d --no-build
  ```

- **Build from your checkout:** `docker compose up -d --build`. Set `TM_VERSION=<version>` in
  `.env` if you want the image stamped with a version. Without `TM_IMAGE` the image is tagged
  `tidemark:local`.

Check the status with `docker compose ps`. Both services must show `(healthy)`:

- **ClickHouse:** the health check runs `SELECT 1` as `tidemark_reader`.
- **OpenTidemark:** the health check runs `tidemark healthcheck`, which requests
  `http://127.0.0.1:8080/healthz` and succeeds only on `200`. The image has no shell, so the
  binary checks itself.

Then continue with [first sign-in and hardening](#8-first-sign-in-and-hardening).

### 4.4 Syslog and Fluent forward (optional `collector` profile)

OpenTidemark doesn't receive syslog or Fluent forward itself. The `collector` profile adds an
OpenTelemetry Collector (`otel/opentelemetry-collector-contrib`). It receives syslog (RFC 5424) on
5514 tcp/udp and Fluent forward on 24224, and forwards everything to OpenTidemark over OTLP/HTTP
with `COLLECTOR_INGEST_KEY`.

```bash
docker compose --profile collector up -d
```

The collector starts once OpenTidemark is healthy and has no health check of its own. It is
configured in `otel-collector.yaml` (`deploy/otel-collector.yaml` in a source checkout). Pass `--profile collector` to the other `docker
compose` commands as well (for example `down`) so they include it.

### 4.5 Everyday commands

Run these in the bundle directory or in `deploy/`.

| Task | Command |
|---|---|
| Status | `docker compose ps` |
| Logs | `docker compose logs -f tidemark` (ClickHouse: `docker compose logs clickhouse`) |
| Stop (data is kept) | `docker compose down` |
| Start | `docker compose up -d` |
| Run an operator command | `docker compose exec tidemark tidemark doctor` (see [section 10](#10-operations)) |

## 5. Native install on Linux (systemd)

The native install runs `/usr/local/bin/tidemark` as the systemd service `tidemark`, under its own
system user. ClickHouse runs either on the same host from the official packages, or in Docker.

### 5.1 Install OpenTidemark

Unpack the release archive for your architecture and run the installer as root:

```bash
tar -xzf tidemark_<version>_linux_amd64.tar.gz
cd tidemark_<version>_linux_amd64
sudo ./deploy/install.sh native ./tidemark          # add --start to start it right away
```

`install.sh native` does the following:

1. Creates the system user and group `tidemark`, with home `/var/lib/tidemark` and no login shell.
   It does nothing if the user already exists.
2. Creates `/var/lib/tidemark` (owner `tidemark:tidemark`, mode `0750`) and `/etc/tidemark` (owner
   `root:tidemark`, mode `0750`).
3. Installs the binary to `/usr/local/bin/tidemark` (owner root, mode `0755`).
4. Writes `/etc/tidemark/tidemark.env` (owner `root:tidemark`, mode `0640`) from `.env.example`,
   but only if the file doesn't exist yet. It contains random ClickHouse passwords, a bootstrap
   ingest key and `TIDEMARK_CLICKHOUSE_ADDR=localhost:9000`.
5. Installs `deploy/tidemark.service` to `/etc/systemd/system/tidemark.service`, runs
   `systemctl daemon-reload` and enables the service. With `--start` it also starts the service
   (`systemctl enable --now`). Without it the service is only enabled.

The unit runs `tidemark serve` as `tidemark:tidemark` with `TIDEMARK_DATA_DIR=/var/lib/tidemark`
and `TIDEMARK_ENV_FILE=/etc/tidemark/tidemark.env`. It starts after `network-online.target` and
`clickhouse-server.service`, and restarts on failure after 5 s. It is hardened:

- `ProtectSystem=strict`, with only `/var/lib/tidemark` writable;
- `ProtectHome`, `PrivateTmp`, `PrivateDevices`, `NoNewPrivileges`, the `ProtectKernel*` and
  `Protect*` options and `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`;
- `SystemCallFilter=@system-service`, no capabilities and `UMask=0027`.

`MemoryDenyWriteExecute` is deliberately not set, because the SQLite driver compiles WebAssembly
at run time. Change settings in `/etc/tidemark/tidemark.env` (variables in
[Configuration](configuration.md)), not in the unit. The installer replaces the unit file
whenever it runs.

Don't start OpenTidemark until ClickHouse is ready (section 5.2). If you passed `--start`, the
service keeps logging `waiting for clickhouse` until it can connect.

### 5.2 ClickHouse from the native packages

Install ClickHouse 25.8 by following the official instructions for your distribution:
<https://clickhouse.com/docs/install>. Then give it OpenTidemark's configuration and users. Run the
following from the unpacked archive directory.

1. **Copy the configuration files.** The package's `config.d` and `users.d` directories are owned
   by the `clickhouse` user and are not readable by anyone else, so install the files with that
   owner:

   ```bash
   sudo install -m 0440 -o clickhouse -g clickhouse deploy/clickhouse/config.d/tidemark.xml /etc/clickhouse-server/config.d/tidemark.xml
   sudo install -m 0440 -o clickhouse -g clickhouse deploy/clickhouse/users.d/tidemark.xml  /etc/clickhouse-server/users.d/tidemark.xml
   ```

   `config.d/tidemark.xml` sets the log level to `warning` and turns off the MySQL and PostgreSQL
   protocol ports. It also declares the `backups` disk at `/var/lib/clickhouse/backups/`, which
   `tidemark backup` uses, and it switches off ClickHouse's crash reports to the ClickHouse
   developers (`send_crash_reports`). It doesn't change the listen address, so ClickHouse keeps the
   package default of `127.0.0.1` and `::1`, which is what you want when OpenTidemark runs on the
   same host. If OpenTidemark runs on another host, add a `listen_host` for a private address in a
   file of your own and allow only OpenTidemark's host to reach port 9000 in the firewall. Also give
   ClickHouse's own `default` user a password, as the ClickHouse installation guide describes.

2. **Pass the passwords to ClickHouse.** `users.d/tidemark.xml` defines `tidemark_writer` (DDL and
   inserts on the `tidemark` database) and `tidemark_reader` (read-only, `readonly=2`). It doesn't
   contain passwords. It reads them from the **environment of the ClickHouse server process**
   (`<password from_env="TIDEMARK_CLICKHOUSE_WRITER_PASSWORD"/>` and
   `from_env="TIDEMARK_CLICKHOUSE_READER_PASSWORD"`). Copy the two lines from OpenTidemark's env
   file into a file of their own, and load that file into the `clickhouse-server` unit with a
   systemd drop-in:

   ```bash
   sudo install -m 0640 -o root -g clickhouse /dev/null /etc/clickhouse-server/tidemark.env
   sudo grep -E '^TIDEMARK_CLICKHOUSE_(WRITER|READER)_PASSWORD=' /etc/tidemark/tidemark.env \
     | sudo tee /etc/clickhouse-server/tidemark.env >/dev/null
   sudo mkdir -p /etc/systemd/system/clickhouse-server.service.d
   printf '[Service]\nEnvironmentFile=/etc/clickhouse-server/tidemark.env\n' \
     | sudo tee /etc/systemd/system/clickhouse-server.service.d/tidemark.conf >/dev/null
   sudo systemctl daemon-reload
   sudo systemctl restart clickhouse-server
   ```

   The passwords in both files must stay the same. If you change one, change the other and
   restart both services.

3. **Check the users.** Each command should print `1`:

   ```bash
   P=$(sudo grep '^TIDEMARK_CLICKHOUSE_READER_PASSWORD=' /etc/tidemark/tidemark.env | cut -d= -f2)
   clickhouse-client --user tidemark_reader --password "$P" -q 'SELECT 1'
   ```

   The equivalent check for `tidemark_writer` uses `TIDEMARK_CLICKHOUSE_WRITER_PASSWORD`.

OpenTidemark creates the `tidemark` database and its tables itself on the first start. There is
nothing to create by hand.

### 5.3 ClickHouse in Docker instead

You can also run ClickHouse in a container on the same host and publish its native port on
loopback only. Use the same configuration files and pass the passwords as environment variables:

```bash
set -a; . <(sudo grep -E '^TIDEMARK_CLICKHOUSE_(WRITER|READER)_PASSWORD=' /etc/tidemark/tidemark.env); set +a
docker run -d --name tidemark-clickhouse --restart unless-stopped \
  -p 127.0.0.1:9000:9000 --ulimit nofile=262144:262144 \
  -e TIDEMARK_CLICKHOUSE_WRITER_PASSWORD -e TIDEMARK_CLICKHOUSE_READER_PASSWORD \
  -v tidemark-clickhouse-data:/var/lib/clickhouse \
  -v "$PWD/deploy/clickhouse/config.d/tidemark.xml:/etc/clickhouse-server/config.d/tidemark.xml:ro" \
  -v "$PWD/deploy/clickhouse/users.d/tidemark.xml:/etc/clickhouse-server/users.d/tidemark.xml:ro" \
  clickhouse/clickhouse-server:25.8
```

Keep the two XML files somewhere permanent (not in a download directory you will delete). The
ClickHouse image's own configuration makes the server listen on every interface inside the
container, and `-p 127.0.0.1:9000:9000` publishes only the native port, on loopback, so
`localhost:9000` in `tidemark.env` reaches the container and nothing outside the host does. The container's `clickhouse` user (UID 101) must be
able to read the mounted files (see
[ClickHouse doesn't start](#clickhouse-doesnt-start-permission-denied-on-its-configuration)).

### 5.4 Start, logs and firewall

```bash
sudo systemctl start tidemark
systemctl status tidemark
journalctl -u tidemark -f              # OpenTidemark logs JSON lines to the journal
curl -s http://localhost:8080/readyz   # {"status":"ready"} once ClickHouse and the metadata database answer
```

Operator commands need the same configuration as the service. Run them as the `tidemark` user and
point them at the env file:

```bash
sudo -u tidemark tidemark doctor --env-file /etc/tidemark/tidemark.env
```

Open only the ports your clients use (8080 for the UI, plus the ingestion ports from
[section 2](#ports)) in your firewall (for example `ufw` or `firewalld`). Keep ClickHouse's ports
closed to other hosts. For HTTPS, see [section 8](#8-first-sign-in-and-hardening).

## 6. Native install on Windows (service)

The native Windows install runs `tidemark.exe` as the Windows service **Tidemark**. ClickHouse runs
in Docker Desktop with the WSL2 backend, and its native port is published on the loopback address
so that the service can reach it.

### 6.1 Install OpenTidemark

Unpack `tidemark_<version>_windows_amd64.zip` and run the installer in **PowerShell as
Administrator** from the unpacked directory:

```powershell
.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe              # add -OpenFirewall and/or -Start
```

`install.ps1 -Mode Native` does the following:

1. Stops the service if it already exists, and copies `tidemark.exe` to
   `%ProgramFiles%\Tidemark\tidemark.exe`.
2. Writes `%ProgramData%\Tidemark\tidemark.env` from `.env.example`, but only if it doesn't exist
   yet. It contains random ClickHouse passwords, a bootstrap ingest key and
   `TIDEMARK_CLICKHOUSE_ADDR=localhost:9000`. `%ProgramData%\Tidemark` is OpenTidemark's default
   data directory, and `tidemark.env` in it is read at every start.
3. Runs `tidemark.exe service install` if the service doesn't exist yet. This registers the service
   `Tidemark` ("OpenTidemark observability") running `tidemark.exe serve` as the virtual account
   `NT SERVICE\Tidemark`. It uses delayed automatic start, restarts the service after a failure (up
   to three times, 5 s apart, with the failure count reset after a day) and registers the event
   log source `Tidemark`.
4. Gives full control of `%ProgramData%\Tidemark` only to Administrators, SYSTEM and
   `NT SERVICE\Tidemark`, with inherited permissions removed.
5. With `-OpenFirewall`, it adds inbound Windows Firewall rules "Tidemark TCP 8080", 5341, 9200,
   4317 and 4318 for `tidemark.exe`. Without the switch, no ports are opened.
6. With `-Start`, it starts the service. Otherwise, start it later with `Start-Service Tidemark`.

### 6.2 ClickHouse in Docker Desktop

Install Docker Desktop with the WSL2 backend (<https://docs.docker.com/desktop/>). Copy the
`deploy\clickhouse` folder of the archive to a permanent place, for example `C:\tidemark-clickhouse`.
Then start ClickHouse from the same elevated PowerShell. The first line loads the two passwords
from `tidemark.env` into the session, so they don't appear on the command line:

```powershell
foreach ($l in Select-String '^TIDEMARK_CLICKHOUSE_(WRITER|READER)_PASSWORD=' "$env:ProgramData\Tidemark\tidemark.env") { $k, $v = $l.Line -split '=', 2; Set-Item "env:$k" $v }
docker run -d --name tidemark-clickhouse --restart unless-stopped `
  -p 127.0.0.1:9000:9000 --ulimit nofile=262144:262144 `
  -e TIDEMARK_CLICKHOUSE_WRITER_PASSWORD -e TIDEMARK_CLICKHOUSE_READER_PASSWORD `
  -v tidemark-clickhouse-data:/var/lib/clickhouse `
  -v "C:\tidemark-clickhouse\config.d\tidemark.xml:/etc/clickhouse-server/config.d/tidemark.xml:ro" `
  -v "C:\tidemark-clickhouse\users.d\tidemark.xml:/etc/clickhouse-server/users.d/tidemark.xml:ro" `
  clickhouse/clickhouse-server:25.8
```

This is the same image, configuration and users as the Compose setup. The difference is that the
native port is published on `127.0.0.1:9000`, where the service's `localhost:9000` reaches it.
(The ClickHouse image already listens on every interface inside the container, so the
Compose-only `config.d/docker-listen.xml` isn't needed here.)
Don't publish it on other addresses. `--restart unless-stopped` starts the container again
whenever Docker Desktop starts, but Docker Desktop itself only runs when it is started (typically
when you sign in). Until ClickHouse answers, OpenTidemark logs `waiting for clickhouse` and retries
with growing delays, and it eventually stops with an error. If the service has stopped because
ClickHouse was not up, start it again with `Start-Service Tidemark`.

If you change a password in `tidemark.env`, recreate the container
(`docker rm -f tidemark-clickhouse`, then the commands above). The data stays in the
`tidemark-clickhouse-data` volume.

### 6.3 Managing the service

```powershell
Get-Service Tidemark
Start-Service Tidemark; Stop-Service Tidemark; Restart-Service Tidemark
sc.exe qc Tidemark                         # configuration: binary path, account, start type
Invoke-RestMethod http://localhost:8080/readyz
```

`tidemark.exe service start` and `tidemark.exe service stop` do the same as `Start-Service` and
`Stop-Service` (run them as Administrator).

**Logs:** when the service process stops, the reason (or "OpenTidemark stopped") is written to the
Windows **Application** event log with the source `Tidemark`:

```powershell
Get-WinEvent -FilterHashtable @{ LogName = 'Application'; ProviderName = 'Tidemark' } -MaxEvents 20
```

OpenTidemark writes its running log to standard error, which Windows doesn't keep for services. To
watch it while troubleshooting, stop the service and run the binary in the foreground from an
elevated PowerShell. It reads the same `%ProgramData%\Tidemark\tidemark.env`:

```powershell
Stop-Service Tidemark
& "$env:ProgramFiles\Tidemark\tidemark.exe" serve      # Ctrl+C to stop, then Start-Service Tidemark
```

Operator commands such as `doctor` and `backup` also run from an elevated PowerShell with
`& "$env:ProgramFiles\Tidemark\tidemark.exe" <command>` and use the same default data directory
and env file.

## 7. macOS (development only)

macOS is supported for development and for trying OpenTidemark, **not for production**. There is no
macOS release archive. Build the binary from source and run ClickHouse in Docker Desktop. The
quickest way is `make dev` in a source checkout (Go 1.26, Node 22 and Docker are needed):

- It starts ClickHouse as the Compose project `tidemark-dev`, with the development override
  `deploy/docker-compose.dev.yml`. That override publishes ClickHouse only on
  `127.0.0.1:19000` (change it with `DEV_CLICKHOUSE_PORT`), and both ClickHouse users get the
  password `tidemark-dev`.
- It runs `tidemark serve` from source with its data in `.dev/data` in the checkout.
- When `web/package.json` exists, it also starts the web UI's development server (`npm run dev`),
  which proxies `/api` to OpenTidemark.

`make build` produces `bin/tidemark` with the web UI embedded. The same `make dev` workflow works on
Linux, and `.\build.ps1 dev` on Windows. To stop the development ClickHouse:

```bash
TIDEMARK_CLICKHOUSE_WRITER_PASSWORD=tidemark-dev TIDEMARK_CLICKHOUSE_READER_PASSWORD=tidemark-dev \
  docker compose -p tidemark-dev -f deploy/docker-compose.yml -f deploy/docker-compose.dev.yml down
```

Never use `docker-compose.dev.yml` or its fixed password outside development.

## 8. First sign-in and hardening

### First sign-in

Open `http://<host>:8080` and sign in as **admin / admin**. **Change the password right away**
(Settings → Users). While the default password is in use, OpenTidemark logs a warning on every start
and shows a red banner. New passwords need at least 12 characters, must differ from the user name
and must not be a common password. Alternatively, set `TIDEMARK_ADMIN_PASSWORD` (and
`TIDEMARK_ADMIN_USERNAME`) in `.env` or `tidemark.env` **before the first start**. Both are only
used when no users exist yet.

If you lose the admin password, `tidemark admin reset-password --username admin` prints a new
random password, which must be changed at the next sign-in. Run it the way section 10 describes
for your install method.

### Users, teams and what they see

Roles decide what a user may do (viewer, editor, admin); **teams** decide which streams' data a
user sees. Create users under Settings → Users and teams under Settings → Teams: a team has
members and is granted stream names (names can be granted before any data arrives). Admins see
every stream. A user in no team sees every stream too, unless you turn on **Deny by default**
on the Teams page, after which such users see nothing until they are added to a team. API
tokens see the same streams as their owner. Every signed-in user can check what they see on
their account page (Settings → Users).

### Connections and keys

Ingestion always needs a connection key. There is no anonymous ingestion. The first start creates
the connection `bootstrap` from `TIDEMARK_BOOTSTRAP_INGEST_KEY`, which accepts every protocol.
Use it for a first test. For real traffic, create **one connection per application or shipper**
under Connections. You can limit each one by protocol, source network (CIDR), expiry and rate,
and rotate or revoke its key. Keys are shown once. How clients send them is described in
[Sending data](ingestion.md#connections-and-api-keys).

### TLS

Use HTTPS for anything beyond a local test. There are two options.

- **Reverse proxy (recommended with Compose):** terminate TLS in a proxy and keep OpenTidemark's
  ports private (`TM_BIND_ADDR=127.0.0.1` when the proxy runs on the same host). Then set:
  - `TIDEMARK_PUBLIC_URL=https://<your host>`, which makes session cookies `Secure` and turns on
    HSTS;
  - `TIDEMARK_TRUSTED_PROXIES=<proxy address>/32`, so that client addresses are taken from the
    proxy's `X-Forwarded-For`.

  The proxy must pass Server-Sent Events (live tail) without buffering, and HTTP/2 (h2c) for
  OTLP/gRPC. Uploads in Settings → Import can be up to 1 GiB. The HAProxy, nginx and Caddy
  examples in [`deploy/ha/lb/`](../deploy/ha/lb/) show these settings, written for three nodes.
- **Built-in TLS:** set `TIDEMARK_TLS_CERT_FILE` and `TIDEMARK_TLS_KEY_FILE` (PEM). All listeners
  then use TLS 1.2 or newer. Also set `TIDEMARK_PUBLIC_URL` to the `https` address. The files must
  be readable by the user OpenTidemark runs as: `tidemark` natively, `NT SERVICE\Tidemark` on
  Windows, UID 65532 in the container. With the systemd unit's `ProtectSystem=strict`, keep them
  under a readable path such as `/etc/tidemark`.

  In Compose, mount the files into the container with your own override file. Note that the
  bundled health check requests plain `http://127.0.0.1:8080/healthz`, so it reports `unhealthy`
  once TLS is on, and the collector profile then doesn't start. `tidemark healthcheck --url
  https://<name>:8080/healthz` only passes when the container trusts the certificate and resolves
  that name. Prefer a reverse proxy with Compose.

### Exposure

- Expose only what clients need. Put the UI (8080) behind HTTPS and restrict the ingestion ports to
  the networks your applications send from, with the firewall or per-connection CIDR limits.
- Never expose ClickHouse. Compose keeps it on an internal network. For native ClickHouse, see
  [section 5.2](#52-clickhouse-from-the-native-packages).
- `/healthz` and `/readyz` need no authentication and return only a status. `/metrics` is off
  unless `TIDEMARK_METRICS_ENABLED=true`. Protect it with `TIDEMARK_METRICS_TOKEN`.
- Every UI and API response carries a strict Content-Security-Policy, `X-Content-Type-Options:
  nosniff`, `Referrer-Policy: no-referrer`, a restrictive `Permissions-Policy` and same-origin
  `Cross-Origin-Opener-Policy` and `Cross-Origin-Resource-Policy` headers and
  `X-Frame-Options: DENY` (plus `Strict-Transport-Security` when the public URL is https), and the
  API allows no CORS. A reverse proxy should pass these headers through unchanged.
- Keep `.env`, `tidemark.env` and the data directory private. The data directory holds
  `secret.key`, which encrypts notification secrets. Back it up together with the metadata.

More in [SECURITY.md](../SECURITY.md).

## 9. High availability

High availability is optional and uses the same binary. The reference setup in
[`deploy/ha/`](../deploy/ha/README.md) has three Linux servers. Each runs a native OpenTidemark
node, a ClickHouse replica and a ClickHouse Keeper. Metadata lives in an external, highly available
PostgreSQL (`TIDEMARK_META_DRIVER=postgres`). A load balancer spreads the traffic over the nodes and
health-checks each node's `/readyz`. In outline:

- Install ClickHouse with the cluster, Keeper and user files from `deploy/ha/clickhouse/`. Load
  the per-node environment file into `clickhouse-server` with a systemd drop-in, the same mechanism
  as in [section 5.2](#52-clickhouse-from-the-native-packages).
- Install OpenTidemark on every server with `install.sh native` (not started). Then use the per-node
  `tidemark.env` from `deploy/ha/nodes/` and a PostgreSQL DSN file.
- Start one node first, then copy its `secret.key` to the others before you start them. Every node
  must use the same key.
- Keep the clocks NTP-synchronised. `tidemark doctor` reports clock skew.

The README there has the requirements, ports, load balancer examples (HAProxy, nginx, Caddy) and
operating notes (rolling upgrades, backups, losing a server).

## 10. Operations

### Running operator commands

| Install | How to run `tidemark <command>` |
|---|---|
| Compose (server running) | `docker compose exec tidemark tidemark <command>` |
| Compose (server stopped, needed for `restore`) | `docker compose stop tidemark`, then `docker compose run --rm --no-deps tidemark <command>` |
| Native Linux | `sudo -u tidemark tidemark <command> --env-file /etc/tidemark/tidemark.env` |
| Native Windows | `& "$env:ProgramFiles\Tidemark\tidemark.exe" <command>` in an elevated PowerShell |

Every command also accepts the configuration variables and flags from
[Configuration](configuration.md).

| Command | What it does |
|---|---|
| `tidemark version` | Prints the version. |
| `tidemark doctor [--json]` | Checks the configuration, metadata database and pending migrations, each ClickHouse address, the reader user, clock skew, the write-ahead log and free disk space. Exits non-zero when a check fails. Safe while the server runs. |
| `tidemark backup --out <dir> [--meta-only]` | Writes `<dir>/tidemark-backup-<timestamp>/` with the metadata (`meta.json`, plus a native SQLite copy) and `manifest.json`. It also runs a ClickHouse `BACKUP` of the telemetry tables to ClickHouse's `backups` disk. |
| `tidemark restore --from <backup dir> [--force] [--with-clickhouse]` | Restores the metadata with the server stopped. An existing SQLite database needs `--force` and is first saved as `meta-pre-restore-<timestamp>.db` in the backup directory. `--with-clickhouse` also restores the ClickHouse tables, which must be absent or empty. |
| `tidemark migrate [--status \| --dry-run]` | Applies pending migrations, or lists them or prints their SQL without changing anything. |
| `tidemark admin reset-password --username <name> [--password-stdin]` | Resets a password. |
| `tidemark healthcheck [--url URL]` | Exits 0 when the URL (default `http://127.0.0.1:8080/healthz`) answers `200`. |
| `tidemark.exe service install \| uninstall \| start \| stop` | Windows service management (Windows only). |

### Backups

A complete backup has three parts:

- the `tidemark backup` directory;
- the ClickHouse backup it names (`tidemark-<timestamp>` on ClickHouse's `backups` disk, which is
  `/var/lib/clickhouse/backups/` on the ClickHouse server);
- `secret.key` from the data directory.

Copy all three off the server. With Compose, back up to the data volume and copy the results out:

```bash
docker compose exec tidemark tidemark backup --out /data/manual-backups
docker compose cp tidemark:/data/manual-backups ./tidemark-backups
docker compose cp clickhouse:/var/lib/clickhouse/backups ./clickhouse-backups
```

On a native Linux install, create a directory the service user can write to
(`sudo install -d -m 0700 -o tidemark -g tidemark /var/backups/tidemark`) and run
`sudo -u tidemark tidemark backup --env-file /etc/tidemark/tidemark.env --out /var/backups/tidemark`.

To restore, stop OpenTidemark, run `tidemark restore --from <backup dir> --force` (add
`--with-clickhouse` to restore telemetry into an empty ClickHouse), then start OpenTidemark again.

### Upgrading

Upgrades keep your data. Database changes are additive. Before migrating the metadata, OpenTidemark
copies it to `<data dir>/backups` (the last 10 copies are kept), and it refuses to start if that
copy fails. A binary that is older than the database requires refuses to start (downgrade
protection). Read the [changelog](../CHANGELOG.md) first: before 1.0, minor versions may contain
breaking changes. It is also a good idea to run `tidemark backup` first.

- **Docker bundle:** unpack the new release's bundle, copy your `.env` into its directory and run
  `docker compose pull && docker compose up -d` there. The Compose project is always named
  `tidemark`, so the containers are replaced and the volumes are kept. Remove or update a
  `TM_IMAGE` line in `.env` first, if you set one.
- **Compose with the published image (checkout):** set the new tag in `TM_IMAGE`, then run
  `docker compose pull tidemark && docker compose up -d --no-build`.
- **Compose built from source:** update the checkout (for example `git fetch --tags && git checkout
  v<version>`), then run `docker compose up -d --build`.
- **Native Linux:** stop the service, then run the new archive's installer. It replaces the binary
  and the unit and keeps `/etc/tidemark/tidemark.env`:

  ```bash
  sudo systemctl stop tidemark
  sudo ./deploy/install.sh native ./tidemark --start
  ```

- **Native Windows:** run the new archive's installer as Administrator. It stops the service,
  replaces the binary, keeps `tidemark.env` and the service registration, and starts it again with
  `-Start`: `.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe -Start`.
- **ClickHouse:** upgrade it with your package manager, or by changing the image tag. Stay on the
  release line documented in [section 2](#software).

For HA clusters, upgrade one node at a time (see `deploy/ha/README.md`).

### Uninstalling

- **Compose:** `docker compose down` removes the containers and keeps the volumes `ch-data` and
  `tm-data`. `docker compose down -v` also **deletes all data**. Remove `.env` and the
  image if you no longer need them.
- **Native Linux:**

  ```bash
  sudo systemctl disable --now tidemark
  sudo rm /etc/systemd/system/tidemark.service /usr/local/bin/tidemark
  sudo systemctl daemon-reload
  sudo rm -r /var/lib/tidemark /etc/tidemark      # deletes the metadata, keys and settings
  sudo userdel tidemark
  ```

  To remove ClickHouse's OpenTidemark parts, drop the database `tidemark`, delete the files you
  added to `config.d`, `users.d` and `clickhouse-server.service.d`, and remove
  `/etc/clickhouse-server/tidemark.env`. Or uninstall ClickHouse entirely.
- **Native Windows** (elevated PowerShell):

  ```powershell
  Stop-Service Tidemark
  & "$env:ProgramFiles\Tidemark\tidemark.exe" service uninstall
  Get-NetFirewallRule -DisplayName 'Tidemark TCP *' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
  Remove-Item -Recurse "$env:ProgramFiles\Tidemark"
  Remove-Item -Recurse "$env:ProgramData\Tidemark"   # deletes the metadata, keys and settings
  docker rm -f tidemark-clickhouse                   # add: docker volume rm tidemark-clickhouse-data to delete the telemetry
  ```

  `service uninstall` also removes the event log source.

## 11. Troubleshooting

### Checking health

- `GET /healthz` answers `200 {"status":"ok"}` while the process runs.
- `GET /readyz` answers `200 {"status":"ready"}` when ClickHouse and the metadata database are
  reachable. Otherwise it answers `503 {"status":"not ready"}`, or `{"status":"draining"}` during
  shutdown.
- `tidemark doctor` names the failing part. Run it as shown in
  [section 10](#running-operator-commands).
- With `TIDEMARK_METRICS_ENABLED=true`, `/metrics` shows the backlog per signal:
  `tidemark_wal_bytes` (write-ahead log on disk) and `tidemark_pipeline_queue_depth` (events
  accepted but not yet in ClickHouse) grow while ClickHouse is unreachable, and
  `tidemark_pipeline_write_errors_total` counts the failed inserts. Alert when they keep growing.

### ClickHouse doesn't start: permission denied on its configuration

On Linux, ClickHouse may exit right after starting, with one of these errors:

```
Code: 1001, type: std::__1::filesystem::filesystem_error, e.what() = filesystem error: in directory_iterator::directory_iterator(...): Permission denied ["/etc/clickhouse-server/config.d"]
Code: 1000 … Failed to merge config with '/etc/clickhouse-server/config.d/tidemark.xml': Access to file denied
```

The `clickhouse` user can't open the configuration. In the container that is UID 101, and the
files are bind-mounted from `deploy/clickhouse`. A directory it can't traverse gives Code 1001, and
a file it can't read gives Code 1000. To fix it:

- **On SELinux hosts** (`getenforce` prints `Enforcing`), label the files for container use:
  `sudo chcon -Rt container_file_t deploy/clickhouse`.
- **Otherwise,** make the files readable and the directories traversable:
  `chmod -R a+rX deploy/clickhouse`. This is often needed after a checkout made with a restrictive
  `umask`.
- **Native ClickHouse:** the same applies to `/etc/clickhouse-server/config.d` and `users.d`. Keep
  the files owned by `clickhouse` (or readable by it), as in
  [section 5.2](#52-clickhouse-from-the-native-packages).

### Port already in use

`docker compose up` fails with `port is already allocated`, or the native service exits because a
listener can't bind. Another program uses one of the ports in [section 2](#ports). Stop that
program, or move or disable OpenTidemark's listener with `TIDEMARK_HTTP_ADDR`, `TIDEMARK_SEQ_ADDR`,
`TIDEMARK_ES_ADDR`, `TIDEMARK_OTLP_HTTP_ADDR` or `TIDEMARK_OTLP_GRPC_ADDR` (`""` disables it). The
host-side ports in `docker-compose.yml` are fixed. For the native Windows setup, 9000 on the
loopback address must be free for ClickHouse.

### `.env` missing

Compose stops with `run install.sh docker to create deploy/.env`. Run `./install.sh docker
--no-start` (or `.\install.ps1 -Mode Docker -NoStart`) in the bundle directory or `deploy/`.

### A container stays unhealthy or keeps restarting

- Check ClickHouse first with `docker compose logs clickhouse`, because OpenTidemark only starts
  once ClickHouse is healthy. Both containers take the passwords from `.env`. After you edit `.env`,
  run `docker compose up -d` so that both containers are recreated with the new values.
- Then check `docker compose logs tidemark`. Invalid configuration stops the start with a clear
  message. `waiting for clickhouse` means ClickHouse is not reachable yet.
- With built-in TLS, the bundled health check fails (see [TLS](#tls)).

### `401` or `403` when sending data

- `401`: the key is missing or unknown. Check the header your client sends (see
  [Sending data](ingestion.md#connections-and-api-keys)) and copy the key again. The bootstrap key
  is in `.env` or `tidemark.env`.
- `403`: the key is valid but not allowed for this protocol or source address, or it has expired
  or been revoked. Check the connection under Connections. Repeated failures from one address are
  also blocked for a few minutes.
- For `429`, `413` and `503`, see [Sending data](ingestion.md#common-behaviour).

### The Windows service doesn't start or stops

- Read the reason in the Application event log (source `Tidemark`, see
  [section 6.3](#63-managing-the-service)).
- Make sure Docker Desktop runs and `docker ps` shows `tidemark-clickhouse` with
  `127.0.0.1:9000->9000/tcp`.
- Check that `%ProgramData%\Tidemark\tidemark.env` has the passwords the ClickHouse container was
  started with.
- Run the binary in the foreground (section 6.3) to see the full log.
- `tidemark.exe service install` reports `Access is denied` unless it runs as Administrator.

### The Linux service doesn't start

Run `journalctl -u tidemark -e`. Common causes are ClickHouse not running or a password mismatch
between `/etc/tidemark/tidemark.env` and `/etc/clickhouse-server/tidemark.env`. A file referenced
by a `TIDEMARK_*` variable that the `tidemark` user can't read also stops the start.
`sudo -u tidemark tidemark doctor --env-file /etc/tidemark/tidemark.env` shows which check fails.
