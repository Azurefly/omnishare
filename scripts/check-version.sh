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
print(f'Version consistency passed: {expected}')
PY
