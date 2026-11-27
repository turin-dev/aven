[CmdletBinding()]
param(
    [string]$Server = '',
    [string]$NodeId = '',
    [string]$Token = '',
    [string]$VmName = '',
    [switch]$Run
)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$ProgressPreference = 'SilentlyContinue'

$dataRoot = Join-Path $env:ProgramData 'Aven\HyperV'
$hostDataRoot = Join-Path $dataRoot $NodeId
$configPath = Join-Path $hostDataRoot 'host.json'
$taskName = if ($NodeId) { "Aven-HyperV-$NodeId" } else { 'Aven-HyperV' }
$logPath = Join-Path (Join-Path $env:ProgramData 'Aven\logs') 'aven-hyperv-host.log'

function Write-HostLog([string]$Message) {
    $directory = Split-Path -Parent $logPath
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    Add-Content -LiteralPath $logPath -Value ("{0:u} {1}" -f [DateTime]::UtcNow, $Message) -Encoding UTF8
}

function Assert-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'Run this script from an elevated (Administrator) PowerShell.'
    }
}

function Invoke-AvenApi($Config, [string]$Method, [string]$Path, $Body = $null) {
    $params = @{
        Uri = ($Config.server.TrimEnd('/') + $Path)
        Method = $Method
        Headers = @{ Authorization = "Bearer $($Config.credential)" }
        UseBasicParsing = $true
        TimeoutSec = 20
    }
    if ($null -ne $Body) {
        $params.ContentType = 'application/json'
        $params.Body = ConvertTo-Json -InputObject $Body -Compress -Depth 8
    }
    try {
        $response = Invoke-WebRequest @params
    } catch {
        $status = 0
        try { $status = [int]$_.Exception.Response.StatusCode } catch { }
        if ($status -eq 204) { return $null }
        throw
    }
    if ($response.StatusCode -eq 204 -or [string]::IsNullOrWhiteSpace($response.Content)) { return $null }
    return ($response.Content | ConvertFrom-Json)
}

function Get-VmPowerState([string]$Name) {
    $vm = Get-VM -Name $Name -ErrorAction Stop
    switch -Regex ($vm.State.ToString()) {
        '^Running$' { return 'running' }
        '^Paused$' { return 'paused' }
        '^Off$|^Saved$' { return 'off' }
        default { return 'unknown' }
    }
}

function Wait-VmState([string]$Name, [string]$Expected, [int]$Seconds) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if ((Get-VmPowerState $Name) -eq $Expected) { return $true }
        Start-Sleep -Seconds 2
    } while ([DateTime]::UtcNow -lt $deadline)
    return ((Get-VmPowerState $Name) -eq $Expected)
}

function Invoke-VmPowerAction($Config, $Command) {
    $ok = $false
    $errorMessage = ''
    try {
        $state = Get-VmPowerState $Config.vm_name
        if ($Command.action -eq 'start') {
            if ($state -eq 'off') { Start-VM -Name $Config.vm_name | Out-Null }
            elseif ($state -eq 'paused') { Resume-VM -Name $Config.vm_name | Out-Null }
            if (-not (Wait-VmState $Config.vm_name 'running' 90)) { throw 'VM did not reach the running state within 90 seconds.' }
        } elseif ($Command.action -eq 'stop') {
            if ($state -ne 'off') {
                Stop-VM -Name $Config.vm_name -AsJob -ErrorAction Stop | Out-Null
                if (-not (Wait-VmState $Config.vm_name 'off' 180)) { throw 'Ubuntu did not shut down within 180 seconds. The VM was left running; it was not force powered off.' }
            }
        } else {
            throw "Unsupported Hyper-V action: $($Command.action)"
        }
        $ok = $true
    } catch {
        $errorMessage = $_.Exception.Message
        Write-HostLog ("VM action {0} failed: {1}" -f $Command.action, $errorMessage)
    }

    $powerState = 'unknown'
    try { $powerState = Get-VmPowerState $Config.vm_name } catch { }
    try {
        $resultPath = '/api/hyperv/commands/' + [Uri]::EscapeDataString([string]$Command.id) + '/result'
        Invoke-AvenApi $Config 'POST' $resultPath @{ ok = $ok; error = $errorMessage; power_state = $powerState } | Out-Null
    } catch {
        Write-HostLog ("Could not report VM action result: {0}" -f $_.Exception.Message)
    }
    try { Invoke-AvenApi $Config 'POST' '/api/hyperv/status' @{ power_state = $powerState } | Out-Null } catch { }
}

if (-not $Run) {
    Assert-Administrator
    if (-not (Get-Command Get-VM -ErrorAction SilentlyContinue)) {
        throw 'Hyper-V PowerShell tools are not installed. Install the Hyper-V role and rerun this command.'
    }
    if (-not $Server.StartsWith('https://', [StringComparison]::OrdinalIgnoreCase)) { throw 'Server must be an HTTPS URL.' }
    if ($NodeId -notmatch '^nod_[a-z2-7]+$') { throw 'NodeId is invalid.' }
    if ($Token -notmatch '^[a-fA-F0-9]{64}$') { throw 'Token must be a 64-character pairing token from Aven.' }
    if ($VmName -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') { throw 'VmName is invalid.' }
    try { Get-VM -Name $VmName -ErrorAction Stop | Out-Null } catch { throw "Hyper-V VM '$VmName' was not found on this host." }
    Set-VM -Name $VmName -AutomaticStartAction Start -AutomaticStartDelay 30 -AutomaticStopAction Save

    $serverUri = $null
    if (-not [Uri]::TryCreate($Server, [UriKind]::Absolute, [ref]$serverUri) -or $serverUri.Scheme -ne 'https') { throw 'Server must be an absolute HTTPS URL.' }
    $Server = $Server.TrimEnd('/')
    $body = @{ token = $Token; host_name = $env:COMPUTERNAME }
    $response = Invoke-RestMethod -Uri ($Server + '/api/hyperv/enroll') -Method Post -ContentType 'application/json' `
        -Body (ConvertTo-Json -InputObject $body -Compress) -UseBasicParsing -TimeoutSec 20
    if ($response.node_id -ne $NodeId -or $response.vm_name -ne $VmName -or $response.credential -notmatch '^[a-fA-F0-9]{64}$') {
        throw 'Aven returned a pairing response that does not match this node and VM.'
    }

    New-Item -ItemType Directory -Force -Path $hostDataRoot | Out-Null
    icacls.exe $hostDataRoot /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restrict access to the Hyper-V host credential.' }
    Stop-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
    $config = [ordered]@{ server = $Server; node_id = $NodeId; vm_name = $VmName; credential = $response.credential }
    $utf8 = [Text.UTF8Encoding]::new($false)
    [IO.File]::WriteAllText($configPath, (ConvertTo-Json -InputObject $config -Compress), $utf8)
    icacls.exe $configPath /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Could not restrict access to the Hyper-V host credential file.' }
    $Token = ''

    $installRoot = Join-Path $env:ProgramFiles 'Aven'
    New-Item -ItemType Directory -Force -Path $installRoot | Out-Null
    $installedScript = Join-Path $installRoot 'aven-hyperv-host.ps1'
    if ([IO.Path]::GetFullPath($PSCommandPath) -ne [IO.Path]::GetFullPath($installedScript)) {
        Copy-Item -Force -LiteralPath $PSCommandPath -Destination $installedScript
    }
    $powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    $arguments = "-NoProfile -ExecutionPolicy Bypass -File `"$installedScript`" -Run -NodeId `"$NodeId`""
    $action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
    Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $taskName
    Write-Host "Paired this Hyper-V host with Aven node $NodeId. Aven can now start and stop VM '$VmName'." -ForegroundColor Green
    Write-Host 'The controller runs at Windows startup and connects to the Aven server over HTTPS.'
    Write-Host "Log: $logPath"
    exit 0
}

if ($NodeId -notmatch '^nod_[a-z2-7]+$') { throw 'NodeId is required for the background controller.' }
if (-not (Test-Path -LiteralPath $configPath)) { throw "Hyper-V host configuration is missing: $configPath" }
$config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
if (-not (Get-Command Get-VM -ErrorAction SilentlyContinue)) { throw 'Hyper-V PowerShell tools are not installed.' }
Write-HostLog ("Controller started for node {0}, VM {1}" -f $config.node_id, $config.vm_name)
$lastLoopError = ''
$lastLoopErrorAt = [DateTime]::MinValue

while ($true) {
    try {
        $powerState = Get-VmPowerState $config.vm_name
        Invoke-AvenApi $config 'POST' '/api/hyperv/status' @{ power_state = $powerState } | Out-Null
        $command = Invoke-AvenApi $config 'GET' '/api/hyperv/commands/next'
        if ($null -ne $command) {
            Write-HostLog ("Running VM action {0} ({1})" -f $command.action, $command.id)
            Invoke-VmPowerAction $config $command
        }
        if ($lastLoopError) {
            Write-HostLog 'Controller connection recovered.'
            $lastLoopError = ''
        }
    } catch {
        $message = $_.Exception.Message
        if ($message -ne $lastLoopError -or ([DateTime]::UtcNow - $lastLoopErrorAt).TotalMinutes -ge 1) {
            Write-HostLog ("Controller loop: {0}" -f $message)
            $lastLoopError = $message
            $lastLoopErrorAt = [DateTime]::UtcNow
        }
    }
    Start-Sleep -Seconds 5
}
