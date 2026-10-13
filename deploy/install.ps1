# SPDX-License-Identifier: Apache-2.0
<#
.SYNOPSIS
  Tidemark installer for Windows (SPEC §17.2, §17.3).

.DESCRIPTION
  -Mode Docker
      Creates deploy\.env from .env.example on first run (random ClickHouse passwords and a
      bootstrap ingest key), then runs `docker compose up -d --build` unless -NoStart is given.

  -Mode Native -Binary <path to tidemark.exe> [-OpenFirewall] [-Start]
      Run as Administrator. Copies tidemark.exe to %ProgramFiles%\Tidemark, writes
      %ProgramData%\Tidemark\tidemark.env (random secrets on first run), registers the Windows
      service `Tidemark` (delayed auto start, restart on failure, virtual account
      NT SERVICE\Tidemark) and restricts the data directory to Administrators, SYSTEM and the
      service account. Firewall rules for the Tidemark ports are added only with -OpenFirewall.
      ClickHouse runs separately (Docker Desktop) with the users from deploy\clickhouse\users.d and
      the passwords written to tidemark.env.

.EXAMPLE
  .\install.ps1 -Mode Docker
  .\install.ps1 -Mode Native -Binary ..\bin\tidemark.exe -OpenFirewall -Start
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)][ValidateSet('Docker', 'Native')][string]$Mode,
    [string]$Binary,
    [switch]$OpenFirewall,
    [switch]$Start,
    [switch]$NoStart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Placeholder = 'generate-me'
$KeyPrefix = 'tmk_'
$KeyLength = 40
$SecretBytes = 32
$Base62 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'
$ServiceName = 'Tidemark'
$ServiceAccount = 'NT SERVICE\Tidemark'
$FirewallPorts = @(8080, 5341, 9200, 4317, 4318)

# Get-RandomBytes reads from the OS CSPRNG.
function Get-RandomBytes {
    param([int]$Count)
    $bytes = New-Object byte[] $Count
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    return , $bytes
}

# New-HexSecret returns 64 hex characters (256 bits).
function New-HexSecret {
    return (-join ((Get-RandomBytes $SecretBytes) | ForEach-Object { $_.ToString('x2') }))
}

# New-ConnectionKey returns a key in the SPEC §15.3 format: tmk_ + 40 base62 characters.
# Bytes >= 248 are rejected so every character is uniformly distributed (248 = 4 * 62).
function New-ConnectionKey {
    $sb = New-Object System.Text.StringBuilder
    while ($sb.Length -lt $KeyLength) {
        foreach ($b in (Get-RandomBytes 64)) {
            if ($b -lt 248 -and $sb.Length -lt $KeyLength) { [void]$sb.Append($Base62[$b % 62]) }
        }
    }
    return $KeyPrefix + $sb.ToString()
}

# Write-EnvFile renders an env file from the template: every "generate-me" becomes a fresh secret
# and the commented bootstrap key line gets a generated key. Lines are written with LF endings.
function Write-EnvFile {
    param([string]$Template, [string]$Target, [hashtable]$Replace = @{})
    $out = foreach ($line in [IO.File]::ReadAllLines($Template)) {
        if ($line.EndsWith("=$Placeholder")) {
            $line.Substring(0, $line.Length - $Placeholder.Length) + (New-HexSecret)
        } elseif ($line -eq '#TIDEMARK_BOOTSTRAP_INGEST_KEY=') {
            'TIDEMARK_BOOTSTRAP_INGEST_KEY=' + (New-ConnectionKey)
        } elseif ($Replace.ContainsKey($line)) {
            $Replace[$line]
        } else {
            $line
        }
    }
    [IO.File]::WriteAllText($Target, (($out -join "`n") + "`n"))
}

# Protect-Path removes inherited permissions and grants full control to the given principals.
function Protect-Path {
    param([string]$Path, [string[]]$Principals)
    $acl = New-Object System.Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($p in $Principals) {
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($p, 'FullControl',
            'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Install-Docker {
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) { throw 'install: docker is not installed' }
    $envFile = Join-Path $PSScriptRoot '.env'
    if (Test-Path -LiteralPath $envFile) {
        Write-Host "install: keeping existing $envFile"
    } else {
        Write-EnvFile -Template (Join-Path $PSScriptRoot '.env.example') -Target $envFile
        Protect-File -Path $envFile
        Write-Host "install: created $envFile with random ClickHouse passwords and a bootstrap ingest key"
    }
    if ($NoStart) { return }
    Push-Location $PSScriptRoot
    try {
        & docker compose up -d --build
        if ($LASTEXITCODE -ne 0) { throw "docker compose failed with exit code $LASTEXITCODE" }
    } finally { Pop-Location }
    Write-Host 'install: open http://localhost:8080 and sign in as admin / admin, then change the password.'
    Write-Host "install: the bootstrap ingest key is TIDEMARK_BOOTSTRAP_INGEST_KEY in $envFile."
}

# Protect-File limits a file to the current user, Administrators and SYSTEM.
function Protect-File {
    param([string]$Path)
    $acl = New-Object System.Security.AccessControl.FileSecurity
    $acl.SetAccessRuleProtection($true, $false)
    $me = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    foreach ($p in @($me, 'BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM') | Select-Object -Unique) {
        $acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($p, 'FullControl', 'Allow')))
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Assert-Administrator {
    $principal = New-Object System.Security.Principal.WindowsPrincipal([System.Security.Principal.WindowsIdentity]::GetCurrent())
    if (-not $principal.IsInRole([System.Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'install: native mode must run as Administrator'
    }
}

function Install-Native {
    Assert-Administrator
    if (-not $Binary -or -not (Test-Path -LiteralPath $Binary -PathType Leaf)) { throw 'install: -Binary must point to tidemark.exe' }
    $programDir = Join-Path $env:ProgramFiles 'Tidemark'
    $dataDir = Join-Path $env:ProgramData 'Tidemark'
    $exe = Join-Path $programDir 'tidemark.exe'
    New-Item -ItemType Directory -Force -Path $programDir, $dataDir | Out-Null
    if (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue) {
        Stop-Service -Name $ServiceName -ErrorAction SilentlyContinue
    }
    Copy-Item -LiteralPath $Binary -Destination $exe -Force

    $envFile = Join-Path $dataDir 'tidemark.env'
    if (Test-Path -LiteralPath $envFile) {
        Write-Host "install: keeping existing $envFile"
    } else {
        # Native installs talk to a ClickHouse published on this host; the data directory is the default.
        Write-EnvFile -Template (Join-Path $PSScriptRoot '.env.example') -Target $envFile `
            -Replace @{ '#TIDEMARK_CLICKHOUSE_ADDR=clickhouse:9000' = 'TIDEMARK_CLICKHOUSE_ADDR=localhost:9000' }
        Write-Host "install: created $envFile; configure ClickHouse users with the passwords it contains"
    }

    if (-not (Get-Service -Name $ServiceName -ErrorAction SilentlyContinue)) {
        & $exe service install
        if ($LASTEXITCODE -ne 0) { throw "tidemark service install failed with exit code $LASTEXITCODE" }
    }
    # The virtual account exists once the service is registered.
    Protect-Path -Path $dataDir -Principals @('BUILTIN\Administrators', 'NT AUTHORITY\SYSTEM', $ServiceAccount)

    if ($OpenFirewall) {
        foreach ($port in $FirewallPorts) {
            $name = "Tidemark TCP $port"
            if (-not (Get-NetFirewallRule -DisplayName $name -ErrorAction SilentlyContinue)) {
                New-NetFirewallRule -DisplayName $name -Direction Inbound -Protocol TCP -LocalPort $port -Action Allow -Program $exe | Out-Null
            }
        }
        Write-Host "install: opened inbound TCP ports $($FirewallPorts -join ', ')"
    }
    if ($Start) {
        Start-Service -Name $ServiceName
        Write-Host 'install: service started; open http://localhost:8080'
    } else {
        Write-Host "install: start with: Start-Service $ServiceName"
    }
}

switch ($Mode) {
    'Docker' { Install-Docker }
    'Native' { Install-Native }
}
