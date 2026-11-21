#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PLATFORM="$(uname -s)"

cd "$ROOT_DIR"

echo "[desktop] building frontend assets"
npm --prefix frontend run build

echo "[desktop] preparing Go backend resource"
mkdir -p desktop/src-tauri/resources

if [[ "$PLATFORM" == "Darwin" ]]; then
  echo "[desktop] building universal macOS backend (arm64 + x86_64)"
  (
    cd backend
    GOOS=darwin GOARCH=arm64 go build -trimpath -o ../desktop/src-tauri/resources/omnishare-arm64 ./cmd/omnishare
    GOOS=darwin GOARCH=amd64 go build -trimpath -o ../desktop/src-tauri/resources/omnishare-x86_64 ./cmd/omnishare
    lipo -create \
      ../desktop/src-tauri/resources/omnishare-arm64 \
      ../desktop/src-tauri/resources/omnishare-x86_64 \
      -output ../desktop/src-tauri/resources/omnishare
    chmod +x ../desktop/src-tauri/resources/omnishare
    rm ../desktop/src-tauri/resources/omnishare-arm64 ../desktop/src-tauri/resources/omnishare-x86_64
  )
  rustup target add aarch64-apple-darwin x86_64-apple-darwin
else
  echo "[desktop] building Linux backend"
  (
    cd backend
    go build -trimpath -o ../desktop/src-tauri/resources/omnishare ./cmd/omnishare
  )
fi

echo "[desktop] installing desktop dependencies"
npm --prefix desktop install

if [[ "$PLATFORM" == "Darwin" ]]; then
  echo "[desktop] building universal macOS Tauri bundle"
  npm --prefix desktop run build -- --target universal-apple-darwin
  echo "[desktop] bundle output: desktop/src-tauri/target/universal-apple-darwin/release/bundle"
else
  echo "[desktop] building Linux Tauri bundle"
  npm --prefix desktop run build
  echo "[desktop] bundle output: desktop/src-tauri/target/release/bundle"
fi
