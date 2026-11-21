#!/usr/bin/env python3
"""Native macOS packaging; release is fail-closed, rehearsal is explicitly ad-hoc.

No third-party Python dependencies. Credential values, native tool output and
notarization logs must never be printed or written to public evidence files.
"""

import argparse
import base64
import binascii
from contextlib import contextmanager
from dataclasses import dataclass, field
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parent.parent
SECRET_NAMES = (
    "APPLE_CERTIFICATE", "APPLE_CERTIFICATE_PASSWORD", "APPLE_TEAM_ID",
    "APPLE_ID", "APPLE_APP_SPECIFIC_PASSWORD",
)
VERSION_PATTERN = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-(?:alpha|beta|rc)\.?[0-9]+)?")
TEAM_PATTERN = re.compile(r"[A-Z0-9]{10}")
SECURITY = "/usr/bin/security"
CODESIGN = "/usr/bin/codesign"
DITTO = "/usr/bin/ditto"
HDIUTIL = "/usr/bin/hdiutil"
XCRUN = "/usr/bin/xcrun"


class PackagingError(Exception):
    """Only fixed, non-secret messages may be used for this exception."""


@dataclass(repr=False)
class Credentials:
    certificate: bytes = field(repr=False)
    certificate_password: str = field(repr=False)
    team_id: str = field(repr=False)
    apple_id: str = field(repr=False)
    app_password: str = field(repr=False)


def credential_issues(env):
    """A read-only format check, not proof of a valid private key or Apple account."""
    missing = [name for name in SECRET_NAMES if not env.get(name)]
    invalid = []
    for name in SECRET_NAMES:
        value = env.get(name, "")
        if not value:
            continue
        valid = "\0" not in value
        if name == "APPLE_CERTIFICATE":
            compact = "".join(value.split())
            try:
                decoded = base64.b64decode(compact, validate=True) if len(compact) <= 2_000_000 else b""
                valid = valid and len(decoded) >= 16 and decoded[:1] == b"\x30"
            except (binascii.Error, ValueError):
                valid = False
        elif name == "APPLE_TEAM_ID":
            valid = valid and TEAM_PATTERN.fullmatch(value) is not None
        elif name == "APPLE_ID":
            valid = valid and re.fullmatch(r"[^\s@\x00-\x1f\x7f]+@[^\s@\x00-\x1f\x7f]+\.[^\s@\x00-\x1f\x7f]+", value) is not None
        elif name == "APPLE_APP_SPECIFIC_PASSWORD":
            valid = valid and re.fullmatch(r"(?:[A-Za-z0-9]{4}-){3}[A-Za-z0-9]{4}|[A-Za-z0-9]{16}", value) is not None
        if not valid:
            invalid.append(name)
    return missing, invalid


def load_credentials(env):
    missing, invalid = credential_issues(env)
    if missing or invalid:
        details = []
        if missing:
            details.append("missing: " + ", ".join(missing))
        if invalid:
            details.append("invalid format: " + ", ".join(invalid))
        raise PackagingError("Apple signing credentials rejected (" + "; ".join(details) + ").")
    return Credentials(
        base64.b64decode("".join(env["APPLE_CERTIFICATE"].split()), validate=True),
        env["APPLE_CERTIFICATE_PASSWORD"], env["APPLE_TEAM_ID"], env["APPLE_ID"],
        env["APPLE_APP_SPECIFIC_PASSWORD"],
    )


class NativeTools:
    """Never let subprocess exceptions or raw output expose command credentials."""

    def __init__(self, env=None):
        self.env = dict(os.environ if env is None else env)
        for name in SECRET_NAMES:
            self.env.pop(name, None)
        self.env["LC_ALL"] = "C"

    def run(self, argv, stage, timeout=180):
        try:
            result = subprocess.run(
                [str(arg) for arg in argv], stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=self.env,
                text=True, errors="replace", timeout=timeout, check=False,
            )
        except subprocess.TimeoutExpired:
            raise PackagingError(stage + " timed out.") from None
        except OSError:
            raise PackagingError(stage + " could not start.") from None
        if result.returncode:
            raise PackagingError(stage + " failed (exit " + str(result.returncode) + ").")
        # notarytool's JSON lives on stdout; unrelated diagnostics on stderr
        # must not corrupt it. codesign intentionally displays details on stderr.
        return result.stdout + result.stderr if str(argv[0]) == CODESIGN and "--display" in argv else result.stdout


def developer_identity(output, team_id):
    identities = re.findall(r'^\s*\d+\)\s+([0-9A-Fa-f]{40})\s+"([^"\r\n]+)"\s*$', output, re.MULTILINE)
    matches = [fingerprint.upper() for fingerprint, name in identities
               if name.startswith("Developer ID Application: ") and name.endswith(" (" + team_id + ")")]
    if len(matches) != 1:
        raise PackagingError("Expected exactly one valid Developer ID Application identity for APPLE_TEAM_ID.")
    return matches[0]


@contextmanager
def signing_keychain(tools, work, credentials):
    keychain = work / "signing.keychain-db"
    p12 = work / "certificate.p12"
    password = secrets.token_urlsafe(36)
    profile = "omnishare-notary"
    with p12.open("xb") as handle:
        os.chmod(p12, 0o600)
        handle.write(credentials.certificate)
    try:
        tools.run([SECURITY, "create-keychain", "-p", password, keychain], "Create temporary keychain")
        tools.run([SECURITY, "set-keychain-settings", "-lut", "21600", keychain], "Configure temporary keychain")
        tools.run([SECURITY, "unlock-keychain", "-p", password, keychain], "Unlock temporary keychain")
        tools.run([SECURITY, "import", p12, "-k", keychain, "-P", credentials.certificate_password,
                   "-f", "pkcs12", "-T", CODESIGN], "Import signing identity")
        p12.unlink()
        tools.run([SECURITY, "set-key-partition-list", "-S", "apple-tool:,apple:", "-s", "-k", password, keychain],
                  "Authorize codesign for temporary keychain")
        identity = developer_identity(
            tools.run([SECURITY, "find-identity", "-v", "-p", "codesigning", keychain], "Validate signing identity"),
            credentials.team_id,
        )
        tools.run([XCRUN, "notarytool", "store-credentials", profile, "--keychain", keychain,
                   "--apple-id", credentials.apple_id, "--team-id", credentials.team_id,
                   "--password", credentials.app_password], "Validate notarization credentials")
        yield keychain, identity, profile
    finally:
        p12.unlink(missing_ok=True)
        # Remove only our keychain and its entry, without replacing the user's
        # search list, changing the default keychain or altering any trust policy.
        tools.run([SECURITY, "delete-keychain", keychain], "Delete temporary signing keychain")


def parse_notary_response(output):
    try:
        data = json.loads(output)
        submission_id = data["id"]
        if not isinstance(submission_id, str) or str(uuid.UUID(submission_id)) != submission_id.lower():
            raise ValueError()
        return submission_id.lower(), data.get("status")
    except (ValueError, TypeError, KeyError):
        raise PackagingError("Notarization returned an invalid response.") from None


def notarize(tools, target, keychain, profile, sleep=time.sleep):
    auth = ["--keychain-profile", profile, "--keychain", keychain]
    expected_sha = sha256(target)
    # Submit exactly once. Retry only waiting on that ID, never a duplicate upload.
    submission_id, status = parse_notary_response(tools.run(
        [XCRUN, "notarytool", "submit", target, *auth, "--output-format", "json"], "Submit for notarization", timeout=600,
    ))
    for attempt in range(3):
        if status == "Accepted":
            break
        if status not in (None, "In Progress", "Uploaded"):
            raise PackagingError("Apple did not accept notarization submission " + submission_id + ".")
        try:
            received_id, status = parse_notary_response(tools.run(
                [XCRUN, "notarytool", "wait", submission_id, *auth, "--output-format", "json", "--timeout", "10m"],
                "Wait for notarization", timeout=630,
            ))
            if received_id != submission_id:
                raise PackagingError("Notarization response does not match the submitted artifact.")
        except PackagingError as error:
            if not str(error).startswith("Wait for notarization") or attempt == 2:
                raise
            sleep(10)
    if status != "Accepted":
        raise PackagingError("Apple did not accept notarization submission " + submission_id + ".")
    # Apple recommends inspecting the log even for Accepted submissions. Keep
    # it in memory, bind it to the uploaded bytes, and reject unresolved issues.
    log_output = tools.run([XCRUN, "notarytool", "log", submission_id, *auth], "Inspect accepted notarization log")
    try:
        log = json.loads(log_output)
    except (ValueError, TypeError):
        raise PackagingError("Notarization log is not valid JSON.") from None
    try:
        log_job_id = str(uuid.UUID(log["jobId"])) if isinstance(log, dict) and isinstance(log.get("jobId"), str) else None
    except ValueError:
        log_job_id = None
    if (not isinstance(log, dict) or log_job_id != submission_id or log.get("status") != "Accepted"
            or log.get("sha256") != expected_sha or sha256(target) != expected_sha):
        raise PackagingError("Notarization log does not match the accepted submission and artifact.")
    if log.get("issues") not in (None, []):
        raise PackagingError("Notarization log contains unresolved issues for submission " + submission_id + ".")
    return {"submission_id": submission_id, "status": "Accepted", "stapled": False}


def staple(tools, target, record, sleep=time.sleep):
    for attempt in range(3):
        try:
            tools.run([XCRUN, "stapler", "staple", target], "Staple notarization ticket", timeout=180)
            break
        except PackagingError:
            if attempt == 2:
                raise
            sleep(10)
    tools.run([XCRUN, "stapler", "validate", target], "Validate notarization ticket", timeout=180)
    record["stapled"] = True


def verify_signature(tools, target, mode, team_id, app=False):
    tools.run([CODESIGN, "--verify", "--deep", "--strict", "--verbose=2", target], "Verify code signature")
    details = tools.run([CODESIGN, "--display", "--verbose=4", target], "Inspect code signature")
    if mode == "rehearsal":
        if "Signature=adhoc" not in details:
            raise PackagingError("Rehearsal artifact does not have an ad-hoc signature.")
        return
    if (not re.search(r"^Authority=Developer ID Application: .+ \(" + re.escape(team_id) + r"\)$", details, re.MULTILINE)
            or not re.search(r"^TeamIdentifier=" + re.escape(team_id) + r"$", details, re.MULTILINE)
            or not re.search(r"^Timestamp=.+$", details, re.MULTILINE)
            or "Signature=adhoc" in details):
        raise PackagingError("Developer ID, Team ID or secure timestamp verification failed.")
    if app and not re.search(r"^.*flags=.*\bruntime\b.*$", details, re.MULTILINE):
        raise PackagingError("The application does not enable hardened runtime.")


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def assemble_app(tools, work, binary, version, mode, root, arch):
    app = work / "OmniShare.app"
    executable = app / "Contents/MacOS/OmniShare"
    resources = app / "Contents/Resources"
    executable.parent.mkdir(parents=True)
    resources.mkdir()
    backend = executable.parent / "omnishare-server"
    shutil.copyfile(binary, backend)
    backend.chmod(0o755)
    machine = "x86_64" if arch == "amd64" else "arm64"
    tools.run([XCRUN, "swiftc", "-O", "-target", machine + "-apple-macosx12.0",
               "-framework", "AppKit", root / "packaging/macos/Launcher.swift", "-o", executable],
              "Compile native application launcher", timeout=300)
    executable.chmod(0o755)
    if tools.run(["/usr/bin/lipo", "-archs", executable], "Inspect launcher architecture").strip() != machine:
        raise PackagingError("The native launcher architecture does not match the requested architecture.")
    short_version = version.split("-", 1)[0]
    info = (root / "packaging/macos/Info.plist").read_text(encoding="utf-8")
    info = info.replace("__SHORT_VERSION__", short_version).replace("__BUNDLE_VERSION__", short_version)
    notice = (
        "正式发布包：Developer ID 签名、hardened runtime 和安全时间戳；应用与 DMG 均经 Apple 公证并附加票据。"
        if mode == "release" else
        "CI 演练包：仅 ad-hoc 完整性签名，未经 Developer ID 签名或 Apple 公证；不能作为正式安装包发布。"
    )
    readme = (root / "packaging/macos/README.txt").read_text(encoding="utf-8")
    readme = readme.replace("__VERSION__", version).replace("__SIGNING_NOTICE__", notice)
    if re.search(r"__[A-Z_]+__", info + readme):
        raise PackagingError("A macOS bundle template contains an unresolved placeholder.")
    for path, content in ((app / "Contents/Info.plist", info), (resources / "README.txt", readme)):
        path.write_text(content, encoding="utf-8")
        path.chmod(0o644)
    for directory in (app, app / "Contents", executable.parent, resources):
        directory.chmod(0o755)
    tools.run(["/usr/bin/plutil", "-lint", app / "Contents/Info.plist"], "Validate application metadata")
    return app


def create_zip(tools, app, destination):
    tools.run([DITTO, "-c", "-k", "--sequesterRsrc", "--keepParent", app, destination], "Archive macOS application")


def create_dmg(tools, app, destination, work):
    image_root = work / "dmg-root"
    image_root.mkdir(mode=0o755)
    tools.run([DITTO, app, image_root / "OmniShare.app"], "Stage disk image application")
    (image_root / "Applications").symlink_to("/Applications")
    tools.run([HDIUTIL, "create", "-volname", "OmniShare", "-srcfolder", image_root,
               "-format", "UDZO", "-fs", "HFS+", destination], "Create installation disk image", timeout=600)


def package(binary, output, arch, mode="release", env=None, tools=None, root=ROOT):
    env = os.environ if env is None else env
    credentials = load_credentials(env) if mode == "release" else None
    if mode not in ("release", "rehearsal"):
        raise PackagingError("Unsupported packaging mode.")
    if platform.system() != "Darwin":
        raise PackagingError("macOS artifacts must be finalized and signed on macOS.")
    version = (root / "VERSION").read_text(encoding="utf-8").strip()
    if not VERSION_PATTERN.fullmatch(version):
        raise PackagingError("Unsupported release version.")
    if arch not in ("amd64", "arm64"):
        raise PackagingError("Unsupported macOS architecture.")
    binary = Path(binary)
    if binary.is_symlink() or not binary.is_file() or binary.stat().st_size == 0:
        raise PackagingError("Expected a nonempty regular Mach-O binary.")
    binary = binary.resolve()
    tools = tools or NativeTools(env)
    needed = ["/usr/bin/lipo", "/usr/bin/plutil", CODESIGN, DITTO, HDIUTIL, XCRUN]
    if mode == "release":
        needed += [SECURITY, XCRUN]
    if any(not os.access(path, os.X_OK) for path in needed):
        raise PackagingError("A required native macOS packaging tool is unavailable.")
    expected_arch = "x86_64" if arch == "amd64" else "arm64"
    if tools.run(["/usr/bin/lipo", "-archs", binary], "Inspect Mach-O architecture").strip() != expected_arch:
        raise PackagingError("The Mach-O architecture does not match the requested architecture.")
    names = {
        "zip": f"OmniShare-v{version}-macos-{arch}.zip",
        "dmg": f"OmniShare-v{version}-macos-{arch}.dmg",
        "report": f"omnishare-macos-{arch}-packaging.json",
    }
    output = Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any((output / name).exists() or (output / name).is_symlink() for name in names.values()):
        raise PackagingError("Refusing to overwrite existing macOS release artifacts.")
    with tempfile.TemporaryDirectory(prefix=".omnishare-macos-", dir=output) as directory:
        work = Path(directory)
        work.chmod(0o700)
        app = assemble_app(tools, work, binary, version, mode, root, arch)
        backend = app / "Contents/MacOS/omnishare-server"
        zip_path, dmg_path = work / names["zip"], work / names["dmg"]
        notary = {kind: {"submission_id": None, "status": "NotSubmitted", "stapled": False} for kind in ("app", "dmg")}
        if mode == "release":
            with signing_keychain(tools, work, credentials) as (keychain, identity, profile):
                tools.run([CODESIGN, "--force", "--sign", identity, "--keychain", keychain,
                           "--options", "runtime", "--timestamp", backend], "Sign embedded macOS backend")
                verify_signature(tools, backend, mode, credentials.team_id, app=True)
                tools.run([CODESIGN, "--force", "--sign", identity, "--keychain", keychain,
                           "--options", "runtime", "--timestamp", app], "Sign macOS application")
                verify_signature(tools, app, mode, credentials.team_id, app=True)
                submission_zip = work / "notarization-submission.zip"
                create_zip(tools, app, submission_zip)
                notary["app"] = notarize(tools, submission_zip, keychain, profile)
                staple(tools, app, notary["app"])
                verify_signature(tools, app, mode, credentials.team_id, app=True)
                create_zip(tools, app, zip_path)
                create_dmg(tools, app, dmg_path, work)
                tools.run([CODESIGN, "--force", "--sign", identity, "--keychain", keychain,
                           "--timestamp", dmg_path], "Sign installation disk image")
                verify_signature(tools, dmg_path, mode, credentials.team_id)
                notary["dmg"] = notarize(tools, dmg_path, keychain, profile)
                if notary["app"]["submission_id"] == notary["dmg"]["submission_id"]:
                    raise PackagingError("Application and disk image notarization submissions must be distinct.")
                staple(tools, dmg_path, notary["dmg"])
                verify_signature(tools, dmg_path, mode, credentials.team_id)
        else:
            tools.run([CODESIGN, "--force", "--sign", "-", "--timestamp=none", backend], "Ad-hoc sign rehearsal backend")
            verify_signature(tools, backend, mode, None, app=True)
            tools.run([CODESIGN, "--force", "--sign", "-", "--timestamp=none", app], "Ad-hoc sign rehearsal application")
            verify_signature(tools, app, mode, None, app=True)
            create_zip(tools, app, zip_path)
            create_dmg(tools, app, dmg_path, work)
            tools.run([CODESIGN, "--force", "--sign", "-", "--timestamp=none", dmg_path], "Ad-hoc sign rehearsal disk image")
            verify_signature(tools, dmg_path, mode, None)
        report = {
            "schema_version": 1, "version": version, "arch": arch, "mode": mode,
            "signing_mode": "developer-id" if mode == "release" else "ad-hoc",
            "team_id": credentials.team_id if credentials else None,
            "artifacts": {kind: {"name": names[kind], "sha256": sha256(work / names[kind])} for kind in ("zip", "dmg")},
            "notarization": notary,
        }
        report_path = work / names["report"]
        report_path.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
        published = []
        try:
            for name in (names["zip"], names["dmg"], names["report"]):
                source, target = work / name, output / name
                source.chmod(0o644)
                # Exclusive hard links on this filesystem prevent accidental
                # overwrite; evidence is last and failures remove our outputs.
                os.link(source, target)
                published.append(target)
                source.unlink()
        except BaseException:
            for path in published:
                path.unlink(missing_ok=True)
            raise
    return report


def argument_parser():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", nargs="?")
    parser.add_argument("output_directory", nargs="?")
    parser.add_argument("arch", nargs="?", choices=("amd64", "arm64"))
    parser.add_argument("--mode", choices=("release", "rehearsal"), default="release")
    parser.add_argument("--check-credentials", action="store_true", help="Read-only presence/format check; works on Linux.")
    return parser


def main(argv=None):
    parser = argument_parser()
    args = parser.parse_args(argv)
    if args.check_credentials:
        if args.binary or args.output_directory or args.arch:
            parser.error("--check-credentials does not accept packaging paths")
        try:
            load_credentials(os.environ)
        except PackagingError as error:
            print(str(error), file=sys.stderr)
            return 1
        print("Apple signing credential presence/format check passed. Certificate validity and Apple authorization require macOS release packaging.")
        return 0
    if not args.binary or not args.output_directory or not args.arch:
        parser.error("expected <binary> <output-directory> <amd64|arm64>")
    previous_handler = signal.getsignal(signal.SIGTERM)
    def terminate(_signum, _frame):
        raise PackagingError("macOS packaging was cancelled.")
    signal.signal(signal.SIGTERM, terminate)
    try:
        os.umask(0o022)
        report = package(args.binary, args.output_directory, args.arch, args.mode)
        print("Created macOS " + report["mode"] + " ZIP, DMG and packaging evidence.")
        return 0
    except PackagingError as error:
        print(str(error), file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("macOS packaging was cancelled.", file=sys.stderr)
        return 130
    except Exception:
        print("macOS packaging failed; no successful packaging evidence was produced.", file=sys.stderr)
        return 1
    finally:
        signal.signal(signal.SIGTERM, previous_handler)


if __name__ == "__main__":
    sys.exit(main())
