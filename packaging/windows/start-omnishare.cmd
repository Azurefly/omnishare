@echo off
setlocal
set "DATA_DIR=%APPDATA%\OmniShare"
if not exist "%DATA_DIR%" mkdir "%DATA_DIR%"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$exe=[IO.Path]::GetFullPath('%~dp0omnishare.exe'); $p=Get-CimInstance Win32_Process -Filter "Name='omnishare.exe'" -ErrorAction SilentlyContinue ^| ? { $_.ExecutablePath -eq $exe }; if(-not $p){Start-Process -FilePath $exe -ArgumentList '--data-dir','"%DATA_DIR%"'} else {Write-Host 'OmniShare is already running.'}"
endlocal
