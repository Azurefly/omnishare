#!/usr/bin/env python3
"""Verify the actual macOS release ZIP without changing Gatekeeper policy."""

import argparse
import ctypes
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
import subprocess
import sys
import tempfile
import time
import unicodedata
import zipfile
from pathlib import Path


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


def check_archive(path):
    """Reject unsafe paths/types before invoking the macOS ZIP extractor."""
    required = {
        "OmniShare.app/Contents/Info.plist",
        "OmniShare.app/Contents/MacOS/OmniShare",
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


def verify_bundle(app, arch, version):
    binary = app / "Contents/MacOS/OmniShare"
    for item in [app, *app.rglob("*")]:
        require(not item.is_symlink() and (item.is_dir() or item.is_file()),
                "Extracted bundle has a link or special file")
        require(not item.stat().st_mode & 0o022, "Extracted bundle is group/world writable")
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
    signature = tool(["/usr/bin/codesign", "-d", "--verbose=4", app])
    require("Signature=adhoc" in signature.stderr.splitlines(),
            "Expected the explicitly documented ad-hoc signing mode")
    tool(["/usr/bin/codesign", "--verify", "--deep", "--strict", "--verbose=2", app])
    return binary


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


class ScopedLaunch:
    """Identify the LaunchServices child by its private lock, path and arguments."""

    def __init__(self, app, data, port):
        self.app = app.resolve()
        self.binary = self.app / "Contents/MacOS/OmniShare"
        self.data = data.resolve()
        self.port = port
        self.args = ["--data-dir", str(self.data), "--listen", "127.0.0.1",
                     "--port", str(port), "--no-browser"]
        self.pid = None
        self.started = None
        self.launcher = None
        self.stopped = False
        self.libproc = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        self.libproc.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
        self.libproc.proc_pidpath.restype = ctypes.c_int

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

    def identity(self, pid):
        buffer = ctypes.create_string_buffer(4096)
        if self.libproc.proc_pidpath(pid, buffer, len(buffer)) <= 0:
            return None
        require(Path(os.fsdecode(buffer.value)).resolve() == self.binary,
                "Refusing to operate on a process outside this extracted app")
        result = tool(
            ["/bin/ps", "-ww", "-p", str(pid), "-o", "uid=", "-o", "lstart=",
             "-o", "command="], allow_failure=True,
        )
        if result.returncode:
            return None
        values = result.stdout.strip().split(maxsplit=6)
        require(len(values) == 7 and int(values[0]) == os.getuid() and
                values[6].endswith(" " + " ".join(self.args)),
                "Refusing a process with different ownership or startup arguments")
        started = " ".join(values[1:6])
        require(self.started is None or self.started == started,
                "Isolated instance PID was reused")
        return started

    def start(self):
        self.launcher = subprocess.Popen(
            ["/usr/bin/open", "-n", "-W", str(self.app), "--args", *self.args],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            pid = self.lock_pid()
            if pid is not None:
                identity = self.identity(pid)
                if identity is not None:
                    self.pid, self.started = pid, identity
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

    def stop(self):
        graceful = False
        try:
            pid = self.pid or self.lock_pid()
            if pid is not None:
                identity = self.identity(pid)
                if identity is not None:
                    if self.started is None:
                        self.started = identity
                    os.kill(pid, signal.SIGTERM)
                    deadline = time.monotonic() + 15
                    while time.monotonic() < deadline and self.identity(pid) is not None:
                        time.sleep(0.1)
                    if self.identity(pid) is not None:
                        # Revalidate before fallback cleanup; this can never
                        # pass the clean_shutdown check.
                        os.kill(pid, signal.SIGKILL)
                        deadline = time.monotonic() + 5
                        while time.monotonic() < deadline and self.identity(pid) is not None:
                            time.sleep(0.1)
                        require(self.identity(pid) is None, "Isolated test process did not exit")
                    else:
                        graceful = True
        finally:
            # Even if a PID identity check fails, close only our own open
            # subprocess. Never fall back to killing an app by name or port.
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


def verify(args, report):
    require(platform.system() == "Darwin", "Native macOS is required")
    require(re.fullmatch(r"\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.?\d+)?", args.version),
            "Unsupported release version")
    require(args.archive.name == f"OmniShare-v{args.version}-macos-{args.arch}.zip",
            "Archive filename does not match version and architecture")
    expected_machine = {"amd64": "x86_64", "arm64": "arm64"}[args.arch]
    require(platform.machine() == expected_machine, "Use a native runner matching the archive")
    check_archive(args.archive)
    with tempfile.TemporaryDirectory(prefix="OmniShare macOS verification ") as temporary:
        root = Path(temporary).resolve()
        extracted = root / "Application With Spaces"
        extracted.mkdir(mode=0o700)
        tool(["/usr/bin/ditto", "-x", "-k", args.archive, extracted])
        app = extracted / "OmniShare.app"
        verify_bundle(app, args.arch, args.version)
        report["checks"].update(archive_layout=True, signature_valid=True,
                                signature_survives_archive=True)
        check_tamper(app, root / "Tamper Check")
        report["checks"]["resource_tamper_rejected"] = True
        assessment = tool(
            ["/usr/sbin/spctl", "--assess", "--type", "execute", "--verbose=4", app],
            allow_failure=True, timeout=45,
        )
        report["gatekeeper"].update(
            assessment_exit_code=assessment.returncode,
            assessment_result="accepted" if assessment.returncode == 0 else "rejected",
            assessment_output=safe_summary(assessment.stderr or assessment.stdout),
        )
        data = root / "Isolated Test Data"
        data.mkdir(mode=0o700)
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            port = reservation.getsockname()[1]
        launch = ScopedLaunch(app, data, port)
        try:
            launch.start()
            report["checks"]["launch_services"] = True
            launch.check_http(args.version)
            report["checks"].update(health_version=True, http_ui=True)
            report["runtime"] = {"launch_method": "open -n -W", "pid": launch.pid,
                                 "loopback_only": True, "browser_suppressed": True,
                                 "path_contains_spaces": " " in str(app)}
            require(launch.stop(), "The test instance did not shut down cleanly")
            report["checks"]["clean_shutdown"] = True
        finally:
            if not launch.stopped:
                launch.stop()
        # Running the app must not modify its signed bundle.
        tool(["/usr/bin/codesign", "--verify", "--deep", "--strict", app])
    require(file_digest(args.archive) == report["sha256"], "Archive changed during verification")
    report["success"] = True


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path)
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--version", required=True)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    args.archive = args.archive.resolve()
    report = {
        "schema_version": 1, "success": False, "version": args.version,
        "arch": args.arch, "asset": args.archive.name, "sha256": "",
        "signing_mode": "ad-hoc", "minimum_macos": "12.0",
        "checks": {name: False for name in (
            "archive_layout", "signature_valid", "signature_survives_archive",
            "resource_tamper_rejected", "launch_services", "health_version",
            "http_ui", "clean_shutdown")},
        "gatekeeper": {"notarized": False, "first_download_verified": False},
    }
    try:
        report["sha256"] = file_digest(args.archive)
        verify(args, report)
    except (Exception, KeyboardInterrupt) as error:
        report["error"] = safe_summary(error)
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["success"] else 1


if __name__ == "__main__":
    sys.exit(main())
