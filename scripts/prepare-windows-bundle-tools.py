#!/usr/bin/env python3
"""Prepare verified WiX/NSIS tools for Tauri CLI 2.12.4 on Windows x64.

Layout and source digests follow Tauri commit
993038c49185307dfc895ab7657d77ca0d5ed7c7, windows/msi and windows/nsis.
This script downloads and extracts tools; it never executes their contents.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import stat
import sys
import tempfile
import urllib.request
import uuid
import zipfile

from release_tool_downloads import Asset, OfficialRedirects, PreparationError, prepare_asset


ROOT = Path(__file__).resolve().parent.parent
CLI_VERSION = "2.12.4"
ASSETS = (
    Asset("wix314-binaries.zip", "https://api.github.com/repos/wixtoolset/wix3/releases/assets/158158052",
          41297555, "6ac824e1642d6f7277d0ed7ea09411a508f6116ba6fae0aa5f2c7daa2ff43d31"),
    Asset("nsis-3.13.zip", "https://api.github.com/repos/tauri-apps/binary-releases/releases/assets/623832696",
          2363925, "ba63dffc4410ee89193e1cb5a41989991bd77c61068da17e3156d136b7b0b3d8"),
    Asset("nsis_tauri_utils.dll", "https://api.github.com/repos/tauri-apps/nsis-tauri-utils/releases/assets/345882097",
          34304, "5ba143b5db4a87d32d6e7802e033330aae56cbceabe0d1e3ba41948385ad4709"),
)
NSIS_ZIP_SHA1 = "db14f8af2ea346b786c6ae19343f61aaf5e09b8e"
NSIS_PLUGIN_SHA1 = "75197fee3c6a814fe035788d1c34ead39349b860"
NSIS_EXE_SHA1 = "6d45d4e8ccf18fe1fdac9164058f356102a2ecf2"
WIX_REQUIRED_FILES = (
    "candle.exe", "candle.exe.config", "darice.cub", "light.exe", "light.exe.config",
    "wconsole.dll", "winterop.dll", "wix.dll", "WixUIExtension.dll", "WixUtilExtension.dll",
)
NSIS_PLUGIN = "Plugins/x86-unicode/additional/nsis_tauri_utils.dll"
NSIS_REQUIRED_FILES = (
    "makensis.exe", "Bin/makensis.exe", "Stubs/lzma-x86-unicode", "Stubs/lzma_solid-x86-unicode",
    NSIS_PLUGIN, "Include/MUI2.nsh", "Include/FileFunc.nsh", "Include/x64.nsh", "Include/nsDialogs.nsh",
    "Include/WinMessages.nsh", "Include/Win/COM.nsh", "Include/Win/Propkey.nsh", "Include/Win/RestartManager.nsh",
)
MAX_ZIP_ENTRIES = 10000
MAX_MEMBER_BYTES = 256 * 1024 * 1024
MAX_EXPANDED_BYTES = 512 * 1024 * 1024
RESERVED_NAME = re.compile(r"^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)", re.IGNORECASE)


def read_json(path):
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (OSError, ValueError):
        raise PreparationError("Required Tauri package or configuration metadata is missing or invalid.") from None


def validate_project(root=ROOT):
    if platform.system() != "Windows" or platform.machine().lower() not in ("amd64", "x86_64"):
        raise PreparationError("Windows bundle tool preparation supports Windows x64 only.")
    desktop = root / "desktop"
    declared = read_json(desktop / "package.json")
    installed = read_json(desktop / "node_modules/@tauri-apps/cli/package.json")
    dependencies = declared.get("devDependencies")
    if (not isinstance(dependencies, dict) or dependencies.get("@tauri-apps/cli") != CLI_VERSION
            or installed.get("name") != "@tauri-apps/cli" or installed.get("version") != CLI_VERSION):
        raise PreparationError("Both declared and installed @tauri-apps/cli versions must be exactly " + CLI_VERSION + ".")
    bundle = read_json(desktop / "src-tauri/tauri.conf.json").get("bundle", {})
    if not isinstance(bundle, dict) or bundle.get("useLocalToolsDir", False) is not False:
        raise PreparationError("Windows tool preparation requires bundle.useLocalToolsDir=false.")


def cache_directory(env=None):
    env = os.environ if env is None else env
    local = env.get("LOCALAPPDATA", "")
    if not local or not Path(local).is_absolute():
        raise PreparationError("An absolute LOCALAPPDATA directory is required for Tauri's Windows cache.")
    return Path(local) / "tauri"


def is_link(path):
    return path.is_symlink() or getattr(path, "is_junction", lambda: False)()


def file_hash(path, algorithm="sha256"):
    digest = hashlib.new(algorithm)
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def safe_extract_zip(archive_path, destination, prefix=None):
    """Validate the entire archive before writing; reject Windows path aliases."""
    try:
        with zipfile.ZipFile(archive_path) as archive:
            entries = archive.infolist()
            if len(entries) > MAX_ZIP_ENTRIES:
                raise PreparationError("Tool archive contains too many entries.")
            plan, seen, files, directories = [], set(), set(), set()
            total = 0
            for info in entries:
                name = info.filename.replace("\\", "/")
                parts = name.rstrip("/").split("/")
                file_type = stat.S_IFMT(info.external_attr >> 16)
                directory = name.endswith("/") or file_type == stat.S_IFDIR
                if (info.orig_filename != info.filename or not name or name.startswith("/") or file_type not in (0, stat.S_IFREG, stat.S_IFDIR)
                        or info.flag_bits & 1 or any(
                            part in ("", ".", "..") or ":" in part or "\0" in part
                            or part.rstrip(" .") != part or RESERVED_NAME.match(part) for part in parts)):
                    raise PreparationError("Tool archive contains an unsafe path, link or encrypted entry.")
                if prefix is not None:
                    if parts[0] != prefix:
                        raise PreparationError("Tool archive does not have its expected root directory.")
                    parts = parts[1:]
                    if not parts:
                        if directory:
                            continue
                        raise PreparationError("Tool archive has an invalid root entry.")
                key = "/".join(parts).casefold()
                parents = ["/".join(parts[:i]).casefold() for i in range(1, len(parts))]
                if key in seen or any(parent in files for parent in parents) or (not directory and key in directories):
                    raise PreparationError("Tool archive contains conflicting Windows paths.")
                seen.add(key)
                directories.update(parents)
                (directories if directory else files).add(key)
                total += info.file_size
                if info.file_size > MAX_MEMBER_BYTES or total > MAX_EXPANDED_BYTES:
                    raise PreparationError("Tool archive exceeds its extraction limit.")
                plan.append((info, parts, directory))
            destination.mkdir(parents=True, exist_ok=False)
            for info, parts, directory in plan:
                target = destination.joinpath(*parts)
                if directory:
                    target.mkdir(parents=True, exist_ok=True)
                else:
                    target.parent.mkdir(parents=True, exist_ok=True)
                    with archive.open(info) as source, target.open("xb") as output:
                        shutil.copyfileobj(source, output, length=1024 * 1024)
                    if target.stat().st_size != info.file_size:
                        raise PreparationError("Extracted tool file has an unexpected size.")
    except (zipfile.BadZipFile, RuntimeError, NotImplementedError):
        raise PreparationError("Tool archive could not be safely extracted.") from None


def require_files(directory, required):
    if any(is_link(directory / name) or not (directory / name).is_file()
           or (directory / name).stat().st_size == 0 for name in required):
        raise PreparationError("Tool archive is missing files required by the pinned Tauri CLI.")


def tree_manifest(directory):
    if is_link(directory) or not directory.is_dir():
        return None
    result = {}
    for parent, directories, files in os.walk(directory, followlinks=False):
        for name in directories:
            path = Path(parent) / name
            if is_link(path):
                return None
            result[path.relative_to(directory).as_posix()] = ("directory",)
        for name in files:
            path = Path(parent) / name
            if is_link(path) or not path.is_file():
                return None
            result[path.relative_to(directory).as_posix()] = ("file", path.stat().st_size, file_hash(path))
    return result


def install_tree(staged, target):
    if is_link(target):
        raise PreparationError("Refusing to replace a linked tool cache directory.")
    if tree_manifest(target) == tree_manifest(staged):
        return "verified cache"
    previous = target.parent / ("." + target.name + ".previous-" + uuid.uuid4().hex)
    had_previous = target.exists()
    try:
        if had_previous:
            os.replace(target, previous)
        # Publish only a fully extracted and verified directory, never merge it
        # into the old cache. Windows cannot atomically overwrite nonempty dirs.
        os.replace(staged, target)
    except BaseException:
        # A signal can arrive after either rename took effect but before Python
        # returns from it. Recover from the filesystem state, not a flag written
        # after the call. Move our new tree aside before restoring a nonempty dir.
        if previous.exists():
            if target.exists() and not staged.exists():
                os.replace(target, staged)
            if not target.exists():
                os.replace(previous, target)
        elif not had_previous and target.exists() and not staged.exists():
            # An interrupted first install has no prior tree to restore.
            os.replace(target, staged)
        raise
    if had_previous:
        if previous.is_dir():
            shutil.rmtree(previous)
        else:
            previous.unlink()
    return "installed verified tools"


def prepare_tools(root=ROOT, env=None, opener=None):
    validate_project(root)
    cache = cache_directory(env)
    cache.mkdir(parents=True, exist_ok=True)
    downloads = cache / ".omnishare-tool-downloads"
    downloads.mkdir(exist_ok=True)
    opener = opener or urllib.request.build_opener(OfficialRedirects())
    for asset in ASSETS:
        result = prepare_asset(asset, downloads, opener)
        print(asset.name + ": " + result)
    # Keep Tauri's own pinned SHA1 checks in addition to stronger archive SHA256.
    if (file_hash(downloads / ASSETS[1].name, "sha1") != NSIS_ZIP_SHA1
            or file_hash(downloads / ASSETS[2].name, "sha1") != NSIS_PLUGIN_SHA1):
        raise PreparationError("Downloaded NSIS tools do not match the pinned Tauri source checksums.")
    with tempfile.TemporaryDirectory(prefix=".omnishare-windows-tools-", dir=cache) as temporary:
        work = Path(temporary)
        wix, nsis = work / "WixTools314", work / "NSIS"
        safe_extract_zip(downloads / ASSETS[0].name, wix)
        safe_extract_zip(downloads / ASSETS[1].name, nsis, prefix="nsis-3.13")
        plugin = nsis / NSIS_PLUGIN
        plugin.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(downloads / ASSETS[2].name, plugin)
        require_files(wix, WIX_REQUIRED_FILES)
        require_files(nsis, (*NSIS_REQUIRED_FILES, "NSIS.exe"))
        if file_hash(nsis / "NSIS.exe", "sha1") != NSIS_EXE_SHA1:
            raise PreparationError("NSIS.exe does not match the pinned Tauri source checksum.")
        # Validate both tool trees before changing either existing cache.
        for staged in (wix, nsis):
            print(staged.name + ": " + install_tree(staged, cache / staged.name))
    print("Windows bundle tools verified for Tauri CLI " + CLI_VERSION + ".")


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
        print("Windows tool cache could not be prepared.", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("Windows tool preparation was cancelled.", file=sys.stderr)
        return 130
    finally:
        signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    sys.exit(main())
