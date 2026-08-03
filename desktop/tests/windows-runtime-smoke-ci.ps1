[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BundleRoot,

    [Parameter(Mandatory = $true)]
    [string]$EvidenceRoot
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

New-Item -ItemType Directory -Force -Path $EvidenceRoot | Out-Null
$resolvedEvidence = (Resolve-Path $EvidenceRoot).Path

# GitHub-hosted runners can have proxy variables configured. All desktop
# runtime probes are loopback-only and must never be sent through a proxy.
$env:NO_PROXY = "127.0.0.1,localhost"
$env:no_proxy = "127.0.0.1,localhost"
$env:OMNISHARE_DESKTOP_LOG_DIR = $resolvedEvidence

try {
    & "$PSScriptRoot/windows-runtime-smoke.ps1" `
        -BundleRoot $BundleRoot `
        -EvidenceRoot $resolvedEvidence
}
finally {
    Remove-Item Env:OMNISHARE_DESKTOP_LOG_DIR -ErrorAction SilentlyContinue
}
