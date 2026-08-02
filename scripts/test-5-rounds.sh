#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOG="${1:-$ROOT/test-results-5-rounds.log}"
VERSION="$(tr -d '[:space:]' < "$ROOT/VERSION")"
DEB_VERSION="${VERSION/-rc/~rc}"
TEST_BUILD="$ROOT/.test-build"
if [ "${OMNISHARE_APPEND:-0}" != "1" ]; then : > "$LOG"; fi

curl() {
  command curl --connect-timeout 2 --max-time 15 "$@"
}

run() {
  printf '\n%s\n' "$1" | tee -a "$LOG"
  shift
  "$@" 2>&1 | tee -a "$LOG"
}

core_regression() {
  (cd "$ROOT/backend" && go test ./internal/storage ./internal/config ./internal/api ./internal/instance ./internal/discovery ./internal/durable -count=1)
}

free_port() {
  python3 - <<'PY'
import socket
s=socket.socket(); s.bind(('127.0.0.1',0)); print(s.getsockname()[1]); s.close()
PY
}

json_field() {
  python3 -c 'import json,sys; value=json.load(sys.stdin); value=value.get("data"); [value:=value[k] for k in sys.argv[1].split(".") if k]; print(value)' "$1"
}

assert_json() {
  python3 -c 'import json,sys; data=json.load(sys.stdin); assert eval(sys.argv[1], {"__builtins__":{}}, {"data":data}), (sys.argv[1], data)' "$1"
}

wait_health() {
  local url="$1"
  for _ in $(seq 1 100); do
    if curl -fsS "$url/api/v1/health" >/dev/null 2>&1; then return 0; fi
    sleep .1
  done
  return 1
}

round1() {
  "$ROOT/scripts/check-version.sh"
  test -z "$(cd "$ROOT/backend" && gofmt -l .)"
  (cd "$ROOT/backend" && go vet ./...)
  npm --prefix "$ROOT/frontend" run check
  npm --prefix "$ROOT/frontend" run test
  core_regression
}

round2() {
  (cd "$ROOT/backend" && go test -race ./... -count=1)
  core_regression
}

round3() {
  core_regression
  (cd "$ROOT/backend" && go test -race ./internal/api ./internal/storage ./internal/config ./internal/instance ./internal/discovery -count=1)

  local tmp port base pid key second_status
  tmp="$(mktemp -d)"
  port="$(free_port)"
  base="http://127.0.0.1:$port"
  key="0123456789abcdef-round3"
  cleanup_round3() {
    if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
    rm -rf "$tmp"
  }
  trap cleanup_round3 RETURN

  (cd "$ROOT/backend" && go build -trimpath -o "$tmp/omnishare" ./cmd/omnishare)
  "$tmp/omnishare" --no-browser --port "$port" --data-dir "$tmp/data" >"$tmp/server.log" 2>&1 & pid=$!
  wait_health "$base" || { cat "$tmp/server.log"; return 1; }

  test "$(curl -sS -o /dev/null -w '%{http_code}' -H 'Origin: https://evil.example' "$base/api/v1/dashboard")" = 403
  test "$(curl -sS -o /dev/null -w '%{http_code}' -H 'Host: attacker.example' "$base/api/v1/health")" = 400
  test "$(curl -sS -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data-binary '{"content":"a"}{"content":"b"}' "$base/api/v1/notes")" = 400

  config_payload="$(cat <<JSON
{"node_name":"round3-secure","port":$port,"listen_address":"127.0.0.1","allow_lan":false,"auto_open_browser":false,"max_upload_mb":128,"retention_days":30,"trash_retention_days":14,"allowed_origins":[],"peers":[],"access_key":"$key"}
JSON
)"
  curl -fsS -H 'Content-Type: application/json' --data-binary "$config_payload" -X PUT "$base/api/v1/config" | assert_json 'data["code"] == 0 and data["data"]["has_access_key"] is True'
  ! grep -q "$key" "$tmp/data/config.json"
  test "$(curl -sS -o /dev/null -w '%{http_code}' "$base/api/v1/dashboard?key=$key")" = 401
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/dashboard" >/dev/null

  note_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary '{"content":"secret-content #secure","max_read_count":2}' "$base/api/v1/notes")"
  note_id="$(printf '%s' "$note_json" | json_field id)"
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/notes" | assert_json 'data["data"][0]["content_redacted"] is True and data["data"][0]["content"] == ""'
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$note_id/manage" | assert_json 'data["data"]["content"] == "secret-content #secure" and data["data"]["read_count"] == 0'
  test "$(curl -fsS -H "X-OmniShare-Key: $key" "$base/n/$note_id/raw")" = 'secret-content #secure'
  curl -fsSI -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$note_id" >/dev/null
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$note_id/manage" | assert_json 'data["data"]["read_count"] == 0'
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$note_id" | assert_json 'data["data"]["read_count"] == 1'

  expired_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary '{"content":"expires","ttl_seconds":1}' "$base/api/v1/notes")"
  expired_id="$(printf '%s' "$expired_json" | json_field id)"
  sleep 1.2
  test "$(curl -sS -o /dev/null -w '%{http_code}' -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$expired_id/manage")" = 404

  share_note_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary '{"content":"share-session-content"}' "$base/api/v1/notes")"
  share_note_id="$(printf '%s' "$share_note_json" | json_field id)"
  share_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary "{\"object_type\":\"note\",\"object_id\":\"$share_note_id\",\"ttl_seconds\":3600,\"max_access_count\":1}" "$base/api/v1/shares")"
  share_url="$(printf '%s' "$share_json" | json_field url)"
  curl -fsSI "$share_url" >/dev/null
  curl -fsS -c "$tmp/cookies" "$share_url" | grep -q 'share-session-content'
  curl -fsS -b "$tmp/cookies" "$share_url" | grep -q 'share-session-content'
  test "$(curl -sS -o /dev/null -w '%{http_code}' "$share_url")" = 410

  printf 'range-test-content' > "$tmp/demo.txt"
  upload1="$(curl -fsS -H "X-OmniShare-Key: $key" -F "file=@$tmp/demo.txt;filename=one.txt" "$base/api/v1/files/upload")"
  upload2="$(curl -fsS -H "X-OmniShare-Key: $key" -F "file=@$tmp/demo.txt;filename=two.txt" "$base/api/v1/files/upload")"
  file1="$(printf '%s' "$upload1" | json_field id)"; file2="$(printf '%s' "$upload2" | json_field id)"
  path1="$(printf '%s' "$upload1" | json_field storage_path)"; path2="$(printf '%s' "$upload2" | json_field storage_path)"
  test "$file1" != "$file2"; test "$path1" = "$path2"; test -f "$tmp/data/$path1"

  ticket_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary '{"disposition":"inline"}' "$base/api/v1/files/$file1/ticket")"
  ticket_url="$(printf '%s' "$ticket_json" | json_field url)"
  test "$(curl -fsS -H 'Range: bytes=0-4' "$ticket_url")" = range

  rm "$tmp/data/$path1"
  test "$(curl -sS -o "$tmp/bad-backup.json" -w '%{http_code}' -H "X-OmniShare-Key: $key" "$base/api/v1/backup")" = 500
  upload3="$(curl -fsS -H "X-OmniShare-Key: $key" -F "file=@$tmp/demo.txt;filename=recovered.txt" "$base/api/v1/files/upload")"
  test "$(printf '%s' "$upload3" | json_field storage_path)" = "$path1"; test -f "$tmp/data/$path1"

  if "$tmp/omnishare" --no-browser --port "$(free_port)" --data-dir "$tmp/data" >"$tmp/second.log" 2>&1; then
    echo 'second instance unexpectedly started' >&2; return 1
  fi
  grep -qi 'instance lock' "$tmp/second.log"

  curl -fsS -H "X-OmniShare-Key: $key" -o "$tmp/backup.zip" "$base/api/v1/backup"
  unzip -t "$tmp/backup.zip" >/dev/null
  post_json="$(curl -fsS -H "X-OmniShare-Key: $key" -H 'Content-Type: application/json' --data-binary '{"content":"post-backup"}' "$base/api/v1/notes")"
  post_id="$(printf '%s' "$post_json" | json_field id)"
  curl -fsS -H "X-OmniShare-Key: $key" -H 'X-OmniShare-Confirm: RESTORE-BACKUP' -F "backup=@$tmp/backup.zip" "$base/api/v1/restore" >/dev/null
  test "$(curl -sS -o /dev/null -w '%{http_code}' -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$post_id/manage")" = 404
  curl -fsS -H "X-OmniShare-Key: $key" "$base/api/v1/notes/$note_id/manage" | grep -q 'secret-content'
}

round4() {
  core_regression
  npm --prefix "$ROOT/frontend" run build
  npm --prefix "$ROOT/frontend" run test
  rm -rf "$TEST_BUILD"; mkdir -p "$TEST_BUILD"
  for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
    os="${target%/*}"; arch="${target#*/}"; ext=""; [ "$os" = windows ] && ext=.exe
    (cd "$ROOT/backend" && CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags '-s -w' -o "$TEST_BUILD/omnishare-$os-$arch$ext" ./cmd/omnishare) &
  done
  wait
  test "$(find "$TEST_BUILD" -maxdepth 1 -type f | wc -l)" -eq 6
  for f in "$TEST_BUILD"/*; do test -s "$f"; done
  file "$TEST_BUILD"/*
  file "$TEST_BUILD/omnishare-windows-amd64.exe" | grep -q 'PE32+'
  file "$TEST_BUILD/omnishare-linux-amd64" | grep -q 'ELF 64-bit'
  file "$TEST_BUILD/omnishare-darwin-arm64" | grep -q 'Mach-O 64-bit arm64'
}

round5() {
  core_regression
  test "$(find "$TEST_BUILD" -maxdepth 1 -type f | wc -l)" -eq 6
  local tmp release install_home install_port installed_pid
  tmp="$(mktemp -d)"; release="$tmp/release"; install_home="$tmp/home"; mkdir -p "$install_home"
  cleanup_round5() {
    if [ -n "${installed_pid:-}" ] && kill -0 "$installed_pid" 2>/dev/null; then kill "$installed_pid" 2>/dev/null || true; wait "$installed_pid" 2>/dev/null || true; fi
    rm -rf "$tmp"
  }
  trap cleanup_round5 RETURN
  "$ROOT/scripts/package-release.sh" "$TEST_BUILD" "$release"
  (cd "$release/packages" && sha256sum -c SHA256SUMS.txt)
  for arch in amd64 arm64; do
    unzip -t "$release/packages/OmniShare-v${VERSION}-windows-${arch}.zip" >/dev/null
    tar -tzf "$release/packages/OmniShare-v${VERSION}-linux-${arch}.tar.gz" >/dev/null
    dpkg-deb -I "$release/packages/omnishare_${DEB_VERSION}_${arch}.deb" >/dev/null
    unzip -t "$release/packages/OmniShare-v${VERSION}-macos-${arch}.zip" >/dev/null
  done

  mkdir -p "$tmp/linux"; tar -xzf "$release/packages/OmniShare-v${VERSION}-linux-amd64.tar.gz" -C "$tmp/linux"
  HOME="$install_home" XDG_DATA_HOME="$install_home/.data" XDG_STATE_HOME="$install_home/.state" OMNISHARE_NO_START=1 "$tmp/linux/OmniShare/install.sh"
  test -x "$install_home/.local/lib/omnishare/omnishare"; test -x "$install_home/.local/bin/omnishare"
  install_port="$(free_port)"
  HOME="$install_home" XDG_DATA_HOME="$install_home/.data" XDG_STATE_HOME="$install_home/.state" "$install_home/.local/bin/omnishare" --no-browser --port "$install_port" >"$tmp/installed.log" 2>&1 & installed_pid=$!
  mkdir -p "$install_home/.state/omnishare"; printf '%s\n' "$installed_pid" > "$install_home/.state/omnishare/omnishare.pid"
  wait_health "http://127.0.0.1:$install_port"
  curl -fsS "http://127.0.0.1:$install_port/api/v1/health" | assert_json 'data["data"]["version"] == "'"$VERSION"'"'
  HOME="$install_home" XDG_DATA_HOME="$install_home/.data" XDG_STATE_HOME="$install_home/.state" "$tmp/linux/OmniShare/uninstall.sh"
  for _ in $(seq 1 30); do kill -0 "$installed_pid" 2>/dev/null || break; sleep .1; done
  ! kill -0 "$installed_pid" 2>/dev/null
  installed_pid=""
  test ! -e "$install_home/.local/lib/omnishare/omnishare"
  test -d "$install_home/.data/omnishare"
}

ROUNDS="${OMNISHARE_ROUNDS:-1,2,3,4,5}"
wants_round() { case ",$ROUNDS," in *",$1,"*) return 0;; *) return 1;; esac; }
wants_round 1 && run "ROUND 1/5 — SOURCE, VERSION, FRONTEND AND CORE REGRESSION" round1
wants_round 2 && run "ROUND 2/5 — FULL RACE DETECTOR AND CORE REGRESSION" round2
wants_round 3 && run "ROUND 3/5 — ADVERSARIAL SECURITY, DATA RECOVERY AND CORE REGRESSION" round3
wants_round 4 && run "ROUND 4/5 — FRONTEND, SIX TARGET BUILDS AND CORE REGRESSION" round4
wants_round 5 && run "ROUND 5/5 — PACKAGE INTEGRITY, REAL LINUX INSTALL AND CORE REGRESSION" round5
if [ "$ROUNDS" = "1,2,3,4,5" ]; then
  printf '\nALL 5 ROUNDS PASSED — OmniShare v%s\n' "$VERSION" | tee -a "$LOG"
else
  printf '\nSELECTED ROUNDS %s PASSED — OmniShare v%s\n' "$ROUNDS" "$VERSION" | tee -a "$LOG"
fi
