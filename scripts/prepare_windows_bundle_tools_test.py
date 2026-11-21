#!/usr/bin/env python3
"""Windows bundler preparation tests; fixtures never execute downloaded code."""

from contextlib import ExitStack, redirect_stdout
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

from release_tool_downloads import Asset, PreparationError


SPEC = importlib.util.spec_from_file_location("prepare_windows", Path(__file__).with_name("prepare-windows-bundle-tools.py"))
prepare = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = prepare
SPEC.loader.exec_module(prepare)
PLUGIN = b"synthetic-nsis-plugin"
NSIS_EXE = b"synthetic-nsis-exe"


def archive_bytes(entries):
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, content in entries:
            info = name if isinstance(name, zipfile.ZipInfo) else zipfile.ZipInfo(name)
            # Identical fixtures must keep the same archive digest across calls.
            info.date_time = (2024, 1, 1, 0, 0, 0)
            archive.writestr(info, content, compress_type=zipfile.ZIP_DEFLATED)
    return buffer.getvalue()


class Response(io.BytesIO):
    def geturl(self):
        return "https://release-assets.githubusercontent.com/verified-tool"

    def getcode(self):
        return 200


class Opener:
    def __init__(self, bodies):
        self.bodies = list(bodies)
        self.requests = []

    def open(self, request, timeout):
        self.requests.append(request)
        return Response(self.bodies.pop(0))


class WindowsPreparationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.local = self.root / "LocalAppData"
        self.cache = self.local / "tauri"
        self.make_project()

    def write_json(self, path, data):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data))

    def make_project(self, declared="2.12.4", installed="2.12.4", local=False):
        self.write_json(self.root / "desktop/package.json", {"devDependencies": {"@tauri-apps/cli": declared}})
        self.write_json(self.root / "desktop/node_modules/@tauri-apps/cli/package.json",
                        {"name": "@tauri-apps/cli", "version": installed})
        self.write_json(self.root / "desktop/src-tauri/tauri.conf.json", {"bundle": {"useLocalToolsDir": local}})

    def fixtures(self, missing_wix=None, missing_nsis=None):
        wix = archive_bytes([(name, b"wix:" + name.encode()) for name in prepare.WIX_REQUIRED_FILES if name != missing_wix])
        entries = [("nsis-3.13/", b"")]
        entries += [("nsis-3.13/" + name, b"nsis:" + name.encode())
                    for name in prepare.NSIS_REQUIRED_FILES if name not in (prepare.NSIS_PLUGIN, missing_nsis)]
        entries.append(("nsis-3.13/NSIS.exe", NSIS_EXE))
        nsis = archive_bytes(entries)
        bodies = [wix, nsis, PLUGIN]
        assets = tuple(Asset(original.name, original.url, len(body), hashlib.sha256(body).hexdigest())
                       for original, body in zip(prepare.ASSETS, bodies))
        return assets, bodies

    def environment(self, assets, bodies):
        stack = ExitStack()
        stack.enter_context(patch.object(prepare.platform, "system", return_value="Windows"))
        stack.enter_context(patch.object(prepare.platform, "machine", return_value="AMD64"))
        stack.enter_context(patch.object(prepare, "ASSETS", assets))
        stack.enter_context(patch.object(prepare, "NSIS_ZIP_SHA1", hashlib.sha1(bodies[1]).hexdigest()))
        stack.enter_context(patch.object(prepare, "NSIS_PLUGIN_SHA1", hashlib.sha1(PLUGIN).hexdigest()))
        stack.enter_context(patch.object(prepare, "NSIS_EXE_SHA1", hashlib.sha1(NSIS_EXE).hexdigest()))
        stack.enter_context(redirect_stdout(io.StringIO()))
        return stack

    def run_preparation(self, assets, bodies, opener=None):
        opener = opener or Opener(bodies)
        with self.environment(assets, bodies):
            prepare.prepare_tools(self.root, {"LOCALAPPDATA": str(self.local)}, opener)
        return opener

    def write_zip(self, entries):
        path = self.root / "input.zip"
        path.write_bytes(archive_bytes(entries))
        return path

    def test_exact_cli_platform_and_cache_layout_gates(self):
        with patch.object(prepare.platform, "system", return_value="Windows"), patch.object(prepare.platform, "machine", return_value="AMD64"):
            prepare.validate_project(self.root)
            for declared, installed, local in (("^2.12.4", "2.12.4", False), ("2.12.4", "2.12.3", False), ("2.12.4", "2.12.4", True)):
                self.make_project(declared, installed, local)
                with self.subTest(declared=declared, installed=installed, local=local), self.assertRaises(PreparationError):
                    prepare.validate_project(self.root)
        for system, arch in (("Linux", "x86_64"), ("Windows", "ARM64")):
            with patch.object(prepare.platform, "system", return_value=system), patch.object(prepare.platform, "machine", return_value=arch), \
                    self.assertRaises(PreparationError):
                prepare.validate_project(self.root)
        self.assertEqual(prepare.cache_directory({"LOCALAPPDATA": str(self.local)}), self.cache)
        for env in ({}, {"LOCALAPPDATA": "relative"}):
            with self.assertRaises(PreparationError):
                prepare.cache_directory(env)

    def test_version_mismatch_does_not_download_or_create_cache(self):
        self.make_project(installed="2.12.5")
        opener = Opener([])
        with patch.object(prepare.platform, "system", return_value="Windows"), patch.object(prepare.platform, "machine", return_value="AMD64"), \
                self.assertRaises(PreparationError):
            prepare.prepare_tools(self.root, {"LOCALAPPDATA": str(self.local)}, opener)
        self.assertEqual(opener.requests, [])
        self.assertFalse(self.local.exists())

    def test_verified_assets_create_complete_tauri_layout_and_reuse_matching_cache(self):
        assets, bodies = self.fixtures()
        opener = self.run_preparation(assets, bodies)
        self.assertEqual(len(opener.requests), 3)
        self.assertTrue(all(request.get_header("Authorization") is None for request in opener.requests))
        for name in prepare.WIX_REQUIRED_FILES:
            self.assertTrue((self.cache / "WixTools314" / name).is_file())
        for name in (*prepare.NSIS_REQUIRED_FILES, "NSIS.exe"):
            self.assertTrue((self.cache / "NSIS" / name).is_file())
        self.assertFalse((self.cache / "nsis-3.13").exists())
        self.assertEqual((self.cache / "NSIS" / prepare.NSIS_PLUGIN).read_bytes(), PLUGIN)
        retry = Opener([])
        with patch.object(prepare.os, "replace", side_effect=AssertionError("valid cache replaced")):
            self.run_preparation(assets, bodies, retry)
        self.assertEqual(retry.requests, [])
        self.assertEqual({path.name for path in self.cache.iterdir()}, {".omnishare-tool-downloads", "WixTools314", "NSIS"})

    def test_changed_and_extra_cached_files_are_replaced_from_verified_originals(self):
        assets, bodies = self.fixtures()
        self.run_preparation(assets, bodies)
        candle = self.cache / "WixTools314/candle.exe"
        candle.write_bytes(b"tampered")
        extra = self.cache / "NSIS/untrusted-extra.dll"
        extra.write_bytes(b"unexpected")
        self.run_preparation(assets, bodies, Opener([]))
        self.assertEqual(candle.read_bytes(), b"wix:candle.exe")
        self.assertFalse(extra.exists())
        self.assertFalse(any(".previous-" in path.name for path in self.cache.iterdir()))

    def test_both_staged_trees_are_validated_before_existing_cache_changes(self):
        assets, bodies = self.fixtures()
        self.run_preparation(assets, bodies)
        before = prepare.tree_manifest(self.cache / "WixTools314")
        bad_assets, bad_bodies = self.fixtures(missing_nsis="Bin/makensis.exe")
        # Only the second raw archive needs replacing; the other verified assets
        # remain in the download cache and must not be requested again.
        with self.environment(bad_assets, bad_bodies), self.assertRaisesRegex(PreparationError, "missing files"):
            prepare.prepare_tools(self.root, {"LOCALAPPDATA": str(self.local)}, Opener([bad_bodies[1]]))
        self.assertEqual(prepare.tree_manifest(self.cache / "WixTools314"), before)
        self.assertTrue((self.cache / "NSIS/Bin/makensis.exe").is_file())
        self.assertFalse(any(path.name.startswith(".omnishare-windows-tools-") for path in self.cache.iterdir()))

    def test_missing_wix_required_file_and_wrong_nsis_exe_hash_block_installation(self):
        assets, bodies = self.fixtures(missing_wix="light.exe")
        with self.assertRaisesRegex(PreparationError, "missing files"):
            self.run_preparation(assets, bodies)
        self.assertFalse((self.cache / "WixTools314").exists())
        assets, bodies = self.fixtures()
        with self.environment(assets, bodies), patch.object(prepare, "NSIS_EXE_SHA1", "0" * 40), \
                self.assertRaisesRegex(PreparationError, "NSIS.exe"):
            prepare.prepare_tools(self.root, {"LOCALAPPDATA": str(self.local)}, Opener([bodies[0]]))
        self.assertFalse((self.cache / "NSIS").exists())

    def test_tauri_archive_and_plugin_sha1_checks_remain_enforced(self):
        assets, bodies = self.fixtures()
        for name in ("NSIS_ZIP_SHA1", "NSIS_PLUGIN_SHA1"):
            with self.subTest(name=name), self.environment(assets, bodies), patch.object(prepare, name, "0" * 40), \
                    self.assertRaisesRegex(PreparationError, "source checksums"):
                prepare.prepare_tools(self.root, {"LOCALAPPDATA": str(self.local)}, Opener(bodies))
            self.assertFalse((self.cache / "WixTools314").exists())

    def test_zip_accepts_normal_directories_and_windows_separators(self):
        archive = self.write_zip([("nsis-3.13/", b""), ("nsis-3.13/Include/", b""),
                                  ("nsis-3.13\\Include\\MUI2.nsh", b"safe")])
        destination = self.root / "extracted"
        prepare.safe_extract_zip(archive, destination, prefix="nsis-3.13")
        self.assertEqual((destination / "Include/MUI2.nsh").read_bytes(), b"safe")

    def test_zip_rejects_traversal_absolute_ads_reserved_and_ambiguous_windows_paths(self):
        names = ("../escape", "folder/../../escape", "..\\escape", "/absolute", "C:/escape", "//server/share", "file:stream",
                 "NUL.txt", "folder/COM1", "name. ", "folder//file")
        for name in names:
            with self.subTest(name=name), self.assertRaises(PreparationError):
                prepare.safe_extract_zip(self.write_zip([(name, b"unsafe")]), self.root / "output")
            self.assertFalse((self.root / "output").exists())
        self.assertFalse((self.root.parent / "escape").exists())

    def test_zip_rejects_links_case_collisions_file_parent_conflicts_and_wrong_prefix(self):
        link = zipfile.ZipInfo("linked.exe")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        cases = [([(link, b"/outside")], None), ([("File.dll", b"x"), ("file.dll", b"y")], None),
                 ([("file", b"x"), ("file/child", b"y")], None),
                 ([("file/child", b"x"), ("file", b"y")], None), ([("wrong-root/file", b"x")], "nsis-3.13")]
        for entries, prefix in cases:
            with self.subTest(entries=entries), self.assertRaises(PreparationError):
                prepare.safe_extract_zip(self.write_zip(entries), self.root / "output", prefix)
            self.assertFalse((self.root / "output").exists())

    def test_zip_rejects_raw_nul_filename_before_zipfile_truncation(self):
        archive = self.root / "nul.zip"
        archive.write_bytes(archive_bytes([("unsafeXtail", b"content")]).replace(b"unsafeXtail", b"unsafe\0tail"))
        with zipfile.ZipFile(archive) as source:
            self.assertEqual(source.infolist()[0].filename, "unsafe")
            self.assertNotEqual(source.infolist()[0].orig_filename, source.infolist()[0].filename)
        with self.assertRaises(PreparationError):
            prepare.safe_extract_zip(archive, self.root / "output")
        self.assertFalse((self.root / "output").exists())

    def test_zip_extraction_size_and_entry_limits_are_enforced_before_writing(self):
        for constant, value, entries in (("MAX_ZIP_ENTRIES", 1, [("a", b"x"), ("b", b"y")]),
                                         ("MAX_MEMBER_BYTES", 2, [("a", b"long")]),
                                         ("MAX_EXPANDED_BYTES", 3, [("a", b"xx"), ("b", b"yy")])):
            with self.subTest(constant=constant), patch.object(prepare, constant, value), self.assertRaises(PreparationError):
                prepare.safe_extract_zip(self.write_zip(entries), self.root / "output")
            self.assertFalse((self.root / "output").exists())

    def test_directory_install_restores_previous_cache_if_windows_rename_fails(self):
        staged, target = self.root / "staged", self.root / "target"
        staged.mkdir()
        target.mkdir()
        (staged / "tool.exe").write_bytes(b"verified new")
        (target / "tool.exe").write_bytes(b"previous")
        original = os.replace
        def fail_install(source, destination):
            if source == staged:
                raise PermissionError("open handle")
            original(source, destination)
        with patch.object(prepare.os, "replace", side_effect=fail_install), self.assertRaises(PermissionError):
            prepare.install_tree(staged, target)
        self.assertEqual((target / "tool.exe").read_bytes(), b"previous")
        self.assertEqual((staged / "tool.exe").read_bytes(), b"verified new")
        self.assertFalse(any(".previous-" in path.name for path in self.root.iterdir()))

    def test_install_rejects_a_linked_cache_target_without_touching_referent(self):
        staged, outside, target = self.root / "staged", self.root / "outside", self.root / "target"
        staged.mkdir()
        outside.mkdir()
        (staged / "tool.exe").write_bytes(b"new")
        (outside / "tool.exe").write_bytes(b"keep")
        try:
            target.symlink_to(outside, target_is_directory=True)
        except OSError:
            self.skipTest("Symlink creation unavailable on this test host")
        with self.assertRaises(PreparationError):
            prepare.install_tree(staged, target)
        self.assertEqual((outside / "tool.exe").read_bytes(), b"keep")

    def test_cancellation_after_either_rename_took_effect_restores_previous_cache(self):
        for boundary in ("backup", "install"):
            with self.subTest(boundary=boundary):
                base = self.root / boundary
                base.mkdir()
                staged, target = base / "staged", base / "target"
                staged.mkdir()
                target.mkdir()
                (staged / "tool.exe").write_bytes(b"verified new")
                (target / "tool.exe").write_bytes(b"previous")
                original = os.replace
                interrupted = False
                def interrupt_after_rename(source, destination):
                    nonlocal interrupted
                    original(source, destination)
                    selected = source == target if boundary == "backup" else source == staged
                    if selected and not interrupted:
                        interrupted = True
                        raise KeyboardInterrupt()
                with patch.object(prepare.os, "replace", side_effect=interrupt_after_rename), self.assertRaises(KeyboardInterrupt):
                    prepare.install_tree(staged, target)
                self.assertTrue(interrupted)
                self.assertEqual((target / "tool.exe").read_bytes(), b"previous")
                self.assertEqual((staged / "tool.exe").read_bytes(), b"verified new")
                self.assertEqual({path.name for path in base.iterdir()}, {"staged", "target"})


if __name__ == "__main__":
    unittest.main(verbosity=2)
