# SPDX-License-Identifier: Apache-2.0
<#
.SYNOPSIS
  Windows task runner for OpenTidemark (SPEC §6.1, §17.4, §18). Same targets as the Makefile.

.EXAMPLE
  .\build.ps1 check
  .\build.ps1 build
#>
[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('help', 'build', 'web', 'cross', 'dist', 'checksums', 'docs', 'docs-offline', 'test', 'test-integration', 'lint', 'vuln', 'web-check', 'check', 'licences', 'notices', 'dev', 'clean')]
    [string]$Target = 'build'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$Module = 'github.com/ZeraOrg/opentidemark'
$GoLicensesVersion = 'v2.0.1'
# Reviewed licences allowed in shipped Go code (SPEC §19.1), same list as the Makefile.
$GoAllowedLicenses = 'Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0'
$LicenseCheckerVersion = '25.0.1'
$WebAllowedLicenses = 'MIT;BSD-2-Clause;BSD-3-Clause;Apache-2.0;ISC;MPL-2.0;0BSD;CC0-1.0;BlueOak-1.0.0;Python-2.0;CC-BY-4.0;OFL-1.1'
$CrossTargets = @('linux/amd64', 'linux/arm64', 'windows/amd64')
# dist: release archives (SPEC §17.6), version from the VERSION file only; same file lists as the
# Makefile (internal/repotest keeps them equal).
$DistDir = 'dist'
$SemVerPattern = '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
$DistFiles = @('LICENSE', 'NOTICE', 'THIRD_PARTY_NOTICES.md', 'README.md', 'CHANGELOG.md', 'deploy/.env.example', 'deploy/clickhouse/config.d/tidemark.xml', 'deploy/clickhouse/users.d/tidemark.xml')
$DistFilesLinux = @('deploy/install.sh', 'deploy/tidemark.service')
$DistFilesWindows = @('deploy/install.ps1')
# Docker bundle tidemark_<version>_docker: these files with a leading deploy/ removed, INSTALL.md,
# and docker-compose.yml rewritten to run the published image (no build: block).
$DistFilesDocker = @('LICENSE', 'NOTICE', 'README.md', 'CHANGELOG.md', 'deploy/.env.example', 'deploy/install.sh', 'deploy/install.ps1', 'deploy/otel-collector.yaml', 'deploy/clickhouse/config.d/tidemark.xml', 'deploy/clickhouse/config.d/docker-listen.xml', 'deploy/clickhouse/users.d/tidemark.xml')
$DistImage = 'ghcr.io/zeraorg/tidemark'
# The bsdtar of Windows 10+ (it writes zip as well); a GNU tar earlier in PATH (Git Bash) cannot.
$WindowsTar = if ($env:SystemRoot) { Join-Path $env:SystemRoot 'System32\tar.exe' } else { 'tar' }
$DevDir = '.dev'
$DevClickHousePort = if ($env:DEV_CLICKHOUSE_PORT) { $env:DEV_CLICKHOUSE_PORT } else { '19000' }
$DevClickHousePassword = 'tidemark-dev' # loopback-only dev server, same as the Makefile
$HasWeb = Test-Path -LiteralPath 'web/package.json'

function Get-Version {
    if ($env:VERSION) { return $env:VERSION }
    $v = git describe --tags --always --dirty 2>$null
    if ($LASTEXITCODE -eq 0 -and $v) { return $v.Trim() }
    return 'dev'
}

# Invoke runs a native command and stops on a non-zero exit code.
function Invoke {
    param([Parameter(Mandatory)][string]$Command, [string[]]$Arguments = @())
    Write-Host "> $Command $($Arguments -join ' ')"
    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Command failed with exit code $LASTEXITCODE" }
}

# WithEnv sets environment variables for the duration of a script block.
function WithEnv {
    param([Parameter(Mandatory)][hashtable]$Vars, [Parameter(Mandatory)][scriptblock]$Body)
    $saved = @{}
    foreach ($k in $Vars.Keys) {
        $saved[$k] = [Environment]::GetEnvironmentVariable($k)
        [Environment]::SetEnvironmentVariable($k, [string]$Vars[$k])
    }
    try { & $Body } finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
}

function Target-Help {
    'build web cross dist checksums docs docs-offline test test-integration lint vuln web-check check licences notices dev clean' -split ' ' | ForEach-Object { "  $_" }
}

function Target-Web {
    if (-not $HasWeb) { Write-Host 'web: web/package.json not found, skipping (the placeholder page is embedded)'; return }
    Push-Location web
    try { Invoke npm @('ci'); Invoke npm @('run', 'build') } finally { Pop-Location }
    $dist = 'internal/web/assets/dist'
    if (Test-Path -LiteralPath $dist) { Remove-Item -LiteralPath $dist -Recurse -Force }
    Copy-Item -LiteralPath 'web/dist' -Destination $dist -Recurse
}

function Build-Binary {
    param([string]$Os, [string]$Arch, [string]$Out, [string]$Version = (Get-Version))
    $ldflags = "-s -w -X main.version=$Version"
    WithEnv @{ CGO_ENABLED = '0'; GOOS = $Os; GOARCH = $Arch } {
        Invoke go @('build', '-trimpath', '-ldflags', $ldflags, '-o', $Out, './cmd/tidemark')
    }
}

function Target-Build {
    Target-Web
    Build-Binary -Os (go env GOOS) -Arch (go env GOARCH) -Out "bin/tidemark$(go env GOEXE)"
}

function Target-Cross {
    Target-Web
    foreach ($t in $CrossTargets) {
        $os, $arch = $t -split '/'
        $ext = if ($os -eq 'windows') { '.exe' } else { '' }
        Build-Binary -Os $os -Arch $arch -Out "bin/tidemark-$os-$arch$ext"
    }
}

function Get-DistVersion {
    $v = (Get-Content -LiteralPath 'VERSION' -Raw).Trim()
    if ($v -cnotmatch $SemVerPattern) { throw "dist: VERSION must hold a semantic version without a 'v' (got '$v')" }
    return $v
}

# New-DistArchive packs dist/<name> into dist/<name>.zip (Windows) or .tar.gz with the tar.exe
# (bsdtar) that ships with Windows 10 and later.
function New-DistArchive {
    param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][ValidateSet('zip', 'tar.gz')][string]$Format)
    # -C applies to the files that follow; the archive path stays relative to the repository root.
    # Tar archives record owner and group 0 without names, like the Makefile.
    $owner = if ($Format -eq 'tar.gz') { @('--uid', '0', '--gid', '0', '--numeric-owner') } else { @() }
    Invoke $WindowsTar (@($owner) + @('-C', $DistDir, '-a', '-c', '-f', (Join-Path $DistDir "$Name.$Format"), $Name))
}

# Convert-DistCompose rewrites deploy/docker-compose.yml for the Docker bundle: the build: block
# of the tidemark service is removed, its image is pinned to the release image (TM_IMAGE still
# overrides it) and the .env hints drop the deploy/ prefix. Same rules as the Makefile's sed.
function Convert-DistCompose {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Image)
    $pinned = 'image: ${TM_IMAGE:-' + $Image + '}'
    $out = $Text -replace '(?m)^    build:\r?\n(?:      .*\r?\n)+', ''
    $out = $out.Replace('image: ${TM_IMAGE:-tidemark:local}', $pinned).Replace('deploy/.env', '.env')
    if ($out -match '(?m)^\s*build:' -or -not $out.Contains($pinned)) {
        throw "dist: the Docker bundle's docker-compose.yml must not build and must pin $Image"
    }
    return $out
}

# New-DistDockerBundle stages and packs tidemark_<version>_docker (.tar.gz and .zip).
function New-DistDockerBundle {
    param([Parameter(Mandatory)][string]$Version)
    $name = "tidemark_${Version}_docker"
    $stage = Join-Path $DistDir $name
    Write-Host "dist $name"
    foreach ($f in $DistFilesDocker) {
        $dest = Join-Path $stage ($f -replace '^deploy/', '')
        New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dest) | Out-Null
        Copy-Item -LiteralPath $f -Destination $dest
    }
    Copy-Item -LiteralPath 'deploy/install/INSTALL-docker.md' -Destination (Join-Path $stage 'INSTALL.md')
    $compose = Convert-DistCompose -Text ([IO.File]::ReadAllText((Join-Path $PSScriptRoot 'deploy/docker-compose.yml'))) -Image "${DistImage}:$Version"
    [IO.File]::WriteAllText((Join-Path $PSScriptRoot (Join-Path $stage 'docker-compose.yml')), $compose)
    New-DistArchive -Name $name -Format 'tar.gz'
    New-DistArchive -Name $name -Format 'zip'
    Remove-Item -LiteralPath $stage -Recurse -Force
}

# Target-Dist builds the release archives and checksums.txt into dist/ (same layout as make dist).
function Target-Dist {
    $version = Get-DistVersion
    Target-Web
    if (Test-Path -LiteralPath $DistDir) { Remove-Item -LiteralPath $DistDir -Recurse -Force }
    New-Item -ItemType Directory -Path $DistDir | Out-Null
    foreach ($t in $CrossTargets) {
        $os, $arch = $t -split '/'
        $name = "tidemark_${version}_${os}_${arch}"
        $stage = Join-Path $DistDir $name
        $ext = if ($os -eq 'windows') { '.exe' } else { '' }
        $extra = if ($os -eq 'windows') { $DistFilesWindows } else { $DistFilesLinux }
        Write-Host "dist $name"
        Build-Binary -Os $os -Arch $arch -Out (Join-Path $stage "tidemark$ext") -Version $version
        foreach ($f in @($DistFiles) + @($extra)) {
            $dest = Join-Path $stage $f
            New-Item -ItemType Directory -Force -Path (Split-Path -Parent $dest) | Out-Null
            Copy-Item -LiteralPath $f -Destination $dest
        }
        Copy-Item -LiteralPath "deploy/install/INSTALL-$os.md" -Destination (Join-Path $stage 'INSTALL.md')
        New-DistArchive -Name $name -Format $(if ($os -eq 'windows') { 'zip' } else { 'tar.gz' })
        Remove-Item -LiteralPath $stage -Recurse -Force
    }
    New-DistDockerBundle -Version $version
    Target-Checksums
}

# Target-Checksums rewrites dist/checksums.txt for every archive in dist/ (sha256sum format).
function Target-Checksums {
    $lines = Get-ChildItem -LiteralPath $DistDir -Filter 'tidemark_*' | Sort-Object Name | ForEach-Object {
        "$((Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant())  $($_.Name)"
    }
    $sums = Join-Path $PSScriptRoot (Join-Path $DistDir 'checksums.txt')
    [IO.File]::WriteAllText($sums, (($lines -join "`n") + "`n"))
    Get-Content -LiteralPath $sums
}

# Target-Docs builds the documentation site into site/ (SPEC §17.4); install MkDocs first with
# `pip install --require-hashes -r docs-site/requirements.txt`.
function Target-Docs { Invoke mkdocs @('build', '--strict') }

# Target-DocsOffline builds the offline documentation bundle dist/tidemark_<version>_docs.zip from
# mkdocs.offline.yml (SPEC §17.6) and rewrites checksums.txt, like `make docs-offline`.
function Target-DocsOffline {
    $version = Get-DistVersion
    $name = "tidemark_${version}_docs"
    New-Item -ItemType Directory -Force -Path $DistDir | Out-Null
    foreach ($p in @((Join-Path $DistDir $name), (Join-Path $DistDir "$name.zip"))) {
        if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }
    }
    Invoke mkdocs @('build', '--strict', '-f', 'mkdocs.offline.yml', '-d', (Join-Path $DistDir $name))
    New-DistArchive -Name $name -Format 'zip'
    Remove-Item -LiteralPath (Join-Path $DistDir $name) -Recurse -Force
    Target-Checksums
}

# The race detector needs CGO, which Windows CI does not have; SPEC §18 runs plain tests there.
function Target-Test { Invoke go @('test', './...') }

function Target-TestIntegration {
    if (-not $env:TIDEMARK_TEST_CLICKHOUSE_ADDR) {
        Write-Warning 'TIDEMARK_TEST_CLICKHOUSE_ADDR is not set: integration tests will skip (no native ClickHouse on Windows; see SPEC §18).'
    }
    Invoke go @('test', '-tags', 'integration', '-p', '1', '-count=1', './...')
}

function Target-Lint {
    $files = git ls-files '*.go'
    $unformatted = & gofmt -l @files
    if ($unformatted) { throw "gofmt needed:`n$($unformatted -join "`n")" }
    Invoke go @('vet', './...')
    Invoke go @('vet', '-tags', 'integration', './...')
    Invoke golangci-lint @('run', './...')
    Invoke golangci-lint @('run', '--build-tags', 'integration', './...')
    WithEnv @{ GOOS = 'linux' } { Invoke golangci-lint @('run', './...') }
}

function Target-Vuln { Invoke govulncheck @('./...') }

function Target-WebCheck {
    if (-not $HasWeb) { Write-Host 'web-check: web/package.json not found, skipping'; return }
    Push-Location web
    try {
        Invoke npm @('ci')
        foreach ($s in 'lint', 'format:check', 'typecheck', 'test:coverage', 'build', 'check:csp') { Invoke npm @('run', $s) }
        Invoke npm @('audit', '--audit-level=high')
    } finally { Pop-Location }
}

function Target-Check { Target-Lint; Target-Test; Target-Vuln; Target-WebCheck }

# go-licenses recognises the standard library by its GOROOT path, so GOROOT is passed explicitly
# (a toolchain selected through GOTOOLCHAIN lives in the module cache).
function Invoke-GoLicenses {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $tool = "github.com/google/go-licenses/v2@$GoLicensesVersion"
    WithEnv @{ GOROOT = (go env GOROOT) } { & go run $tool @Arguments }
}

# Reviewed licence exceptions (scripts/licence-exceptions.txt, SPEC §19.1): objects with Module,
# Spdx, Sha256 and the Version and Dir of the module in the build list. Same rules as
# scripts/licence-exceptions.sh.
function Get-LicenceExceptions {
    Get-Content -LiteralPath (Join-Path $PSScriptRoot 'scripts/licence-exceptions.txt') |
        Where-Object { $_.Trim() -and -not $_.StartsWith('#') } |
        ForEach-Object {
            $module, $spdx, $sha256 = $_ -split '\s+'
            Invoke go @('mod', 'download', $module)
            $version, $dir = (& go list -m -f '{{.Version}} {{.Dir}}' $module) -split ' ', 2
            if ($LASTEXITCODE -ne 0) { throw "licence exception ${module}: not a dependency" }
            [pscustomobject]@{ Module = $module; Spdx = $spdx; Sha256 = $sha256; Version = $version; Dir = $dir }
        }
}

function Get-GoLicensesIgnore {
    param([Parameter(Mandatory)][AllowEmptyCollection()][object[]]$Exceptions)
    @('--ignore', $Module) + @($Exceptions | ForEach-Object { '--ignore', $_.Module })
}

function Target-Licences {
    $exceptions = @(Get-LicenceExceptions)
    foreach ($e in $exceptions) {
        $got = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $e.Dir 'LICENSE')).Hash.ToLowerInvariant()
        if ($got -ne $e.Sha256) { throw "licence exception $($e.Module) ($($e.Spdx)): LICENSE SHA-256 is $got, reviewed $($e.Sha256)" }
    }
    Invoke-GoLicenses (@('check', './cmd/...') + (Get-GoLicensesIgnore $exceptions) + @("--allowed_licenses=$GoAllowedLicenses"))
    if ($LASTEXITCODE -ne 0) { throw 'go-licenses check failed' }
}

function Target-Notices {
    Target-Licences
    $exceptions = @(Get-LicenceExceptions)
    $report = Invoke-GoLicenses (@('report', './cmd/...') + (Get-GoLicensesIgnore $exceptions) + @('--template', 'scripts/notices.tpl'))
    if ($LASTEXITCODE -ne 0) { throw 'go-licenses report failed' }
    $lines = @($report) + @($exceptions | ForEach-Object { "| $($_.Module) | $($_.Version) | $($_.Spdx) (reviewed exception) |" })
    if ($HasWeb) {
        Push-Location web
        try {
            Invoke npm @('ci', '--ignore-scripts', '--no-audit', '--no-fund')
            $web = & npx --yes "license-checker@$LicenseCheckerVersion" --production --excludePrivatePackages --onlyAllow $WebAllowedLicenses --markdown
            if ($LASTEXITCODE -ne 0) { throw 'license-checker failed' }
        } finally { Pop-Location }
        $lines += @('', '## npm packages (bundled in the web UI)', '') + @($web)
    }
    [IO.File]::WriteAllText((Join-Path $PSScriptRoot 'THIRD_PARTY_NOTICES.md'), (($lines -join "`n") + "`n"))
}

function Target-Dev {
    $compose = @('compose', '-p', 'tidemark-dev', '-f', 'deploy/docker-compose.yml', '-f', 'deploy/docker-compose.dev.yml')
    WithEnv @{
        TIDEMARK_CLICKHOUSE_WRITER_PASSWORD = $DevClickHousePassword
        TIDEMARK_CLICKHOUSE_READER_PASSWORD = $DevClickHousePassword
        TIDEMARK_DEV_CLICKHOUSE_PORT        = $DevClickHousePort
    } { Invoke docker ($compose + @('up', '-d', '--wait', 'clickhouse')) }
    New-Item -ItemType Directory -Force -Path "$DevDir/data" | Out-Null
    $web = $null
    if ($HasWeb) { $web = Start-Process -FilePath 'npm.cmd' -ArgumentList 'run', 'dev' -WorkingDirectory 'web' -PassThru -NoNewWindow }
    try {
        WithEnv @{
            TIDEMARK_DATA_DIR                   = "$DevDir/data"
            TIDEMARK_CLICKHOUSE_ADDR            = "127.0.0.1:$DevClickHousePort"
            TIDEMARK_CLICKHOUSE_WRITER_PASSWORD = $DevClickHousePassword
            TIDEMARK_CLICKHOUSE_READER_PASSWORD = $DevClickHousePassword
        } { Invoke go @('run', '-ldflags', "-X main.version=$(Get-Version)-dev", './cmd/tidemark', 'serve') }
    } finally {
        if ($web -and -not $web.HasExited) { Stop-Process -Id $web.Id -Force }
    }
}

function Target-Clean {
    foreach ($p in 'bin', 'dist', 'internal/web/assets/dist') {
        if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Recurse -Force }
    }
}

switch ($Target) {
    'help' { Target-Help }
    'build' { Target-Build }
    'web' { Target-Web }
    'cross' { Target-Cross }
    'dist' { Target-Dist }
    'checksums' { Target-Checksums }
    'docs' { Target-Docs }
    'docs-offline' { Target-DocsOffline }
    'test' { Target-Test }
    'test-integration' { Target-TestIntegration }
    'lint' { Target-Lint }
    'vuln' { Target-Vuln }
    'web-check' { Target-WebCheck }
    'check' { Target-Check }
    'licences' { Target-Licences }
    'notices' { Target-Notices }
    'dev' { Target-Dev }
    'clean' { Target-Clean }
}
