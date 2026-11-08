#!/usr/bin/env bash
set -euo pipefail
umask 022

if [ "$#" -ne 3 ]; then
  echo "Usage: package-macos.sh <binary> <output-directory> <amd64|arm64>" >&2
  exit 2
fi
if [ "$(uname -s)" != Darwin ]; then
  echo "macOS app bundles must be finalized and signed on macOS." >&2
  exit 2
fi
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BINARY="$1"
OUT="$2"
ARCH="$3"
case "$ARCH" in
  amd64) MACH_ARCH=x86_64 ;;
  arm64) MACH_ARCH=arm64 ;;
  *) echo "Unsupported macOS architecture: $ARCH" >&2; exit 2 ;;
esac
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.?[0-9]+)?$ ]]; then
  echo "Unsupported release version: $VERSION" >&2
  exit 2
fi
SHORT_VERSION="${VERSION%%-*}"
for tool in /usr/bin/lipo /usr/bin/plutil /usr/bin/codesign /usr/bin/ditto; do
  test -x "$tool" || { echo "Missing macOS tool: $tool" >&2; exit 2; }
done
test -f "$BINARY" && test -s "$BINARY" && test ! -L "$BINARY" ||
  { echo "Expected a nonempty regular Mach-O binary." >&2; exit 2; }
test "$(/usr/bin/lipo -archs "$BINARY")" = "$MACH_ARCH" ||
  { echo "The Mach-O architecture does not match $ARCH." >&2; exit 2; }

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/omnishare-macos.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
APP="$WORK/OmniShare.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$BINARY" "$APP/Contents/MacOS/OmniShare"
chmod 0755 "$APP/Contents/MacOS/OmniShare"
# CFBundleVersion remains a valid three-component version even for an RC.
# The complete release version is retained in the README and the backend.
sed -e "s/__SHORT_VERSION__/$SHORT_VERSION/g" \
    -e "s/__BUNDLE_VERSION__/$SHORT_VERSION/g" \
    "$ROOT/packaging/macos/Info.plist" > "$APP/Contents/Info.plist"
sed "s/__VERSION__/$VERSION/g" \
    "$ROOT/packaging/macos/README.txt" > "$APP/Contents/Resources/README.txt"
chmod 0644 "$APP/Contents/Info.plist" "$APP/Contents/Resources/README.txt"
/usr/bin/plutil -lint "$APP/Contents/Info.plist"

# The Go linker signs only the arm64 Mach-O. Sign the assembled bundle after
# writing every resource so its Info.plist and resources are sealed too.
# An ad-hoc signature checks integrity; it is not Developer ID or notarization.
/usr/bin/codesign --force --sign - --timestamp=none "$APP"
/usr/bin/codesign --verify --deep --strict --verbose=2 "$APP"
ASSET="OmniShare-v${VERSION}-macos-${ARCH}.zip"
/usr/bin/ditto -c -k --sequesterRsrc --keepParent "$APP" "$WORK/$ASSET"
mv "$WORK/$ASSET" "$OUT/$ASSET"
printf 'Created ad-hoc signed macOS bundle: %s\n' "$OUT/$ASSET"
