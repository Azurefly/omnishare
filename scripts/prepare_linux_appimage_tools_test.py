#!/usr/bin/env python3
"""Portable regression tests; no GitHub token or live download is required."""

from contextlib import redirect_stdout
import hashlib
import http.client
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import urllib.error


SPEC = importlib.util.spec_from_file_location("prepare_appimage", Path(__file__).with_name("prepare-linux-appimage-tools.py"))
prepare = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = prepare
SPEC.loader.exec_module(prepare)
PAYLOAD = b"\x7fELF0123456789-original-upstream-tool"
ASSET = prepare.Asset("verified-tool", "https://api.github.com/repos/tauri-apps/binary-releases/releases/assets/1",
                      len(PAYLOAD), hashlib.sha256(PAYLOAD).hexdigest())


class Response(io.BytesIO):
    def __init__(self, body, url="https://release-assets.githubusercontent.com/upstream/tool", status=200):
        super().__init__(body)
        self.url, self.status = url, status

    def geturl(self):
        return self.url

    def getcode(self):
        return self.status


class Opener:
    def __init__(self, outcomes):
        self.outcomes = list(outcomes)
        self.requests = []

    def open(self, request, timeout):
        self.requests.append((request, timeout))
        outcome = self.outcomes.pop(0)
        if isinstance(outcome, BaseException):
            raise outcome
        return outcome if isinstance(outcome, Response) else Response(outcome)


class PreparationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cache = self.root / "cache"
        self.cache.mkdir()
        self.make_project()

    def write_json(self, path, content):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(content))

    def make_project(self, declared="2.12.4", installed="2.12.4", use_local=False):
        self.write_json(self.root / "desktop/package.json", {"devDependencies": {"@tauri-apps/cli": declared}})
        self.write_json(self.root / "desktop/node_modules/@tauri-apps/cli/package.json",
                        {"name": "@tauri-apps/cli", "version": installed})
        self.write_json(self.root / "desktop/src-tauri/tauri.conf.json", {"bundle": {"useLocalToolsDir": use_local}})

    def test_project_requires_declared_and_installed_exact_cli_version(self):
        with patch.object(prepare.platform, "system", return_value="Linux"), patch.object(prepare.platform, "machine", return_value="x86_64"):
            prepare.validate_project(self.root)
            for declared, installed in (("^2.12.4", "2.12.4"), ("2.12.3", "2.12.4"), ("2.12.4", "2.12.5")):
                with self.subTest(declared=declared, installed=installed):
                    self.make_project(declared, installed)
                    with self.assertRaisesRegex(prepare.PreparationError, "exactly 2.12.4"):
                        prepare.validate_project(self.root)
            (self.root / "desktop/node_modules/@tauri-apps/cli/package.json").unlink()
            with self.assertRaisesRegex(prepare.PreparationError, "missing or invalid"):
                prepare.validate_project(self.root)

    def test_project_rejects_wrong_platform_architecture_and_local_tool_directory(self):
        for system, machine in (("Darwin", "x86_64"), ("Linux", "aarch64"), ("Windows", "AMD64")):
            with self.subTest(system=system, machine=machine), patch.object(prepare.platform, "system", return_value=system), \
                    patch.object(prepare.platform, "machine", return_value=machine), self.assertRaises(prepare.PreparationError):
                prepare.validate_project(self.root)
        self.make_project(use_local=True)
        with patch.object(prepare.platform, "system", return_value="Linux"), patch.object(prepare.platform, "machine", return_value="x86_64"), \
                self.assertRaisesRegex(prepare.PreparationError, "useLocalToolsDir=false"):
            prepare.validate_project(self.root)

    def test_version_gate_precedes_any_download_or_cache_creation(self):
        self.make_project(installed="2.12.3")
        absent = self.root / "uncreated-cache"
        opener = Opener([])
        with patch.object(prepare.platform, "system", return_value="Linux"), patch.object(prepare.platform, "machine", return_value="x86_64"), \
                self.assertRaises(prepare.PreparationError):
            prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(absent)}, opener)
        self.assertFalse(absent.exists())
        self.assertEqual(opener.requests, [])

    def test_cache_path_matches_xdg_or_home_fallback(self):
        with patch.object(prepare.Path, "home", return_value=self.root):
            self.assertEqual(prepare.cache_directory({}), self.root / ".cache/tauri")
            self.assertEqual(prepare.cache_directory({"XDG_CACHE_HOME": "relative"}), self.root / ".cache/tauri")
            self.assertEqual(prepare.cache_directory({"XDG_CACHE_HOME": str(self.cache)}), self.cache / "tauri")

    def test_download_is_anonymous_verified_and_atomically_installed_executable(self):
        opener = Opener([PAYLOAD])
        original_replace = os.replace
        def inspect_replace(source, target):
            self.assertTrue(prepare.matches_asset(source, ASSET))
            self.assertEqual(source.stat().st_mode & 0o777, 0o755)
            self.assertFalse(target.exists())
            original_replace(source, target)
        with patch.dict(os.environ, {"GITHUB_TOKEN": "must-not-be-used", "GH_TOKEN": "also-private"}), \
                patch.object(prepare.os, "replace", side_effect=inspect_replace):
            self.assertEqual(prepare.prepare_asset(ASSET, self.cache, opener), "downloaded and verified")
        request, timeout = opener.requests[0]
        self.assertEqual(request.full_url, ASSET.url)
        self.assertEqual(request.get_header("Accept"), "application/octet-stream")
        self.assertIsNone(request.get_header("Authorization"))
        self.assertEqual(timeout, prepare.SOCKET_TIMEOUT)
        self.assertEqual((self.cache / ASSET.name).read_bytes(), PAYLOAD)
        self.assertEqual(list(self.cache.iterdir()), [self.cache / ASSET.name])

    def test_verified_cache_is_reused_without_download(self):
        target = self.cache / ASSET.name
        target.write_bytes(PAYLOAD)
        target.chmod(0o600)
        opener = Opener([])
        self.assertEqual(prepare.prepare_asset(ASSET, self.cache, opener), "verified cache")
        self.assertEqual(opener.requests, [])
        self.assertEqual(target.stat().st_mode & 0o777, 0o755)

    def test_linuxdeploy_mutated_bytes_trigger_download_of_original(self):
        asset = prepare.Asset("linuxdeploy-07333c6-x86_64.AppImage", ASSET.url, ASSET.size, ASSET.sha256)
        modified = bytearray(PAYLOAD)
        modified[8:11] = b"\0\0\0"
        target = self.cache / asset.name
        target.write_bytes(modified)
        opener = Opener([PAYLOAD])
        self.assertEqual(prepare.prepare_asset(asset, self.cache, opener), "downloaded and verified")
        self.assertEqual(len(opener.requests), 1)
        self.assertEqual(target.read_bytes(), PAYLOAD)

    def test_error_pages_wrong_hash_truncation_and_oversize_never_enter_cache(self):
        bodies = [b"<html>504 gateway timeout</html>", b"x" * ASSET.size, PAYLOAD[:-1], PAYLOAD + b"extra"]
        for body in bodies:
            with self.subTest(size=len(body)), self.assertRaises(prepare.PreparationError):
                prepare.prepare_asset(ASSET, self.cache, Opener([body]), attempts=1)
            self.assertEqual(list(self.cache.iterdir()), [])

    def test_partial_response_or_unapproved_redirect_is_rejected(self):
        for response in (Response(PAYLOAD, status=206), Response(PAYLOAD, url="http://api.github.com/asset"),
                         Response(PAYLOAD, url="https://third-party.example/tool")):
            with self.subTest(response=response.url), self.assertRaises(prepare.PreparationError):
                prepare.prepare_asset(ASSET, self.cache, Opener([response]), attempts=1)
            self.assertEqual(list(self.cache.iterdir()), [])

    def test_redirect_policy_rejects_downgrades_before_requesting_destination(self):
        handler = prepare.OfficialRedirects()
        request = prepare.urllib.request.Request(ASSET.url)
        for url in ("http://github.com/asset", "https://mirror.example/asset", "https://user:secret@api.github.com/asset",
                    "https://api.github.com:invalid/asset"):
            with self.subTest(url=url), self.assertRaises(prepare.PreparationError):
                handler.redirect_request(request, None, 302, "Found", {}, url)
        redirected = handler.redirect_request(request, None, 302, "Found", {}, "https://release-assets.githubusercontent.com/tool")
        self.assertEqual(redirected.full_url, "https://release-assets.githubusercontent.com/tool")

    def test_transient_download_failures_retry_a_bounded_number_of_times(self):
        waits = []
        opener = Opener([urllib.error.HTTPError(ASSET.url, 504, "Gateway timeout", {}, None),
                         http.client.IncompleteRead(b"partial", 20), PAYLOAD])
        self.assertEqual(prepare.prepare_asset(ASSET, self.cache, opener, sleep=waits.append), "downloaded and verified")
        self.assertEqual(len(opener.requests), 3)
        self.assertEqual(waits, [1, 2])
        self.assertEqual(list(self.cache.iterdir()), [self.cache / ASSET.name])

    def test_repeated_failure_preserves_existing_file_and_cleans_downloads(self):
        target = self.cache / ASSET.name
        target.write_bytes(b"old-invalid-cache")
        opener = Opener([TimeoutError(), TimeoutError(), TimeoutError()])
        with self.assertRaisesRegex(prepare.PreparationError, "after 3 attempts"):
            prepare.prepare_asset(ASSET, self.cache, opener, sleep=lambda _: None)
        self.assertEqual(len(opener.requests), 3)
        self.assertEqual(target.read_bytes(), b"old-invalid-cache")
        self.assertEqual(list(self.cache.iterdir()), [target])

    def test_cancelled_download_and_failed_atomic_replace_clean_temporary_files(self):
        with self.assertRaises(KeyboardInterrupt):
            prepare.prepare_asset(ASSET, self.cache, Opener([KeyboardInterrupt()]))
        self.assertEqual(list(self.cache.iterdir()), [])
        with patch.object(prepare.os, "replace", side_effect=OSError("disk error")), self.assertRaises(prepare.PreparationError):
            prepare.prepare_asset(ASSET, self.cache, Opener([PAYLOAD]), attempts=1)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_overall_download_deadline_cleans_temporary_files(self):
        with patch.object(prepare.time, "monotonic", side_effect=[0, prepare.DOWNLOAD_TIMEOUT + 1]), \
                self.assertRaisesRegex(prepare.PreparationError, "time limit"):
            prepare.prepare_asset(ASSET, self.cache, Opener([PAYLOAD]), attempts=1)
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_three_pinned_assets_are_prepared_using_expected_tauri_cache_names(self):
        self.assertEqual([asset.name for asset in prepare.ASSETS], ["AppRun-x86_64", "linuxdeploy-07333c6-x86_64.AppImage", "linuxdeploy-plugin-appimage.AppImage"])
        out = io.StringIO()
        with patch.object(prepare.platform, "system", return_value="Linux"), patch.object(prepare.platform, "machine", return_value="x86_64"), \
                patch.object(prepare, "prepare_asset", return_value="verified cache") as fetch, redirect_stdout(out):
            prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(self.cache)}, Opener([]))
        self.assertEqual([call.args[0] for call in fetch.call_args_list], list(prepare.ASSETS))
        self.assertTrue(all(call.args[1] == self.cache / "tauri" for call in fetch.call_args_list))
        self.assertIn("Tauri CLI 2.12.4", out.getvalue())


if __name__ == "__main__":
    unittest.main(verbosity=2)
