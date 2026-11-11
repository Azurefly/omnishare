[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BundleRoot,
    [Parameter(Mandatory = $true)][string]$EvidenceRoot,
    [string]$Version = '',
    [ValidateSet('all', 'msi', 'nsis')][string]$Installer = 'all',
    [switch]$AllowLocalInstall
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows -or $PSVersionTable.PSVersion.Major -lt 7) { throw 'Windows PowerShell 7 or newer is required.' }
if ($env:GITHUB_ACTIONS -ne 'true' -and -not $AllowLocalInstall) {
    throw 'This test installs and uninstalls both real packages. Run on a disposable CI machine, or explicitly pass -AllowLocalInstall on a clean test machine.'
}
. "$PSScriptRoot/windows-test-support.ps1"
. "$PSScriptRoot/windows-webview-runtime-evidence.ps1"
$repo = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
if (-not $Version) { $Version = (Get-Content -Raw (Join-Path $repo 'VERSION')).Trim() }
$bundle = (Resolve-Path -LiteralPath $BundleRoot).Path
$null = New-Item -ItemType Directory -Force -Path $EvidenceRoot
$evidence = (Resolve-Path -LiteralPath $EvidenceRoot).Path
$tempBase = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
$runId = [Guid]::NewGuid().ToString('N')
$private = Join-Path $tempBase "omnishare-installed-$runId"
$null = New-Item -ItemType Directory -Path $private
$settingsDirectory = Join-Path ([Environment]::GetFolderPath('ApplicationData')) 'com.azurefly.omnishare'
$settingsFile = Join-Path $settingsDirectory 'omnishare-desktop.json'
$defaultData = Join-Path $env:USERPROFILE '.omnishare'
$oldEnvironment = @{}
foreach ($name in @('OMNISHARE_PORT', 'OMNISHARE_DATA_DIR', 'OMNISHARE_BACKEND_BIN', 'OMNISHARE_DESKTOP_LOG_DIR', 'WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS', 'WEBVIEW2_USER_DATA_FOLDER', 'WEBVIEW2_BROWSER_EXECUTABLE_FOLDER', 'NO_PROXY', 'no_proxy')) {
    $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$result = [ordered]@{
    schema_version = 1; platform = 'windows'; version = $Version; success = $false
    started_at = (Get-Date).ToUniversalTime().ToString('o'); installers = @()
    gui_method = 'visible native HWND + actual WebView2 CDP'; quit_method = 'native Ctrl+Q accelerator'
    cold_runtime_install_verified = $false; offline_runtime_install_verified = $false
    non_admin_install_verified = $false
}

function Get-OmniShareRegistrations {
    $roots = @('HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall')
    foreach ($root in $roots) {
        if (-not (Test-Path -LiteralPath $root)) { continue }
        foreach ($key in Get-ChildItem -LiteralPath $root) {
            $item = Get-ItemProperty -LiteralPath $key.PSPath
            if ($item.PSObject.Properties.Name -contains 'DisplayName' -and $item.DisplayName -eq 'OmniShare') {
                [pscustomobject]@{ key = $key.PSPath; name = $key.PSChildName; user = $root.StartsWith('HKCU:'); values = $item }
            }
        }
    }
}

function Get-WebViewRuntimeInventory {
    $guid = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
    foreach ($root in @('HKCU:\Software\Microsoft\EdgeUpdate\Clients', 'HKLM:\Software\Microsoft\EdgeUpdate\Clients', 'HKLM:\Software\WOW6432Node\Microsoft\EdgeUpdate\Clients')) {
        $entry = Get-ItemProperty -LiteralPath (Join-Path $root $guid) -Name pv -ErrorAction SilentlyContinue
        if ($entry -and $entry.pv -and $entry.pv -ne '0.0.0.0') {
            [pscustomobject]@{ scope = $(if ($root.StartsWith('HKCU:')) { 'user' } else { 'machine' }); version = [string]$entry.pv }
        }
    }
}

function Read-MsiProperty {
    param([string]$Path, [string]$Property)
    if ($Property -notin @('ProductCode', 'ProductName')) { throw 'Unexpected MSI property request.' }
    $engine = New-Object -ComObject WindowsInstaller.Installer
    $database = $null; $view = $null; $record = $null
    try {
        $database = $engine.OpenDatabase($Path, 0)
        $view = $database.OpenView("SELECT ``Value`` FROM ``Property`` WHERE ``Property``='$Property'")
        $view.Execute(); $record = $view.Fetch()
        if (-not $record) { throw "MSI is missing $Property." }
        return [string]$record.StringData(1)
    }
    finally {
        if ($view) { $view.Close() }
        foreach ($object in @($record, $view, $database, $engine)) {
            if ($null -ne $object) { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($object) }
        }
    }
}

function Get-InstalledShortcuts {
    param([string]$Executable)
    $shell = New-Object -ComObject WScript.Shell
    try {
        foreach ($folder in @('Programs', 'CommonPrograms', 'DesktopDirectory', 'CommonDesktopDirectory')) {
            $root = [Environment]::GetFolderPath($folder)
            if (-not $root -or -not (Test-Path -LiteralPath $root)) { continue }
            foreach ($file in Get-ChildItem -LiteralPath $root -Recurse -File -Filter 'OmniShare.lnk' -ErrorAction SilentlyContinue) {
                $shortcut = $shell.CreateShortcut($file.FullName)
                try {
                    if ($shortcut.TargetPath -and [IO.Path]::GetFullPath($shortcut.TargetPath) -eq $Executable) { $file.FullName }
                }
                finally { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($shortcut) }
            }
        }
    }
    finally { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($shell) }
}

function Get-DataManifest {
    param([string]$Directory)
    $manifest = [ordered]@{}
    foreach ($file in Get-ChildItem -LiteralPath $Directory -Recurse -File | Sort-Object FullName) {
        $manifest[[IO.Path]::GetRelativePath($Directory, $file.FullName)] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash
    }
    return ($manifest | ConvertTo-Json -Compress)
}

function Invoke-CoreFeatures {
    param([int]$Port, [string]$Data, [string]$State, [string]$Phase, [string]$Report)
    & python (Join-Path $repo 'scripts/test-installed-features.py') --base-url "http://127.0.0.1:$Port" --data-dir $Data --version $Version --state $State --phase $Phase --report $Report
    if ($LASTEXITCODE -ne 0) { throw "Installed core functionality failed during $Phase. See the sanitized feature report." }
    $record = Get-Content -Raw -LiteralPath $Report | ConvertFrom-Json
    if (-not $record.success) { throw "Installed core functionality did not report success during $Phase." }
}

function Start-InstalledGui {
    param([string]$Executable, [int]$DebugPort, [string]$Profile)
    $env:WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS = "--remote-debugging-port=$DebugPort --remote-debugging-address=127.0.0.1"
    $env:WEBVIEW2_USER_DATA_FOLDER = $Profile
    $process = Start-Process -FilePath $Executable -PassThru
    $script:currentDesktop = $process
    $null = Assert-WindowsVisibleWindow -Process $process
    Assert-WindowsDebugListener -Port $DebugPort -DesktopProcessId $process.Id
    return $process
}

function Invoke-InstalledGuiProbe {
    param([int]$DebugPort, [int]$Port, [string]$Prefix, [switch]$FirstRun, [string]$State)
    $arguments = @((Join-Path $PSScriptRoot 'windows-webview-probe.mjs'), '--debug-port', [string]$DebugPort,
        '--app-port', [string]$Port, '--report', "$Prefix.json", '--screenshot', "$Prefix.png")
    if ($FirstRun) { $arguments += '--first-run' }
    if ($State) { $arguments += @('--state', $State) }
    & node @arguments
    if ($LASTEXITCODE -ne 0) { throw 'Actual installed WebView GUI validation failed. See the sanitized GUI report.' }
}

function Assert-OwnedBackend {
    param([string]$Executable, [int]$DesktopProcessId)
    $children = @(Get-WindowsProcessesAtPath -Executable $Executable)
    if ($children.Count -ne 1 -or $children[0].ParentProcessId -ne $DesktopProcessId) {
        throw 'The installed desktop must own exactly one bundled backend.'
    }
    return [int]$children[0].ProcessId
}

function Stop-InstalledGuiNormally {
    param([Diagnostics.Process]$Process, [string]$Backend, [int]$Port)
    Invoke-WindowsNormalQuit -Process $Process
    Wait-WindowsCondition -Description 'the owned backend and listening port to exit after Quit' -Seconds 15 -Condition {
        @(Get-WindowsProcessesAtPath -Executable $Backend).Count -eq 0 -and
        @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue).Count -eq 0
    }
    $script:currentDesktop = $null
}

function Uninstall-Candidate {
    param([string]$Kind, [string]$ProductCode, [string]$InstallDirectory, [string]$Log)
    if ($Kind -eq 'msi') {
        $code = Invoke-WindowsNativeCommand -Executable 'msiexec.exe' -Arguments @('/x', $ProductCode, '/qn', '/norestart', '/L*V', $Log)
    }
    else {
        $uninstaller = Join-Path $InstallDirectory 'uninstall.exe'
        if (-not (Test-Path -LiteralPath $uninstaller -PathType Leaf)) { throw 'The exact installed NSIS uninstaller is missing.' }
        $code = Invoke-WindowsNativeCommand -Executable $uninstaller -Arguments @('/S')
    }
    if ($code -notin @(0, 3010)) { throw "$Kind uninstall returned $code." }
    Wait-WindowsCondition -Description "$Kind uninstall registration and application files to disappear" -Seconds 90 -Condition {
        @(Get-OmniShareRegistrations).Count -eq 0 -and
        @(Get-ChildItem -LiteralPath $InstallDirectory -Filter '*.exe' -File -ErrorAction SilentlyContinue).Count -eq 0
    }
    return $code
}

$currentDesktop = $null
try {
    if (@(Get-OmniShareRegistrations).Count -or (Test-Path -LiteralPath $settingsFile) -or (Test-Path -LiteralPath $defaultData)) {
        throw 'A pre-existing OmniShare installation, desktop settings file or default data directory was found; refusing to alter user state.'
    }
    # Recheck signatures and unchanged cache/package bytes before any installer
    # executes. The external post-build evidence cannot be substituted or stale.
    $result.webview_runtime = Assert-WindowsWebViewRuntimeEvidence -BundleRoot $bundle -EvidencePath (Join-Path $evidence 'webview-offline-runtime.json')
    foreach ($name in @('OMNISHARE_PORT', 'OMNISHARE_DATA_DIR', 'OMNISHARE_BACKEND_BIN', 'WEBVIEW2_BROWSER_EXECUTABLE_FOLDER')) {
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    $env:NO_PROXY = '127.0.0.1,localhost'; $env:no_proxy = '127.0.0.1,localhost'
    $env:OMNISHARE_DESKTOP_LOG_DIR = Join-Path $private 'native-logs'
    $kinds = if ($Installer -eq 'all') { @('msi', 'nsis') } else { @($Installer) }
    foreach ($kind in $kinds) {
        $case = [ordered]@{ kind = $kind; success = $false; checks = [ordered]@{}; features = [ordered]@{} }
        $result.installers += $case
        $caseEvidence = Join-Path $evidence $kind
        $casePrivate = Join-Path $private $kind
        $null = New-Item -ItemType Directory -Force -Path $caseEvidence, $casePrivate
        $pattern = if ($kind -eq 'msi') { '*.msi' } else { '*-setup.exe' }
        $packages = @(Get-ChildItem -LiteralPath $bundle -Recurse -File -Filter $pattern)
        if ($packages.Count -ne 1) { throw "Expected exactly one $kind package, found $($packages.Count)." }
        $package = $packages[0]
        $case.asset = @{ name = $package.Name; size = $package.Length; sha256 = (Get-FileHash -LiteralPath $package.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
        $case.webview_runtime_before = @(Get-WebViewRuntimeInventory)
        $case.runtime_download_mode = 'offlineInstaller'
        $case.cold_runtime_install_verified = $false
        $case.offline_runtime_install_verified = $false
        $case.non_admin_install_verified = $false
        $installRoot = if ($kind -eq 'msi') { Join-Path ([Environment]::GetFolderPath('ProgramFiles')) 'OmniShare' } else { Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'OmniShare' }
        if (Test-Path -LiteralPath $installRoot) { throw "The expected $kind install directory already exists; refusing to overwrite it." }
        $productCode = ''; $installationStarted = $false; $cleanInstallPreflight = $false
        $desktopExe = ''; $backendExe = ''; $dataOwned = $false
        $data = if ($kind -eq 'msi') { $defaultData } else { Join-Path $casePrivate '用户 数据 # %' }
        $marker = Join-Path $data ".install-smoke-$runId"
        try {
            if ($kind -eq 'msi') {
                $productCode = Read-MsiProperty -Path $package.FullName -Property ProductCode
                if ($productCode -notmatch '^\{[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}\}$' -or (Read-MsiProperty -Path $package.FullName -Property ProductName) -ne 'OmniShare') { throw 'Unexpected MSI product identity.' }
            }
            # Repeat per package, immediately before mutation. Mark the attempt
            # before awaiting the command: timeout may happen after registration.
            if (@(Get-OmniShareRegistrations).Count -or (Test-Path -LiteralPath $installRoot)) { throw 'Install preflight is no longer clean; refusing to overwrite another installation.' }
            $cleanInstallPreflight = $true
            $installationStarted = $true
            if ($kind -eq 'msi') {
                $installCode = Invoke-WindowsNativeCommand -Executable 'msiexec.exe' -Arguments @('/i', $package.FullName, '/qn', '/norestart', '/L*V', (Join-Path $caseEvidence 'install.log'))
                $case.installation = @{ method = 'msiexec /i'; scope = 'machine'; product_code = $productCode; exit_code = $installCode }
            }
            else {
                $installCode = Invoke-WindowsNativeCommand -Executable $package.FullName -Arguments @('/S')
                $case.installation = @{ method = 'NSIS /S'; scope = 'currentUser'; exit_code = $installCode }
            }
            if ($installCode -notin @(0, 3010)) { throw "$kind actual installation returned $installCode." }
            $registrations = @(Get-OmniShareRegistrations)
            if ($registrations.Count -ne 1) { throw 'Actual installation did not create exactly one product registration.' }
            $registration = $registrations[0]
            if (($kind -eq 'msi' -and ($registration.name -ne $productCode -or $registration.user)) -or
                ($kind -eq 'nsis' -and -not $registration.user)) { throw 'Installer registration has an unexpected identity or user scope.' }
            $registeredPath = [IO.Path]::GetFullPath(([string]$registration.values.InstallLocation).Trim('"').TrimEnd('\'))
            if ($registeredPath -ne [IO.Path]::GetFullPath($installRoot)) { throw 'Actual default install path does not match the declared installer scope.' }
            $desktops = @(Get-ChildItem -LiteralPath $installRoot -File -Filter '*.exe' | Where-Object { $_.Name -notin @('uninstall.exe', 'omnishare.exe') })
            $backends = @(Get-ChildItem -LiteralPath $installRoot -Recurse -File -Filter 'omnishare.exe')
            if ($desktops.Count -ne 1 -or $backends.Count -ne 1 -or $desktops[0].FullName -eq $backends[0].FullName) { throw 'Installed desktop/backend layout is ambiguous or incomplete.' }
            $desktopExe = $desktops[0].FullName; $backendExe = $backends[0].FullName
            if (@(Get-WindowsProcessesAtPath $desktopExe).Count -or @(Get-WindowsProcessesAtPath $backendExe).Count) { throw 'The installer unexpectedly started the application during silent installation.' }
            $shortcuts = @(Get-InstalledShortcuts -Executable $desktopExe)
            if ($shortcuts.Count -eq 0) { throw 'No installed OmniShare shortcut targets the actual desktop executable.' }
            $case.installation.path = $installRoot; $case.installation.shortcuts = $shortcuts
            $case.checks.actual_installation = $true; $case.checks.registration_and_shortcut = $true
            $case.webview_runtime_after = @(Get-WebViewRuntimeInventory)
            $case.cold_runtime_install_verified = $case.webview_runtime_before.Count -eq 0 -and $case.webview_runtime_after.Count -gt 0
            if (Test-Path -LiteralPath $data) { throw 'The isolated installation data directory already exists.' }
            $null = New-Item -ItemType Directory -Path $data
            Set-Content -LiteralPath $marker -Value $runId -NoNewline
            $dataOwned = $true
            if ($kind -eq 'msi') { Remove-Item Env:OMNISHARE_DATA_DIR -ErrorAction SilentlyContinue }
            else { $env:OMNISHARE_DATA_DIR = $data }
            Remove-Item Env:OMNISHARE_PORT -ErrorAction SilentlyContinue
            if (Test-Path -LiteralPath $settingsFile) { throw 'First-run settings were unexpectedly present before GUI startup.' }
            $port = Get-WindowsFreePort; $debugPort = Get-WindowsFreePort
            while ($debugPort -eq $port) { $debugPort = Get-WindowsFreePort }
            $first = Start-InstalledGui -Executable $desktopExe -DebugPort $debugPort -Profile (Join-Path $casePrivate 'webview-first')
            Invoke-InstalledGuiProbe -DebugPort $debugPort -Port $port -FirstRun -Prefix (Join-Path $caseEvidence 'first-run-gui')
            $firstBackend = Assert-OwnedBackend -Executable $backendExe -DesktopProcessId $first.Id
            $case.checks.first_run_gui = $true; $case.checks.native_window_visible = $true
            $case.data_directory_mode = $(if ($kind -eq 'msi') { 'user-profile-default' } else { 'explicit-unicode-spaces-hash-percent' })
            $case.checks.data_directory = $true
            $state = Join-Path $casePrivate 'private-feature-state.json'
            Invoke-CoreFeatures -Port $port -Data $data -State $state -Phase exercise -Report (Join-Path $caseEvidence 'features-exercise.json')
            $case.features.exercise = Get-Content -Raw -LiteralPath (Join-Path $caseEvidence 'features-exercise.json') | ConvertFrom-Json
            $case.checks.core_exercise = $true
            Stop-InstalledGuiNormally -Process $first -Backend $backendExe -Port $port
            $case.checks.owned_backend_exited = $true
            $secondDebugPort = Get-WindowsFreePort
            $second = Start-InstalledGui -Executable $desktopExe -DebugPort $secondDebugPort -Profile (Join-Path $casePrivate 'webview-restart')
            Invoke-InstalledGuiProbe -DebugPort $secondDebugPort -Port $port -State $state -Prefix (Join-Path $caseEvidence 'restart-gui')
            $secondBackend = Assert-OwnedBackend -Executable $backendExe -DesktopProcessId $second.Id
            if ($secondBackend -eq $firstBackend) { throw 'Restart reused a backend process that should have exited.' }
            Invoke-CoreFeatures -Port $port -Data $data -State $state -Phase verify -Report (Join-Path $caseEvidence 'features-verify.json')
            $case.features.verify = Get-Content -Raw -LiteralPath (Join-Path $caseEvidence 'features-verify.json') | ConvertFrom-Json
            $case.checks.restart_gui_and_persistence = $true
            Stop-InstalledGuiNormally -Process $second -Backend $backendExe -Port $port
            $case.checks.second_normal_exit = $true
            if ($kind -eq 'msi') {
                & "$PSScriptRoot/windows-runtime-smoke.ps1" -BundleRoot $bundle -EvidenceRoot $evidence -DesktopExecutable $desktopExe -BackendExecutable $backendExe -PrivateRoot (Join-Path $casePrivate 'runtime-cases')
                & "$PSScriptRoot/windows-watchdog-recovery-smoke.ps1" -EvidenceRoot $evidence -DesktopExecutable $desktopExe -BackendExecutable $backendExe -PrivateRoot (Join-Path $casePrivate 'watchdog-case')
                $case.checks.attach_conflict_watchdog = $true
            }
            $beforeUninstall = Get-DataManifest -Directory $data
            $case.uninstall_exit_code = Uninstall-Candidate -Kind $kind -ProductCode $productCode -InstallDirectory $installRoot -Log (Join-Path $caseEvidence 'uninstall.log')
            if ($shortcuts | Where-Object { Test-Path -LiteralPath $_ }) { throw 'An installed application shortcut remained after uninstall.' }
            if ((Get-DataManifest -Directory $data) -ne $beforeUninstall) { throw 'Uninstall changed or removed existing business data.' }
            if (@(Get-WindowsProcessesAtPath $desktopExe).Count -or @(Get-WindowsProcessesAtPath $backendExe).Count) { throw 'An application process remained after uninstall.' }
            $case.checks.uninstalled = $true; $case.checks.business_data_preserved = $true
            $case.success = $true
        }
        finally {
            # Emergency cleanup cannot make a failed normal-Quit assertion pass.
            foreach ($executable in @($desktopExe, $backendExe)) {
                if ($executable) { Get-WindowsProcessesAtPath $executable | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } }
            }
            $currentDesktop = $null
            try {
                $case.cleanup_uninstalled = Invoke-WindowsOwnedInstallationCleanup -Kind $kind -ProductCode $productCode -InstallDirectory $installRoot `
                    -CleanPreflight $cleanInstallPreflight -InstallationStarted $installationStarted `
                    -ReadRegistrations { Get-OmniShareRegistrations } `
                    -Uninstall { Uninstall-Candidate -Kind $kind -ProductCode $productCode -InstallDirectory $installRoot -Log (Join-Path $caseEvidence 'cleanup-uninstall.log') }
            }
            catch { $case.cleanup_uninstall_failed = $true }
            if ($dataOwned -and (Test-Path -LiteralPath $marker) -and (Get-Content -Raw -LiteralPath $marker) -eq $runId) {
                Remove-Item -LiteralPath $data -Recurse -Force
            }
            # The file was absent at preflight and only this test's boot created it.
            if (Test-Path -LiteralPath $settingsFile) { Remove-Item -LiteralPath $settingsFile -Force }
            Remove-Item Env:OMNISHARE_DATA_DIR -ErrorAction SilentlyContinue
            Remove-Item Env:OMNISHARE_PORT -ErrorAction SilentlyContinue
        }
    }
    $result.success = $true
    Write-Host 'Actual MSI/NSIS install, visible WebView GUI, normal Quit, restart persistence and uninstall checks passed.'
}
catch {
    $result.failure = $_.Exception.Message
    throw
}
finally {
    $result.finished_at = (Get-Date).ToUniversalTime().ToString('o')
    $result | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath (Join-Path $evidence 'windows-install-smoke.json') -Encoding utf8
    $failures = if ($result.success) { 0 } else { 1 }
    $failure = if ($result.success) { '' } else { '<failure message="Actual Windows installation or usability check failed; see sanitized JSON report." />' }
    "<?xml version=`"1.0`" encoding=`"utf-8`"?><testsuite name=`"OmniShare.ActualWindowsInstallation`" tests=`"1`" failures=`"$failures`"><testcase classname=`"desktop.installation`" name=`"actual-install-gui-restart-uninstall`">$failure</testcase></testsuite>" |
        Set-Content -LiteralPath (Join-Path $evidence 'windows-install-smoke.junit.xml') -Encoding utf8
    foreach ($entry in $oldEnvironment.GetEnumerator()) { [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process') }
    # Never upload private state, backend data, WebView profiles or native logs.
    if (Test-Path -LiteralPath $private) { Remove-Item -LiteralPath $private -Recurse -Force -ErrorAction SilentlyContinue }
}
