$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'These process argument tests require native Windows.' }

# Parse every entry point before any installation is allowed. This catches
# PowerShell-specific syntax on the same runtime that will run the installers.
foreach ($script in Get-ChildItem -LiteralPath $PSScriptRoot -Filter 'windows*.ps1' -File) {
    $tokens = $null; $parseErrors = $null
    $null = [Management.Automation.Language.Parser]::ParseFile($script.FullName, [ref]$tokens, [ref]$parseErrors)
    if ($parseErrors.Count) { throw "PowerShell syntax failure in $($script.Name): $($parseErrors[0].Message)" }
}
. "$PSScriptRoot/windows-test-support.ps1"
. "$PSScriptRoot/windows-webview-runtime-evidence.ps1"

Assert-WebViewRuntimeMetadata -Size 160MB -SignatureStatus Valid -Publisher 'Microsoft Corporation' -ProductVersion '140.0.3485.81'
foreach ($record in @(
    @{ Size = 2MB; SignatureStatus = 'Valid'; Publisher = 'Microsoft Corporation'; ProductVersion = '140.0.1.2' },
    @{ Size = 513MB; SignatureStatus = 'Valid'; Publisher = 'Microsoft Corporation'; ProductVersion = '140.0.1.2' },
    @{ Size = 160MB; SignatureStatus = 'HashMismatch'; Publisher = 'Microsoft Corporation'; ProductVersion = '140.0.1.2' },
    @{ Size = 160MB; SignatureStatus = 'NotTrusted'; Publisher = 'Microsoft Corporation'; ProductVersion = '140.0.1.2' },
    @{ Size = 160MB; SignatureStatus = 'Valid'; Publisher = 'A different publisher'; ProductVersion = '140.0.1.2' },
    @{ Size = 160MB; SignatureStatus = 'Valid'; Publisher = 'Microsoft Corporation'; ProductVersion = '' }
)) {
    $rejected = $false
    try { Assert-WebViewRuntimeMetadata @record } catch { $rejected = $true }
    if (-not $rejected) { throw 'An invalid WebView2 metadata fixture was accepted.' }
}

# Simulate the important failure ordering: the installer has already written a
# product registration, then the native command throws before it can return.
$msiCode = '{F7454F89-2FA4-468F-B3DB-3598A6296CA5}'
$msiRoot = 'C:\Program Files\OmniShare'
$nsisRoot = 'C:\Users\runner\AppData\Local\OmniShare'
function New-CleanupRegistration {
    param([string]$Kind = 'msi')
    if ($Kind -eq 'msi') {
        # A partial MSI registration does not need InstallLocation to identify
        # the exact product; only /x with its known package ProductCode is used.
        return [pscustomobject]@{ name = $msiCode; user = $false; values = [pscustomobject]@{ DisplayName = 'OmniShare' } }
    }
    return [pscustomobject]@{ name = 'OmniShare'; user = $true; values = [pscustomobject]@{
        DisplayName = 'OmniShare'; InstallLocation = '"' + $nsisRoot + '"'; UninstallString = '"' + $nsisRoot + '\uninstall.exe"'
    } }
}
$cleanupState = @{ registrations = @(); reads = 0; uninstalls = 0 }
$readCleanup = { $cleanupState.reads++; return $cleanupState.registrations }
$uninstallCleanup = { $cleanupState.uninstalls++; $cleanupState.registrations = @() }
$cleanupArgs = @{ Kind = 'msi'; ProductCode = $msiCode; InstallDirectory = $msiRoot;
    CleanPreflight = $true; InstallationStarted = $true; ReadRegistrations = $readCleanup; Uninstall = $uninstallCleanup }
try {
    $cleanupState.registrations = @(New-CleanupRegistration)
    throw 'Simulated timeout after product registration'
}
catch { if ($_.Exception.Message -ne 'Simulated timeout after product registration') { throw } }
finally { $cleaned = Invoke-WindowsOwnedInstallationCleanup @cleanupArgs }
if (-not $cleaned -or $cleanupState.uninstalls -ne 1 -or $cleanupState.reads -ne 1) { throw 'Cleanup missed registration written before an installation timeout.' }
if (Invoke-WindowsOwnedInstallationCleanup @cleanupArgs) { throw 'Cleanup is not idempotent after a product has disappeared.' }
if ($cleanupState.uninstalls -ne 1) { throw 'Cleanup uninstalled a missing product.' }
foreach ($missingPrerequisite in @('CleanPreflight', 'InstallationStarted')) {
    $guarded = $cleanupArgs.Clone(); $guarded[$missingPrerequisite] = $false
    $cleanupState.registrations = @(New-CleanupRegistration); $cleanupState.reads = 0
    if (Invoke-WindowsOwnedInstallationCleanup @guarded) { throw 'Cleanup ran without clean preflight and an actual installation attempt.' }
    if ($cleanupState.reads -ne 0 -or $cleanupState.uninstalls -ne 1) { throw 'Cleanup inspected or changed a product it did not attempt to install.' }
}
foreach ($scenario in @('msi-product', 'msi-user', 'msi-path', 'ambiguous', 'nsis-key', 'nsis-machine', 'nsis-path', 'nsis-command')) {
    $kind = if ($scenario.StartsWith('nsis-')) { 'nsis' } else { 'msi' }
    $candidate = New-CleanupRegistration -Kind $kind
    switch ($scenario) {
        'msi-product' { $candidate.name = '{AAAAAAAA-AAAA-AAAA-AAAA-AAAAAAAAAAAA}' }
        'msi-user' { $candidate.user = $true }
        'msi-path' { $candidate.values | Add-Member -NotePropertyName InstallLocation -NotePropertyValue 'C:\Unrelated\OmniShare' }
        'nsis-key' { $candidate.name = 'AnotherProduct' }
        'nsis-machine' { $candidate.user = $false }
        'nsis-path' { $candidate.values.InstallLocation = 'C:\Unrelated\OmniShare' }
        'nsis-command' { $candidate.values.UninstallString += ' /unexpected-command' }
    }
    $cleanupState.registrations = @($candidate)
    if ($scenario -eq 'ambiguous') { $cleanupState.registrations += New-CleanupRegistration }
    $argsForCase = $cleanupArgs.Clone(); $argsForCase.Kind = $kind
    $argsForCase.InstallDirectory = if ($kind -eq 'msi') { $msiRoot } else { $nsisRoot }
    $rejected = $false
    try { $null = Invoke-WindowsOwnedInstallationCleanup @argsForCase } catch { $rejected = $true }
    if (-not $rejected -or $cleanupState.uninstalls -ne 1) { throw "Cleanup accepted an unrelated or ambiguous registration: $scenario." }
}
$cleanupState.registrations = @(New-CleanupRegistration -Kind nsis)
$nsisArgs = $cleanupArgs.Clone(); $nsisArgs.Kind = 'nsis'; $nsisArgs.InstallDirectory = $nsisRoot
if (-not (Invoke-WindowsOwnedInstallationCleanup @nsisArgs) -or $cleanupState.uninstalls -ne 2) { throw 'Exact current-user NSIS ownership did not allow cleanup.' }

$temp = Join-Path ([IO.Path]::GetTempPath()) ('omnishare-native-arguments-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $temp
try {
    $child = Join-Path $temp 'child script 中文 #100%.ps1'
    @'
param([string]$Output, [string]$Value, [int]$ExitCode = 0, [int]$DelaySeconds = 0)
[IO.File]::WriteAllText($Output, $Value)
if ($DelaySeconds) { Start-Sleep -Seconds $DelaySeconds }
exit $ExitCode
'@ | Set-Content -LiteralPath $child -Encoding utf8
    $output = Join-Path $temp 'literal arguments 中文 #100%.txt'
    $literal = 'literal spaces 中文 #100% "quotes" $(not-executed); `not-expanded'
    $powershell = (Get-Process -Id $PID).Path
    $base = @('-NoLogo', '-NoProfile', '-NonInteractive', '-File', $child, '-Output', $output, '-Value', $literal)
    $code = Invoke-WindowsNativeCommand -Executable $powershell -Arguments $base
    if ($code -ne 0 -or [IO.File]::ReadAllText($output) -cne $literal) {
        throw 'Windows native argument forwarding changed spaces, Unicode or shell metacharacters.'
    }
    $code = Invoke-WindowsNativeCommand -Executable $powershell -Arguments ($base + @('-ExitCode', '17'))
    if ($code -ne 17) { throw 'The native process helper hid a failing exit code.' }
    $timedOut = $false
    try { $null = Invoke-WindowsNativeCommand -Executable $powershell -Arguments ($base + @('-DelaySeconds', '30')) -TimeoutSeconds 1 }
    catch { $timedOut = $_.Exception.Message -eq 'The installation command exceeded its bounded timeout.' }
    if (-not $timedOut) { throw 'The native process helper failed to bound a nonresponsive child.' }
}
finally { Remove-Item -LiteralPath $temp -Recurse -Force }
Write-Host 'Windows script parsing, native argument/exit/timeout behavior, exact installation cleanup and offline-runtime refusal checks passed.'
