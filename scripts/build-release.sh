#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/dist}"
BIN="$OUT/binaries"
for tool in go node npm zip tar dpkg-deb file sha256sum; do command -v "$tool" >/dev/null || { echo "Missing required tool: $tool" >&2; exit 2; }; done
"$ROOT/scripts/check-version.sh"
rm -rf "$OUT"
mkdir -p "$BIN"

npm --prefix "$ROOT/frontend" run build
npm --prefix "$ROOT/frontend" run check
npm --prefix "$ROOT/frontend" run test
(cd "$ROOT/backend" && go test ./... -count=1)

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os="${target%/*}"; arch="${target#*/}"; ext=""
  [[ "$os" == "windows" ]] && ext=".exe"
  echo "Building $os/$arch"
  (cd "$ROOT/backend" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o "$BIN/omnishare-${os}-${arch}${ext}" ./cmd/omnishare) &
done
wait
test "$(find "$BIN" -maxdepth 1 -type f | wc -l)" -eq 6

"$ROOT/scripts/package-release.sh" "$BIN" "$OUT"
printf 'Release artifacts created in %s\n' "$OUT"
