#!/usr/bin/env python3
"""Verify final macOS ZIP/DMG files without weakening Gatekeeper policy.

The quarantine assessment is a simulation on disposable copies, not a Safari
download or a first-launch UI test. Runtime tests use separate, unquarantined
copies and never acknowledge or bypass a security warning.
"""

import argparse
from contextlib import contextmanager
import ctypes
import errno
import fcntl
import hashlib
import http.client
import json
import os
import platform
import plistlib
import re
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import tempfile
import time
import unicodedata
import uuid
import zipfile
from pathlib import Path


CHECKS = (
    "archive_layout", "signature_valid", "signature_survives_archive",
    "resource_tamper_rejected", "launch_services", "health_version", "http_ui",
    "clean_shutdown", "dmg_layout", "dmg_mount_readonly", "dmg_signature_valid",
    "dmg_app_matches_zip", "dmg_copy_survives_detach", "dmg_launch_services",
    "dmg_health_version", "dmg_http_ui", "dmg_clean_shutdown", "developer_id",
    "hardened_runtime", "secure_timestamp", "app_staple_valid",
    "dmg_staple_valid", "quarantine_policy",
)
PRODUCTION_CHECKS = CHECKS[-6:]
CHECKS = CHECKS[:-6] + (
    "core_features", "restart_persistence", "native_quit", "native_reopen", "port_conflict",
    "dmg_core_features", "dmg_restart_persistence", "dmg_native_quit", "dmg_native_reopen", "dmg_port_conflict",
) + PRODUCTION_CHECKS
QUARANTINE_ATTRIBUTE = "com.apple.quarantine"


class VerificationError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise VerificationError(message)


def safe_summary(text):
    # Backend startup can contain a first-run token. Never publish those lines.
    return "\n".join(
        line for line in str(text).splitlines()
        if "init_token" not in line.lower() and "first-run url" not in line.lower()
    )[:2048]


def tool(args, *, allow_failure=False, timeout=30):
    result = subprocess.run(
        [str(arg) for arg in args], capture_output=True, text=True,
        timeout=timeout, env={**os.environ, "LC_ALL": "C"},
    )
    if result.returncode and not allow_failure:
        raise VerificationError(
            f"{Path(args[0]).name} failed ({result.returncode}): "
            + safe_summary(result.stderr or result.stdout)
        )
    return result


def file_digest(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "Duplicate JSON property")
        result[key] = value
    return result


def validate_packaging(packaging, args, artifacts):
    """Bind the packager's attestations to the bytes being independently tested."""
    require(isinstance(packaging, dict) and
            type(packaging.get("schema_version")) is int and
            packaging["schema_version"] == 1, "Unsupported packaging evidence schema")
    for key in ("version", "arch", "mode"):
        require(packaging.get(key) == getattr(args, key),
                f"Packaging evidence has a different {key}")
    release = args.mode == "release"
    require(packaging.get("signing_mode") == ("developer-id" if release else "ad-hoc"),
            "Packaging evidence has the wrong signing mode")
    if release:
        require(isinstance(args.team_id, str) and
                re.fullmatch(r"[A-Z0-9]{10}", args.team_id),
                "Release verification requires an independently configured Team ID")
        require(packaging.get("team_id") == args.team_id,
                "Packaging evidence belongs to a different developer team")
    else:
        require(packaging.get("team_id") is None, "Rehearsal must not claim a developer team")
    recorded = packaging.get("artifacts")
    require(isinstance(recorded, dict) and set(recorded) == {"zip", "dmg"},
            "Packaging evidence must contain exactly the ZIP and DMG")
    for kind, actual in artifacts.items():
        item = recorded[kind]
        require(isinstance(item, dict) and item.get("name") == actual["name"] and
                item.get("sha256") == actual["sha256"],
                f"Packaging evidence does not match the final {kind.upper()} bytes")
    notarization = packaging.get("notarization")
    require(isinstance(notarization, dict) and set(notarization) == {"app", "dmg"},
            "Both app and DMG notarization records are required")
    identifiers = []
    for kind in ("app", "dmg"):
        record = notarization[kind]
        require(isinstance(record, dict), "Invalid notarization record")
        if release:
            identifier = record.get("submission_id")
            require(isinstance(identifier, str) and re.fullmatch(
                r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", identifier),
                "Invalid notarization submission ID")
            require(record.get("status") == "Accepted" and record.get("stapled") is True,
                    f"The {kind} was not accepted and stapled")
            identifiers.append(identifier)
        else:
            require(record.get("submission_id") is None and
                    record.get("status") == "NotSubmitted" and
                    record.get("stapled") is False,
                    "Rehearsal must not claim production notarization")
    require(not release or len(set(identifiers)) == 2,
            "App and DMG must have independent notarization submissions")
    return notarization


def parse_signature(output, mode, team_id, *, executable):
    """Check actual codesign output, in addition to the cryptographic requirement."""
    lines = output.splitlines()
    if mode == "rehearsal":
        require("Signature=adhoc" in lines, "Rehearsal must use an explicit ad-hoc signature")
        return {"signing_mode": "ad-hoc", "team_id": None,
                "hardened_runtime": False, "secure_timestamp": False}
    require(isinstance(team_id, str) and re.fullmatch(r"[A-Z0-9]{10}", team_id),
            "Invalid expected developer Team ID")
    require("Signature=adhoc" not in lines and f"TeamIdentifier={team_id}" in lines and
            any(line.startswith("Authority=Developer ID Application: ") and
                line.endswith(f"({team_id})") for line in lines),
            "The signature is not from the expected Developer ID Application team")
    timestamp = next((line.partition("=")[2] for line in lines
                      if line.startswith("Timestamp=")), "")
    require(timestamp and timestamp.lower() not in ("none", "not set", "0"),
            "A secure signing timestamp is required")
    flags = re.search(r"^CodeDirectory .*\bflags=0x([0-9a-fA-F]+)\b", output, re.MULTILINE)
    runtime = bool(flags and int(flags[1], 16) & 0x10000)
    require(not executable or runtime, "The app must enable the hardened runtime")
    return {"signing_mode": "developer-id", "team_id": team_id,
            "hardened_runtime": runtime, "secure_timestamp": True}


def verify_signature(path, mode, team_id, *, executable):
    verify = ["/usr/bin/codesign", "--verify", "--strict", "--verbose=2"]
    if executable:
        verify.append("--deep")
    if mode == "release":
        require(isinstance(team_id, str) and re.fullmatch(r"[A-Z0-9]{10}", team_id),
                "Release verification requires an independently configured Team ID")
        # Verify the certificate chain and Developer ID Application OID, not
        # merely the human-readable Authority or TeamIdentifier output.
        requirement = (
            "anchor apple generic and "
            "certificate leaf[field.1.2.840.113635.100.6.1.13] exists and "
            f'certificate leaf[subject.OU] = "{team_id}"'
        )
        verify.extend(["-R", requirement])
    tool([*verify, path])
    displayed = tool(["/usr/bin/codesign", "--display", "--verbose=4", path])
    return parse_signature(displayed.stdout + "\n" + displayed.stderr,
                           mode, team_id, executable=executable)


def bundle_manifest(app):
    manifest = {}
    folded = set()
    for item in [app, *sorted(app.rglob("*"))]:
        relative = item.relative_to(app).as_posix()
        name = unicodedata.normalize("NFC", relative).casefold()
        require(name not in folded, "Ambiguous extracted bundle path")
        folded.add(name)
        mode = item.lstat().st_mode
        require(stat.S_ISDIR(mode) or stat.S_ISREG(mode),
                "Extracted bundle has a link or special file")
        require(not mode & 0o022, "Extracted bundle is group/world writable")
        manifest[relative] = {
            "type": "directory" if stat.S_ISDIR(mode) else "file",
            "mode": stat.S_IMODE(mode),
            "sha256": file_digest(item) if stat.S_ISREG(mode) else None,
        }
    return manifest


def check_archive(path):
    """Reject unsafe paths/types before invoking the macOS ZIP extractor."""
    required = {
        "OmniShare.app/Contents/Info.plist",
        "OmniShare.app/Contents/MacOS/OmniShare",
        "OmniShare.app/Contents/MacOS/omnishare-server",
        "OmniShare.app/Contents/Resources/README.txt",
        "OmniShare.app/Contents/_CodeSignature/CodeResources",
    }
    with zipfile.ZipFile(path) as archive:
        entries = archive.infolist()
        require(0 < len(entries) <= 512, "Unexpected archive entry count")
        require(sum(item.file_size for item in entries) <= 256 * 1024 * 1024,
                "Archive expands beyond the macOS package limit")
        seen = {}
        files = set()
        for item in entries:
            name = item.filename.rstrip("/")
            parts = name.split("/")
            require(item.orig_filename == item.filename and
                    not item.filename.startswith("/") and "\\" not in name and
                    not any(ord(char) < 32 for char in name) and
                    all(part not in ("", ".", "..") for part in parts),
                    "Unsafe archive path")
            require(parts[0] in ("OmniShare.app", "__MACOSX"),
                    "Unexpected archive root")
            folded = unicodedata.normalize("NFC", name).casefold()
            require(folded not in seen, "Ambiguous archive path")
            mode = item.external_attr >> 16
            require(not item.flag_bits & 1 and
                    (stat.S_ISDIR(mode) if item.is_dir() else stat.S_ISREG(mode)),
                    "Encrypted, linked or special archive entry")
            require(not mode & 0o022, "Group/world-writable archive entry")
            seen[folded] = item.is_dir()
            if not item.is_dir():
                files.add(name)
                require(item.file_size > 0, "Empty archive file")
            if name == "OmniShare.app/Contents/MacOS/OmniShare":
                require(mode & stat.S_IXUSR, "Archived executable is not executable")
        for name in seen:
            components = name.split("/")
            for end in range(1, len(components)):
                parent = "/".join(components[:end])
                require(parent not in seen or seen[parent], "Archive file shadows a directory")
        require(required <= files, "Archive is missing bundle/signature files")
        require(archive.testzip() is None, "Archive CRC verification failed")


def verify_bundle(app, arch, version, mode, team_id):
    binary = app / "Contents/MacOS/OmniShare"
    bundle_manifest(app)
    require(binary.is_file() and binary.stat().st_mode & stat.S_IXUSR,
            "Extracted main executable is not executable")
    plist = app / "Contents/Info.plist"
    tool(["/usr/bin/plutil", "-lint", plist])
    with plist.open("rb") as stream:
        info = plistlib.load(stream)
    expected = {
        "CFBundleExecutable": "OmniShare", "CFBundlePackageType": "APPL",
        "CFBundleIdentifier": "local.omnishare.desktop",
        "CFBundleShortVersionString": version.split("-")[0],
        "CFBundleVersion": version.split("-")[0], "LSMinimumSystemVersion": "12.0",
    }
    require(all(info.get(key) == value for key, value in expected.items()),
            "Bundle metadata does not match the release")
    machine = {"amd64": "x86_64", "arm64": "arm64"}[arch]
    require(tool(["/usr/bin/lipo", "-archs", binary]).stdout.strip() == machine,
            "Extracted binary has the wrong architecture")
    backend = binary.with_name("omnishare-server")
    require(backend.is_file() and backend.stat().st_mode & stat.S_IXUSR,
            "Embedded backend is missing or not executable")
    require(tool(["/usr/bin/lipo", "-archs", backend]).stdout.strip() == machine,
            "Embedded backend has the wrong architecture")
    verify_signature(backend, mode, team_id, executable=True)
    return verify_signature(app, mode, team_id, executable=True)


def check_tamper(app, destination):
    copied = destination / "OmniShare.app"
    shutil.copytree(app, copied)
    tool(["/usr/bin/codesign", "--verify", "--deep", "--strict", copied])
    with (copied / "Contents/Resources/README.txt").open("ab") as stream:
        stream.write(b"\nIntentional resource integrity test.\n")
    result = tool(
        ["/usr/bin/codesign", "--verify", "--deep", "--strict", copied],
        allow_failure=True,
    )
    require(result.returncode != 0, "A modified signed resource was accepted")


@contextmanager
def mounted_dmg(dmg, mount):
    """Mount only at our private location and always detach that exact mount."""
    require(not mount.exists(), "DMG mount directory must be new")
    mount.mkdir(mode=0o700)
    try:
        attached = tool([
            "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen",
            "-mountpoint", mount, "-plist", dmg,
        ], timeout=90)
        info = plistlib.loads(attached.stdout.encode())
        mounts = [item.get("mount-point") for item in info.get("system-entities", [])
                  if item.get("mount-point")]
        require(mounts == [str(mount)] and os.path.ismount(mount),
                "DMG did not mount exclusively at the requested private location")
        require(os.statvfs(mount).f_flag & os.ST_RDONLY, "DMG filesystem is not read-only")
        yield mount
    finally:
        # Also clean up a partial attach. Never detach a device or volume based
        # on an untrusted path returned inside the image's plist.
        if os.path.ismount(mount):
            tool(["/usr/bin/hdiutil", "detach", mount], timeout=90)
        require(not os.path.ismount(mount), "The test DMG is still mounted")


def verify_dmg_layout(mount):
    # HFS/APFS may supply these housekeeping entries when a volume is created.
    # They must not introduce another user-facing payload or a linked path.
    housekeeping = {".DS_Store", ".fseventsd", ".Trashes", ".Spotlight-V100"}
    entries = {item.name: item for item in mount.iterdir()}
    require({"OmniShare.app", "Applications"} <= entries.keys() and
            not (entries.keys() - {"OmniShare.app", "Applications"} - housekeeping),
            "DMG must contain only OmniShare.app and the Applications shortcut")
    app, shortcut = entries["OmniShare.app"], entries["Applications"]
    require(app.is_dir() and not app.is_symlink(), "DMG app is missing or linked")
    require(shortcut.is_symlink() and os.readlink(shortcut) == "/Applications",
            "DMG Applications shortcut must point exactly to /Applications")
    require(all(not item.is_symlink() and (item.is_file() or item.is_dir())
                for name, item in entries.items() if name in housekeeping),
            "DMG contains unsafe filesystem housekeeping entries")
    return app


def quarantine_targets(path):
    targets = [path]
    if path.suffix == ".app":
        targets.append(path / "Contents/MacOS/OmniShare")
    return targets


def simulate_quarantine(path):
    # This is deliberately a test-only simulation. The xattr format is not a
    # public download API and this does not emulate Safari/Archive Utility UI.
    value = f"0083;{int(time.time()):x};OmniShareReleaseVerifier;{uuid.uuid4()}"
    for item in quarantine_targets(path):
        require(not item.is_symlink(), "Refusing to quarantine a symlink")
        tool(["/usr/bin/xattr", "-w", QUARANTINE_ATTRIBUTE, value, item])
        recorded = tool(["/usr/bin/xattr", "-p", QUARANTINE_ATTRIBUTE, item]).stdout.strip()
        require(recorded == value,
                "The isolated policy copy was not quarantined")


def verify_quarantine_preserved(path):
    for item in quarantine_targets(path):
        require(not item.is_symlink(), "Policy copy became a symlink")
        value = tool(["/usr/bin/xattr", "-p", QUARANTINE_ATTRIBUTE, item]).stdout.strip()
        parts = value.split(";")
        require(len(parts) >= 3 and re.fullmatch(r"[0-9a-fA-F]{4,8}", parts[0]) and
                int(parts[0], 16) & 1 and re.fullmatch(r"[0-9a-fA-F]+", parts[1]) and
                parts[2] == "OmniShareReleaseVerifier",
                "Policy assessment removed or invalidated the simulated quarantine")


def assess_quarantine(path, *, app):
    simulate_quarantine(path)
    command = ["/usr/sbin/spctl", "--assess", "--type", "execute" if app else "open",
               "--verbose=4"]
    if not app:
        command.extend(["--context", "context:primary-signature"])
    assessment = tool([*command, path], allow_failure=True, timeout=90)
    output = assessment.stderr + "\n" + assessment.stdout
    source = next((line.partition("=")[2] for line in output.splitlines()
                   if line.startswith("source=")), None)
    result = {
        "quarantine_verified": False,
        "assessment_exit_code": assessment.returncode,
        "assessment_result": "accepted" if assessment.returncode == 0 else "rejected",
        "assessment_source": source,
        "assessment_output": safe_summary(assessment.stderr or assessment.stdout),
    }
    if app:
        available = Path("/usr/bin/syspolicy_check").is_file()
        policy = {"available": available, "exit_code": None,
                  "result": "unavailable", "output": ""}
        if available:
            assessed = tool(["/usr/bin/syspolicy_check", "distribution", path],
                            allow_failure=True, timeout=120)
            policy.update(exit_code=assessed.returncode,
                          result="accepted" if assessed.returncode == 0 else "rejected",
                          output=safe_summary(assessed.stderr or assessed.stdout))
        result["syspolicy_check"] = policy
    # An assessment may legitimately update quarantine flags. Check that the
    # original marker still exists, rather than overwriting it with old flags.
    verify_quarantine_preserved(path)
    result["quarantine_verified"] = True
    return result


def policy_accepted(result, *, app):
    accepted = (result.get("quarantine_verified") is True and
                type(result.get("assessment_exit_code")) is int and
                result["assessment_exit_code"] == 0 and
                result.get("assessment_result") == "accepted" and
                result.get("assessment_source") == "Notarized Developer ID")
    if app:
        policy = result.get("syspolicy_check", {})
        accepted = (accepted and policy.get("available") is True and
                    type(policy.get("exit_code")) is int and policy["exit_code"] == 0 and
                    policy.get("result") == "accepted")
    return accepted


def parse_process_arguments(raw):
    """Read only argc arguments from Darwin KERN_PROCARGS2, never environment."""
    require(len(raw) >= 4, "Truncated native process argument header")
    count = struct.unpack_from("=i", raw)[0]
    require(1 <= count <= 256, "Invalid native process argument count")
    end = raw.find(b"\0", 4)
    require(end >= 4, "Missing native process executable path")
    offset = end + 1
    while offset < len(raw) and raw[offset] == 0:
        offset += 1
    arguments = []
    for _ in range(count):
        end = raw.find(b"\0", offset)
        require(end >= offset, "Truncated native process arguments")
        arguments.append(os.fsdecode(raw[offset:end]))
        offset = end + 1
    return arguments


class ScopedLaunch:
    """Bind the native app and its backend to this private installed copy."""

    def __init__(self, app, data, port):
        self.app = app.resolve()
        self.app_binary = self.app / "Contents/MacOS/OmniShare"
        self.binary = self.app / "Contents/MacOS/omnishare-server"
        self.data = data.resolve()
        self.port = port
        self.args = ["--data-dir", str(self.data), "--listen", "127.0.0.1", "--no-browser"]
        if port is not None:
            self.args += ["--port", str(port)]
        self.pid = None
        self.started = None
        self.app_pid = None
        self.app_started = None
        self.previous_pid = None
        self.launcher = None
        self.launch_requested = False
        self.stopped = False
        self.libproc = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        self.libproc.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
        self.libproc.proc_pidpath.restype = ctypes.c_int
        self.libc = ctypes.CDLL(None, use_errno=True)
        self.libc.sysctl.argtypes = [ctypes.POINTER(ctypes.c_int), ctypes.c_uint,
                                    ctypes.c_void_p, ctypes.POINTER(ctypes.c_size_t),
                                    ctypes.c_void_p, ctypes.c_size_t]
        self.libc.sysctl.restype = ctypes.c_int

    def lock_pid(self):
        try:
            content = (self.data / ".omnishare.lock").read_text()
        except FileNotFoundError:
            return None
        # Acquire creates/truncates the lock before it writes the complete PID
        # record. Treat that short window as startup, not a malformed instance.
        if not content or not content.endswith("\n"):
            return None
        match = re.fullmatch(r"pid=([1-9][0-9]*)\n", content)
        require(match, "Invalid isolated instance lock")
        pid = int(match[1])
        require(pid > 1 and pid != os.getpid(), "Invalid isolated instance PID")
        return pid

    def process_arguments(self, pid):
        mib = (ctypes.c_int * 3)(1, 49, pid)  # CTL_KERN, KERN_PROCARGS2, pid
        size = ctypes.c_size_t(0)
        if self.libc.sysctl(mib, 3, None, ctypes.byref(size), None, 0) != 0:
            if ctypes.get_errno() in (errno.ESRCH, errno.EINVAL):
                return None
            raise VerificationError("Unable to size native process arguments")
        require(4 < size.value <= 2 * 1024 * 1024, "Native process arguments exceed test limits")
        buffer = ctypes.create_string_buffer(size.value)
        if self.libc.sysctl(mib, 3, buffer, ctypes.byref(size), None, 0) != 0:
            if ctypes.get_errno() in (errno.ESRCH, errno.EINVAL):
                return None
            raise VerificationError("Unable to read native process arguments")
        return parse_process_arguments(buffer.raw[:size.value])

    def process_identity(self, pid, binary, started, arguments, parent=None):
        buffer = ctypes.create_string_buffer(4096)
        if self.libproc.proc_pidpath(pid, buffer, len(buffer)) <= 0:
            return None
        require(Path(os.fsdecode(buffer.value)).resolve() == binary,
                "Refusing to operate on a process outside this extracted app")
        result = tool(
            ["/bin/ps", "-ww", "-p", str(pid), "-o", "uid=", "-o", "lstart=",
             "-o", "ppid="], allow_failure=True,
        )
        if result.returncode:
            return None
        values = result.stdout.strip().split()
        require(len(values) == 7 and values[0].isdigit() and int(values[0]) == os.getuid(),
                "Refusing a process with different ownership or startup arguments")
        require(parent is None or values[6] == str(parent),
                "Backend is not owned by the native application")
        actual_arguments = self.process_arguments(pid)
        if actual_arguments is None:
            return None
        expected_arguments = [str(binary), *arguments]
        if binary == self.app_binary and parent is None:
            # LaunchServices can inject a process serial number into the GUI
            # application's argv; Swift deliberately ignores the same flag.
            # Permit only one well-formed OS-generated marker on the app,
            # never on the backend and never any other unexpected argument.
            psn = [i for i, value in enumerate(actual_arguments[1:], 1)
                   if re.fullmatch(r"-psn_0_[0-9]+", value)]
            require(len(psn) <= 1, "Unexpected multiple LaunchServices serial arguments")
            if psn:
                actual_arguments = (actual_arguments[:psn[0]]
                                    + actual_arguments[psn[0] + 1:])
        # proc_pidpath above binds the kernel executable path; argv[0] is
        # process-display text and may be rewritten by LaunchServices. Check
        # every real argument, not the untrusted process display name.
        # Foundation standardizes the second --data-dir to an equivalent
        # canonical file URL, so compare that one by resolved identity.
        expected_arguments = [str(binary), *arguments]
        if len(actual_arguments) != len(expected_arguments):
            raise VerificationError("Refusing a process with different startup argument count")
        observed_flags = list(actual_arguments[1:])
        expected_flags = expected_arguments[1:]
        if binary == self.binary and len(observed_flags) > 8:
            require(expected_flags[7] == "--data-dir" and
                    observed_flags[7] == "--data-dir",
                    "Backend data directory argument is missing")
            original_dir = Path(observed_flags[8])
            require(original_dir.is_absolute() and original_dir.resolve() == self.data,
                    "Backend is using a different data directory")
            observed_flags[8] = expected_flags[8]
        require(observed_flags == expected_flags,
                "Refusing a process with different ownership or startup arguments "
                f"(mismatch indexes={[i + 1 for i, (one, two) in enumerate(zip(observed_flags, expected_flags)) if one != two][:6]})")
        actual_started = " ".join(values[1:6])
        require(started is None or started == actual_started,
                "Isolated instance PID was reused")
        return actual_started

    def backend_arguments(self):
        return [*self.args, "--data-dir", str(self.data), "--port", str(self.port),
                "--no-browser", "--shutdown-on-stdin-close"]

    def identity(self, pid, *, require_parent=False):
        return self.process_identity(pid, self.binary, self.started, self.backend_arguments(),
                                     self.app_pid if require_parent else None)

    def app_identity(self, pid):
        return self.process_identity(pid, self.app_binary, self.app_started,
                                     self.args)

    def application_pids(self):
        # NSWorkspace supplies the running application's exact bundle URL.
        # Process path/UID/arguments are independently checked before use.
        script = ("ObjC.import('AppKit'); const apps=$.NSWorkspace.sharedWorkspace.runningApplications;"
                  "const found=[]; for(let i=0;i<apps.count;i++){const a=apps.objectAtIndex(i);"
                  "if(!a.bundleURL.isNil() && ObjC.unwrap(a.bundleURL.path)===" + json.dumps(str(self.app)) + ")"
                  "found.push(Number(a.processIdentifier));} JSON.stringify(found);")
        result = tool(["/usr/bin/osascript", "-l", "JavaScript", "-e", script])
        candidates = json.loads(result.stdout)
        require(isinstance(candidates, list) and all(type(pid) is int and pid > 1 for pid in candidates),
                "Invalid native application PID")
        return candidates

    def find_application(self):
        candidates = self.application_pids()
        require(len(candidates) <= 1,
                "Multiple native applications match this isolated bundle")
        if candidates:
            pid = candidates[0]
            started = self.app_identity(pid)
            if started:
                self.app_pid, self.app_started = pid, started

    def capture_backend(self):
        """Capture a child only after its exact app parent has been identified."""
        if self.app_pid is None:
            return False
        pid = self.lock_pid()
        if pid is None or pid == self.previous_pid:
            return False
        if self.port is None:
            arguments = self.process_arguments(pid)
            if arguments is None:
                return False
            prefix = [str(self.binary), *self.args, "--data-dir", str(self.data), "--port"]
            require(len(arguments) == len(prefix) + 3 and arguments[:len(prefix)] == prefix
                    and arguments[-2:] == ["--no-browser", "--shutdown-on-stdin-close"]
                    and re.fullmatch(r"[0-9]{1,5}", arguments[-3]),
                    "Backend has unexpected startup arguments")
            self.port = int(arguments[-3])
            require(1 <= self.port <= 65535, "Native launcher selected an invalid port")
        identity = self.identity(pid, require_parent=True)
        if identity is None:
            return False
        self.pid, self.started = pid, identity
        return True

    def start(self):
        require(not self.application_pids(), "The isolated native application was already running")
        self.previous_pid = self.lock_pid()
        # A previous run's record cannot attest the exit of this new process.
        (self.data / "desktop-shutdown.json").unlink(missing_ok=True)
        self.launcher = subprocess.Popen(
            ["/usr/bin/open", "-n", "-W", str(self.app), "--args", *self.args],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        self.launch_requested = True
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if self.app_pid is None:
                self.find_application()
            if self.capture_backend():
                return
            require(self.launcher.poll() is None, "LaunchServices exited before app startup")
            time.sleep(0.2)
        raise VerificationError("LaunchServices did not start the isolated app within 45 seconds")

    def request(self, endpoint, limit):
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=2)
        try:
            connection.request("GET", endpoint)
            response = connection.getresponse()
            body = response.read(limit + 1)
            require(len(body) <= limit, "HTTP response exceeded test limits")
            return response.status, response.getheader("Content-Type", ""), body
        finally:
            connection.close()

    def check_http(self, version):
        deadline = time.monotonic() + 30
        while True:
            require(self.identity(self.pid), "App exited before HTTP became ready")
            listeners = tool(
                ["/usr/sbin/lsof", "-n", "-P", "-a", "-p", str(self.pid),
                 "-iTCP", "-sTCP:LISTEN", "-Fpn"], allow_failure=True,
            )
            names = {line[1:] for line in listeners.stdout.splitlines() if line.startswith("n")}
            if names:
                require(names == {f"127.0.0.1:{self.port}"},
                        "App is not listening exclusively on its assigned loopback port")
                try:
                    status, content_type, body = self.request("/api/v1/health", 65536)
                    require(status == 200 and "application/json" in content_type,
                            "Health endpoint did not return JSON successfully")
                    payload = json.loads(body)
                    require(payload.get("code") == 0 and
                            payload.get("data", {}).get("status") == "ok" and
                            payload["data"].get("version") == version,
                            "Health endpoint returned the wrong release version")
                    break
                except (OSError, http.client.HTTPException):
                    pass
            require(time.monotonic() < deadline, "App HTTP listener did not become ready")
            time.sleep(0.2)
        status, content_type, body = self.request("/", 1024 * 1024)
        require(status == 200 and "text/html" in content_type and
                re.search(rb"<title>\s*OmniShare\s*</title>", body, re.IGNORECASE) and
                b"/app.js" in body, "Root URL did not serve the embedded OmniShare UI")
        require(self.identity(self.pid), "App exited during HTTP verification")

    def reopen(self):
        require(self.app_identity(self.app_pid) and self.identity(self.pid),
                "Native app is not running before reopen")
        tool(["/usr/bin/open", str(self.app)])
        time.sleep(0.7)
        require(self.application_pids() == [self.app_pid]
                and self.app_identity(self.app_pid) and self.identity(self.pid, require_parent=True)
                and self.lock_pid() == self.pid, "Reopen created another backend or lost the running instance")

    def force_cleanup(self):
        """Failure-only cleanup; revalidate each independently owned process."""
        forced = False
        failure = None
        if self.pid is None and self.app_pid is not None:
            try:
                self.capture_backend()
            except (VerificationError, OSError) as error:
                failure = error
        for pid, identify in ((self.app_pid, self.app_identity), (self.pid, self.identity)):
            if pid is None:
                continue
            try:
                if identify(pid) is not None:
                    os.kill(pid, signal.SIGKILL)
                    forced = True
                    deadline = time.monotonic() + 5
                    while identify(pid) is not None and time.monotonic() < deadline:
                        time.sleep(0.05)
                    require(identify(pid) is None, "An isolated process survived failure cleanup")
            except ProcessLookupError:
                pass
            except (VerificationError, OSError) as error:
                # A mismatched/reused PID is never signaled. Still clean the
                # other independently identified process before reporting it.
                failure = failure or error
        if failure is not None:
            raise failure
        return forced

    def stop(self):
        graceful = False
        try:
            if self.app_pid is None and self.launch_requested:
                self.find_application()
            if self.pid is None and self.app_pid is not None:
                self.capture_backend()
            pid = self.pid
            if self.app_pid is not None and self.app_identity(self.app_pid):
                script = ("ObjC.import('AppKit'); const a=$.NSRunningApplication."
                          f"runningApplicationWithProcessIdentifier({self.app_pid});"
                          "if(a.isNil() || !a.terminate) throw new Error('Native quit was refused'); 'requested';")
                tool(["/usr/bin/osascript", "-l", "JavaScript", "-e", script])
                deadline = time.monotonic() + 22
                while time.monotonic() < deadline:
                    app_alive = self.app_identity(self.app_pid) is not None
                    child_alive = pid is not None and self.identity(pid) is not None
                    if not app_alive and not child_alive:
                        break
                    time.sleep(0.1)
                if self.app_identity(self.app_pid) is None and pid is not None and self.identity(pid) is None:
                    record_path = self.data / "desktop-shutdown.json"
                    if record_path.is_file() and not record_path.is_symlink():
                        record = json.loads(record_path.read_text())
                        graceful = (type(record.get("backend_pid")) is int and record["backend_pid"] == pid
                                    and type(record.get("exit_code")) is int and record["exit_code"] == 0
                                    and record.get("normal") is True
                                    and record.get("method") == "stdin-eof" and record.get("fallback_used") is False)
        finally:
            try:
                if self.force_cleanup():
                    graceful = False
            finally:
                # This subprocess is the exact open process created above.
                # Its cleanup must still happen if either identity check fails.
                if self.launcher is not None:
                    try:
                        self.launcher.communicate(timeout=5)
                    except subprocess.TimeoutExpired:
                        self.launcher.terminate()
                        graceful = False
                        try:
                            self.launcher.communicate(timeout=5)
                        except subprocess.TimeoutExpired:
                            self.launcher.kill()
                            self.launcher.communicate(timeout=5)
                    graceful = graceful and self.launcher.returncode == 0
                self.stopped = True
        if graceful:
            with (self.data / ".omnishare.lock").open("rb") as stream:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                fcntl.flock(stream, fcntl.LOCK_UN)
        return graceful


def run_features(launch, version, state, phase):
    report_path = state.with_name(state.name + "." + phase + ".report.json")
    result = tool([sys.executable, Path(__file__).with_name("test-installed-features.py"),
                   "--base-url", f"http://127.0.0.1:{launch.port}", "--data-dir", launch.data,
                   "--version", version, "--state", state, "--phase", phase,
                   "--report", report_path], timeout=180)
    report = json.loads(result.stdout)
    require(report.get("success") is True and report.get("phase") == phase
            and report.get("version") == version, "Installed feature verification failed")
    return report


def check_runtime(app, data, version, report, kind):
    for item in quarantine_targets(app):
        require(not item.is_symlink(), "Runtime path became a symlink")
        attributes = tool(["/usr/bin/xattr", item]).stdout.splitlines()
        require(QUARANTINE_ATTRIBUTE not in attributes,
                "Runtime copy is quarantined; refusing to bypass or automate a security prompt")
    data.mkdir(mode=0o700)
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        port = reservation.getsockname()[1]
    launch = ScopedLaunch(app, data, port)
    prefix = "" if kind == "zip" else "dmg_"
    try:
        launch.start()
        report["checks"][prefix + "launch_services"] = True
        launch.check_http(version)
        report["checks"].update({prefix + "health_version": True, prefix + "http_ui": True})
        report.setdefault("runtime", {"quarantined": False, "policy_test_separate": True})[kind] = {
            "launch_method": "open -n -W", "pid": launch.pid, "app_pid": launch.app_pid,
            "loopback_only": True, "browser_suppressed": True,
            "path_contains_spaces": " " in str(app),
            "quarantined": False,
        }
        state = data.parent / (kind + "-private-feature-state.json")
        runtime = report["runtime"][kind]
        runtime["features"] = {"exercise": run_features(launch, version, state, "exercise")}
        report["checks"][prefix + "core_features"] = True
        launch.reopen()
        report["checks"][prefix + "native_reopen"] = True
        require(launch.stop(), f"The {kind.upper()} instance did not shut down cleanly")
        report["checks"][prefix + "clean_shutdown"] = True
        report["checks"][prefix + "native_quit"] = True
        # Exercise Go's existing config-backup recovery through the native
        # launcher too. This is isolated fixture data, never a user's profile.
        config_path = data / "config.json"
        saved_config = config_path.read_bytes()
        require(isinstance(json.loads(saved_config), dict), "Installed configuration is not an object")
        backup_path = data / "config.json.bak"
        backup_path.write_bytes(saved_config)
        backup_path.chmod(0o600)
        config_path.write_bytes(b'{"interrupted-write":')
        # Occupy its persisted port; the native app must choose and save a free
        # port on an ordinary reopen while retaining the same user's database.
        with socket.socket() as blocker:
            blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            blocker.bind(("127.0.0.1", port))
            blocker.listen()
            launch = ScopedLaunch(app, data, None)
            launch.start()
            require(launch.port != port, "Native app did not recover from a port conflict")
            launch.check_http(version)
            report["checks"][prefix + "port_conflict"] = True
            runtime["features"]["verify"] = run_features(launch, version, state, "verify")
            report["checks"][prefix + "restart_persistence"] = True
            require(launch.stop(), "Restarted native app did not quit cleanly")
    finally:
        if not launch.stopped:
            launch.stop()
    # Runtime must not modify the signed bundle, for either installation path.
    tool(["/usr/bin/codesign", "--verify", "--deep", "--strict", app])


def verify(args, report):
    require(platform.system() == "Darwin", "Native macOS is required")
    require(re.fullmatch(r"\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.?\d+)?", args.version),
            "Unsupported release version")
    require(args.archive.name == f"OmniShare-v{args.version}-macos-{args.arch}.zip",
            "Archive filename does not match version and architecture")
    require(args.dmg.name == f"OmniShare-v{args.version}-macos-{args.arch}.dmg",
            "DMG filename does not match version and architecture")
    require(args.packaging_evidence.name == f"omnishare-macos-{args.arch}-packaging.json",
            "Packaging evidence filename does not match architecture")
    expected_machine = {"amd64": "x86_64", "arm64": "arm64"}[args.arch]
    require(platform.machine() == expected_machine, "Use a native runner matching the archive")
    require(args.packaging_evidence.stat().st_size <= 256 * 1024,
            "Packaging evidence exceeds its size limit")
    packaging = json.loads(args.packaging_evidence.read_text(), object_pairs_hook=unique_object)
    report["notarization"] = validate_packaging(packaging, args, report["artifacts"])
    release = args.mode == "release"
    status = tool(["/usr/sbin/spctl", "--status"], allow_failure=True)
    enabled = status.returncode == 0 and "assessments enabled" in (
        status.stdout + "\n" + status.stderr).splitlines()
    report["gatekeeper"]["policy_enabled"] = enabled
    require(not release or enabled, "Gatekeeper must be enabled for release verification")
    require(not release or Path("/usr/bin/syspolicy_check").is_file(),
            "Release policy verification requires syspolicy_check on macOS 14 or newer")
    check_archive(args.archive)
    with tempfile.TemporaryDirectory(prefix="OmniShare macOS verification ") as temporary:
        root = Path(temporary).resolve()
        extracted = root / "Application With Spaces"
        extracted.mkdir(mode=0o700)
        tool(["/usr/bin/ditto", "-x", "-k", args.archive, extracted])
        app = extracted / "OmniShare.app"
        signature = verify_bundle(app, args.arch, args.version, args.mode, args.team_id)
        report["checks"].update(archive_layout=True, signature_valid=True,
                                signature_survives_archive=True)
        report["signatures"] = {"zip_app": signature}
        if release:
            tool(["/usr/bin/xcrun", "stapler", "validate", app], timeout=90)
            report["checks"].update(developer_id=True, hardened_runtime=True,
                                    secure_timestamp=True, app_staple_valid=True)
        check_tamper(app, root / "Tamper Check")
        report["checks"]["resource_tamper_rejected"] = True
        report["signatures"]["dmg"] = verify_signature(
            args.dmg, args.mode, args.team_id, executable=False)
        report["checks"]["dmg_signature_valid"] = True
        tool(["/usr/bin/hdiutil", "verify", args.dmg], timeout=90)
        if release:
            tool(["/usr/bin/xcrun", "stapler", "validate", args.dmg], timeout=90)
            report["checks"]["dmg_staple_valid"] = True
        installed = root / "Installed Applications With Spaces" / "OmniShare.app"
        installed.parent.mkdir(mode=0o700)
        zip_manifest = bundle_manifest(app)
        with mounted_dmg(args.dmg, root / "Read Only DMG") as mount:
            report["checks"]["dmg_mount_readonly"] = True
            mounted_app = verify_dmg_layout(mount)
            report["checks"]["dmg_layout"] = True
            verify_bundle(mounted_app, args.arch, args.version, args.mode, args.team_id)
            require(bundle_manifest(mounted_app) == zip_manifest,
                    "DMG and ZIP contain different app resources, executable bytes or modes")
            report["checks"]["dmg_app_matches_zip"] = True
            tool(["/usr/bin/ditto", mounted_app, installed])
        # The source image has been detached. Validate and launch only the copy,
        # as a user would after dragging the app out of the disk image.
        report["signatures"]["dmg_app"] = verify_bundle(
            installed, args.arch, args.version, args.mode, args.team_id)
        require(bundle_manifest(installed) == zip_manifest,
                "The app changed when copied out of the DMG")
        if release:
            tool(["/usr/bin/xcrun", "stapler", "validate", installed], timeout=90)
        report["checks"]["dmg_copy_survives_detach"] = True
        report["gatekeeper"]["notarized"] = release

        # Assess disposable copies separately from runtime. Never remove an
        # existing quarantine flag and never automate an override dialog. Do
        # this before either runtime launch, so launch cannot prime this check.
        policy_root = root / "Quarantine Policy Simulation"
        policy_root.mkdir(mode=0o700)
        for label, source in (("zip_app", app), ("dmg", args.dmg), ("dmg_app", installed)):
            location = policy_root / label
            location.mkdir(mode=0o700)
            copied = location / source.name
            tool(["/usr/bin/ditto", source, copied])
            if label == "dmg":
                require(file_digest(copied) == report["artifacts"]["dmg"]["sha256"],
                        "Policy copy is not the final DMG")
            else:
                require(bundle_manifest(copied) == zip_manifest,
                        "Policy copy differs from the packaged application")
            result = assess_quarantine(copied, app=label != "dmg")
            report["gatekeeper"][label] = result
            require(not release or policy_accepted(result, app=label != "dmg"),
                    f"Gatekeeper rejected the quarantined {label} policy copy")
        report["gatekeeper"]["quarantine_policy_verified"] = release
        report["checks"]["quarantine_policy"] = release
        check_runtime(app, root / "ZIP 用户 Data #100%", args.version, report, "zip")
        check_runtime(installed, root / "DMG 用户 Data #100%", args.version, report, "dmg")
    for kind, path in (("zip", args.archive), ("dmg", args.dmg)):
        require(file_digest(path) == report["artifacts"][kind]["sha256"],
                f"The final {kind.upper()} changed during verification")
    require(file_digest(args.packaging_evidence) == report["packaging"]["sha256"],
            "Packaging evidence changed during verification")
    required = CHECKS if release else [name for name in CHECKS if name not in PRODUCTION_CHECKS]
    require(all(report["checks"][name] is True for name in required),
            "Required verification checks are incomplete")
    report["production_ready"] = release
    report["success"] = True


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--dmg", required=True, type=Path)
    parser.add_argument("--packaging-evidence", required=True, type=Path)
    parser.add_argument("--mode", choices=("release", "rehearsal"), default="release")
    parser.add_argument("--team-id", default="")
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--evidence", required=True, type=Path)
    return parser.parse_args(argv)


def initial_report(args):
    return {
        "schema_version": 2, "success": False, "production_ready": False,
        "mode": args.mode, "version": args.version,
        "arch": args.arch, "asset": args.archive.name, "sha256": "",
        "signing_mode": "developer-id" if args.mode == "release" else "ad-hoc",
        "team_id": args.team_id if args.mode == "release" else None,
        "minimum_macos": "12.0", "checks": {name: False for name in CHECKS},
        "gatekeeper": {"notarized": False, "first_download_verified": False,
                       "quarantine_policy_verified": False,
                       "simulation": "explicit-quarantine-on-isolated-copies"},
    }


def main(argv=None):
    args = parse_args(argv)
    report = initial_report(args)
    try:
        for name in ("archive", "dmg", "packaging_evidence"):
            path = getattr(args, name)
            require(path.is_file() and not path.is_symlink(), "Input must be a regular file")
            setattr(args, name, path.resolve())
        report["artifacts"] = {
            kind: {"name": path.name, "sha256": file_digest(path), "size": path.stat().st_size}
            for kind, path in (("zip", args.archive), ("dmg", args.dmg))
        }
        report["sha256"] = report["artifacts"]["zip"]["sha256"]
        report["packaging"] = {"name": args.packaging_evidence.name,
                               "sha256": file_digest(args.packaging_evidence)}
        verify(args, report)
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = safe_summary(error)
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["success"] else 1


if __name__ == "__main__":
    sys.exit(main())
