$ErrorActionPreference = 'Stop'

$RootDir = Resolve-Path (Join-Path $PSScriptRoot '..')
$BackendName = 'omnishare.exe'

Set-Location $RootDir

Write-Host '[desktop] building frontend assets'
npm --prefix frontend run build

Write-Host '[desktop] building Go backend resource'
$ResourceDir = Join-Path $RootDir 'desktop/src-tauri/resources'
New-Item -ItemType Directory -Force -Path $ResourceDir | Out-Null
Push-Location (Join-Path $RootDir 'backend')
go build -trimpath -o (Join-Path $ResourceDir $BackendName) ./cmd/omnishare
Pop-Location

Write-Host '[desktop] installing desktop dependencies'
npm --prefix desktop install

Write-Host '[desktop] building Tauri desktop bundle'
npm --prefix desktop run build

Write-Host '[desktop] bundle output: desktop/src-tauri/target/release/bundle'
