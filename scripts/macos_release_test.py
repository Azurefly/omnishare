#!/usr/bin/env python3
"""Behavior tests for the release verifier's fail-closed boundaries.

Native Apple tools are modeled explicitly here. These tests exercise the
orchestration and real ZIP/filesystem handling; only the separate macOS job can
attest an actual Developer ID signature, ticket, or Gatekeeper assessment.
"""

from contextlib import ExitStack, contextmanager, redirect_stdout
from copy import deepcopy
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import struct
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import zipfile


SPEC = importlib.util.spec_from_file_location(
    "macos_release", Path(__file__).with_name("test-macos-release.py"))
MAC = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MAC)
TEAM = "A1B2C3D4E5"
VERSION = "0.1.2"
SIGNED = (
    "CodeDirectory v=20500 size=123 flags=0x10000(runtime) hashes=2+7 location=embedded\n"
    f"Authority=Developer ID Application: Example ({TEAM})\n"
    "Authority=Developer ID Certification Authority\n"
    "Authority=Apple Root CA\n"
    "Timestamp=Nov 8, 2026 at 12:00:00 PM\n"
    f"TeamIdentifier={TEAM}\n"
)


def make_app(parent):
    app = parent / "OmniShare.app"
    for relative in ("Contents/MacOS", "Contents/Resources", "Contents/_CodeSignature"):
        (app / relative).mkdir(parents=True, mode=0o755, exist_ok=True)
    info = {
        "CFBundleExecutable": "OmniShare", "CFBundlePackageType": "APPL",
        "CFBundleIdentifier": "local.omnishare.desktop",
        "CFBundleShortVersionString": VERSION, "CFBundleVersion": VERSION,
        "LSMinimumSystemVersion": "12.0",
    }
    (app / "Contents/Info.plist").write_bytes(plistlib.dumps(info))
    binary = app / "Contents/MacOS/OmniShare"
    binary.write_bytes(b"A modeled native Mach-O executable\n")
    binary.chmod(0o755)
    backend = binary.with_name("omnishare-server")
    backend.write_bytes(b"A modeled nested Go backend\n")
    backend.chmod(0o755)
    (app / "Contents/Resources/README.txt").write_bytes(b"Verified fixture resources\n")
    (app / "Contents/_CodeSignature/CodeResources").write_bytes(b"Fixture resource seal\n")
    for item in [app, *app.rglob("*")]:
        item.chmod(0o755 if item.is_dir() or item in (binary, backend) else 0o644)
    return app


def make_zip(path, app, additions=()):
    with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_STORED) as archive:
        for item in [app, *sorted(app.rglob("*"))]:
            archive.write(item, item.relative_to(app.parent))
        for name, payload, mode in additions:
            info = zipfile.ZipInfo(name)
            info.create_system = 3
            info.external_attr = mode << 16
            archive.writestr(info, payload)


class NativeModel:
    """Tool boundary model: no Apple command or executable runs on the host."""

    def __init__(self, root, app, mode):
        self.root, self.app, self.mode = root, app, mode
        self.calls, self.mounts, self.launches = [], set(), []
        self.xattrs = {}
        self.reject_policy = False
        self.damage_dmg_copy = False
        self.wrong_mount = False
        self.read_only = True
        self.staple_valid = True
        self.quarantine_change = None
        self.signature = SIGNED if mode == "release" else "Signature=adhoc\n"

    def result(self, args, code=0, stdout="", stderr="", allow_failure=False):
        if code and not allow_failure:
            raise MAC.VerificationError(f"Modeled native command failed: {Path(args[0]).name}")
        return subprocess.CompletedProcess(args, code, stdout, stderr)

    def tool(self, args, *, allow_failure=False, timeout=30):
        args = [str(item) for item in args]
        self.calls.append(args)
        command = Path(args[0]).name
        if command == "codesign":
            if "--display" in args:
                return self.result(args, stderr=self.signature)
            target = Path(args[-1])
            resource = target / "Contents/Resources/README.txt"
            tampered = resource.is_file() and b"Intentional resource" in resource.read_bytes()
            return self.result(args, code=1 if tampered else 0, allow_failure=allow_failure)
        if command == "plutil":
            plistlib.loads(Path(args[-1]).read_bytes())
        elif command == "lipo":
            return self.result(args, stdout="arm64\n")
        elif command == "ditto":
            if args[1:3] == ["-x", "-k"]:
                with zipfile.ZipFile(args[3]) as archive:
                    archive.extractall(args[4])
                    for entry in archive.infolist():
                        (Path(args[4]) / entry.filename).chmod(stat.S_IMODE(entry.external_attr >> 16))
            else:
                source, destination = map(Path, args[1:3])
                if source.is_dir():
                    shutil.copytree(source, destination)
                else:
                    shutil.copy2(source, destination)
        elif command == "hdiutil":
            if args[1] == "attach":
                mount = Path(args[args.index("-mountpoint") + 1])
                self.mounts.add(str(mount))
                shutil.copytree(self.app, mount / "OmniShare.app")
                (mount / "Applications").symlink_to("/Applications")
                if self.damage_dmg_copy:
                    (mount / "OmniShare.app/Contents/Resources/README.txt").write_text("Different payload\n")
                reported = "/Volumes/Unexpected" if self.wrong_mount else str(mount)
                return self.result(args, stdout=plistlib.dumps({
                    "system-entities": [{"mount-point": reported}],
                }).decode())
            if args[1] == "detach":
                mount = Path(args[2])
                if str(mount) not in self.mounts:
                    raise AssertionError("Attempted to detach a foreign mount")
                self.mounts.remove(str(mount))
                shutil.rmtree(mount / "OmniShare.app")
                (mount / "Applications").unlink()
        elif command == "xcrun":
            return self.result(args, code=0 if self.staple_valid else 65,
                               allow_failure=allow_failure)
        elif command == "spctl":
            if "--status" in args:
                return self.result(args, stdout="assessments enabled\n")
            if (args[-1], MAC.QUARANTINE_ATTRIBUTE) not in self.xattrs:
                raise AssertionError("Policy assessment used an unquarantined copy")
            marker = (args[-1], MAC.QUARANTINE_ATTRIBUTE)
            if self.quarantine_change == "flags":
                self.xattrs[marker] = self.xattrs[marker].replace("0083;", "0081;", 1)
            elif self.quarantine_change == "remove":
                del self.xattrs[marker]
            code = 3 if self.reject_policy or self.mode == "rehearsal" else 0
            return self.result(args, code=code,
                               stderr="rejected" if code else "accepted\nsource=Notarized Developer ID\n",
                               allow_failure=allow_failure)
        elif command == "syspolicy_check":
            code = 1 if self.reject_policy or self.mode == "rehearsal" else 0
            return self.result(args, code=code, stdout="Policy diagnostic", allow_failure=allow_failure)
        elif command == "xattr":
            if args[1] == "-w":
                self.xattrs[(args[4], args[2])] = args[3]
            elif args[1] == "-p":
                value = self.xattrs.get((args[3], args[2]))
                return self.result(args, code=0 if value is not None else 1,
                                   stdout=(value + "\n") if value is not None else "",
                                   allow_failure=allow_failure)
            else:
                names = [name for (path, name) in self.xattrs if path == args[1]]
                return self.result(args, stdout="\n".join(names))
        else:
            raise AssertionError(f"Unexpected tool: {args}")
        return self.result(args)

    def launch(self, app, data, port):
        model = self

        class Process:
            stopped = False
            pid = 98765
            app_pid = 98764

            def start(self):
                if model.mounts:
                    raise AssertionError("Started the app before detaching its source DMG")
                if (str(app), MAC.QUARANTINE_ATTRIBUTE) in model.xattrs:
                    raise AssertionError("Attempted to automate a quarantined launch")
                model.launches.append(str(app))

            def check_http(self, version):
                if version != VERSION:
                    raise AssertionError("Runtime did not receive the release version")

            def stop(self):
                self.stopped = True
                return True

            def reopen(self):
                pass

        process = Process()
        process.port = port if port is not None else 12345
        process.data = data
        return process


@contextmanager
def fixture(mode="release"):
    with tempfile.TemporaryDirectory(prefix="macos verifier test ") as temporary:
        root = Path(temporary)
        source = root / "source"
        source.mkdir()
        app = make_app(source)
        archive = root / f"OmniShare-v{VERSION}-macos-arm64.zip"
        dmg = root / f"OmniShare-v{VERSION}-macos-arm64.dmg"
        make_zip(archive, app)
        dmg.write_bytes(b"A modeled read-only UDIF image\n")
        evidence = root / "omnishare-macos-arm64-packaging.json"
        args = SimpleNamespace(archive=archive, dmg=dmg, packaging_evidence=evidence,
                               arch="arm64", version=VERSION, mode=mode, team_id=TEAM,
                               evidence=root / "verification.json")
        records = {
            "app": {"submission_id": "11111111-1111-4111-8111-111111111111",
                    "status": "Accepted", "stapled": True},
            "dmg": {"submission_id": "22222222-2222-4222-8222-222222222222",
                    "status": "Accepted", "stapled": True},
        } if mode == "release" else {
            kind: {"submission_id": None, "status": "NotSubmitted", "stapled": False}
            for kind in ("app", "dmg")
        }
        artifacts = {kind: {"name": path.name, "sha256": MAC.file_digest(path)}
                     for kind, path in (("zip", archive), ("dmg", dmg))}
        packaging = {"schema_version": 1, "version": VERSION, "arch": "arm64", "mode": mode,
                     "signing_mode": "developer-id" if mode == "release" else "ad-hoc",
                     "team_id": TEAM if mode == "release" else None,
                     "artifacts": artifacts, "notarization": records}
        evidence.write_text(json.dumps(packaging))
        model = NativeModel(root, app, mode)
        yield SimpleNamespace(root=root, app=app, args=args, packaging=packaging,
                              artifacts=artifacts, model=model)


def run_verifier(data, *, omit_mode=False, team_id=TEAM):
    args = data.args
    argv = ["--archive", str(args.archive), "--dmg", str(args.dmg),
            "--packaging-evidence", str(args.packaging_evidence),
            "--arch", args.arch, "--version", args.version, "--evidence", str(args.evidence),
            "--team-id", team_id]
    if not omit_mode:
        argv.extend(["--mode", args.mode])
    model = data.model
    is_file = Path.is_file
    statvfs = os.statvfs

    def features(launch, version, state, phase):
        if phase == "exercise":
            (launch.data / "config.json").write_text(json.dumps({"port": launch.port}))
        return {"schema_version": 1, "version": version, "phase": phase, "success": True,
                "checks": {"modeled_http_features": True}}

    with ExitStack() as stack:
        for target, value in (
            ("tool", model.tool), ("ScopedLaunch", model.launch),
            ("run_features", features),
        ):
            stack.enter_context(patch.object(MAC, target, value))
        stack.enter_context(patch.object(MAC.platform, "system", return_value="Darwin"))
        stack.enter_context(patch.object(MAC.platform, "machine", return_value="arm64"))
        stack.enter_context(patch.object(Path, "is_file", lambda p:
            True if str(p) == "/usr/bin/syspolicy_check" else is_file(p)))
        stack.enter_context(patch.object(MAC.os.path, "ismount", lambda p: str(p) in model.mounts))
        stack.enter_context(patch.object(MAC.os, "statvfs", lambda p:
            SimpleNamespace(f_flag=os.ST_RDONLY if model.read_only else 0)
            if str(p) in model.mounts else statvfs(p)))
        stack.enter_context(redirect_stdout(io.StringIO()))
        code = MAC.main(argv)
    return code, json.loads(args.evidence.read_text())


class MacReleaseTests(unittest.TestCase):
    def test_default_release_verifies_both_final_formats_without_claiming_first_download(self):
        with fixture() as data:
            code, report = run_verifier(data, omit_mode=True)
            self.assertEqual(code, 0, report.get("error"))
            self.assertTrue(report["production_ready"])
            self.assertTrue(all(report["checks"].values()))
            self.assertEqual(len(report["checks"]), 33)
            self.assertFalse(report["gatekeeper"]["first_download_verified"])
            self.assertTrue(report["gatekeeper"]["quarantine_policy_verified"])
            self.assertFalse(report["runtime"]["quarantined"])
            self.assertEqual(set(report["runtime"]) - {"quarantined", "policy_test_separate"}, {"zip", "dmg"})
            self.assertEqual(len(data.model.launches), 4)
            self.assertFalse(data.model.mounts)
            self.assertEqual(report["artifacts"]["dmg"]["sha256"], MAC.file_digest(data.args.dmg))
            requirements = [call[call.index("-R") + 1] for call in data.model.calls if "-R" in call]
            self.assertTrue(requirements)
            self.assertTrue(all(TEAM in value and "1.2.840.113635.100.6.1.13" in value
                                for value in requirements))
            self.assertTrue(all("Quarantine Policy Simulation" in path
                                for path, _ in data.model.xattrs))

    def test_rehearsal_is_explicit_and_never_production_ready(self):
        with fixture("rehearsal") as data:
            code, report = run_verifier(data)
            self.assertEqual(code, 0, report.get("error"))
            self.assertFalse(report["production_ready"])
            self.assertFalse(report["gatekeeper"]["notarized"])
            self.assertFalse(report["gatekeeper"]["quarantine_policy_verified"])
            self.assertTrue(all(report["checks"][name] is False for name in MAC.PRODUCTION_CHECKS))
            code, report = run_verifier(data, omit_mode=True)
            self.assertNotEqual(code, 0)
            self.assertFalse(report["production_ready"])

    def test_missing_expected_team_does_not_fall_back_to_ad_hoc(self):
        with fixture() as data:
            code, report = run_verifier(data, team_id="")
            self.assertNotEqual(code, 0)
            self.assertIn("independently configured Team ID", report["error"])
            self.assertEqual(data.model.calls, [])

    def test_notarized_package_still_fails_when_policy_rejects(self):
        with fixture() as data:
            data.model.reject_policy = True
            code, report = run_verifier(data)
            self.assertNotEqual(code, 0)
            self.assertTrue(report["gatekeeper"]["notarized"])
            self.assertFalse(report["production_ready"])
            self.assertFalse(report["gatekeeper"]["quarantine_policy_verified"])
            self.assertEqual(report["gatekeeper"]["zip_app"]["assessment_result"], "rejected")
            self.assertFalse(data.model.mounts)

    def test_stapled_claim_requires_a_successful_native_validation(self):
        with fixture() as data:
            data.model.staple_valid = False
            code, report = run_verifier(data)
            self.assertNotEqual(code, 0)
            self.assertFalse(report["production_ready"])
            self.assertFalse(report["checks"]["app_staple_valid"])
            self.assertEqual(data.model.launches, [])

    def test_policy_allows_system_flag_updates_but_rejects_a_removed_marker(self):
        for change in ("flags", "remove"):
            with self.subTest(change=change), fixture() as data:
                data.model.quarantine_change = change
                code, report = run_verifier(data)
                self.assertEqual(code, 0 if change == "flags" else 1, report.get("error"))
                self.assertEqual(report["production_ready"], change == "flags")
                # Each policy target is marked once. A failed or changed marker
                # is never 'fixed' by clearing it or writing trusted flags.
                writes = [call[-1] for call in data.model.calls if call[:2] == ["/usr/bin/xattr", "-w"]]
                self.assertEqual(len(writes), len(set(writes)))

    def test_different_dmg_payload_is_rejected_and_image_is_detached(self):
        with fixture() as data:
            data.model.damage_dmg_copy = True
            code, report = run_verifier(data)
            self.assertNotEqual(code, 0)
            self.assertIn("different app resources", report["error"])
            self.assertFalse(data.model.mounts)
            self.assertEqual(data.model.launches, [])

    def test_bad_mounts_are_rejected_and_only_the_private_mount_is_detached(self):
        for failure in ("wrong_mount", "read_only"):
            with self.subTest(failure=failure), fixture() as data:
                setattr(data.model, failure, failure == "wrong_mount")
                code, report = run_verifier(data)
                self.assertNotEqual(code, 0)
                self.assertFalse(report["production_ready"])
                detaches = [call[2] for call in data.model.calls if call[1] == "detach"]
                self.assertEqual(len(detaches), 1)
                self.assertIn("Read Only DMG", detaches[0])
                self.assertFalse(data.model.mounts)

    def test_packaging_hash_team_and_notary_records_cannot_substitute_for_each_other(self):
        with fixture() as data:
            self.assertEqual(MAC.validate_packaging(data.packaging, data.args, data.artifacts),
                             data.packaging["notarization"])
            changes = (
                lambda p: p["artifacts"]["zip"].update(sha256="0" * 64),
                lambda p: p.update(team_id="Z9Y8X7W6V5"),
                lambda p: p["notarization"]["app"].update(status="In Progress"),
                lambda p: p["notarization"]["dmg"].update(stapled="true"),
                lambda p: p["notarization"]["dmg"].update(
                    submission_id=p["notarization"]["app"]["submission_id"]),
            )
            for change in changes:
                changed = deepcopy(data.packaging)
                change(changed)
                with self.subTest(evidence=changed), self.assertRaises(MAC.VerificationError):
                    MAC.validate_packaging(changed, data.args, data.artifacts)
        with self.assertRaises(MAC.VerificationError):
            json.loads('{"mode":"release","mode":"rehearsal"}', object_pairs_hook=MAC.unique_object)

    def test_developer_signature_requires_identity_timestamp_and_executable_runtime(self):
        valid = MAC.parse_signature(SIGNED, "release", TEAM, executable=True)
        self.assertTrue(valid["hardened_runtime"])
        for output in (
            "Signature=adhoc\n", SIGNED.replace(TEAM, "Z9Y8X7W6V5"),
            SIGNED.replace("Timestamp=", "Signed Time="),
            SIGNED.replace("flags=0x10000(runtime)", "flags=0x0(none)"),
        ):
            with self.subTest(output=output), self.assertRaises(MAC.VerificationError):
                MAC.parse_signature(output, "release", TEAM, executable=True)
        image = MAC.parse_signature(SIGNED.replace("flags=0x10000(runtime)", "flags=0x0(none)"),
                                    "release", TEAM, executable=False)
        self.assertTrue(image["secure_timestamp"])

    def test_real_zip_rejects_traversal_links_collisions_and_crc_damage(self):
        additions = (
            ("../outside", b"outside", stat.S_IFREG | 0o644),
            ("OmniShare.app/linked", b"/Applications", stat.S_IFLNK | 0o777),
            ("omnishare.app/extra", b"case alias", stat.S_IFREG | 0o644),
            ("OmniShare.app/Contents/MacOS/OmniShare/child", b"file parent", stat.S_IFREG | 0o644),
        )
        with fixture() as data:
            MAC.check_archive(data.args.archive)
            for entry in additions:
                make_zip(data.args.archive, data.app, (entry,))
                with self.subTest(path=entry[0]), self.assertRaises(MAC.VerificationError):
                    MAC.check_archive(data.args.archive)
            make_zip(data.args.archive, data.app)
            contents = data.args.archive.read_bytes()
            data.args.archive.write_bytes(contents.replace(b"Verified fixture resources", b"Modified fixture resources", 1))
            with self.assertRaises(MAC.VerificationError):
                MAC.check_archive(data.args.archive)

    def test_dmg_shortcut_cannot_point_to_another_installation_location(self):
        with fixture() as data:
            mount = data.root / "layout"
            mount.mkdir()
            shutil.copytree(data.app, mount / "OmniShare.app")
            (mount / "Applications").symlink_to("/Applications")
            self.assertEqual(MAC.verify_dmg_layout(mount), mount / "OmniShare.app")
            (mount / "Applications").unlink()
            (mount / "Applications").symlink_to("/tmp/Applications")
            with self.assertRaises(MAC.VerificationError):
                MAC.verify_dmg_layout(mount)

    def test_runtime_refuses_existing_quarantine_without_launching(self):
        marked = subprocess.CompletedProcess([], 0, MAC.QUARANTINE_ATTRIBUTE + "\n", "")
        with fixture() as data, patch.object(MAC, "tool", return_value=marked), \
                patch.object(MAC, "ScopedLaunch") as launch:
            with self.assertRaises(MAC.VerificationError):
                MAC.check_runtime(data.app, data.root / "runtime", VERSION,
                                  MAC.initial_report(data.args), "zip")
            launch.assert_not_called()

    def test_policy_requires_both_tools_and_a_verified_quarantine_marker(self):
        accepted = {"quarantine_verified": True, "assessment_exit_code": 0,
                    "assessment_result": "accepted", "assessment_source": "Notarized Developer ID",
                    "syspolicy_check": {
                        "available": True, "exit_code": 0, "result": "accepted"}}
        self.assertTrue(MAC.policy_accepted(accepted, app=True))
        for field, value in (("quarantine_verified", False), ("assessment_exit_code", False),
                             ("assessment_source", "Developer ID"),
                             ("syspolicy_check", {"available": False}),
                             ("syspolicy_check", {"available": True, "exit_code": 1, "result": "rejected"})):
            changed = deepcopy(accepted)
            changed[field] = value
            with self.subTest(field=field, value=value):
                self.assertFalse(MAC.policy_accepted(changed, app=True))


class ScopedLaunchTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="mac scoped launch ")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = make_app(self.root)
        self.data = self.root / "Data 中文 #100%"
        self.data.mkdir()

    def launch(self, port=19081):
        library = SimpleNamespace(proc_pidpath=Mock(return_value=0), sysctl=Mock(return_value=0))
        with patch.object(MAC.ctypes, "CDLL", return_value=library):
            launch = MAC.ScopedLaunch(self.app, self.data, port)
        launch.app_pid, launch.pid = 61001, 61002
        launch.app_started = launch.started = "Tue Nov 10 10:00:00 2026"
        launch.launch_requested = True
        launch.launcher = SimpleNamespace(communicate=Mock(), returncode=0)
        (self.data / ".omnishare.lock").write_text("pid=61002\n")
        return launch

    def test_procargs_preserves_boundaries_and_never_returns_environment(self):
        arguments = ["/App With Spaces/OmniShare", "--data-dir", "/Data With Spaces", "--name", ""]
        raw = struct.pack("=i", len(arguments)) + b"/executable\0\0\0" + b"\0".join(os.fsencode(a) for a in arguments) + b"\0PRIVATE=not-an-argument\0"
        self.assertEqual(MAC.parse_process_arguments(raw), arguments)
        for malformed in (b"", struct.pack("=i", -1), struct.pack("=i", 257),
                          struct.pack("=i", 2) + b"/exe\0\0/exe\0truncated"):
            with self.subTest(raw=malformed), self.assertRaises(MAC.VerificationError):
                MAC.parse_process_arguments(malformed)

    def test_identity_rejects_path_uid_parent_birth_and_argv_substitution(self):
        launch = self.launch()
        expected = [str(launch.binary), *launch.backend_arguments()]
        cases = ("valid", "path", "uid", "parent", "birth", "arguments")
        for case in cases:
            with self.subTest(case=case):
                actual_path = str(launch.binary) if case != "path" else "/another/application"
                def path(_pid, buffer, _size):
                    raw = os.fsencode(actual_path) + b"\0"
                    MAC.ctypes.memmove(buffer, raw, len(raw))
                    return len(raw) - 1
                launch.libproc.proc_pidpath.side_effect = path
                arguments = list(expected)
                if case == "arguments":
                    index = arguments.index(str(self.data))
                    arguments[index:index + 1] = str(self.data).split(" ")
                    self.assertEqual(" ".join(arguments), " ".join(expected))
                uid = os.getuid() + (case == "uid")
                parent = launch.app_pid + (case == "parent")
                birth = "Wed Nov 11 10:00:00 2026" if case == "birth" else launch.started
                result = subprocess.CompletedProcess([], 0, f"{uid} {birth} {parent}\n", "")
                with patch.object(MAC, "tool", return_value=result), patch.object(launch, "process_arguments", return_value=arguments):
                    if case == "valid":
                        self.assertEqual(launch.identity(launch.pid, require_parent=True), launch.started)
                    else:
                        with self.assertRaises(MAC.VerificationError):
                            launch.identity(launch.pid, require_parent=True)

    def test_launchservices_serial_is_permitted_only_on_the_native_app(self):
        launch = self.launch()
        app = str(launch.app_binary)
        expected = [app, *launch.args]
        def path(_pid, buffer, _size):
            raw = os.fsencode(app) + b"\0"
            MAC.ctypes.memmove(buffer, raw, len(raw))
            return len(raw) - 1
        launch.libproc.proc_pidpath.side_effect = path
        ps = subprocess.CompletedProcess(
            [], 0, f"{os.getuid()} {launch.app_started} 1\n", "")
        valid = [app, "-psn_0_123456", *launch.args]
        with patch.object(MAC, "tool", return_value=ps), patch.object(
                launch, "process_arguments", return_value=valid):
            self.assertEqual(launch.app_identity(launch.app_pid), launch.app_started)
        for bad in (
                [app, "-psn_0_11", "-psn_0_12", *launch.args],
                [app, "-psn_0_bad", *launch.args],
                [app, "-psn_0_123", "--unknown", *launch.args],
                [app, "-psn_0_123", *launch.args[:-1]]):
            with self.subTest(argv=bad), patch.object(
                    MAC, "tool", return_value=ps), patch.object(
                    launch, "process_arguments", return_value=bad):
                with self.assertRaises(MAC.VerificationError):
                    launch.app_identity(launch.app_pid)
        # The same flag must never be accepted as part of the child backend's
        # identity, even if its other arguments match.
        backend = str(launch.binary)
        def backend_path(_pid, buffer, _size):
            raw = os.fsencode(backend) + b"\0"
            MAC.ctypes.memmove(buffer, raw, len(raw))
            return len(raw) - 1
        launch.libproc.proc_pidpath.side_effect = backend_path
        child = [backend, "-psn_0_123", *launch.backend_arguments()]
        with patch.object(MAC, "tool", return_value=ps), patch.object(
                launch, "process_arguments", return_value=child):
            with self.assertRaises(MAC.VerificationError):
                launch.identity(launch.pid)

    def test_dynamic_port_is_taken_from_exact_child_arguments_and_parent_is_required(self):
        launch = self.launch(None)
        launch.pid, launch.started = None, None
        arguments = [str(launch.binary), *launch.args, "--data-dir", str(self.data), "--port", "49172",
                     "--no-browser", "--shutdown-on-stdin-close"]
        with patch.object(launch, "process_arguments", return_value=arguments), \
                patch.object(launch, "identity", return_value="birth") as identity:
            self.assertTrue(launch.capture_backend())
        self.assertEqual(launch.port, 49172)
        self.assertEqual(launch.pid, 61002)
        identity.assert_called_once_with(61002, require_parent=True)
        launch.port, launch.pid = None, None
        arguments[-3] = "65536"
        with patch.object(launch, "process_arguments", return_value=arguments), self.assertRaises(MAC.VerificationError):
            launch.capture_backend()
        self.assertIsNone(launch.pid)

    def test_quit_request_failure_still_cleans_both_owned_processes(self):
        launch = self.launch()
        alive = {launch.app_pid, launch.pid}
        signaled = []
        def identify(pid, **_):
            return "birth" if pid in alive else None
        def signal(pid, kind):
            signaled.append((pid, kind))
            alive.discard(pid)
        with patch.object(launch, "app_identity", side_effect=identify), patch.object(launch, "identity", side_effect=identify), \
                patch.object(MAC, "tool", side_effect=MAC.VerificationError("native quit refused")), \
                patch.object(MAC.os, "kill", side_effect=signal), self.assertRaisesRegex(MAC.VerificationError, "native quit refused"):
            launch.stop()
        self.assertEqual(signaled, [(61001, MAC.signal.SIGKILL), (61002, MAC.signal.SIGKILL)])
        self.assertFalse(alive)
        self.assertTrue(launch.stopped)
        launch.launcher.communicate.assert_called_once()

    def test_reused_app_pid_is_never_signaled_but_its_verified_child_is_cleaned(self):
        launch = self.launch()
        alive = {launch.pid}
        signaled = []
        def child(pid, **_):
            return "birth" if pid in alive else None
        def signal(pid, kind):
            signaled.append(pid)
            alive.discard(pid)
        with patch.object(launch, "app_identity", side_effect=MAC.VerificationError("PID reused")), \
                patch.object(launch, "identity", side_effect=child), patch.object(MAC.os, "kill", side_effect=signal), \
                self.assertRaisesRegex(MAC.VerificationError, "PID reused"):
            launch.stop()
        self.assertEqual(signaled, [61002])
        launch.launcher.communicate.assert_called_once()

    def test_only_a_matching_stdin_eof_exit_record_can_pass_normal_quit(self):
        normal = {"backend_pid": 61002, "exit_code": 0, "normal": True,
                  "method": "stdin-eof", "fallback_used": False}
        changes = ({}, {"method": "signal-fallback", "fallback_used": True},
                   {"backend_pid": 61003}, {"exit_code": False}, {"normal": False})
        for change in changes:
            with self.subTest(change=change):
                launch = self.launch()
                alive = {launch.app_pid, launch.pid}
                def identify(pid, **_):
                    return "birth" if pid in alive else None
                def quit_requested(*_, **__):
                    alive.clear()
                    (self.data / "desktop-shutdown.json").write_text(json.dumps({**normal, **change}))
                    return subprocess.CompletedProcess([], 0, "requested", "")
                with patch.object(launch, "app_identity", side_effect=identify), patch.object(launch, "identity", side_effect=identify), \
                        patch.object(MAC, "tool", side_effect=quit_requested), patch.object(MAC.os, "kill") as signal:
                    self.assertEqual(launch.stop(), not change)
                signal.assert_not_called()

    def test_reopen_rejects_an_additional_native_app_even_when_old_backend_survives(self):
        launch = self.launch()
        with patch.object(launch, "app_identity", return_value="birth"), patch.object(launch, "identity", return_value="birth"), \
                patch.object(launch, "application_pids", return_value=[61001, 61003]), \
                patch.object(MAC, "tool"), patch.object(MAC.time, "sleep"), self.assertRaisesRegex(MAC.VerificationError, "another backend"):
            launch.reopen()

    def test_start_rejects_preexisting_apps_and_removes_stale_shutdown_evidence(self):
        launch = self.launch()
        with patch.object(launch, "application_pids", return_value=[61001]), patch.object(MAC.subprocess, "Popen") as spawn, \
                self.assertRaisesRegex(MAC.VerificationError, "already running"):
            launch.start()
        spawn.assert_not_called()
        record = self.data / "desktop-shutdown.json"
        record.write_text('{"normal":true}')
        with patch.object(launch, "application_pids", return_value=[]), \
                patch.object(MAC.subprocess, "Popen", return_value=launch.launcher), \
                patch.object(launch, "capture_backend", return_value=True):
            launch.start()
        self.assertFalse(record.exists())


if __name__ == "__main__":
    unittest.main()
