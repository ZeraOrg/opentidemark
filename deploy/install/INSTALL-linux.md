<!-- SPDX-License-Identifier: Apache-2.0 -->
# Installing OpenTidemark on Linux (native, systemd)

This archive contains the `tidemark` binary with the web UI built in, the installer
`deploy/install.sh`, the systemd unit `deploy/tidemark.service`, the update helper units
`deploy/tidemark-update.service` and `deploy/tidemark-update.path`, and the ClickHouse
configuration in `deploy/clickhouse/`. OpenTidemark stores telemetry in ClickHouse 25.8, which you install
separately (step 3). The full guide, with every option and troubleshooting, is
<https://github.com/ZeraOrg/opentidemark/blob/main/docs/installation.md>.

Replace `<version>` and `<arch>` (`amd64` or `arm64`) below with the values in the archive name.

## 1. Verify the download

In the directory with the archive, `checksums.txt` and `checksums.txt.minisig` from the same
release:

```bash
minisign -Vm checksums.txt -P RWTzWC1xqWTY457VdX92WvxpPeXXOUhpiGzch3af0NEoe9ENK8bEbiDE
sha256sum -c checksums.txt --ignore-missing
gh attestation verify tidemark_<version>_linux_<arch>.tar.gz --repo ZeraOrg/opentidemark   # optional, needs the GitHub CLI
```

The first command checks the [minisign](https://jedisct1.github.io/minisign/) signature of
`checksums.txt` (`checksums.txt.minisig`, same release) and must print
`Trusted comment: opentidemark v<version> checksums.txt`. If the release was signed with the
offline backup key, use
`-P RWSeiFy+aYECqVbvFDNIW//gdsHLox72jDPcY3swGgJm/WQTA+0s9nwP` instead. Both keys are listed in
[SECURITY.md](https://github.com/ZeraOrg/opentidemark/blob/main/SECURITY.md). Releases 0.4.0 and
earlier are not signed.

## 2. Install the service

```bash
tar -xzf tidemark_<version>_linux_<arch>.tar.gz
cd tidemark_<version>_linux_<arch>
sudo ./deploy/install.sh native ./tidemark
```

This creates the system user `tidemark`, `/var/lib/tidemark`, `/etc/tidemark/tidemark.env` (random
ClickHouse passwords, a bootstrap ingest key and `TIDEMARK_CLICKHOUSE_ADDR=localhost:9000`; an
existing file is kept), installs the binary to `/usr/local/bin/tidemark` and enables the hardened
unit `tidemark.service`. Add `--start` to start it right away.

It also installs the update helper: `tidemark-update.path` watches
`/var/lib/tidemark/update/request.json` and starts `tidemark-update.service`, which installs a
release the service has downloaded and verified, as root but without network or capabilities,
and rolls it back if the new version doesn't come up. The service itself can't change its binary.
Add `--no-self-update` to leave the helper out (or remove it).

## 3. Set up ClickHouse

Install ClickHouse 25.8 from the official packages (<https://clickhouse.com/docs/install>), then
give it OpenTidemark's configuration and users and pass it the two passwords from `tidemark.env`.
Run this from the unpacked directory:

```bash
sudo install -m 0440 -o clickhouse -g clickhouse deploy/clickhouse/config.d/tidemark.xml /etc/clickhouse-server/config.d/tidemark.xml
sudo install -m 0440 -o clickhouse -g clickhouse deploy/clickhouse/users.d/tidemark.xml  /etc/clickhouse-server/users.d/tidemark.xml
sudo install -m 0640 -o root -g clickhouse /dev/null /etc/clickhouse-server/tidemark.env
sudo grep -E '^TIDEMARK_CLICKHOUSE_(WRITER|READER)_PASSWORD=' /etc/tidemark/tidemark.env \
  | sudo tee /etc/clickhouse-server/tidemark.env >/dev/null
sudo mkdir -p /etc/systemd/system/clickhouse-server.service.d
printf '[Service]\nEnvironmentFile=/etc/clickhouse-server/tidemark.env\n' \
  | sudo tee /etc/systemd/system/clickhouse-server.service.d/tidemark.conf >/dev/null
sudo systemctl daemon-reload
sudo systemctl restart clickhouse-server
```

`config.d/tidemark.xml` keeps ClickHouse on its default loopback address and switches off its
crash reports. Also give ClickHouse's own `default` user a password, as the ClickHouse guide
describes. ClickHouse in Docker instead of the packages is described in section 5.3 of the
[installation guide](https://github.com/ZeraOrg/opentidemark/blob/main/docs/installation.md#53-clickhouse-in-docker-instead).

## 4. Start, check and sign in

```bash
sudo systemctl start tidemark
systemctl status tidemark
journalctl -u tidemark -f              # logs (JSON lines)
curl -s http://localhost:8080/readyz   # {"status":"ready"} once ClickHouse answers
sudo -u tidemark tidemark doctor --env-file /etc/tidemark/tidemark.env
```

Open `http://<host>:8080`, sign in as **admin / admin** and **change the password right away**.
Then create a connection (one API key per application) and point your clients at OpenTidemark:
<https://github.com/ZeraOrg/opentidemark/blob/main/docs/ingestion.md>. Open only the ports your
clients use in the firewall, and keep ClickHouse's ports closed to other hosts.

## 5. Upgrade

Read the changelog first (`CHANGELOG.md`). Unpack the new archive, then from its directory:

```bash
sudo systemctl stop tidemark
sudo ./deploy/install.sh native ./tidemark --start
```

The installer replaces the binary and the units and keeps `/etc/tidemark/tidemark.env` and your
data. An install made before the update helper existed gets it the first time you run the new
installer. `tidemark update check` asks GitHub whether a newer release exists.

## 6. Uninstall

```bash
sudo systemctl disable --now tidemark
sudo systemctl disable --now tidemark-update.path   # when the update helper is installed
sudo rm -f /etc/systemd/system/tidemark.service /etc/systemd/system/tidemark-update.service \
  /etc/systemd/system/tidemark-update.path /usr/local/bin/tidemark /usr/local/bin/tidemark.previous
sudo systemctl daemon-reload
sudo rm -rf /var/lib/tidemark-update
sudo rm -r /var/lib/tidemark /etc/tidemark      # deletes the metadata, keys and settings
sudo userdel tidemark
```

To remove OpenTidemark's parts from ClickHouse, drop the database `tidemark`, delete the files you
added to `config.d`, `users.d` and `clickhouse-server.service.d`, and remove
`/etc/clickhouse-server/tidemark.env`.
