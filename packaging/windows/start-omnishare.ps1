$ErrorActionPreference = 'Stop'

$exe = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'omnishare.exe'))
$appData = [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
if ([string]::IsNullOrWhiteSpace($appData)) {
    throw 'Unable to resolve the current user application-data directory.'
}
$dataDir = [IO.Path]::GetFullPath((Join-Path $appData 'OmniShare'))

if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) {
    throw "OmniShare executable not found: $exe"
}
[IO.Directory]::CreateDirectory($dataDir) | Out-Null

$running = @(
    Get-CimInstance Win32_Process -Filter "Name='omnishare.exe'" -ErrorAction SilentlyContinue |
        Where-Object {
            $_.ExecutablePath -and
            ([IO.Path]::GetFullPath($_.ExecutablePath) -eq $exe)
        }
)

if ($running.Count -gt 0) {
    Write-Host 'OmniShare is already running.'
    exit 0
}

$quotedDataDir = '"' + $dataDir + '"'
Start-Process -FilePath $exe -ArgumentList @('--data-dir', $quotedDataDir)
