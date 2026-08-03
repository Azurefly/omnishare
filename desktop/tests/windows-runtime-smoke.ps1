[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BundleRoot,

    [string]$EvidenceRoot = "desktop-test-evidence/Windows"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

New-Item -ItemType Directory -Force -Path $EvidenceRoot | Out-Null
$EvidenceRoot = (Resolve-Path $EvidenceRoot).Path
$startedProcesses = [System.Collections.Generic.List[System.Diagnostics.Process]]::new()
$backgroundJobs = [System.Collections.Generic.List[System.Management.Automation.Job]]::new()
$results = [ordered]@{
    startedAt = (Get-Date).ToString("o")
    bundleRoot = $BundleRoot
    phases = @()
    success = $false
}

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

function Get-FreePortBlock {
    param([int]$Count = 12)

    for ($attempt = 0; $attempt -lt 100; $attempt++) {
        $start = Get-Random -Minimum 20000 -Maximum (65000 - $Count)
        $listeners = [System.Collections.Generic.List[System.Net.Sockets.TcpListener]]::new()
        $available = $true
        try {
            for ($offset = 0; $offset -lt $Count; $offset++) {
                $listener = [System.Net.Sockets.TcpListener]::new(
                    [System.Net.IPAddress]::Loopback,
                    $start + $offset
                )
                try {
                    $listener.Start()
                    $listeners.Add($listener)
                }
                catch {
                    $available = $false
                    break
                }
            }
        }
        finally {
            foreach ($listener in $listeners) {
                $listener.Stop()
            }
        }
        if ($available) {
            return $start
        }
    }

    throw "Unable to reserve a free local port block for the Windows runtime test."
}

function Get-HttpBody {
    param(
        [int]$Port,
        [int]$TimeoutMilliseconds = 500
    )

    $handler = [System.Net.Http.HttpClientHandler]::new()
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromMilliseconds($TimeoutMilliseconds)
    try {
        return $client.GetStringAsync("http://127.0.0.1:$Port/").GetAwaiter().GetResult()
    }
    catch {
        return $null
    }
    finally {
        $client.Dispose()
        $handler.Dispose()
    }
}

function Test-PageMarker {
    param(
        [int]$Port,
        [string]$Marker
    )
    $body = Get-HttpBody -Port $Port
    return $null -ne $body -and $body.Contains($Marker)
}

function Test-OmniSharePort {
    param([int]$Port)
    return Test-PageMarker -Port $Port -Marker '<title>OmniShare</title>'
}

function Wait-PageMarker {
    param(
        [int[]]$Ports,
        [string]$Marker,
        [int]$TimeoutSeconds
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        foreach ($port in $Ports) {
            if (Test-PageMarker -Port $port -Marker $Marker) {
                return $port
            }
            if ((Get-Date) -ge $deadline) {
                break
            }
        }
        Start-Sleep -Milliseconds 250
    }
    return $null
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

function Stop-TrackedProcesses {
    foreach ($process in @($script:startedProcesses)) {
        try {
            $process.Refresh()
            if (-not $process.HasExited) {
                Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
                $process.WaitForExit(5000) | Out-Null
            }
        }
        catch { }
    }
    foreach ($job in @($script:backgroundJobs)) {
        try { Stop-Job $job -ErrorAction SilentlyContinue } catch { }
        try { Remove-Job $job -Force -ErrorAction SilentlyContinue } catch { }
    }
}

function Start-Desktop {
    param(
        [string]$DesktopExe,
        [int]$Port,
        [string]$DataDir
    )

    $env:OMNISHARE_PORT = [string]$Port
    $env:OMNISHARE_DATA_DIR = $DataDir
    $process = Start-Process -FilePath $DesktopExe -PassThru
    $script:startedProcesses.Add($process)
    return $process
}

function Start-Backend {
    param(
        [string]$BackendExe,
        [int]$Port,
        [string]$DataDir,
        [string]$StdoutLogPath,
        [string]$StderrLogPath
    )

    New-Item -ItemType Directory -Force -Path $DataDir | Out-Null
    $arguments = @(
        '--no-browser',
        '--listen', '127.0.0.1',
        '--port', [string]$Port,
        '--data-dir', $DataDir
    )
    $process = Start-Process `
        -FilePath $BackendExe `
        -ArgumentList $arguments `
        -RedirectStandardOutput $StdoutLogPath `
        -RedirectStandardError $StderrLogPath `
        -PassThru
    $script:startedProcesses.Add($process)
    return $process
}

function Write-JUnit {
    param(
        [bool]$Success,
        [string]$FailureMessage = ""
    )

    $escaped = [System.Security.SecurityElement]::Escape($FailureMessage)
    if ($Success) {
        $xml = @"
<?xml version="1.0" encoding="utf-8"?>
<testsuite name="OmniShare.WindowsDesktopRuntime" tests="4" failures="0">
  <testcase classname="desktop.runtime" name="installed-layout-and-icon" />
  <testcase classname="desktop.runtime" name="port-conflict-fallback" />
  <testcase classname="desktop.runtime" name="existing-backend-attach" />
  <testcase classname="desktop.runtime" name="single-instance" />
</testsuite>
"@
    }
    else {
        $xml = @"
<?xml version="1.0" encoding="utf-8"?>
<testsuite name="OmniShare.WindowsDesktopRuntime" tests="1" failures="1">
  <testcase classname="desktop.runtime" name="runtime-smoke">
    <failure message="$escaped">$escaped</failure>
  </testcase>
</testsuite>
"@
    }
    Set-Content -Path (Join-Path $EvidenceRoot 'windows-runtime-smoke.junit.xml') -Value $xml -Encoding UTF8
}

try {
    $resolvedBundle = (Resolve-Path $BundleRoot).Path
    $msi = Get-ChildItem -Path $resolvedBundle -Recurse -File -Filter '*.msi' | Select-Object -First 1
    if (-not $msi) {
        throw "No MSI package found below $resolvedBundle"
    }

    $extractRoot = Join-Path $EvidenceRoot 'msi-layout'
    New-Item -ItemType Directory -Force -Path $extractRoot | Out-Null
    $msiLog = Join-Path $EvidenceRoot 'msi-administrative-extract.log'
    $msiArgs = @(
        '/a', "`"$($msi.FullName)`"",
        '/qn',
        "TARGETDIR=`"$extractRoot`"",
        '/L*V', "`"$msiLog`""
    )
    $msiProcess = Start-Process -FilePath 'msiexec.exe' -ArgumentList $msiArgs -PassThru
    if (-not $msiProcess.WaitForExit(60000)) {
        Stop-Process -Id $msiProcess.Id -Force -ErrorAction SilentlyContinue
        throw "MSI administrative extraction exceeded 60 seconds. See $msiLog"
    }
    if ($msiProcess.ExitCode -ne 0) {
        throw "MSI administrative extraction failed with exit code $($msiProcess.ExitCode). See $msiLog"
    }

    $backendExe = Get-ChildItem -Path $extractRoot -Recurse -File -Filter 'omnishare.exe' | Select-Object -First 1
    if (-not $backendExe) {
        throw "Bundled backend executable omnishare.exe was not found in the extracted MSI layout."
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
        throw "Desktop executable was not found in the extracted MSI layout."
    }

    Add-Type -AssemblyName System.Drawing
    $associatedIcon = [System.Drawing.Icon]::ExtractAssociatedIcon($desktopExe.FullName)
    if (-not $associatedIcon -or $associatedIcon.Width -le 0 -or $associatedIcon.Height -le 0) {
        throw "Desktop executable does not expose a valid associated Windows icon."
    }
    $iconEvidence = Join-Path $EvidenceRoot 'desktop-associated-icon.ico'
    $iconStream = [System.IO.File]::Create($iconEvidence)
    try {
        $associatedIcon.Save($iconStream)
    }
    finally {
        $iconStream.Dispose()
        $associatedIcon.Dispose()
    }

    Add-PhaseResult -Name 'installed-layout-and-icon' -Passed $true -Details @{
        msi = $msi.FullName
        desktopExe = $desktopExe.FullName
        backendExe = $backendExe.FullName
        iconEvidence = $iconEvidence
    }

    # Scenario 1: an unrelated HTTP service occupies the requested port.
    $preferredPort = Get-FreePortBlock
    $dummyJob = Start-Job -ArgumentList $preferredPort -ScriptBlock {
        param($Port)
        $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
        $listener.Start()
        try {
            while ($true) {
                $client = $listener.AcceptTcpClient()
                try {
                    $stream = $client.GetStream()
                    $buffer = New-Object byte[] 2048
                    $null = $stream.Read($buffer, 0, $buffer.Length)
                    $body = '<html><title>Occupied By Test</title></html>'
                    $response = "HTTP/1.0 200 OK`r`nContent-Length: $($body.Length)`r`nConnection: close`r`n`r`n$body"
                    $bytes = [System.Text.Encoding]::UTF8.GetBytes($response)
                    $stream.Write($bytes, 0, $bytes.Length)
                }
                finally {
                    $client.Dispose()
                }
            }
        }
        finally {
            $listener.Stop()
        }
    }
    $backgroundJobs.Add($dummyJob)
    if (-not (Wait-PageMarker -Ports @($preferredPort) -Marker 'Occupied By Test' -TimeoutSeconds 10)) {
        throw "The deterministic port-occupier did not become ready on port $preferredPort."
    }

    $fallbackData = Join-Path $EvidenceRoot 'data-port-fallback'
    $desktopPhase1 = Start-Desktop -DesktopExe $desktopExe.FullName -Port $preferredPort -DataDir $fallbackData
    $candidatePorts = (($preferredPort + 1)..($preferredPort + 10))
    $selectedPort = Wait-PageMarker -Ports $candidatePorts -Marker '<title>OmniShare</title>' -TimeoutSeconds 35
    if (-not $selectedPort) {
        throw "Desktop did not start OmniShare on a fallback port after preferred port $preferredPort was occupied."
    }

    $desktopCount = (Get-ProcessesByExecutable -Executable $desktopExe.FullName).Count
    $backendCount = (Get-ProcessesByExecutable -Executable $backendExe.FullName).Count
    if ($desktopCount -ne 1) {
        throw "Expected one desktop process during fallback test; found $desktopCount."
    }
    if ($backendCount -ne 1) {
        throw "Expected one managed backend during fallback test; found $backendCount."
    }

    Add-PhaseResult -Name 'port-conflict-fallback' -Passed $true -Details @{
        preferredPort = $preferredPort
        selectedPort = $selectedPort
        desktopProcesses = $desktopCount
        backendProcesses = $backendCount
    }

    Stop-Process -Id $desktopPhase1.Id -Force -ErrorAction SilentlyContinue
    Get-ProcessesByExecutable -Executable $backendExe.FullName |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Stop-Job $dummyJob -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2

    # Scenario 2: an existing packaged OmniShare backend is already healthy.
    $attachPort = Get-FreePortBlock
    $attachData = Join-Path $EvidenceRoot 'data-existing-backend'
    $standaloneBackendStdout = Join-Path $EvidenceRoot 'standalone-backend.stdout.log'
    $standaloneBackendStderr = Join-Path $EvidenceRoot 'standalone-backend.stderr.log'
    $standaloneBackend = Start-Backend `
        -BackendExe $backendExe.FullName `
        -Port $attachPort `
        -DataDir $attachData `
        -StdoutLogPath $standaloneBackendStdout `
        -StderrLogPath $standaloneBackendStderr
    if (-not (Wait-PageMarker -Ports @($attachPort) -Marker '<title>OmniShare</title>' -TimeoutSeconds 25)) {
        throw "Standalone packaged backend failed to become healthy on port $attachPort."
    }

    $desktopPhase2 = Start-Desktop -DesktopExe $desktopExe.FullName -Port $attachPort -DataDir $attachData
    Start-Sleep -Seconds 5
    if (-not (Test-OmniSharePort -Port $attachPort)) {
        throw "Desktop lost connectivity to the existing backend on port $attachPort."
    }

    $backendCountAfterAttach = (Get-ProcessesByExecutable -Executable $backendExe.FullName).Count
    if ($backendCountAfterAttach -ne 1) {
        throw "Desktop spawned a duplicate backend while attaching; expected 1 backend, found $backendCountAfterAttach."
    }

    Add-PhaseResult -Name 'existing-backend-attach' -Passed $true -Details @{
        port = $attachPort
        backendProcesses = $backendCountAfterAttach
        standaloneBackendPid = $standaloneBackend.Id
        backendStdout = $standaloneBackendStdout
        backendStderr = $standaloneBackendStderr
    }

    # Scenario 3: repeated launch forwards activation to the existing desktop instance.
    $secondLaunch = Start-Process -FilePath $desktopExe.FullName -PassThru
    $startedProcesses.Add($secondLaunch)
    if (-not $secondLaunch.WaitForExit(10000)) {
        $secondLaunch.Refresh()
    }
    Start-Sleep -Seconds 2
    $desktopCountAfterSecondLaunch = (Get-ProcessesByExecutable -Executable $desktopExe.FullName).Count
    if ($desktopCountAfterSecondLaunch -ne 1) {
        throw "Single-instance enforcement failed; expected 1 desktop process, found $desktopCountAfterSecondLaunch."
    }
    $secondLaunch.Refresh()
    if (-not $secondLaunch.HasExited -and $secondLaunch.Id -ne $desktopPhase2.Id) {
        throw "Second desktop launch remained running instead of forwarding to the existing instance."
    }

    Add-PhaseResult -Name 'single-instance' -Passed $true -Details @{
        desktopProcesses = $desktopCountAfterSecondLaunch
        originalDesktopPid = $desktopPhase2.Id
        secondLaunchExited = $secondLaunch.HasExited
    }

    $results.success = $true
    $results.finishedAt = (Get-Date).ToString("o")
    $results | ConvertTo-Json -Depth 8 |
        Set-Content -Path (Join-Path $EvidenceRoot 'windows-runtime-smoke.json') -Encoding UTF8
    Write-JUnit -Success $true
    Write-Host "Windows installed-layout runtime smoke test passed. Evidence: $EvidenceRoot"
}
catch {
    $message = $_.Exception.ToString()
    $results.success = $false
    $results.finishedAt = (Get-Date).ToString("o")
    $results.failure = $message
    $results | ConvertTo-Json -Depth 8 |
        Set-Content -Path (Join-Path $EvidenceRoot 'windows-runtime-smoke.json') -Encoding UTF8
    Write-JUnit -Success $false -FailureMessage $message
    Write-Error $message
    exit 1
}
finally {
    Stop-TrackedProcesses
    Remove-Item Env:OMNISHARE_PORT -ErrorAction SilentlyContinue
    Remove-Item Env:OMNISHARE_DATA_DIR -ErrorAction SilentlyContinue
}
