#!/usr/bin/env bash
set -euo pipefail
umask 022

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Credentials stay in the environment, never in this wrapper's arguments or logs.
exec python3 "$ROOT/scripts/macos-signing.py" "$@"
