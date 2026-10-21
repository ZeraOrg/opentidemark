<!-- SPDX-License-Identifier: Apache-2.0 -->
# Installing OpenTidemark on Windows (native service)

This archive contains `tidemark.exe` with the web UI built in, the installer
`deploy\install.ps1` and the ClickHouse configuration in `deploy\clickhouse\`. OpenTidemark runs as
the Windows service **Tidemark**; ClickHouse has no Windows build, so it runs in Docker Desktop
(step 4). The full guide, with every option and troubleshooting, is
<https://github.com/ZeraOrg/opentidemark/blob/main/docs/installation.md>.

Replace `<version>` below with the version in the archive name. Run every command in
**PowerShell as Administrator**.

## 1. Verify the download

Check the signature of `checksums.txt` (with `checksums.txt.minisig` from the same release), then
compare the hash with the line for the archive in it:

```powershell
minisign -Vm checksums.txt -P RWTzWC1xqWTY457VdX92WvxpPeXXOUhpiGzch3af0NEoe9ENK8bEbiDE
Get-FileHash .\tidemark_<version>_windows_amd64.zip -Algorithm SHA256
gh attestation verify .\tidemark_<version>_windows_amd64.zip --repo ZeraOrg/opentidemark   # optional, needs the GitHub CLI
```

The first command checks the [minisign](https://jedisct1.github.io/minisign/) signature of
`checksums.txt` (`checksums.txt.minisig`, same release) and must print
`Trusted comment: opentidemark v<version> checksums.txt`. If the release was signed with the
offline backup key, use
`-P RWSeiFy+aYECqVbvFDNIW//gdsHLox72jDPcY3swGgJm/WQTA+0s9nwP` instead. Both keys are listed in
[SECURITY.md](https://github.com/ZeraOrg/opentidemark/blob/main/SECURITY.md). Releases 0.4.0 and
earlier are not signed.

## 2. Unblock and unpack

Files downloaded from the internet are marked as such, and PowerShell refuses to run a marked
script under the default execution policy. Unblock the archive before unpacking it, so the files
in it are not marked:

```powershell
Unblock-File .\tidemark_<version>_windows_amd64.zip
Expand-Archive .\tidemark_<version>_windows_amd64.zip -DestinationPath .
cd .\tidemark_<version>_windows_amd64
```

If you unpacked it already, run the installer with
`powershell -ExecutionPolicy Bypass -File .\deploy\install.ps1 …` instead of `.\deploy\install.ps1 …`.

`tidemark.exe` is not code-signed. If you start it by hand and Windows SmartScreen shows "Windows
protected your PC", check the hash (step 1), then choose **More info → Run anyway**.

## 3. Install the service

```powershell
.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe              # add -OpenFirewall and/or -Start
```

This copies `tidemark.exe` to `%ProgramFiles%\Tidemark`, writes
`%ProgramData%\Tidemark\tidemark.env` (random ClickHouse passwords, a bootstrap ingest key and
`TIDEMARK_CLICKHOUSE_ADDR=localhost:9000`; an existing file is kept), registers the service
`Tidemark` (virtual account `NT SERVICE\Tidemark`, delayed automatic start, restart on failure)
and restricts `%ProgramData%\Tidemark` to Administrators, SYSTEM and the service account.
`-OpenFirewall` adds inbound rules for TCP 8080, 5341, 9200, 4317 and 4318; `-Start` starts the
service.

It also registers the update helper service `TidemarkUpdate` (LocalSystem, manual start), which
installs a release the Tidemark service has downloaded and verified, restarts the service and
rolls the update back if the new version doesn't come up. The Tidemark service may only start and
query it; it can't change its own binary. Add `-NoSelfUpdate` to leave the helper out (or remove
it).

## 4. ClickHouse in Docker Desktop

Install Docker Desktop with the WSL2 backend (<https://docs.docker.com/desktop/>). Copy the
`deploy\clickhouse` folder to a permanent place, for example `C:\tidemark-clickhouse`, then:

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

ClickHouse's native port is published on `127.0.0.1` only, where the service reaches it. Until
ClickHouse answers, OpenTidemark logs `waiting for clickhouse` and retries; if the service stopped
because ClickHouse was not up yet, start it again with `Start-Service Tidemark`.

## 5. Manage the service and sign in

```powershell
Get-Service Tidemark
Start-Service Tidemark; Stop-Service Tidemark; Restart-Service Tidemark
Invoke-RestMethod http://localhost:8080/readyz
Get-WinEvent -FilterHashtable @{ LogName = 'Application'; ProviderName = 'Tidemark' } -MaxEvents 20
& "$env:ProgramFiles\Tidemark\tidemark.exe" doctor
```

Open `http://localhost:8080`, sign in as **admin / admin** and **change the password right
away**. Then create a connection (one API key per application) and point your clients at
OpenTidemark: <https://github.com/ZeraOrg/opentidemark/blob/main/docs/ingestion.md>.

## 6. Upgrade

Read the changelog first (`CHANGELOG.md`). Unblock and unpack the new archive, then from its
directory:

```powershell
.\deploy\install.ps1 -Mode Native -Binary .\tidemark.exe -Start
```

The installer stops the service, replaces the binary, keeps `tidemark.env`, the service
registration and your data, and starts it again. An install made before the update helper existed
gets it the first time you run the new installer. `& "$env:ProgramFiles\Tidemark\tidemark.exe"
update check` asks GitHub whether a newer release exists.

## 7. Uninstall

```powershell
Stop-Service Tidemark
& "$env:ProgramFiles\Tidemark\tidemark.exe" service uninstall   # also removes TidemarkUpdate
Get-NetFirewallRule -DisplayName 'Tidemark TCP *' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
Remove-Item -Recurse "$env:ProgramFiles\Tidemark"
Remove-Item -Recurse "$env:ProgramData\Tidemark"   # deletes the metadata, keys and settings
docker rm -f tidemark-clickhouse                   # add: docker volume rm tidemark-clickhouse-data to delete the telemetry
```
