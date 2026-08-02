$ErrorActionPreference = "SilentlyContinue"
$Target = Join-Path $env:LOCALAPPDATA "OmniShare"
$Exe = Join-Path $Target "omnishare.exe"
Get-CimInstance Win32_Process -Filter "Name='omnishare.exe'" | Where-Object { $_.ExecutablePath -eq $Exe } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }
Remove-Item (Join-Path ([Environment]::GetFolderPath("Desktop")) "OmniShare.lnk") -Force
Remove-Item (Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs\OmniShare.lnk") -Force
Remove-Item $Target -Recurse -Force
Write-Host "程序已卸载。用户数据仍保留在 $env:APPDATA\OmniShare；如不再需要可手动删除。"
