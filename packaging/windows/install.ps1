$ErrorActionPreference = "Stop"
$Target = Join-Path $env:LOCALAPPDATA "OmniShare"
$Data = Join-Path $env:APPDATA "OmniShare"
$Exe = Join-Path $Target "omnishare.exe"
New-Item -ItemType Directory -Force -Path $Target, $Data | Out-Null
Copy-Item (Join-Path $PSScriptRoot "omnishare.exe") $Exe -Force

$Shell = New-Object -ComObject WScript.Shell
$Desktop = [Environment]::GetFolderPath("Desktop")
$Shortcut = $Shell.CreateShortcut((Join-Path $Desktop "OmniShare.lnk"))
$Shortcut.TargetPath = $Exe
$Shortcut.Arguments = "--data-dir `"$Data`""
$Shortcut.WorkingDirectory = $Target
$Shortcut.Description = "OmniShare 私有化多设备内容中枢"
$Shortcut.Save()

$StartMenu = Join-Path $env:APPDATA "Microsoft\Windows\Start Menu\Programs"
$MenuShortcut = $Shell.CreateShortcut((Join-Path $StartMenu "OmniShare.lnk"))
$MenuShortcut.TargetPath = $Exe
$MenuShortcut.Arguments = "--data-dir `"$Data`""
$MenuShortcut.WorkingDirectory = $Target
$MenuShortcut.Description = "OmniShare 私有化多设备内容中枢"
$MenuShortcut.Save()

$Running = Get-CimInstance Win32_Process -Filter "Name='omnishare.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -eq $Exe }
if (-not $Running) {
    Start-Process -FilePath $Exe -ArgumentList "--data-dir", "`"$Data`""
} else {
    Write-Host "OmniShare 已在运行。"
}
Write-Host "OmniShare 已安装到 $Target"
Write-Host "用户数据保存在 $Data"
