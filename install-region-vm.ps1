[CmdletBinding()]
param(
    [string]$VmName = 'AvenRegion',
    [string]$SwitchName = '',
    [string]$VmPath = '',
    [string]$UbuntuIso = '',
    [int]$MemoryGB = 4,
    [int]$Processors = 2,
    [int]$DiskGB = 80,
    [switch]$UseExisting
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
# Windows PowerShell's progress bar makes Invoke-WebRequest downloads (the ISO) many times slower.
$ProgressPreference = 'SilentlyContinue'

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script again from an elevated (Administrator) PowerShell.'
    }
}

function Read-RequiredValue([string]$Prompt, [string]$Current = '') {
    while ($true) {
        $shown = if ($Current) { "$Prompt [$Current]" } else { $Prompt }
        $value = Read-Host $shown
        if ([string]::IsNullOrWhiteSpace($value)) { $value = $Current }
        if (-not [string]::IsNullOrWhiteSpace($value)) { return $value.Trim() }
        Write-Host 'A value is required.' -ForegroundColor Yellow
    }
}

function Read-Setting([string]$Prompt, [int]$Current, [int]$Minimum, [int]$Maximum) {
    while ($true) {
        $raw = Read-Host ("{0} [{1}]" -f $Prompt, $Current)
        if ([string]::IsNullOrWhiteSpace($raw)) { return $Current }
        $number = 0
        if ([int]::TryParse($raw, [ref]$number) -and $number -ge $Minimum -and $number -le $Maximum) { return $number }
        Write-Host ("Enter a number from {0} to {1}." -f $Minimum, $Maximum) -ForegroundColor Yellow
    }
}

function Select-ExternalSwitch([string]$RequestedName) {
    $switches = @(Get-VMSwitch | Where-Object { $_.SwitchType -eq 'External' } | Sort-Object Name)
    if ($switches.Count -eq 0) {
        throw 'No external Hyper-V virtual switch was found. Create one in Hyper-V Manager > Virtual Switch Manager, then run this script again.'
    }
    if ($RequestedName) {
        $selected = $switches | Where-Object Name -eq $RequestedName | Select-Object -First 1
        if (-not $selected) { throw "External virtual switch not found: $RequestedName" }
        return $selected.Name
    }
    if ($switches.Count -eq 1) { return $switches[0].Name }
    Write-Host "`nChoose the external virtual switch for the Ubuntu VM."
    for ($i = 0; $i -lt $switches.Count; $i++) {
        Write-Host ("  {0}) {1} ({2})" -f ($i + 1), $switches[$i].Name, $switches[$i].NetAdapterInterfaceDescription)
    }
    while ($true) {
        $choice = Read-Host 'Number'
        $number = 0
        if ([int]::TryParse($choice, [ref]$number) -and $number -ge 1 -and $number -le $switches.Count) {
            return $switches[$number - 1].Name
        }
        Write-Host 'Enter a number from the list.' -ForegroundColor Yellow
    }
}

function Get-UbuntuIso([string]$RequestedPath) {
    if ($RequestedPath) {
        if (-not (Test-Path -LiteralPath $RequestedPath -PathType Leaf)) { throw "Ubuntu ISO not found: $RequestedPath" }
        return (Resolve-Path -LiteralPath $RequestedPath).Path
    }

    $isoRoot = Join-Path $env:ProgramData 'Aven\downloads'
    New-Item -ItemType Directory -Force -Path $isoRoot | Out-Null
    $releasePage = 'https://releases.ubuntu.com/24.04/'
    Write-Host 'Looking up the Ubuntu Server 24.04 LTS ISO...'
    $listing = (Invoke-WebRequest -Uri $releasePage -UseBasicParsing).Content
    $isoNames = @([regex]::Matches($listing, 'ubuntu-24\.04(?:\.\d+)?-live-server-amd64\.iso') | ForEach-Object { $_.Value } | Sort-Object -Unique)
    if ($isoNames.Count -eq 0) { throw 'Could not find the Ubuntu Server 24.04 LTS ISO on releases.ubuntu.com. Pass an ISO path with -UbuntuIso.' }
    $isoName = $isoNames | Sort-Object {
        if ($_ -match '^ubuntu-24\.04\.(\d+)-') { [int]$Matches[1] } else { 0 }
    } -Descending | Select-Object -First 1
    $isoPath = Join-Path $isoRoot $isoName
    $sumsPath = Join-Path $isoRoot 'SHA256SUMS'

    Write-Host "Downloading $isoName and checking it against Ubuntu's SHA256SUMS."
    Invoke-WebRequest -Uri ($releasePage + 'SHA256SUMS') -OutFile $sumsPath -UseBasicParsing
    $sumLine = Get-Content -LiteralPath $sumsPath | Where-Object { $_ -match ('^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($isoName) + '$') } | Select-Object -First 1
    if (-not $sumLine) { throw "No hash for $isoName in Ubuntu's SHA256SUMS." }
    $expectedHash = ($sumLine.Trim() -split '\s+')[0].ToLowerInvariant()
    if (-not (Test-Path -LiteralPath $isoPath)) {
        Invoke-WebRequest -Uri ($releasePage + $isoName) -OutFile $isoPath -UseBasicParsing
    }
    $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $isoPath).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash) {
        Remove-Item -Force -LiteralPath $isoPath -ErrorAction SilentlyContinue
        throw 'The Ubuntu ISO failed its SHA-256 check and was deleted. Run this script again to download it again.'
    }
    return $isoPath
}

Assert-Administrator
$os = Get-CimInstance Win32_OperatingSystem
if ($os.ProductType -eq 1) { throw 'This installer creates a Hyper-V region VM on Windows Server. On Windows 10/11, use install-server.ps1.' }
if (-not [Environment]::Is64BitOperatingSystem -or (Get-CimInstance Win32_Processor | Select-Object -First 1).Architecture -ne 9) {
    throw 'The Aven region VM installer needs x64 Windows Server.'
}
if (-not (Get-Command Get-VM -ErrorAction SilentlyContinue)) {
    throw "The Hyper-V role is not installed. In an elevated PowerShell, run 'Install-WindowsFeature -Name Hyper-V -IncludeManagementTools -Restart', then run this script again after the restart."
}

$VmName = Read-RequiredValue 'Ubuntu region VM name' $VmName
if ($VmName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'VmName may only contain letters, digits, dots, hyphens and underscores.' }
$createdVM = $false
$existing = Get-VM -Name $VmName -ErrorAction SilentlyContinue
if ($existing -and -not $UseExisting) {
    throw "Hyper-V VM '$VmName' already exists. Re-run with -UseExisting to start that VM without changing its CPU, memory, disk, or automatic start settings, or choose another name."
}
if ($UseExisting -and -not $existing) {
    throw "Hyper-V VM '$VmName' was not found. Omit -UseExisting to create it."
}
if ($existing) {
    Write-Host "`nUsing the existing VM '$VmName'. Its CPU, memory, disk, and automatic start settings are not changed." -ForegroundColor Yellow
    if ($existing.State -ne 'Running') { Start-VM -Name $VmName | Out-Null }
} else {
    $hostMemoryGB = [math]::Floor((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB)
    $hostCores = (Get-CimInstance Win32_Processor | Measure-Object -Property NumberOfLogicalProcessors -Sum).Sum
    if ($hostMemoryGB -lt 4) { throw "The host needs at least 4 GB of memory: 2 GB for the VM and 2 GB for Windows. Host memory: ${hostMemoryGB} GB" }
    if ($MemoryGB -gt ($hostMemoryGB - 2)) { $MemoryGB = [math]::Min(4, $hostMemoryGB - 2) }
    if ($Processors -gt $hostCores) { $Processors = $hostCores }
    $MemoryGB = Read-Setting 'VM memory (GB)' $MemoryGB 2 512
    $Processors = Read-Setting 'VM vCPU count' $Processors 1 128
    $DiskGB = Read-Setting 'VM disk (GB)' $DiskGB 40 4096
    if ($MemoryGB -gt ($hostMemoryGB - 2)) { throw "Cannot give the VM ${MemoryGB} GB: at least 2 GB must stay with the Windows host. Host memory: ${hostMemoryGB} GB" }
    if ($Processors -gt $hostCores) { throw "The VM cannot have more vCPUs than the host has logical processors. Requested: $Processors, host: $hostCores" }
    Write-Host "`nHyper-V Ubuntu region VM"
    Write-Host "  VM name: $VmName"
    Write-Host "  vCPU / memory / disk: $Processors / ${MemoryGB} GB / ${DiskGB} GB"
    $selectedSwitch = Select-ExternalSwitch $SwitchName
    if (-not $VmPath) { $VmPath = Join-Path $env:ProgramData 'Aven\Hyper-V' }
    $vmRoot = Join-Path $VmPath $VmName
    New-Item -ItemType Directory -Force -Path $vmRoot | Out-Null
    $isoPath = Get-UbuntuIso $UbuntuIso
    $diskPath = Join-Path $vmRoot "$VmName.vhdx"

    $diskExisted = Test-Path -LiteralPath $diskPath
    Write-Host 'Creating the Ubuntu VM...'
    try {
        New-VM -Name $VmName -Generation 2 -MemoryStartupBytes ([long]$MemoryGB * 1GB) `
            -NewVHDPath $diskPath -NewVHDSizeBytes ([long]$DiskGB * 1GB) -SwitchName $selectedSwitch -Path $vmRoot | Out-Null
        $createdVM = $true
        Set-VMProcessor -VMName $VmName -Count $Processors
        Set-VMMemory -VMName $VmName -DynamicMemoryEnabled $false
        Set-VMFirmware -VMName $VmName -EnableSecureBoot On -SecureBootTemplate MicrosoftUEFICertificateAuthority
        $dvd = Get-VMDvdDrive -VMName $VmName -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($dvd) {
            Set-VMDvdDrive -VMName $VmName -ControllerNumber $dvd.ControllerNumber -ControllerLocation $dvd.ControllerLocation -Path $isoPath
        } else {
            Add-VMDvdDrive -VMName $VmName -Path $isoPath | Out-Null
            $dvd = Get-VMDvdDrive -VMName $VmName | Select-Object -First 1
        }
        Set-VMFirmware -VMName $VmName -FirstBootDevice $dvd
        Start-VM -Name $VmName | Out-Null
        # Automatic start is applied only after this run has created and started the VM.
        Set-VM -Name $VmName -AutomaticStartAction Start -AutomaticStartDelay 30 -AutomaticStopAction Save
    } catch {
        $failure = $_
        if (Get-VM -Name $VmName -ErrorAction SilentlyContinue) {
            Stop-VM -Name $VmName -TurnOff -Force -ErrorAction SilentlyContinue
            Remove-VM -Name $VmName -Force -ErrorAction SilentlyContinue
        }
        if (-not $diskExisted -and (Test-Path -LiteralPath $diskPath)) {
            Remove-Item -Force -LiteralPath $diskPath -ErrorAction SilentlyContinue
        }
        throw $failure
    }
}

Write-Host "`nVM '$VmName' is running." -ForegroundColor Green
Write-Host "  vmconnect.exe localhost `"$VmName`""
if ($createdVM) {
    Write-Host "`nThe VM starts automatically when Windows Server restarts."
    Write-Host 'Finish the Ubuntu Server installation in the VM console, then run this in a terminal inside the VM:'
    Write-Host '  curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sh'
    Write-Host 'Choose Region server, then enter the main server URL, a join token, and whether to install Docker.'
    Write-Host 'After the region node connects, pair this Windows host from Admin > Server nodes > (node actions) > Connect Hyper-V host.'
    Write-Host 'To serve public app traffic from this VM, forward TCP 80 and 443 from the outside network to it.'
} else {
    Write-Host 'The existing Ubuntu VM is running. If this host is already paired, you can control VM power from the Aven dashboard.'
}
