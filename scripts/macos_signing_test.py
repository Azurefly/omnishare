#!/usr/bin/env python3
"""Portable behavior tests for signing orchestration; native trust is a CI gate."""

from contextlib import redirect_stderr, redirect_stdout
import base64
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("macos_signing", Path(__file__).with_name("macos-signing.py"))
signing = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = signing
SPEC.loader.exec_module(signing)

TEAM = "A1B2C3D4E5"
FINGERPRINT = "A" * 40
IDENTITY = f'  1) {FINGERPRINT} "Developer ID Application: Test Company ({TEAM})"\n  1 valid identities found\n'
SUBMISSION_IDS = ["2efe2717-52ef-43a5-96dc-0797e4ca1041", "a2222222-2222-4222-8222-222222222222"]


def credentials():
    return {
        "APPLE_CERTIFICATE": base64.b64encode(b"\x30synthetic-format-only-p12").decode(),
        "APPLE_CERTIFICATE_PASSWORD": "private-p12-password-do-not-log",
        "APPLE_TEAM_ID": TEAM,
        "APPLE_ID": "private-account@example.test",
        "APPLE_APP_SPECIFIC_PASSWORD": "abcd-efgh-ijkl-mnop",
    }


class FakeNativeTools:
    """Model file mutations so final digests and operation order are observable."""

    def __init__(self, fail_stage=None, notary_status="Accepted", identity=IDENTITY):
        self.calls = []
        self.fail_stage = fail_stage
        self.notary_status = notary_status
        self.identity = identity
        self.signatures = {}
        self.submissions = []
        self.private_paths = []
        self.readme = None
        self.dmg_entries = None
        self.p12_mode = None
        self.work_mode = None

    def run(self, argv, stage, timeout=180):
        argv = list(map(str, argv))
        self.calls.append((argv, stage))
        if stage == self.fail_stage:
            raise signing.PackagingError(stage + " failed (exit 1).")
        tool = Path(argv[0]).name
        if tool == "lipo":
            return "x86_64\n"
        if tool == "security":
            if argv[1] == "create-keychain":
                path = Path(argv[-1])
                self.private_paths.append(path)
                path.write_bytes(b"private-keychain")
                self.work_mode = path.parent.stat().st_mode & 0o777
            if argv[1] == "import":
                path = Path(argv[2])
                self.private_paths.append(path)
                self.p12_mode = path.stat().st_mode & 0o777
            if argv[1] == "find-identity":
                return self.identity
            if argv[1] == "delete-keychain":
                Path(argv[-1]).unlink(missing_ok=True)
        if tool == "codesign":
            path = Path(argv[-1])
            if "--sign" in argv:
                mode = "rehearsal" if argv[argv.index("--sign") + 1] == "-" else "release"
                self.signatures[str(path)] = mode
                if path.suffix == ".dmg":
                    path.write_bytes(path.read_bytes() + b"|signed")
            if "--display" in argv:
                if self.signatures[str(path)] == "rehearsal":
                    return "Signature=adhoc\nTeamIdentifier=not set\n"
                return (f"Authority=Developer ID Application: Test Company ({TEAM})\n"
                        f"TeamIdentifier={TEAM}\nTimestamp=Nov 8, 2026\nCodeDirectory flags=0x10000(runtime)\n")
        if tool == "ditto":
            source, target = Path(argv[-2]), Path(argv[-1])
            if "-c" in argv:
                self.readme = (source / "Contents/Resources/README.txt").read_text()
                target.write_bytes(b"zip:" + (b"stapled" if (source / "ticket").exists() else b"unstapled"))
            else:
                shutil.copytree(source, target)
        if tool == "hdiutil":
            source = Path(argv[argv.index("-srcfolder") + 1])
            self.dmg_entries = set(path.name for path in source.iterdir())
            if not (source / "Applications").is_symlink() or os.readlink(source / "Applications") != "/Applications":
                raise AssertionError("Missing Applications drag-and-drop link")
            ticket = b"stapled" if (source / "OmniShare.app/ticket").exists() else b"unstapled"
            Path(argv[-1]).write_bytes(b"dmg:" + ticket)
        if tool == "xcrun":
            if argv[1] == "swiftc":
                output = Path(argv[argv.index("-o") + 1])
                output.write_bytes(b"modeled-native-AppKit-launcher")
            if argv[1:3] == ["notarytool", "submit"]:
                submission_id = SUBMISSION_IDS[len(self.submissions)]
                self.submissions.append((submission_id, Path(argv[3]).read_bytes()))
                return json.dumps({"id": submission_id, "status": "In Progress"})
            if argv[1:3] == ["notarytool", "wait"]:
                return json.dumps({"id": argv[3], "status": self.notary_status})
            if argv[1:3] == ["notarytool", "log"]:
                data = next(data for submission_id, data in self.submissions if submission_id == argv[3])
                return json.dumps({"jobId": argv[3], "status": "Accepted", "sha256": hashlib.sha256(data).hexdigest(), "issues": None})
            if argv[1:3] == ["stapler", "staple"]:
                path = Path(argv[-1])
                if path.is_dir():
                    (path / "ticket").write_bytes(b"ticket")
                else:
                    path.write_bytes(path.read_bytes() + b"|ticket")
        return ""


class SigningTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.workspace = Path(self.temp.name)
        self.binary = self.workspace / "omnishare"
        self.binary.write_bytes(b"synthetic-native-binary")
        self.output = self.workspace / "output"

    def run_package(self, tools, mode="release", env=None):
        with patch.object(signing.platform, "system", return_value="Darwin"), patch.object(signing.os, "access", return_value=True):
            return signing.package(self.binary, self.output, "amd64", mode=mode,
                                   env=credentials() if env is None else env, tools=tools)

    def assert_no_outputs(self, tools):
        self.assertEqual(list(self.output.iterdir()) if self.output.exists() else [], [])
        for path in tools.private_paths:
            self.assertFalse(path.exists())

    def test_release_is_default_and_missing_credentials_never_fall_back(self):
        self.assertEqual(signing.argument_parser().parse_args(["binary", "out", "amd64"]).mode, "release")
        tools = FakeNativeTools()
        with self.assertRaisesRegex(signing.PackagingError, "missing:"):
            self.run_package(tools, env={})
        self.assertEqual(tools.calls, [])
        self.assert_no_outputs(tools)

    def test_preflight_reports_only_names_without_native_tools_or_writes(self):
        environment = {name: "invalid-secret-value-for-" + name for name in signing.SECRET_NAMES}
        environment.pop("APPLE_CERTIFICATE_PASSWORD")
        out, error = io.StringIO(), io.StringIO()
        with patch.dict(os.environ, environment, clear=True), patch.object(signing.subprocess, "run", side_effect=AssertionError("native tool")), \
                patch.object(signing.tempfile, "TemporaryDirectory", side_effect=AssertionError("filesystem write")), \
                redirect_stdout(out), redirect_stderr(error):
            self.assertEqual(signing.main(["--check-credentials"]), 1)
        message = out.getvalue() + error.getvalue()
        for name in signing.SECRET_NAMES:
            self.assertIn(name, message)
        for value in environment.values():
            self.assertNotIn(value, message)

    def test_preflight_success_does_not_claim_certificate_or_account_validity(self):
        out = io.StringIO()
        with patch.dict(os.environ, credentials(), clear=True), patch.object(signing.subprocess, "run", side_effect=AssertionError("native tool")), redirect_stdout(out):
            self.assertEqual(signing.main(["--check-credentials"]), 0)
        self.assertIn("presence/format", out.getvalue())
        self.assertIn("require macOS release packaging", out.getvalue())
        for value in credentials().values():
            self.assertNotIn(value, out.getvalue())

    def test_credential_format_rejects_invalid_base64_team_and_password(self):
        for name, value in (("APPLE_CERTIFICATE", "%%%"), ("APPLE_CERTIFICATE", base64.b64encode(b"not-p12").decode()),
                            ("APPLE_TEAM_ID", "wrong-team"), ("APPLE_ID", "invalid-account"),
                            ("APPLE_APP_SPECIFIC_PASSWORD", "wrong-password"), ("APPLE_CERTIFICATE_PASSWORD", "bad\0value")):
            with self.subTest(name=name):
                env = credentials()
                env[name] = value
                self.assertEqual(signing.credential_issues(env), ([], [name]))
        env = credentials()
        env["APPLE_CERTIFICATE"] = "\n" + env["APPLE_CERTIFICATE"][:10] + "\n" + env["APPLE_CERTIFICATE"][10:] + "\n"
        self.assertEqual(signing.credential_issues(env), ([], []))

    def test_identity_selection_is_exact_and_rejects_ambiguous_or_wrong_certificates(self):
        self.assertEqual(signing.developer_identity(IDENTITY, TEAM), FINGERPRINT)
        invalid = ["", IDENTITY.replace(TEAM, "Z9Y8X7W6V5"), IDENTITY.replace("Application", "Installer"),
                   IDENTITY.replace("Developer ID Application", "Apple Development"), IDENTITY + IDENTITY,
                   IDENTITY.replace('"\n', '" (CSSMERR_TP_CERT_EXPIRED)\n')]
        for output in invalid:
            with self.subTest(output=output):
                with self.assertRaises(signing.PackagingError):
                    signing.developer_identity(output, TEAM)

    def test_release_orders_real_mutations_before_hashes_and_publishes_only_safe_report(self):
        tools = FakeNativeTools()
        report = self.run_package(tools)
        self.assertEqual(report["mode"], "release")
        self.assertEqual(report["signing_mode"], "developer-id")
        self.assertEqual(report["team_id"], TEAM)
        self.assertEqual(tools.p12_mode, 0o600)
        self.assertEqual(tools.work_mode, 0o700)
        self.assertEqual(tools.dmg_entries, {"OmniShare.app", "Applications"})
        self.assertNotIn("__", tools.readme)
        self.assertIn("正式发布包", tools.readme)
        self.assertEqual(tools.submissions[0][1], b"zip:unstapled")
        self.assertEqual(tools.submissions[1][1], b"dmg:stapled|signed")
        for kind, expected_bytes in (("zip", b"zip:stapled"), ("dmg", b"dmg:stapled|signed|ticket")):
            asset = self.output / report["artifacts"][kind]["name"]
            self.assertEqual(asset.read_bytes(), expected_bytes)
            self.assertEqual(report["artifacts"][kind]["sha256"], hashlib.sha256(expected_bytes).hexdigest())
        for kind, submission_id in zip(("app", "dmg"), SUBMISSION_IDS):
            self.assertEqual(report["notarization"][kind], {"submission_id": submission_id, "status": "Accepted", "stapled": True})
        content = (self.output / "omnishare-macos-amd64-packaging.json").read_text()
        self.assertEqual(json.loads(content), report)
        for name, value in credentials().items():
            if name != "APPLE_TEAM_ID":
                self.assertNotIn(value, content)
        self.assertNotIn(str(self.workspace), content)
        self.assertEqual(len(list(self.output.iterdir())), 3)
        for path in tools.private_paths:
            self.assertFalse(path.exists())
        app_sign = next(argv for argv, stage in tools.calls if stage == "Sign macOS application")
        self.assertIn("--timestamp", app_sign)
        self.assertIn("--keychain", app_sign)
        self.assertEqual(app_sign[app_sign.index("--sign") + 1], FINGERPRINT)
        self.assertEqual(app_sign[app_sign.index("--options") + 1], "runtime")
        self.assertNotIn("--deep", app_sign)
        stages = [stage for _, stage in tools.calls]
        self.assertLess(stages.index("Sign embedded macOS backend"), stages.index("Sign macOS application"))
        compiler = next(argv for argv, stage in tools.calls if stage == "Compile native application launcher")
        self.assertIn("x86_64-apple-macosx12.0", compiler)
        for argv, _ in tools.calls:
            self.assertNotIn("default-keychain", argv)
            self.assertNotIn("list-keychains", argv)
            self.assertNotIn("add-trusted-cert", argv)
            self.assertNotIn("-A", argv)

    def test_rehearsal_never_reads_signing_credentials_or_calls_notarization(self):
        tools = FakeNativeTools()
        with patch.object(signing, "load_credentials", side_effect=AssertionError("read credentials")):
            report = self.run_package(tools, mode="rehearsal", env={})
        self.assertEqual(report["signing_mode"], "ad-hoc")
        self.assertIsNone(report["team_id"])
        self.assertEqual(set(tools.signatures.values()), {"rehearsal"})
        self.assertEqual(len(tools.signatures), 3)
        self.assertIn("CI 演练包", tools.readme)
        self.assertNotIn("__SIGNING_NOTICE__", tools.readme)
        for record in report["notarization"].values():
            self.assertEqual(record, {"submission_id": None, "status": "NotSubmitted", "stapled": False})
        self.assertFalse(any(Path(argv[0]).name == "security" or
                             (Path(argv[0]).name == "xcrun" and argv[1] != "swiftc")
                             for argv, _ in tools.calls))

    def test_app_and_dmg_cannot_reuse_one_notary_submission(self):
        tools = FakeNativeTools()
        duplicate = [{"submission_id": SUBMISSION_IDS[0], "status": "Accepted", "stapled": False} for _ in range(2)]
        with patch.object(signing, "notarize", side_effect=duplicate), \
                self.assertRaisesRegex(signing.PackagingError, "must be distinct"):
            self.run_package(tools)
        self.assert_no_outputs(tools)

    def test_production_signature_requires_expected_team_timestamp_and_runtime(self):
        corruptions = ((f"TeamIdentifier={TEAM}", "TeamIdentifier=Z9Y8X7W6V5"),
                       ("Developer ID Application", "Apple Development"),
                       ("Timestamp=Nov 8, 2026\n", ""), ("flags=0x10000(runtime)", "flags=0x0(none)"))
        for original, replacement in corruptions:
            class InvalidSignatureTools(FakeNativeTools):
                def run(self, argv, stage, timeout=180):
                    output = super().run(argv, stage, timeout)
                    return output.replace(original, replacement) if stage == "Inspect code signature" else output
            with self.subTest(original=original):
                tools = InvalidSignatureTools()
                with self.assertRaises(signing.PackagingError):
                    self.run_package(tools)
                self.assert_no_outputs(tools)
                self.assertEqual(tools.submissions, [])

    def test_failures_remove_all_sensitive_files_and_never_publish_success(self):
        for stage in ("Create temporary keychain", "Import signing identity", "Validate notarization credentials",
                      "Sign macOS application", "Create installation disk image", "Sign installation disk image",
                      "Validate notarization ticket", "Delete temporary signing keychain"):
            with self.subTest(stage=stage):
                tools = FakeNativeTools(fail_stage=stage)
                with self.assertRaises(signing.PackagingError):
                    self.run_package(tools)
                self.assert_no_outputs(tools)
                self.assertTrue(any(call_stage == "Delete temporary signing keychain" for _, call_stage in tools.calls))

    def test_invalid_or_unfinished_notarization_blocks_artifacts(self):
        for status in ("Invalid", "Rejected", "In Progress", False, "accepted"):
            with self.subTest(status=status):
                tools = FakeNativeTools(notary_status=status)
                with self.assertRaises(signing.PackagingError):
                    self.run_package(tools)
                self.assert_no_outputs(tools)
                self.assertFalse(any(argv[1:3] == ["stapler", "staple"] for argv, _ in tools.calls))

    def test_notarization_wait_retries_same_id_without_resubmission(self):
        calls = []
        target = self.workspace / "app.zip"
        target.write_bytes(b"submission")
        class WaitTools:
            def run(self, argv, stage, timeout=180):
                calls.append(argv)
                if argv[2] == "submit":
                    return json.dumps({"id": SUBMISSION_IDS[0], "status": "In Progress"})
                if argv[2] == "log":
                    return json.dumps({"jobId": SUBMISSION_IDS[0], "status": "Accepted", "sha256": signing.sha256(target), "issues": []})
                if len(calls) == 2:
                    raise signing.PackagingError("Wait for notarization timed out.")
                return json.dumps({"id": SUBMISSION_IDS[0], "status": "Accepted"})
        report = signing.notarize(WaitTools(), target, Path("private.keychain"), "profile", sleep=lambda _: None)
        self.assertEqual(report["status"], "Accepted")
        self.assertEqual(sum(call[2] == "submit" for call in calls), 1)
        self.assertEqual([call[3] for call in calls if call[2] == "wait"], [SUBMISSION_IDS[0], SUBMISSION_IDS[0]])

    def test_notarization_rejects_bad_json_wrong_id_or_false_acceptance(self):
        for output in ("raw private server error", "{}", "[]", '{"id":"not-an-id","status":"Accepted"}'):
            with self.subTest(output=output), self.assertRaises(signing.PackagingError):
                signing.parse_notary_response(output)
        class WrongSubmission:
            def run(self, argv, stage, timeout=180):
                index = 0 if argv[2] == "submit" else 1
                return json.dumps({"id": SUBMISSION_IDS[index], "status": "In Progress" if index == 0 else "Accepted"})
        with self.assertRaisesRegex(signing.PackagingError, "does not match"):
            signing.notarize(WrongSubmission(), self.binary, Path("private.keychain"), "profile")

    def test_accepted_notary_logs_are_required_and_bound_to_bytes_and_submission(self):
        variants = ["invalid JSON", {}, {"jobId": "not-a-uuid"}, {"jobId": SUBMISSION_IDS[1]}, {"status": "Invalid"},
                    {"sha256": "0" * 64}, {"issues": [{"severity": "warning", "message": "private log detail"}]}]
        for variant in variants:
            class InvalidLogTools(FakeNativeTools):
                def run(self, argv, stage, timeout=180):
                    output = super().run(argv, stage, timeout)
                    if stage != "Inspect accepted notarization log":
                        return output
                    if isinstance(variant, str):
                        return variant
                    data = json.loads(output)
                    if variant:
                        data.update(variant)
                    else:
                        data = {}
                    return json.dumps(data)
            with self.subTest(variant=variant):
                tools = InvalidLogTools()
                with self.assertRaises(signing.PackagingError) as raised:
                    self.run_package(tools)
                self.assertNotIn("private log detail", str(raised.exception))
                self.assert_no_outputs(tools)
                self.assertFalse(any(argv[1:3] == ["stapler", "staple"] for argv, _ in tools.calls))

    def test_notary_log_accepts_uppercase_and_lowercase_equivalent_uuid(self):
        for case in (str.lower, str.upper):
            class CaseLogTools(FakeNativeTools):
                def run(self, argv, stage, timeout=180):
                    output = super().run(argv, stage, timeout)
                    if stage == "Inspect accepted notarization log":
                        log = json.loads(output)
                        log["jobId"] = case(log["jobId"])
                        return json.dumps(log)
                    return output
            with self.subTest(case=case.__name__):
                self.output = self.workspace / case.__name__
                report = self.run_package(CaseLogTools())
                self.assertEqual(report["notarization"]["app"]["submission_id"], SUBMISSION_IDS[0])
                self.assertEqual(report["notarization"]["dmg"]["submission_id"], SUBMISSION_IDS[1])

    def test_command_failures_and_timeouts_never_include_arguments_or_output(self):
        marker = "private-value-must-never-appear"
        tools = signing.NativeTools(credentials())
        for exception in (subprocess.TimeoutExpired(["tool", marker], 1, output=marker), OSError(marker)):
            with self.subTest(exception=type(exception).__name__), patch.object(signing.subprocess, "run", side_effect=exception):
                with self.assertRaises(signing.PackagingError) as raised:
                    tools.run(["tool", marker], "Safe stage")
                self.assertNotIn(marker, str(raised.exception))
        completed = subprocess.CompletedProcess(["tool", marker], 1, marker, marker)
        with patch.object(signing.subprocess, "run", return_value=completed) as run:
            with self.assertRaises(signing.PackagingError) as raised:
                tools.run(["tool", marker], "Safe stage")
            self.assertNotIn(marker, str(raised.exception))
            for name in signing.SECRET_NAMES:
                self.assertNotIn(name, run.call_args.kwargs["env"])

    def test_stderr_diagnostics_cannot_corrupt_notary_json(self):
        result = subprocess.CompletedProcess([], 0, '{"id":"safe"}', "private diagnostic")
        with patch.object(signing.subprocess, "run", return_value=result):
            self.assertEqual(signing.NativeTools({}).run([signing.XCRUN, "notarytool"], "Safe stage"), '{"id":"safe"}')

    def test_cancellation_cleans_keychain_before_propagating(self):
        class CancelTools(FakeNativeTools):
            def run(self, argv, stage, timeout=180):
                if stage == "Sign macOS application":
                    raise KeyboardInterrupt()
                return super().run(argv, stage, timeout)
        tools = CancelTools()
        with self.assertRaises(KeyboardInterrupt):
            self.run_package(tools)
        self.assert_no_outputs(tools)
        self.assertEqual(tools.calls[-1][1], "Delete temporary signing keychain")

    def test_existing_artifacts_cannot_be_silently_replaced(self):
        self.output.mkdir()
        existing = self.output / "omnishare-macos-amd64-packaging.json"
        existing.write_text("previous evidence")
        tools = FakeNativeTools()
        with self.assertRaisesRegex(signing.PackagingError, "Refusing to overwrite"):
            self.run_package(tools)
        self.assertEqual(existing.read_text(), "previous evidence")
        self.assertFalse(any(Path(argv[0]).name == "security" for argv, _ in tools.calls))

    def test_partial_publication_is_rolled_back(self):
        original_link = os.link
        def fail_dmg(source, target):
            if str(target).endswith(".dmg"):
                raise OSError("disk error")
            original_link(source, target)
        tools = FakeNativeTools()
        with patch.object(signing.os, "link", side_effect=fail_dmg), self.assertRaises(OSError):
            self.run_package(tools)
        self.assert_no_outputs(tools)


if __name__ == "__main__":
    unittest.main(verbosity=2)
