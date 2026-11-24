[CmdletBinding()]
param(
    [string]$Version = 'latest',
    [string]$PublicUrl = '',
    [string]$BaseDomain = '',
    [string]$AdminPassword = '',
    [switch]$NoStart
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this installer from an elevated PowerShell window.'
    }
}

function Download-Asset($Asset, $Destination) {
    if (-not $Asset) { throw "Release asset was not found: $Destination" }
    Invoke-WebRequest -Uri $Asset.browser_download_url -OutFile $Destination -UseBasicParsing
}

function Get-ExpectedHash([string]$Name, [string]$Checksums) {
    foreach ($line in ($Checksums -split "`n")) {
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Length -eq 2 -and [IO.Path]::GetFileName($parts[1].TrimStart('*')) -eq $Name) {
            return $parts[0].ToLowerInvariant()
        }
    }
    throw "No SHA-256 entry found for $Name."
}

function Assert-Checksum([string]$Path, [string]$Name, [string]$Checksums) {
    $expected = Get-ExpectedHash $Name $Checksums
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Path).Hash.ToLowerInvariant()
    if ($actual -ne $expected) { throw "SHA-256 check failed for $Name." }
}

function Ensure-DockerDesktop {
    $dockerRoot = Join-Path $env:ProgramFiles 'Docker\Docker'
    $dockerPath = Join-Path $dockerRoot 'resources\bin\docker.exe'
    $desktopPath = Join-Path $dockerRoot 'Docker Desktop.exe'
    $perUserDesktopPath = Join-Path $env:LOCALAPPDATA 'Programs\DockerDesktop\Docker Desktop.exe'
    $perUserDockerPath = Join-Path $env:LOCALAPPDATA 'Programs\DockerDesktop\resources\bin\docker.exe'

    if (-not (Test-Path -LiteralPath $dockerPath)) {
        if ((Test-Path -LiteralPath $perUserDesktopPath) -or (Test-Path -LiteralPath $perUserDockerPath)) {
            throw 'A per-user Docker Desktop installation was found. Uninstall it, then rerun this installer from an elevated PowerShell window so Docker Desktop can be installed for all users.'
        }

        $installerPath = Join-Path ([IO.Path]::GetTempPath()) ("Docker-Desktop-Installer-" + [Guid]::NewGuid().ToString('N') + '.exe')
        try {
            Write-Host 'Docker Desktop was not found. Downloading the official installer...'
            Invoke-WebRequest -Uri 'https://desktop.docker.com/win/main/amd64/Docker%20Desktop%20Installer.exe' -OutFile $installerPath -UseBasicParsing
            $signature = Get-AuthenticodeSignature -FilePath $installerPath
            if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch '(?i)Docker') {
                throw 'The Docker Desktop installer signature could not be verified as a valid Docker signature.'
            }

            Write-Host 'Installing Docker Desktop for all users. The installer will not accept the license agreement on your behalf.'
            $installProcess = Start-Process -FilePath $installerPath -ArgumentList @('install', '--quiet') -Wait -PassThru
            if ($installProcess.ExitCode -in @(3010, 1641)) {
                throw 'Docker Desktop setup requires a Windows restart. Restart Windows, then run the Aven installer again.'
            }
            if ($installProcess.ExitCode -ne 0) {
                throw "Docker Desktop setup failed with exit code $($installProcess.ExitCode)."
            }
        } finally {
            Remove-Item -Force -LiteralPath $installerPath -ErrorAction SilentlyContinue
        }
    }

    if (-not (Test-Path -LiteralPath $dockerPath)) {
        throw 'Docker Desktop installation completed, but docker.exe was not found in C:\Program Files\Docker\Docker. Check the Docker Desktop installation and rerun this installer.'
    }
    if (-not (Test-Path -LiteralPath $desktopPath)) {
        throw 'Docker Desktop is installed, but Docker Desktop.exe was not found. Repair Docker Desktop, then rerun this installer.'
    }

    $env:DOCKER_HOST = 'npipe:////./pipe/docker_engine'
    Remove-Item Env:DOCKER_CONTEXT -ErrorAction SilentlyContinue

    if (-not (Get-Process -Name 'Docker Desktop' -ErrorAction SilentlyContinue)) {
        Write-Host 'Starting Docker Desktop...'
        Start-Process -FilePath $desktopPath
    }
    Write-Host 'Waiting for Docker Desktop Linux engine. Accept its license agreement in the Docker Desktop window if prompted.'

    $deadline = (Get-Date).AddMinutes(10)
    $switchAttempted = $false
    $dockerType = ''
    do {
        $dockerOutput = & $dockerPath info --format '{{.OSType}}' 2>$null
        $dockerExitCode = $LASTEXITCODE
        $dockerType = ($dockerOutput | Select-Object -First 1)
        if ($dockerType) { $dockerType = $dockerType.Trim() }

        if ($dockerExitCode -eq 0 -and $dockerType -eq 'linux') {
            return $dockerPath
        }
        if ($dockerExitCode -eq 0 -and $dockerType -eq 'windows' -and -not $switchAttempted) {
            $switchAttempted = $true
            Write-Host 'Switching Docker Desktop to its Linux engine...'
            & $dockerPath desktop engine use linux 2>$null | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Write-Host 'Switch Docker Desktop to Linux containers from its tray menu. The installer will keep waiting.'
            }
        }
        Start-Sleep -Seconds 5
    } while ((Get-Date) -lt $deadline)

    throw "Docker Desktop did not become ready in Linux-container mode within 10 minutes. Accept any license agreement, confirm the Linux engine is running, then rerun this installer. Last reported engine type: '$dockerType'."
}

Assert-Administrator

$os = Get-CimInstance Win32_OperatingSystem
if ($os.ProductType -ne 1) {
    throw 'Aven native hosting currently requires Windows 10/11 with Docker Desktop. Windows Server editions do not support Docker Desktop; use a Linux server for the Docker host.'
}
if (-not [Environment]::Is64BitOperatingSystem -or [int]$os.BuildNumber -lt 19045 -or ([int]$os.BuildNumber -gt 19045 -and [int]$os.BuildNumber -lt 22631)) {
    throw 'Use 64-bit Windows 10 22H2 (build 19045) or Windows 11 23H2 (build 22631) or later.'
}
if ((Get-CimInstance Win32_Processor | Select-Object -First 1).Architecture -ne 9) {
    throw 'This Aven release requires an x64 Windows processor.'
}
if ($PublicUrl) {
    if ($PublicUrl.Contains("`n") -or $PublicUrl.Contains("`r")) { throw 'PublicUrl must be a single-line URL.' }
    $publicUri = $null
    if (-not [Uri]::TryCreate($PublicUrl, [UriKind]::Absolute, [ref]$publicUri) -or $publicUri.Scheme -notin @('http', 'https')) {
        throw 'PublicUrl must be an absolute http:// or https:// URL.'
    }
}
if ($BaseDomain -and $BaseDomain -notmatch '^[A-Za-z0-9.-]+$') { throw 'BaseDomain must contain only letters, digits, dots, and hyphens.' }

$dockerPath = Ensure-DockerDesktop

if ($Version -eq 'latest') {
    $release = Invoke-RestMethod -Uri 'https://api.github.com/repos/turin-dev/aven/releases/latest' -Headers @{ 'User-Agent' = 'Aven-Windows-Installer' }
} else {
    $tag = if ($Version.StartsWith('v')) { $Version } else { "v$Version" }
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/turin-dev/aven/releases/tags/$tag" -Headers @{ 'User-Agent' = 'Aven-Windows-Installer' }
}
$versionTag = $release.tag_name
$assetNames = @(
    'checksums.txt',
    'aven-server-windows-amd64.exe',
    'aven-windows-amd64.exe',
    'aven-agent-linux-amd64',
    'aven-agent-linux-arm64'
)
$assets = @{}
foreach ($name in $assetNames) {
    $assets[$name] = $release.assets | Where-Object { $_.name -eq $name } | Select-Object -First 1
    if (-not $assets[$name]) { throw "Release $versionTag does not contain $name." }
}

$programRoot = Join-Path $env:ProgramFiles 'Aven'
$dataRoot = Join-Path $env:ProgramData 'Aven'
$dataDir = Join-Path $dataRoot 'data'
$envFile = Join-Path $dataRoot 'aven.env'
$downloadRoot = Join-Path ([IO.Path]::GetTempPath()) ("aven-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $downloadRoot, $programRoot, $dataDir | Out-Null
$generatedAdminPassword = $false

try {
    foreach ($name in $assetNames) {
        if ($name -eq 'checksums.txt') { continue }
        Download-Asset $assets[$name] (Join-Path $downloadRoot $name)
    }
    Download-Asset $assets['checksums.txt'] (Join-Path $downloadRoot 'checksums.txt')
    $checksums = Get-Content -Raw -LiteralPath (Join-Path $downloadRoot 'checksums.txt')
    foreach ($name in ($assetNames | Where-Object { $_ -ne 'checksums.txt' })) {
        Assert-Checksum (Join-Path $downloadRoot $name) $name $checksums
    }

    $railpackRelease = Invoke-RestMethod -Uri 'https://api.github.com/repos/railwayapp/railpack/releases/latest' -Headers @{ 'User-Agent' = 'Aven-Windows-Installer' }
    $railpackName = "railpack-$($railpackRelease.tag_name)-x86_64-pc-windows-msvc.zip"
    $railpackAsset = $railpackRelease.assets | Where-Object { $_.name -eq $railpackName } | Select-Object -First 1
    if (-not $railpackAsset) { throw "Railpack Windows release asset was not found: $railpackName" }
    $railpackZip = Join-Path $downloadRoot $railpackName
    Download-Asset $railpackAsset $railpackZip
    $railpackDir = Join-Path $downloadRoot 'railpack'
    Expand-Archive -Force -LiteralPath $railpackZip -DestinationPath $railpackDir
    $railpackExe = Get-ChildItem -LiteralPath $railpackDir -Filter railpack.exe -Recurse | Select-Object -First 1
    if (-not $railpackExe) { throw 'The Railpack archive did not contain railpack.exe.' }

    $service = Get-Service -Name AvenServer -ErrorAction SilentlyContinue
    if ($service) {
        if ($service.Status -ne 'Stopped') { Stop-Service -Name AvenServer -Force }
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }

    foreach ($name in ($assetNames | Where-Object { $_ -ne 'checksums.txt' })) {
        Copy-Item -Force -LiteralPath (Join-Path $downloadRoot $name) -Destination (Join-Path $programRoot $name)
    }
    Copy-Item -Force -LiteralPath $railpackExe.FullName -Destination (Join-Path $programRoot 'railpack.exe')

    if (-not (Test-Path -LiteralPath $envFile)) {
        if (-not $AdminPassword) {
            $bytes = New-Object byte[] 32
            $rng = [Security.Cryptography.RandomNumberGenerator]::Create()
            try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
            $AdminPassword = -join ($bytes | ForEach-Object { $_.ToString('x2') })
            $generatedAdminPassword = $true
        }
        if ($AdminPassword.Length -lt 8 -or $AdminPassword.Contains("`n") -or $AdminPassword.Contains("`r")) {
            throw 'AdminPassword must be at least 8 characters and contain no newline.'
        }
        $settings = @(
            "AVEN_DATA=$dataDir",
            "AVEN_DOCKER_BIN=$dockerPath",
            "AVEN_RAILPACK_BIN=$(Join-Path $programRoot 'railpack.exe')",
            "AVEN_AGENT_BIN_DIR=$programRoot",
            'DOCKER_HOST=npipe:////./pipe/docker_engine',
            'AVEN_LISTEN=127.0.0.1:7070',
            'AVEN_SELF_PROJECT=off',
            "AVEN_ADMIN_PASSWORD=$AdminPassword"
        )
        if ($PublicUrl) { $settings += "AVEN_PUBLIC_URL=$PublicUrl" }
        if ($BaseDomain) { $settings += "AVEN_BASE_DOMAIN=$BaseDomain" }
        $utf8 = [Text.UTF8Encoding]::new($false)
        [IO.File]::WriteAllLines($envFile, $settings, $utf8)
    }

    icacls.exe $dataRoot /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restrict access to the Aven data directory.' }
    icacls.exe $envFile /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restrict access to the Aven settings file.' }

    $serverExe = Join-Path $programRoot 'aven-server-windows-amd64.exe'
    $binaryPath = '"{0}"' -f $serverExe
    if (-not (Get-Service -Name AvenServer -ErrorAction SilentlyContinue)) {
        New-Service -Name AvenServer -BinaryPathName $binaryPath -DisplayName 'Aven Server' -StartupType Automatic | Out-Null
    }
    sc.exe config AvenServer binPath= $binaryPath start= delayed-auto | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure the Aven service executable path.' }
    sc.exe failure AvenServer reset= 86400 actions= restart/5000/restart/15000/restart/30000 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not configure Aven service recovery actions.' }
    sc.exe failureflag AvenServer 1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not enable Aven service recovery actions.' }

    $firewallRules = @(
        @{ Name = 'Aven HTTP'; Protocol = 'TCP'; Port = '80'; Remote = 'Any' },
        @{ Name = 'Aven HTTPS'; Protocol = 'TCP'; Port = '443'; Remote = 'Any' },
        @{ Name = 'Aven HTTPS QUIC'; Protocol = 'UDP'; Port = '443'; Remote = 'Any' },
        @{ Name = 'Aven public route from local proxy'; Protocol = 'TCP'; Port = '7071'; Remote = 'LocalSubnet' },
        @{ Name = 'Aven traces from local containers'; Protocol = 'TCP'; Port = '4318'; Remote = 'LocalSubnet' },
        @{ Name = 'Aven wake from local proxy'; Protocol = 'TCP'; Port = '5381'; Remote = 'LocalSubnet' }
    )
    # Older installers opened the plain-HTTP dashboard port (7070) to the local subnet. The dashboard now
    # listens on loopback only, so drop that rule when upgrading.
    Get-NetFirewallRule -DisplayName 'Aven API from local containers' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    foreach ($rule in $firewallRules) {
        Get-NetFirewallRule -DisplayName $rule.Name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
        New-NetFirewallRule -DisplayName $rule.Name -Direction Inbound -Action Allow -Protocol $rule.Protocol -LocalPort $rule.Port -RemoteAddress $rule.Remote -Profile Any | Out-Null
    }

    if (-not $NoStart) {
        Start-Service -Name AvenServer
        Write-Host "Aven $versionTag installed. Service status: $((Get-Service AvenServer).Status)"
    } else {
        Write-Host "Aven $versionTag installed. Start it with: Start-Service AvenServer"
    }
    if ($generatedAdminPassword) { Write-Host "Initial admin password (save it now): $AdminPassword" }
    Write-Host "Settings: $envFile"
    Write-Host "Data: $dataDir"
    Write-Host 'Logs: Get-Content "$env:ProgramData\Aven\logs\aven-server.log" -Wait'
} finally {
    Remove-Item -Recurse -Force -LiteralPath $downloadRoot -ErrorAction SilentlyContinue
}
