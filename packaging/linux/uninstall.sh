#!/usr/bin/env sh
set -eu
INSTALL_DIR="$HOME/.local/lib/omnishare"
BIN_DIR="$HOME/.local/bin"
DATA_HOME="${XDG_DATA_HOME:-$HOME/.local/share}"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/omnishare"
PID_FILE="$STATE_DIR/omnishare.pid"
if [ -f "$PID_FILE" ]; then
  pid=$(cat "$PID_FILE" 2>/dev/null || true)
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    exe=$(readlink "/proc/$pid/exe" 2>/dev/null || true)
    if [ "$exe" = "$INSTALL_DIR/omnishare" ]; then
      kill "$pid" 2>/dev/null || true
    fi
  fi
  rm -f "$PID_FILE"
fi
rm -rf "$INSTALL_DIR"
rm -f "$BIN_DIR/omnishare" "$DATA_HOME/applications/omnishare.desktop"
echo "Program removed. User data remains in $DATA_HOME/omnishare"
