[CmdletBinding()]
param(
    [Alias('BundleRoot')][string]$RuntimeBundleRoot = '',
    [Alias('EvidenceRoot')][string]$RuntimeEvidenceRoot = ''
)
Set-StrictMode -Version Latest

function Assert-WebViewRuntimeMetadata {
    param([long]$Size, [string]$SignatureStatus, [string]$Publisher, [string]$ProductVersion)
    # A complete x64 offline payload, not the tiny online bootstrapper. Future
    # growth beyond this generous bound must be reviewed rather than executed.
    if ($Size -lt 64MB -or $Size -gt 512MB) { throw 'WebView2 offline installer size is outside the accepted 64-512 MiB range.' }
    if ($SignatureStatus -cne 'Valid' -or $Publisher -cne 'Microsoft Corporation') {
        throw 'WebView2 offline installer must have a Valid Microsoft Corporation Authenticode signature.'
    }
    if (-not $ProductVersion -or $ProductVersion.Length -gt 128 -or $ProductVersion -notmatch '^\d+(\.\d+){1,3}(\s.*)?$') {
        throw 'WebView2 offline installer has no usable ProductVersion.'
    }
}

function Get-CheckedWebViewRuntime {
    param([string]$Root, [string]$Kind)
    if (-not (Test-Path -LiteralPath $Root -PathType Container)) { throw "$Kind offline WebView2 build cache is missing." }
    $rootItem = Get-Item -LiteralPath $Root -Force
    if ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'WebView2 cache cannot be a reparse point.' }
    $candidates = @()
    # Tauri 2.12.4 stores each Microsoft's final filestreamingservice download
    # under one GUID directory. Do not recursively follow links or guess which
    # of several historical payloads the just-built installer embedded.
    foreach ($directory in Get-ChildItem -LiteralPath $Root -Directory -Force) {
        if ($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'WebView2 cache contains a directory reparse point.' }
        if ($directory.Name -notmatch '^[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}$') {
            throw 'Unexpected WebView2 cache directory layout.'
        }
        foreach ($file in Get-ChildItem -LiteralPath $directory.FullName -Force) {
            if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'WebView2 cache contains an unexpected nested directory or link.' }
            if ($file.Name -ine 'MicrosoftEdgeWebView2RuntimeInstallerX64.exe') { throw 'Unexpected file in the WebView2 offline payload cache.' }
            $candidates += $file
        }
    }
    if ($candidates.Count -ne 1) { throw "$Kind must have exactly one complete offline WebView2 payload; missing or ambiguous cache refused." }
    $file = $candidates[0]
    # Deny replacement/writes while WinVerifyTrust, version reading and hashing
    # inspect the same bytes. Nothing in this verifier executes the payload.
    $guard = [IO.File]::Open($file.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        if ($guard.ReadByte() -ne 0x4d -or $guard.ReadByte() -ne 0x5a) { throw 'WebView2 payload is not a Windows executable.' }
        $signature = Get-AuthenticodeSignature -LiteralPath $file.FullName
        $publisher = if ($signature.SignerCertificate) {
            $signature.SignerCertificate.GetNameInfo([Security.Cryptography.X509Certificates.X509NameType]::SimpleName, $false)
        } else { '' }
        $version = [Diagnostics.FileVersionInfo]::GetVersionInfo($file.FullName).ProductVersion
        Assert-WebViewRuntimeMetadata -Size $guard.Length -SignatureStatus ([string]$signature.Status) -Publisher $publisher -ProductVersion $version
        return [ordered]@{
            kind = $Kind; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            size = $guard.Length; product_version = $version
            signature_status = [string]$signature.Status; publisher = $publisher
        }
    }
    finally { $guard.Dispose() }
}

function New-WindowsWebViewRuntimeEvidence {
    param([string]$BundleRoot)
    if (-not $IsWindows) { throw 'Native Windows Authenticode verification is required.' }
    $root = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '../..'))
    $package = Get-Content -Raw -LiteralPath (Join-Path $root 'desktop/package.json') | ConvertFrom-Json
    $config = Get-Content -Raw -LiteralPath (Join-Path $root 'desktop/src-tauri/tauri.conf.json') | ConvertFrom-Json
    if ($package.devDependencies.'@tauri-apps/cli' -ne '2.12.4' -or
        $config.bundle.windows.webviewInstallMode.type -ne 'offlineInstaller' -or
        $config.bundle.windows.webviewInstallMode.silent -ne $true -or
        ($config.bundle.PSObject.Properties.Name -contains 'useLocalToolsDir' -and $config.bundle.useLocalToolsDir)) {
        throw 'The offline WebView2 cache contract requires Tauri CLI 2.12.4 and the declared offline installer configuration.'
    }
    $bundle = (Resolve-Path -LiteralPath $BundleRoot).Path
    $releaseRoot = Split-Path -Parent $bundle
    $msiCache = Join-Path $releaseRoot 'wix/x64/x64'
    $nsisCache = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'tauri/x64'
    $runtimes = @((Get-CheckedWebViewRuntime -Root $msiCache -Kind msi), (Get-CheckedWebViewRuntime -Root $nsisCache -Kind nsis))
    $artifacts = @()
    foreach ($kind in @('msi', 'nsis')) {
        $pattern = if ($kind -eq 'msi') { '*.msi' } else { '*-setup.exe' }
        $files = @(Get-ChildItem -LiteralPath $bundle -Recurse -File -Filter $pattern)
        if ($files.Count -ne 1) { throw "Expected exactly one built $kind installer for the WebView2 evidence binding." }
        $file = $files[0]
        if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'A built installer cannot be a reparse point.' }
        $artifacts += [ordered]@{ kind = $kind; name = $file.Name; size = $file.Length; sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    return [ordered]@{
        schema_version = 1; success = $true; mode = 'offlineInstaller'; tauri_cli = '2.12.4'
        artifacts = $artifacts; runtimes = $runtimes
        cold_install_verified = $false; offline_install_verified = $false
        scope = 'Native Authenticode verification of the complete cached payloads used by this build; no cold-machine/offline installation claim.'
    }
}

function Assert-WindowsWebViewRuntimeEvidence {
    param([string]$BundleRoot, [string]$EvidencePath)
    if (-not (Test-Path -LiteralPath $EvidencePath -PathType Leaf)) { throw 'Verified offline WebView2 evidence is required before running either installer.' }
    $saved = Get-Content -Raw -LiteralPath $EvidencePath | ConvertFrom-Json
    $current = New-WindowsWebViewRuntimeEvidence -BundleRoot $BundleRoot
    if ($saved.schema_version -ne 1 -or $saved.success -ne $true -or $saved.mode -ne 'offlineInstaller' -or
        @($saved.runtimes).Count -ne 2 -or @($saved.artifacts).Count -ne 2) { throw 'Offline WebView2 evidence is incomplete.' }
    foreach ($kind in @('msi', 'nsis')) {
        $oldRuntime = @($saved.runtimes | Where-Object kind -eq $kind)
        $newRuntime = @($current.runtimes | Where-Object { $_.kind -eq $kind })
        $oldPackage = @($saved.artifacts | Where-Object kind -eq $kind)
        $newPackage = @($current.artifacts | Where-Object { $_.kind -eq $kind })
        if ($oldRuntime.Count -ne 1 -or $oldPackage.Count -ne 1) { throw 'Offline WebView2 evidence contains an ambiguous installer identity.' }
        foreach ($field in @('sha256', 'size', 'product_version', 'signature_status', 'publisher')) {
            if ($oldRuntime[0].$field -cne $newRuntime[0].$field) { throw 'Offline WebView2 payload changed after post-build verification.' }
        }
        foreach ($field in @('name', 'sha256', 'size')) {
            if ($oldPackage[0].$field -cne $newPackage[0].$field) { throw 'Built installer changed after its offline WebView2 verification.' }
        }
    }
    return $current
}

if ($MyInvocation.InvocationName -ne '.') {
    $ErrorActionPreference = 'Stop'
    if (-not $RuntimeBundleRoot -or -not $RuntimeEvidenceRoot) { throw '-BundleRoot and -EvidenceRoot are required.' }
    $null = New-Item -ItemType Directory -Force -Path $RuntimeEvidenceRoot
    $record = New-WindowsWebViewRuntimeEvidence -BundleRoot $RuntimeBundleRoot
    $record | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $RuntimeEvidenceRoot 'webview-offline-runtime.json') -Encoding utf8
    Write-Host 'Both complete WebView2 payloads have valid Microsoft Corporation signatures and are bound to the built MSI/NSIS digests.'
}
