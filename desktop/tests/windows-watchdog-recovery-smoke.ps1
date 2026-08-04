[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$EvidenceRoot
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

New-Item -ItemType Directory -Force -Path $EvidenceRoot | Out-Null
$EvidenceRoot = (Resolve-Path $EvidenceRoot).Path
$results = [ordered]@{
    startedAt = (Get-Date).ToString("o")
    phases = @()
    success = $false
}
$desktopProcess = $null

function Add-PhaseResult {
    param(
        [string]$Name,
        [bool]$Passed,
        [hashtable]$Details
    )
    $script:results.phases += [ordered]@{
        name = $Name
        passed = $Passed
        details = $Details
    }
}

function Get-FreePort {
    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        $listener = [System.Net.Sockets.TcpListener]::new(
            [System.Net.IPAddress]::Loopback,
            0
        )
        try {
            $listener.Start()
            return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
        }
        finally {
            $listener.Stop()
        }
    }
    throw "Unable to allocate a free local port."
}

function Get-ProcessesByExecutable {
    param([string]$Executable)

    $normalized = [System.IO.Path]::GetFullPath($Executable)
    return @(Get-CimInstance Win32_Process | Where-Object {
        if (-not $_.ExecutablePath) {
            return $false
        }
        try {
            return ([System.IO.Path]::GetFullPath($_.ExecutablePath) -eq $normalized)
        }
        catch {
            return $false
        }
    })
}

function Get-Json {
    param(
        [string]$Url,
        [int]$TimeoutMilliseconds = 750
    )

    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromMilliseconds($TimeoutMilliseconds)
    try {
        $body = $client.GetStringAsync($Url).GetAwaiter().GetResult()
        return $body | ConvertFrom-Json
    }
    catch {
        return $null
    }
    finally {
        $client.Dispose()
        $handler.Dispose()
    }
}

function Test-OmniSharePort {
    param([int]$Port)
    $payload = Get-Json -Url "http://127.0.0.1:$Port/api/v1/health"
    return $null -ne $payload -and $payload.code -eq 0 -and $payload.data.status -eq 'ok'
}

function Wait-OmniSharePort {
    param(
        [int]$Port,
        [int]$TimeoutSeconds
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (Test-OmniSharePort -Port $Port) {
            return $true
        }
        Start-Sleep -Milliseconds 250
    }
    return $false
}

function Write-JUnit {
    param(
        [bool]$Success,
        [string]$FailureMessage = ""
    )

    $path = Join-Path $EvidenceRoot 'windows-watchdog-recovery.junit.xml'
    if ($Success) {
        $xml = @"
<?xml version="1.0" encoding="utf-8"?>
<testsuite name="OmniShare.WindowsBackendRecovery" tests="2" failures="0">
  <testcase classname="desktop.recovery" name="managed-backend-watchdog-restart" />
  <testcase classname="desktop.discovery" name="single-local-device-summary" />
</testsuite>
"@
    }
    else {
        $escaped = [System.Security.SecurityElement]::Escape($FailureMessage)
        $xml = @"
<?xml version="1.0" encoding="utf-8"?>
<testsuite name="OmniShare.WindowsBackendRecovery" tests="1" failures="1">
  <testcase classname="desktop.recovery" name="watchdog-and-device-summary">
    <failure message="$escaped">$escaped</failure>
  </testcase>
</testsuite>
"@
    }
    Set-Content -Path $path -Value $xml -Encoding UTF8
}

function Stop-TestProcesses {
    param(
        [string]$DesktopExe,
        [string]$BackendExe
    )

    foreach ($path in @($DesktopExe, $BackendExe)) {
        if (-not $path) {
            continue
        }
        Get-ProcessesByExecutable -Executable $path |
            ForEach-Object {
                Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue
            }
    }
}

try {
    $extractRoot = Join-Path $EvidenceRoot 'msi-layout'
    if (-not (Test-Path $extractRoot)) {
        throw "MSI installed layout was not found at $extractRoot. Run windows-runtime-smoke.ps1 first."
    }

    $backendExe = Get-ChildItem -Path $extractRoot -Recurse -File -Filter 'omnishare.exe' |
        Select-Object -First 1
    if (-not $backendExe) {
        throw "Bundled backend executable was not found in the MSI installed layout."
    }

    $backendPath = [System.IO.Path]::GetFullPath($backendExe.FullName)
    $desktopExe = Get-ChildItem -Path $extractRoot -Recurse -File -Filter '*.exe' |
        Where-Object {
            ([System.IO.Path]::GetFullPath($_.FullName) -ne $backendPath) -and
            ($_.Name -notmatch '(?i)uninstall')
        } |
        Sort-Object Length -Descending |
        Select-Object -First 1
    if (-not $desktopExe) {
        throw "Desktop executable was not found in the MSI installed layout."
    }

    Stop-TestProcesses -DesktopExe $desktopExe.FullName -BackendExe $backendExe.FullName
    Start-Sleep -Seconds 2

    $port = Get-FreePort
    $dataDir = Join-Path $EvidenceRoot 'data-watchdog-recovery'
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null
    $env:OMNISHARE_PORT = [string]$port
    $env:OMNISHARE_DATA_DIR = $dataDir

    $desktopProcess = Start-Process -FilePath $desktopExe.FullName -PassThru
    if (-not (Wait-OmniSharePort -Port $port -TimeoutSeconds 35)) {
        throw "Desktop did not start the bundled backend on port $port."
    }

    $desktopBefore = Get-ProcessesByExecutable -Executable $desktopExe.FullName
    $backendBefore = Get-ProcessesByExecutable -Executable $backendExe.FullName
    if ($desktopBefore.Count -ne 1 -or $backendBefore.Count -ne 1) {
        throw "Recovery setup expected one desktop and one backend; found desktop=$($desktopBefore.Count), backend=$($backendBefore.Count)."
    }

    $devicesPayload = Get-Json -Url "http://127.0.0.1:$port/api/v1/devices"
    if ($null -eq $devicesPayload -or $devicesPayload.code -ne 0) {
        throw "Device API did not return a successful response."
    }
    $localDevices = @($devicesPayload.data | Where-Object { $_.is_local })
    if ($localDevices.Count -ne 1) {
        throw "Expected exactly one local product device, found $($localDevices.Count)."
    }
    $localIP = [string]$localDevices[0].ip
    if ($localIP -like '169.254.*' -or $localIP.ToLowerInvariant().StartsWith('fe80:')) {
        throw "Local device summary selected a link-local address: $localIP"
    }

    Add-PhaseResult -Name 'single-local-device-summary' -Passed $true -Details @{
        localDeviceCount = $localDevices.Count
        selectedIP = $localIP
        selectedURL = [string]$localDevices[0].url
    }

    $terminatedBackendPid = $backendBefore[0].ProcessId
    Stop-Process -Id $terminatedBackendPid -Force -ErrorAction Stop

    $offlineDeadline = (Get-Date).AddSeconds(10)
    while ((Get-Date) -lt $offlineDeadline -and (Test-OmniSharePort -Port $port)) {
        Start-Sleep -Milliseconds 250
    }
    if (Test-OmniSharePort -Port $port) {
        throw "The managed backend did not stop on port $port."
    }

    if (-not (Wait-OmniSharePort -Port $port -TimeoutSeconds 45)) {
        throw "Desktop watchdog did not restore the bundled backend on port $port."
    }

    $desktopAfter = Get-ProcessesByExecutable -Executable $desktopExe.FullName
    $backendAfter = Get-ProcessesByExecutable -Executable $backendExe.FullName
    if ($desktopAfter.Count -ne 1 -or $backendAfter.Count -ne 1) {
        throw "Watchdog recovery expected one desktop and one backend; found desktop=$($desktopAfter.Count), backend=$($backendAfter.Count)."
    }
    if ($backendAfter[0].ProcessId -eq $terminatedBackendPid) {
        throw "Watchdog recovery did not create a replacement backend process."
    }

    Add-PhaseResult -Name 'managed-backend-watchdog-restart' -Passed $true -Details @{
        port = $port
        terminatedBackendPid = $terminatedBackendPid
        replacementBackendPid = $backendAfter[0].ProcessId
        desktopProcesses = $desktopAfter.Count
        backendProcesses = $backendAfter.Count
    }

    $results.success = $true
    $results.finishedAt = (Get-Date).ToString("o")
    $results | ConvertTo-Json -Depth 8 |
        Set-Content -Path (Join-Path $EvidenceRoot 'windows-watchdog-recovery.json') -Encoding UTF8
    Write-JUnit -Success $true
    Write-Host "Windows backend watchdog recovery and local-device summary tests passed."
}
catch {
    $message = $_.Exception.ToString()
    $results.success = $false
    $results.finishedAt = (Get-Date).ToString("o")
    $results.failure = $message
    $results | ConvertTo-Json -Depth 8 |
        Set-Content -Path (Join-Path $EvidenceRoot 'windows-watchdog-recovery.json') -Encoding UTF8
    Write-JUnit -Success $false -FailureMessage $message
    throw
}
finally {
    if ($desktopProcess) {
        try {
            $desktopProcess.Refresh()
            if (-not $desktopProcess.HasExited) {
                Stop-Process -Id $desktopProcess.Id -Force -ErrorAction SilentlyContinue
            }
        }
        catch { }
    }
    if ($desktopExe -and $backendExe) {
        Stop-TestProcesses -DesktopExe $desktopExe.FullName -BackendExe $backendExe.FullName
    }
    Remove-Item Env:OMNISHARE_PORT -ErrorAction SilentlyContinue
    Remove-Item Env:OMNISHARE_DATA_DIR -ErrorAction SilentlyContinue
}
