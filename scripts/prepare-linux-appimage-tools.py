#!/usr/bin/env python3
"""Prepare verified upstream AppImage tools for the pinned Tauri CLI.

Cache names follow tauri-cli 2.12.4, commit
993038c49185307dfc895ab7657d77ca0d5ed7c7 (tauri-bundler's linux/appimage).
GTK/GStreamer scripts are embedded in that CLI and need no network download.
The output plugin receives the verified type2 runtime through LDAI_RUNTIME_FILE.
"""

import argparse
import json
import os
from pathlib import Path
import platform
import signal
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

from release_tool_downloads import (
    ALLOWED_HOSTS, ATTEMPTS, DOWNLOAD_TIMEOUT, SOCKET_TIMEOUT, Asset,
    OfficialRedirects, PreparationError, download_asset, matches_asset,
    prepare_asset, require_official_https,
)


ROOT = Path(__file__).resolve().parent.parent
CLI_VERSION = "2.12.4"


# Immutable release asset IDs, sizes and GitHub-published digests. In particular,
# never follow the plugin or type2 runtime's moving "continuous" release tag.
ASSETS = (
    Asset("AppRun-x86_64",
          "https://api.github.com/repos/tauri-apps/binary-releases/releases/assets/274691722",
          31552, "f30140a43a0a59e46db21bdefdf749b9e9f2c6946e92afabbacf98b8ae73fb4f"),
    Asset("linuxdeploy-07333c6-x86_64.AppImage",
          "https://api.github.com/repos/tauri-apps/binary-releases/releases/assets/576945544",
          19810808, "36a2d7e274d12e1050d0e9ecfe11d339ed54720b2bec464c286d53f8b07f5c62"),
    Asset("linuxdeploy-plugin-appimage.AppImage",
          "https://api.github.com/repos/linuxdeploy/linuxdeploy-plugin-appimage/releases/assets/602435573",
          16488952, "49d6a17160675a6bd1781699aae6bdf7692d98552e02a3671d2183d10547842e"),
    Asset("runtime-x86_64",
          "https://api.github.com/repos/AppImage/type2-runtime/releases/assets/657374641",
          944632, "617aa77314d57c8b8e4fae9c3f0b35af7b30d5529cb69e98f64f92510ed98a46"),
)


def read_json(path):
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (OSError, ValueError):
        raise PreparationError("Required Tauri package or configuration metadata is missing or invalid.") from None


def validate_project(root=ROOT):
    if platform.system() != "Linux" or platform.machine().lower() not in ("x86_64", "amd64"):
        raise PreparationError("This AppImage tool preparation supports Linux x86_64 only.")
    desktop = root / "desktop"
    declared = read_json(desktop / "package.json")
    installed = read_json(desktop / "node_modules/@tauri-apps/cli/package.json")
    dependencies = declared.get("devDependencies")
    if (not isinstance(dependencies, dict) or dependencies.get("@tauri-apps/cli") != CLI_VERSION
            or installed.get("name") != "@tauri-apps/cli" or installed.get("version") != CLI_VERSION):
        raise PreparationError("Both declared and installed @tauri-apps/cli versions must be exactly " + CLI_VERSION + ".")
    config = read_json(desktop / "src-tauri/tauri.conf.json")
    bundle = config.get("bundle", {})
    if not isinstance(bundle, dict) or bundle.get("useLocalToolsDir", False) is not False:
        raise PreparationError("AppImage tool preparation requires bundle.useLocalToolsDir=false.")


def cache_directory(env=None):
    env = os.environ if env is None else env
    xdg = env.get("XDG_CACHE_HOME", "")
    # Match dirs::cache_dir on Linux: relative XDG paths are not valid.
    base = Path(xdg) if xdg and Path(xdg).is_absolute() else Path.home() / ".cache"
    return base / "tauri"


def prepare_tools(root=ROOT, env=None, opener=None):
    validate_project(root)
    env = os.environ if env is None else env
    cache = cache_directory(env)
    cache.mkdir(parents=True, exist_ok=True)
    opener = opener or urllib.request.build_opener(OfficialRedirects())
    for asset in ASSETS:
        result = prepare_asset(asset, cache, opener)
        print(asset.name + ": " + result)
    runtime = cache / "runtime-x86_64"
    if runtime.is_symlink() or not runtime.is_file():
        raise PreparationError("Verified AppImage runtime must be a regular file, not a symbolic link.")
    # appimagetool can change its working directory; pass a canonical absolute
    # path rather than relying on its inherited working directory.
    runtime = runtime.resolve(strict=True)
    runtime_path = str(runtime)
    if "\r" in runtime_path or "\n" in runtime_path:
        raise PreparationError("AppImage runtime path cannot contain newlines.")
    output = env.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as handle:
            handle.write("runtime_path=" + runtime_path + "\n")
    print("AppImage tools verified for Tauri CLI " + CLI_VERSION + ".")
    print("AppImage runtime path: " + runtime_path)
    return runtime


def main(argv=None):
    argparse.ArgumentParser(description=__doc__).parse_args(argv)
    previous = signal.getsignal(signal.SIGTERM)
    def terminate(_signum, _frame):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, terminate)
    try:
        prepare_tools()
        return 0
    except PreparationError as error:
        print(str(error), file=sys.stderr)
        return 1
    except OSError:
        print("AppImage tool cache could not be prepared.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("AppImage tool preparation was cancelled.", file=sys.stderr)
        return 130
    finally:
        signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    sys.exit(main())
