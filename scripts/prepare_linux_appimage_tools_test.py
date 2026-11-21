#!/usr/bin/env python3
"""Portable regression tests; no GitHub token or live download is required."""

from contextlib import contextmanager, redirect_stdout
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

    @contextmanager
    def fixture_tools(self):
        assets = tuple(prepare.Asset(asset.name, asset.url, len(PAYLOAD), hashlib.sha256(PAYLOAD).hexdigest())
                       for asset in prepare.ASSETS)
        with patch.object(prepare, "ASSETS", assets), patch.object(prepare.platform, "system", return_value="Linux"), \
                patch.object(prepare.platform, "machine", return_value="x86_64"):
            yield assets

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
        with patch.dict(os.environ, {"GITHUB_TOKEN": "must-not-be-used", "GH_TOKEN": "also-private", "OMNISHARE_TOOL_GITHUB_TOKEN": ""}), \
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

    def test_explicit_credential_is_sent_only_on_initial_api_request_never_redirected(self):
        token = "explicit-private-token"
        opener = Opener([PAYLOAD])
        with patch.dict(os.environ, {"OMNISHARE_TOOL_GITHUB_TOKEN": token, "GITHUB_TOKEN": "ambient-token"}):
            prepare.prepare_asset(ASSET, self.cache, opener)
        request = opener.requests[0][0]
        self.assertEqual(request.get_header("Authorization"), "Bearer " + token)
        self.assertNotIn("Authorization", request.headers)
        handler = prepare.OfficialRedirects()
        for destination in ("https://release-assets.githubusercontent.com/tool", "https://api.github.com/repos/tauri-apps/binary-releases/releases/assets/2"):
            with self.subTest(destination=destination):
                redirected = handler.redirect_request(request, None, 302, "Found", {}, destination)
                self.assertIsNone(redirected.get_header("Authorization"))
                self.assertNotIn(token, repr(redirected.header_items()))

    def test_explicit_token_is_never_sent_to_other_hosts_or_unscoped_api_paths(self):
        urls = ("https://github.com/tauri-apps/binary-releases/releases/download/tool/file", "https://release-assets.githubusercontent.com/tool",
                "https://api.github.com/repos/tauri-apps/binary-releases", ASSET.url + "?query=value")
        with patch.dict(os.environ, {"OMNISHARE_TOOL_GITHUB_TOKEN": "explicit-token"}):
            for index, url in enumerate(urls):
                with self.subTest(url=url):
                    asset = prepare.Asset("tool-" + str(index), url, ASSET.size, ASSET.sha256)
                    opener = Opener([PAYLOAD])
                    prepare.prepare_asset(asset, self.cache, opener)
                    self.assertIsNone(opener.requests[0][0].get_header("Authorization"))
                    direct = Opener([PAYLOAD])
                    prepare.download_asset(asset, self.cache, direct, token="explicit-token")
                    self.assertIsNone(direct.requests[0][0].get_header("Authorization"))

    def test_client_errors_and_rate_limits_stop_without_retry_or_auth_fallback(self):
        cases = ((401, {}, "Bad credentials", "access_denied"),
                 (403, {"X-RateLimit-Remaining": "0"}, "rate exceeded", "rate_limit"),
                 (403, {}, "You have exceeded a secondary rate limit", "rate_limit"),
                 (403, {"X-RateLimit-Remaining": "900"}, "Resource not accessible by integration", "access_denied"),
                 (404, {}, "Not Found", "not_found"), (422, {}, "Invalid request", "client_error"),
                 (429, {"Retry-After": "60"}, "Too many requests", "rate_limit"))
        for status, headers, message, category in cases:
            with self.subTest(status=status, category=category), patch.dict(os.environ, {"OMNISHARE_TOOL_GITHUB_TOKEN": "explicit-token"}):
                error = urllib.error.HTTPError(ASSET.url, status, "private HTTP reason", headers, io.BytesIO(json.dumps({"message": message}).encode()))
                opener = Opener([error, PAYLOAD])
                waits = []
                with self.assertRaises(prepare.PreparationError) as raised:
                    prepare.prepare_asset(ASSET, self.cache, opener, sleep=waits.append)
                self.assertIn("[" + category + "]", str(raised.exception))
                self.assertEqual(len(opener.requests), 1)
                self.assertEqual(waits, [])
                self.assertEqual(opener.requests[0][0].get_header("Authorization"), "Bearer explicit-token")
                self.assertEqual(list(self.cache.iterdir()), [])

    def test_http_diagnostics_are_bounded_and_never_echo_private_response_data(self):
        token, ip, signed_url = "private-token-do-not-print", "203.0.113.10", "https://release-assets.githubusercontent.com/file?secret=private-signature"
        class TrackedBody(io.BytesIO):
            def __init__(self, body):
                super().__init__(body)
                self.read_sizes = []
            def read(self, size=-1):
                self.read_sizes.append(size)
                return super().read(size)
        body = TrackedBody(json.dumps({"message": "API rate limit exceeded for " + ip + " " + token, "url": signed_url}).encode())
        headers = {"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1770000000", "Retry-After": "30",
                   "X-RateLimit-Limit": token, "X-Private-Debug": signed_url}
        error = urllib.error.HTTPError(signed_url, 403, token, headers, body)
        with self.assertRaises(prepare.PreparationError) as raised:
            prepare.prepare_asset(ASSET, self.cache, Opener([error]))
        diagnostic = str(raised.exception)
        for secret in (token, ip, signed_url, "private-signature", "API rate limit exceeded for"):
            self.assertNotIn(secret, diagnostic)
        self.assertIn("x-ratelimit-remaining=0", diagnostic)
        self.assertIn("x-ratelimit-reset=1770000000", diagnostic)
        self.assertIn("retry-after=30", diagnostic)
        self.assertEqual(body.read_sizes, [4096])

    def test_invalid_explicit_credential_fails_without_download_or_secret_output(self):
        token = "private-token\r\nmalformed-header"
        opener = Opener([])
        with patch.dict(os.environ, {"OMNISHARE_TOOL_GITHUB_TOKEN": token}), self.assertRaises(prepare.PreparationError) as raised:
            prepare.prepare_asset(ASSET, self.cache, opener)
        self.assertNotIn("private-token", str(raised.exception))
        self.assertEqual(opener.requests, [])
        self.assertEqual(list(self.cache.iterdir()), [])

    def test_server_error_retries_keep_the_same_explicit_credential(self):
        class ChangingEnvironmentOpener(Opener):
            def open(self, request, timeout):
                if not self.requests:
                    os.environ["OMNISHARE_TOOL_GITHUB_TOKEN"] = "different-token"
                return super().open(request, timeout)
        opener = ChangingEnvironmentOpener([urllib.error.HTTPError(ASSET.url, 504, "Gateway timeout", {}, None), PAYLOAD])
        with patch.dict(os.environ, {"OMNISHARE_TOOL_GITHUB_TOKEN": "original-token"}):
            prepare.prepare_asset(ASSET, self.cache, opener, sleep=lambda _: None)
        self.assertEqual([request.get_header("Authorization") for request, _ in opener.requests], ["Bearer original-token", "Bearer original-token"])

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

    def test_four_pinned_assets_are_prepared_using_expected_tauri_cache_names(self):
        self.assertEqual([asset.name for asset in prepare.ASSETS], ["AppRun-x86_64", "linuxdeploy-07333c6-x86_64.AppImage",
                                                                   "linuxdeploy-plugin-appimage.AppImage", "runtime-x86_64"])
        self.assertEqual(prepare.ASSETS[-1], prepare.Asset("runtime-x86_64",
            "https://api.github.com/repos/AppImage/type2-runtime/releases/assets/657374641", 944632,
            "617aa77314d57c8b8e4fae9c3f0b35af7b30d5529cb69e98f64f92510ed98a46"))
        runtime = self.cache / "tauri/runtime-x86_64"
        runtime.parent.mkdir()
        runtime.write_bytes(PAYLOAD)
        out = io.StringIO()
        with patch.object(prepare.platform, "system", return_value="Linux"), patch.object(prepare.platform, "machine", return_value="x86_64"), \
                patch.object(prepare, "prepare_asset", return_value="verified cache") as fetch, redirect_stdout(out):
            result = prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(self.cache)}, Opener([]))
        self.assertEqual([call.args[0] for call in fetch.call_args_list], list(prepare.ASSETS))
        self.assertTrue(all(call.args[1] == self.cache / "tauri" for call in fetch.call_args_list))
        self.assertEqual(result, runtime.resolve())
        self.assertIn("Tauri CLI 2.12.4", out.getvalue())

    def test_verified_runtime_absolute_path_is_returned_and_appended_to_github_output(self):
        output = self.root / "step output.txt"
        cache = self.root / "cache with spaces"
        output.write_text("previous=value\n", encoding="utf-8")
        out = io.StringIO()
        with self.fixture_tools() as assets, redirect_stdout(out):
            opener = Opener([PAYLOAD] * len(assets))
            result = prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(cache), "GITHUB_OUTPUT": str(output)}, opener)
            self.assertEqual(len(opener.requests), len(assets))
            self.assertTrue(prepare.matches_asset(result, assets[-1]))
        self.assertIsInstance(result, Path)
        self.assertTrue(result.is_absolute())
        self.assertEqual(result, (cache / "tauri/runtime-x86_64").resolve())
        self.assertFalse(result.is_symlink())
        self.assertEqual(output.read_text(encoding="utf-8"), "previous=value\nruntime_path=" + str(result) + "\n")
        self.assertIn(str(result), out.getvalue())

    def test_default_environment_supplies_github_output_but_explicit_environment_does_not_inherit_it(self):
        output = self.root / "ambient-output"
        env = {"XDG_CACHE_HOME": str(self.cache), "GITHUB_OUTPUT": str(output)}
        with self.fixture_tools() as assets, patch.dict(os.environ, env), redirect_stdout(io.StringIO()):
            result = prepare.prepare_tools(self.root, opener=Opener([PAYLOAD] * len(assets)))
            expected = "runtime_path=" + str(result) + "\n"
            self.assertEqual(output.read_text(encoding="utf-8"), expected)
            self.assertEqual(prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(self.cache)}, Opener([])), result)
        self.assertEqual(output.read_text(encoding="utf-8"), expected)

    def test_any_asset_failure_preserves_output_and_never_prints_runtime_path(self):
        for failed_index in range(len(prepare.ASSETS)):
            with self.subTest(failed_index=failed_index):
                cache = self.root / ("failed-cache-" + str(failed_index))
                output = self.root / ("failed-output-" + str(failed_index))
                output.write_text("previous=value\n", encoding="utf-8")
                out = io.StringIO()
                failure = urllib.error.HTTPError(ASSET.url, 403, "Forbidden", {}, io.BytesIO(b"{}"))
                with self.fixture_tools(), redirect_stdout(out), self.assertRaises(prepare.PreparationError):
                    prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(cache), "GITHUB_OUTPUT": str(output)},
                                          Opener([PAYLOAD] * failed_index + [failure]))
                self.assertEqual(output.read_text(encoding="utf-8"), "previous=value\n")
                self.assertNotIn(str(cache / "tauri/runtime-x86_64"), out.getvalue())
                self.assertNotIn("runtime_path=", out.getvalue())

    def test_runtime_checksum_failure_never_publishes_a_path(self):
        output = self.root / "uncreated-output"
        out = io.StringIO()
        with self.fixture_tools() as assets, redirect_stdout(out), \
                self.assertRaisesRegex(prepare.PreparationError, "SHA256"):
            # Exercise the real digest failure with one attempt and no delay.
            original = prepare.prepare_asset
            with patch.object(prepare, "prepare_asset", side_effect=lambda asset, cache, opener: original(asset, cache, opener, attempts=1)):
                prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(self.cache), "GITHUB_OUTPUT": str(output)},
                                      Opener([PAYLOAD] * (len(assets) - 1) + [b"x" * len(PAYLOAD)]))
        self.assertFalse(output.exists())
        self.assertFalse((self.cache / "tauri/runtime-x86_64").exists())
        self.assertNotIn(str(self.cache / "tauri/runtime-x86_64"), out.getvalue())

    def test_runtime_paths_with_cr_or_lf_are_rejected_without_output_injection(self):
        for character in ("\r", "\n"):
            with self.subTest(character=repr(character)):
                cache = self.root / ("unsafe" + character + "injected=value")
                output = self.root / "newline-output"
                out = io.StringIO()
                with self.fixture_tools() as assets, redirect_stdout(out), self.assertRaisesRegex(prepare.PreparationError, "newline"):
                    prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(cache), "GITHUB_OUTPUT": str(output)},
                                          Opener([PAYLOAD] * len(assets)))
                self.assertFalse(output.exists())
                self.assertNotIn("injected=value", out.getvalue())

    def test_runtime_must_remain_an_actual_regular_file_before_output(self):
        for kind in ("missing", "directory", "symlink"):
            with self.subTest(kind=kind):
                cache = self.root / ("runtime-" + kind)
                runtime = cache / "tauri/runtime-x86_64"
                runtime.parent.mkdir(parents=True)
                if kind == "directory":
                    runtime.mkdir()
                elif kind == "symlink":
                    target = self.root / "symlink-target"
                    target.write_bytes(PAYLOAD)
                    runtime.symlink_to(target)
                output = self.root / ("invalid-output-" + kind)
                out = io.StringIO()
                with self.fixture_tools(), patch.object(prepare, "prepare_asset", return_value="verified cache"), \
                        redirect_stdout(out), self.assertRaisesRegex(prepare.PreparationError, "regular file"):
                    prepare.prepare_tools(self.root, {"XDG_CACHE_HOME": str(cache), "GITHUB_OUTPUT": str(output)}, Opener([]))
                self.assertFalse(output.exists())
                self.assertNotIn(str(runtime), out.getvalue())


if __name__ == "__main__":
    unittest.main(verbosity=2)
