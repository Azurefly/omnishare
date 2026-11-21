#!/usr/bin/env sh
set -eu
INSTALL_DIR="$HOME/.local/lib/omnishare"
BIN_DIR="$HOME/.local/bin"
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/omnishare"
STATE_DIR="${XDG_STATE_HOME:-$HOME/.local/state}/omnishare"
DESKTOP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
SOURCE_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# Desktop Entry values cannot contain literal line breaks. Reject them before
# writing any files instead of generating a truncated command or extra keys.
newline='
'
carriage_return=$(printf '\r')
for path in "$INSTALL_DIR" "$BIN_DIR" "$DATA_DIR" "$STATE_DIR" "$DESKTOP_DIR"; do
  case "$path" in
    *"$newline"*|*"$carriage_return"*)
      echo "OmniShare installation paths must not contain line breaks" >&2
      exit 2
      ;;
  esac
done

shell_quote() {
  printf "'"
  printf '%s' "$1" | sed "s/'/'\\\\''/g"
  printf "'"
}

desktop_quote() {
  # First preserve Desktop Entry string escapes, then quote the Exec argument.
  # A literal backslash needs four backslashes in the file. Percent signs must
  # be doubled so paths cannot be interpreted as Desktop Entry field codes.
  printf '"'
  printf '%s' "$1" | sed \
    -e 's/\\/\\\\\\\\/g' \
    -e 's/"/\\\\"/g' \
    -e 's/`/\\\\`/g' \
    -e 's/\$/\\\\$/g' \
    -e 's/%/%%/g'
  printf '"'
}

mkdir -p "$INSTALL_DIR" "$BIN_DIR" "$DATA_DIR" "$STATE_DIR" "$DESKTOP_DIR"
cp "$SOURCE_DIR/omnishare" "$INSTALL_DIR/omnishare"
chmod 0755 "$INSTALL_DIR/omnishare"
quoted_binary=$(shell_quote "$INSTALL_DIR/omnishare")
quoted_data=$(shell_quote "$DATA_DIR")
cat > "$BIN_DIR/omnishare" <<LAUNCHER
#!/usr/bin/env sh
exec $quoted_binary --data-dir $quoted_data "\$@"
LAUNCHER
chmod 0755 "$BIN_DIR/omnishare"
desktop_command=$(desktop_quote "$BIN_DIR/omnishare")
# GIO validates the executable before expanding %% field escapes. Keep the
# executable constant, passing the quoted user path as a literal env argument.
cat > "$DESKTOP_DIR/omnishare.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=OmniShare
Comment=Private multi-device notes, files and collaboration hub
Exec=/usr/bin/env -- $desktop_command
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
