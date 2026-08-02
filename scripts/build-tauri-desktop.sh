#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKEND_NAME="omnishare"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  BACKEND_NAME="omnishare.exe"
fi

cd "$ROOT_DIR"

echo "[desktop] building frontend assets"
npm --prefix frontend run build

echo "[desktop] building Go backend resource"
mkdir -p desktop/src-tauri/resources
(
  cd backend
  go build -trimpath -o "../desktop/src-tauri/resources/${BACKEND_NAME}" ./cmd/omnishare
)

echo "[desktop] installing desktop dependencies"
npm --prefix desktop install

echo "[desktop] building Tauri desktop bundle"
npm --prefix desktop run build

echo "[desktop] bundle output: desktop/src-tauri/target/release/bundle"
