#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
python3 - "$ROOT" "$VERSION" <<'PY'
import json,re,sys
from pathlib import Path
root=Path(sys.argv[1]); expected=sys.argv[2]
package=json.loads((root/'frontend/package.json').read_text())['version']
if package!=expected: raise SystemExit(f'frontend version {package} != {expected}')
text=(root/'backend/internal/buildinfo/version.go').read_text()
m=re.search(r'const Version = "([^"]+)"',text)
if not m or m.group(1)!=expected: raise SystemExit('backend build version mismatch')
sw=(root/'frontend/web/service-worker.js').read_text()
if f'omnishare-v{expected}' not in sw: raise SystemExit('service worker cache version mismatch')
desktop_package=json.loads((root/'desktop/package.json').read_text())['version']
desktop_cargo=(root/'desktop/src-tauri/Cargo.toml').read_text()
m=re.search(r'^version\s*=\s*"([^"]+)"',desktop_cargo,re.M)
if not m or m.group(1)!=expected: raise SystemExit('desktop Cargo version mismatch')
desktop_tauri=json.loads((root/'desktop/src-tauri/tauri.conf.json').read_text())['version']
if desktop_package!=expected: raise SystemExit(f'desktop package version {desktop_package} != {expected}')
if desktop_tauri!=expected: raise SystemExit(f'desktop tauri version {desktop_tauri} != {expected}')
print(f'Version consistency passed: {expected}')
PY
