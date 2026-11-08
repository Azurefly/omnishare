#!/usr/bin/env python3
"""Prepare verified upstream AppImage tools for the pinned Tauri CLI.

Cache names follow tauri-cli 2.12.4, commit
993038c49185307dfc895ab7657d77ca0d5ed7c7 (tauri-bundler's linux/appimage).
GTK/GStreamer scripts are embedded in that CLI and need no network download.
"""

import argparse
from dataclasses import dataclass
import hashlib
import http.client
import json
import os
from pathlib import Path
import platform
import signal
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
CLI_VERSION = "2.12.4"
ATTEMPTS = 3
SOCKET_TIMEOUT = 45
DOWNLOAD_TIMEOUT = 180
ALLOWED_HOSTS = frozenset(("api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"))


@dataclass(frozen=True)
class Asset:
    name: str
    url: str
    size: int
    sha256: str


# Immutable release asset IDs, sizes and GitHub-published digests. In particular,
# never follow the AppImage plugin's moving "continuous" release tag.
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
)


class PreparationError(Exception):
    pass


def require_official_https(url):
    try:
        parsed = urllib.parse.urlsplit(url)
        allowed = (parsed.scheme == "https" and parsed.hostname in ALLOWED_HOSTS
                   and not parsed.username and not parsed.password and parsed.port in (None, 443))
    except ValueError:
        allowed = False
    if not allowed:
        raise PreparationError("AppImage tools may only be downloaded over official GitHub HTTPS endpoints.")


class OfficialRedirects(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        require_official_https(new_url)
        return super().redirect_request(request, fp, code, message, headers, new_url)


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


def matches_asset(path, asset):
    if path.is_symlink() or not path.is_file() or path.stat().st_size != asset.size:
        return False
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest() == asset.sha256


def download_asset(asset, cache, opener):
    require_official_https(asset.url)
    temporary = None
    try:
        fd, filename = tempfile.mkstemp(prefix="." + asset.name + ".", suffix=".download", dir=cache)
        temporary = Path(filename)
        # No token, authorization header or user credential is consulted.
        request = urllib.request.Request(asset.url, headers={
            "Accept": "application/octet-stream", "User-Agent": "OmniShare-AppImage-Tools",
            "X-GitHub-Api-Version": "2022-11-28",
        })
        deadline = time.monotonic() + DOWNLOAD_TIMEOUT
        with os.fdopen(fd, "wb") as destination:
            with opener.open(request, timeout=SOCKET_TIMEOUT) as response:
                require_official_https(response.geturl())
                if response.getcode() != 200:
                    raise PreparationError("GitHub did not return a complete asset response.")
                total = 0
                digest = hashlib.sha256()
                # read1 returns after a socket read, allowing a bounded overall
                # deadline even if a peer continually sends very small chunks.
                read = getattr(response, "read1", response.read)
                while True:
                    if time.monotonic() > deadline:
                        raise PreparationError("Asset download exceeded its time limit.")
                    chunk = read(min(1024 * 1024, asset.size - total + 1))
                    if not chunk:
                        break
                    total += len(chunk)
                    if total > asset.size:
                        raise PreparationError("Downloaded asset exceeds its pinned size.")
                    digest.update(chunk)
                    destination.write(chunk)
                if total != asset.size:
                    raise PreparationError("Downloaded asset is truncated or has an unexpected size.")
                if digest.hexdigest() != asset.sha256:
                    raise PreparationError("Downloaded asset does not match its pinned SHA256.")
            destination.flush()
            os.fsync(destination.fileno())
        temporary.chmod(0o755)
        os.replace(temporary, cache / asset.name)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def prepare_asset(asset, cache, opener, sleep=time.sleep, attempts=ATTEMPTS):
    target = cache / asset.name
    if matches_asset(target, asset):
        target.chmod(0o755)
        return "verified cache"
    # Tauri legitimately zeroes linuxdeploy's ELF bytes at offsets 8..10. That
    # modified cache is not the published artifact: obtain fresh original bytes.
    for attempt in range(attempts):
        try:
            download_asset(asset, cache, opener)
            return "downloaded and verified"
        except urllib.error.HTTPError as error:
            failure = "GitHub returned HTTP " + str(error.code) + "."
            error.close()
        except (urllib.error.URLError, TimeoutError, http.client.HTTPException):
            failure = "The GitHub asset download failed or timed out."
        except OSError:
            failure = "An I/O error interrupted asset preparation."
        except PreparationError as error:
            failure = str(error)
        if attempt + 1 < attempts:
            sleep(2 ** attempt)
    raise PreparationError("Unable to prepare " + asset.name + " after " + str(attempts) + " attempts. " + failure)


def prepare_tools(root=ROOT, env=None, opener=None):
    validate_project(root)
    cache = cache_directory(env)
    cache.mkdir(parents=True, exist_ok=True)
    opener = opener or urllib.request.build_opener(OfficialRedirects())
    for asset in ASSETS:
        result = prepare_asset(asset, cache, opener)
        print(asset.name + ": " + result)
    print("AppImage tools verified for Tauri CLI " + CLI_VERSION + ".")


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
