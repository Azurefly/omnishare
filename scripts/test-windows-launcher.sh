#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CMD="$ROOT/packaging/windows/start-omnishare.cmd"
PS1="$ROOT/packaging/windows/start-omnishare.ps1"

python3 - "$CMD" "$PS1" <<'PY'
from pathlib import Path
import sys

cmd_path, ps1_path = map(Path, sys.argv[1:])
cmd_bytes = cmd_path.read_bytes()
ps1_bytes = ps1_path.read_bytes()
assert cmd_bytes.startswith(b'\xef\xbb\xbf'), 'CMD launcher must be UTF-8 BOM'
assert ps1_bytes.startswith(b'\xef\xbb\xbf'), 'PowerShell launcher must be UTF-8 BOM'
cmd = cmd_bytes.decode('utf-8-sig').replace('\r\n', '\n')
ps1 = ps1_bytes.decode('utf-8-sig').replace('\r\n', '\n')
assert '^|' not in cmd, 'CMD launcher must not contain escaped PowerShell pipes'
assert '-Command' not in cmd, 'CMD launcher must not embed PowerShell source'
assert '-File "%~dp0start-omnishare.ps1"' in cmd
assert 'Get-CimInstance Win32_Process' in ps1
assert '|\n        Where-Object' in ps1
assert "Start-Process -FilePath $exe" in ps1
assert "@('--data-dir', $quotedDataDir)" in ps1
assert "[Environment]::GetFolderPath" in ps1
PY

if [[ $# -gt 0 ]]; then
  zip_file="$1"
  test -s "$zip_file"
  unzip -Z1 "$zip_file" | grep -qx 'OmniShare/start-omnishare.cmd'
  unzip -Z1 "$zip_file" | grep -qx 'OmniShare/start-omnishare.ps1'
  ! unzip -p "$zip_file" OmniShare/start-omnishare.cmd | grep -q '\^|'
  unzip -p "$zip_file" OmniShare/start-omnishare.cmd | grep -q 'start-omnishare.ps1'
fi

printf 'Windows launcher contract passed.\n'
