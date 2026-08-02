#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${1:?binary directory required}"
OUT="${2:-$ROOT/dist}"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
DEB_VERSION="${VERSION/-rc/~rc}"
BUNDLE_VERSION="$(printf '%s' "$VERSION" | sed -E 's/-rc([0-9]+)/.\1/; s/[^0-9.].*$//')"
SHORT_VERSION="${VERSION%%-*}"
PKG="$OUT/packages"
RELEASE_NOTES="$ROOT/docs/RELEASE_NOTES_v${VERSION}.md"

require() { command -v "$1" >/dev/null 2>&1 || { echo "Missing required tool: $1" >&2; exit 2; }; }
for tool in zip tar dpkg-deb sed sha256sum; do require "$tool"; done
for target in windows-amd64.exe windows-arm64.exe linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
  test -s "$BIN/omnishare-$target" || { echo "Missing binary: $BIN/omnishare-$target" >&2; exit 2; }
done
test -f "$RELEASE_NOTES" || { echo "Missing release notes: $RELEASE_NOTES" >&2; exit 2; }

rm -rf "$PKG" "$OUT/work"
mkdir -p "$PKG" "$OUT/work"

for arch in amd64 arm64; do
  work="$OUT/work/windows-$arch/OmniShare"
  mkdir -p "$work"
  cp "$BIN/omnishare-windows-$arch.exe" "$work/omnishare.exe"
  cp "$ROOT/packaging/windows/install.ps1" "$ROOT/packaging/windows/start-omnishare.cmd" "$ROOT/packaging/windows/start-omnishare.ps1" "$ROOT/packaging/windows/uninstall.ps1" "$work/"
  sed "s/__VERSION__/$VERSION/g" "$ROOT/packaging/windows/README.txt" > "$work/README.txt"
  (cd "$(dirname "$work")" && zip -qr "$PKG/OmniShare-v${VERSION}-windows-${arch}.zip" OmniShare)
done

for arch in amd64 arm64; do
  work="$OUT/work/linux-$arch/OmniShare"
  mkdir -p "$work"
  cp "$BIN/omnishare-linux-$arch" "$work/omnishare"; chmod +x "$work/omnishare"
  cp "$ROOT/packaging/linux/install.sh" "$ROOT/packaging/linux/uninstall.sh" "$work/"; chmod +x "$work/"*.sh
  sed "s/__VERSION__/$VERSION/g" "$ROOT/packaging/linux/README.txt" > "$work/README.txt"
  (cd "$(dirname "$work")" && tar -czf "$PKG/OmniShare-v${VERSION}-linux-${arch}.tar.gz" OmniShare)

  deb="$(mktemp -d)"
  mkdir -p "$deb/DEBIAN" "$deb/usr/bin" "$deb/usr/share/applications" "$deb/usr/share/doc/omnishare"
  chmod g-s "$deb" "$deb/DEBIAN" 2>/dev/null || true
  cp "$BIN/omnishare-linux-$arch" "$deb/usr/bin/omnishare"; chmod 0755 "$deb/usr/bin/omnishare"
  cp "$ROOT/packaging/linux/omnishare.desktop" "$deb/usr/share/applications/omnishare.desktop"
  cp "$ROOT/README.md" "$deb/usr/share/doc/omnishare/README.md"
  cat > "$deb/DEBIAN/control" <<CONTROL
Package: omnishare
Version: $DEB_VERSION
Section: utils
Priority: optional
Architecture: $arch
Maintainer: OmniShare Project
Description: Private multi-device notes, files and collaboration hub
 A lightweight local-first desktop companion with a local web interface.
CONTROL
  dpkg-deb --build --root-owner-group "$deb" "$PKG/omnishare_${DEB_VERSION}_${arch}.deb" >/dev/null
  rm -rf "$deb"
done

for arch in amd64 arm64; do
  app="$OUT/work/macos-$arch/OmniShare.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  cp "$BIN/omnishare-darwin-$arch" "$app/Contents/MacOS/OmniShare"; chmod +x "$app/Contents/MacOS/OmniShare"
  sed -e "s/__SHORT_VERSION__/$SHORT_VERSION/g" -e "s/__BUNDLE_VERSION__/$BUNDLE_VERSION/g" "$ROOT/packaging/macos/Info.plist" > "$app/Contents/Info.plist"
  sed "s/__VERSION__/$VERSION/g" "$ROOT/packaging/macos/README.txt" > "$app/Contents/Resources/README.txt"
  (cd "$(dirname "$app")" && zip -qr "$PKG/OmniShare-v${VERSION}-macos-${arch}.zip" OmniShare.app)
done

for doc in "$ROOT/README.md" "$RELEASE_NOTES" "$ROOT/docs/REMEDIATION_v1.3.0-rc1.md"; do
  [ -f "$doc" ] && cp "$doc" "$PKG/"
done
(
  cd "$PKG"
  find . -maxdepth 1 -type f ! -name SHA256SUMS.txt -printf '%f\n' | sort | xargs sha256sum > SHA256SUMS.txt
)
printf 'Packages created in %s\n' "$PKG"
