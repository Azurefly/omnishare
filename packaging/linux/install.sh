#!/usr/bin/env sh
set -eu
INSTALL_DIR="$HOME/.local/lib/omnishare"
BIN_DIR="$HOME/.local/bin"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/omnishare"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/omnishare"
DESKTOP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
mkdir -p "$INSTALL_DIR" "$BIN_DIR" "$DATA_DIR" "$STATE_DIR" "$DESKTOP_DIR"
cp "$SOURCE_DIR/omnishare" "$INSTALL_DIR/omnishare"
chmod 0755 "$INSTALL_DIR/omnishare"
cat > "$BIN_DIR/omnishare" <<LAUNCHER
#!/usr/bin/env sh
exec "$INSTALL_DIR/omnishare" --data-dir "$DATA_DIR" "\$@"
LAUNCHER
chmod 0755 "$BIN_DIR/omnishare"
cat > "$DESKTOP_DIR/omnishare.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=OmniShare
Comment=Private multi-device notes, files and collaboration hub
Exec=$BIN_DIR/omnishare
Terminal=false
Categories=Utility;Network;
StartupNotify=true
DESKTOP
if [ "${OMNISHARE_NO_START:-0}" != "1" ]; then
  if [ -f "$STATE_DIR/omnishare.pid" ] && kill -0 "$(cat "$STATE_DIR/omnishare.pid")" 2>/dev/null; then
    echo "OmniShare is already running"
  else
    nohup "$BIN_DIR/omnishare" >"$STATE_DIR/omnishare.log" 2>&1 &
    echo $! > "$STATE_DIR/omnishare.pid"
  fi
fi
echo "OmniShare installed to $INSTALL_DIR"
echo "User data: $DATA_DIR"
