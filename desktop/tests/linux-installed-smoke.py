#!/usr/bin/env python3
"""Install the actual DEB and exercise its GUI, then the FUSE-free AppImage.

Run as a normal user inside a fresh Ubuntu runner, under dbus-run-session and
xvfb-run. Only APT uses sudo. Accessibility and real window checks are mandatory;
an HTTP response alone cannot make this test pass. Private credentials, backend
logs, and feature-test state stay in a temporary directory outside the evidence.
"""

import argparse
from contextlib import ExitStack
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
FEATURE_TEST = ROOT / "scripts/test-installed-features.py"
MARKER = "OMNISHARE_INSTALL_TEST_ID"
PACKAGE = "omni-share"
INSTALLED_BINARY = Path("/usr/bin/omnishare-desktop")
INSTALLED_BACKEND = Path("/usr/lib/OmniShare/resources/omnishare")
INSTALLED_ENTRY = Path("/usr/share/applications/OmniShare.desktop")


class SmokeFailure(Exception):
    pass


def require(condition, message):
    if not condition:
        raise SmokeFailure(message)


def sha256(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(arguments, *, env=None, timeout=30, input_text=None, check=True):
    result = subprocess.run(arguments, env=env, input=input_text, capture_output=True,
                            text=True, timeout=timeout)
    if check and result.returncode:
        # The full command and stderr can contain a private path or credential.
        raise SmokeFailure(Path(str(arguments[0])).name + " exited with code " + str(result.returncode))
    return result


def wait_for(predicate, description, timeout=60):
    print("CHECK " + description, flush=True)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.2)
    raise SmokeFailure("Timed out waiting for " + description)


def os_release():
    values = {}
    for line in Path("/etc/os-release").read_text().splitlines():
        if "=" in line:
            name, value = line.split("=", 1)
            values[name] = value.strip('"')
    return {"id": values.get("ID"), "version": values.get("VERSION_ID")}


def process_info(pid):
    try:
        directory = Path("/proc") / str(pid)
        if directory.stat().st_uid != os.getuid():
            return None
        environ = dict(item.split(b"=", 1) for item in (directory / "environ").read_bytes().split(b"\0") if b"=" in item)
        return {"pid": int(pid), "executable": (directory / "exe").resolve(strict=True),
                "environment": environ,
                "arguments": [item.decode("utf-8", "surrogateescape") for item in (directory / "cmdline").read_bytes().split(b"\0") if item]}
    except (FileNotFoundError, PermissionError, ProcessLookupError, OSError, ValueError):
        return None


def owned_processes(marker):
    owned = []
    for directory in Path("/proc").iterdir():
        if directory.name.isdigit():
            info = process_info(directory.name)
            if info and info["environment"].get(MARKER.encode()) == marker.encode():
                owned.append(info)
    return owned


def maximum_glibc(binary):
    output = run(["readelf", "--version-info", str(binary)]).stdout
    versions = {tuple(map(int, value.split("."))) for value in re.findall(r"\bGLIBC_([0-9.]+)", output)}
    return max(versions) if versions else (0,)


def data_fingerprint(directory):
    files = {}
    for path in sorted(directory.rglob("*")):
        require(not path.is_symlink(), "Test data unexpectedly contains a symbolic link")
        if path.is_file():
            files[str(path.relative_to(directory))] = sha256(path)
    require(bool(files), "Application did not retain any data files")
    return files


class AccessibleGUI:
    def __init__(self):
        try:
            import gi
            gi.require_version("Atspi", "2.0")
            from gi.repository import Atspi, GLib
            import pyatspi
            Atspi.set_timeout(1000, 3000)
            self.atspi = pyatspi
            self.context = GLib.MainContext.default()
            self.cache_none = Atspi.Cache.NONE
        except (ImportError, ValueError, AttributeError) as error:
            raise SmokeFailure("System python3-pyatspi and Atspi 2.0 are required for real WebKit verification") from error

    def nodes(self, pid):
        # Dispatch accessibility state changes without a second event-loop
        # thread. Otherwise cached visibility can survive navigation/restart.
        dispatched = 0
        while self.context.pending() and dispatched < 1000:
            self.context.iteration(False)
            dispatched += 1
        desktop = self.atspi.Registry.getDesktop(0)
        applications = []
        # WebKit may register its web-process accessibility root separately
        # from GTK. Accept only this test instance's marker-owned processes.
        allowed_pids = {pid}
        owner = process_info(pid)
        if owner:
            marker = owner["environment"].get(MARKER.encode())
            if marker:
                allowed_pids.update(item["pid"] for item in owned_processes(os.fsdecode(marker)))
        deadline = time.monotonic() + 3
        for index in range(desktop.childCount):
            try:
                app = desktop.getChildAtIndex(index)
                if app.get_process_id() in allowed_pids:
                    app.setCacheMask(self.cache_none)
                    applications.append(app)
            except Exception:
                continue
        pending = [(app, 0) for app in applications]
        found = []
        while pending and len(found) < 1000 and time.monotonic() < deadline:
            node, depth = pending.pop()
            if depth > 40:
                continue
            try:
                found.append(node)
                for index in reversed(range(min(node.childCount, 5000))):
                    child = node.getChildAtIndex(index)
                    if child is not None:
                        pending.append((child, depth + 1))
            except Exception:
                continue
        return found

    def showing(self, node):
        try:
            return node.getState().contains(self.atspi.STATE_SHOWING)
        except Exception:
            return False

    def text(self, pid):
        documents = []
        fragments = []
        for node in self.nodes(pid):
            try:
                if not self.showing(node):
                    continue
                role = node.getRole()
                if role in (self.atspi.ROLE_DOCUMENT_WEB, self.atspi.ROLE_DOCUMENT_FRAME):
                    rect = node.queryComponent().getExtents(self.atspi.DESKTOP_COORDS)
                    if rect.width > 50 and rect.height > 50:
                        documents.append(node)
                if role == self.atspi.ROLE_PASSWORD_TEXT:
                    continue
                if node.name:
                    fragments.append(node.name)
                try:
                    fragments.append(node.queryText().getText(0, -1))
                except Exception:
                    pass
            except Exception:
                continue
        return bool(documents), " ".join(" ".join(fragments).split())

    def rendered(self, pid, *texts):
        document, text = self.text(pid)
        return document and all(" ".join(value.split()) in text for value in texts)

    def auth_prompt(self, pid):
        # showModal() makes the background inert and removes its navigation
        # from the accessibility tree. Require the actual visible dialog and
        # its controls instead of waiting for inaccessible background text.
        return (self.rendered(pid, "需要访问密钥")
                and self.find(pid, "需要访问密钥", {self.atspi.ROLE_DIALOG}) is not None
                and self.find(pid, "访问密钥", {self.atspi.ROLE_PASSWORD_TEXT}) is not None
                and self.find(pid, "验证并进入", {self.atspi.ROLE_PUSH_BUTTON}) is not None)

    def find(self, pid, label, roles=None):
        for node in self.nodes(pid):
            try:
                if self.showing(node) and label in node.name and (roles is None or node.getRole() in roles):
                    return node
            except Exception:
                continue
        return None

    def click(self, node):
        require(node is not None, "Required visible WebKit control was not found")
        try:
            rect = node.queryComponent().getExtents(self.atspi.DESKTOP_COORDS)
        except Exception as error:
            raise SmokeFailure("WebKit control has no screen coordinates") from error
        require(rect.width > 0 and rect.height > 0, "WebKit control is not visibly rendered")
        run(["xdotool", "mousemove", "--sync", str(rect.x + rect.width // 2), str(rect.y + rect.height // 2), "click", "1"])

    def enter(self, node, text):
        self.click(node)
        run(["xdotool", "key", "--clearmodifiers", "ctrl+a"])
        # Keep even these disposable credentials out of process command lines.
        run(["xdotool", "type", "--clearmodifiers", "--delay", "1", "--file", "-"], input_text=text)


class ApplicationSession:
    def __init__(self, flavor, private, evidence, version, gui, appimage=None):
        self.flavor = flavor
        self.private = private / flavor
        self.private.mkdir()
        self.evidence = evidence
        self.version = version
        self.gui = gui
        self.appimage = appimage
        self.marker = secrets.token_hex(24)
        self.data = self.private / "用户 数据 # %"
        self.state = self.private / "private-feature-state.json"
        self.data.mkdir()
        self.home = self.private / "home with spaces"
        self.home.mkdir()
        self.env = dict(os.environ)
        for name in ("OMNISHARE_BACKEND_BIN", "OMNISHARE_PORT", "APPDIR", "APPIMAGE", "ARGV0", "APPIMAGE_EXTRACT_AND_RUN"):
            self.env.pop(name, None)
        for name, directory in (("HOME", self.home), ("XDG_CONFIG_HOME", self.private / "config"),
                                ("XDG_DATA_HOME", self.private / "share"), ("XDG_CACHE_HOME", self.private / "cache"),
                                ("XDG_STATE_HOME", self.private / "state"), ("XDG_RUNTIME_DIR", self.private / "runtime")):
            directory.mkdir(exist_ok=True, mode=0o700)
            self.env[name] = str(directory)
        self.env.update({MARKER: self.marker, "OMNISHARE_DATA_DIR": str(self.data),
                         "OMNISHARE_DESKTOP_LOG_DIR": str(self.private / "logs"),
                         "GDK_BACKEND": "x11", "NO_AT_BRIDGE": "0", "GTK_MODULES": "atk-bridge",
                         "ACCESSIBILITY_ENABLED": "1"})
        self.log = (self.private / "private-process.log").open("wb")
        self.launcher = None
        self.pid = None
        self.backend_pid = None
        self.window = None
        self.cleaned = False
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.result = {"checks": {}, "core_reports": [], "features": {}}

    def app_process(self):
        for info in owned_processes(self.marker):
            if info["executable"].name == "omnishare-desktop":
                if self.flavor == "deb":
                    require(info["executable"] == INSTALLED_BINARY, "The launched application is not the installed DEB executable")
                return info
        return None

    def focus(self):
        run(["xdotool", "windowfocus", "--sync", self.window])

    def start(self, first):
        command = ["gio", "launch", str(INSTALLED_ENTRY)] if self.flavor == "deb" else [str(self.appimage), "--appimage-extract-and-run"]
        self.launcher = subprocess.Popen(command, cwd=self.private, env=self.env,
                                         stdin=subprocess.DEVNULL, stdout=self.log, stderr=self.log,
                                         start_new_session=True)
        info = wait_for(self.app_process, "the packaged native desktop process", 90)
        self.pid = info["pid"]
        def find_window():
            result = run(["xdotool", "search", "--onlyvisible", "--pid", str(self.pid)], check=False)
            return result.stdout.splitlines()[0] if result.returncode == 0 and result.stdout.splitlines() else None
        self.window = wait_for(find_window, "a visible native application window", 30)
        self.focus()
        if first:
            # This is a fresh isolated profile before any access key is entered.
            run(["scrot", str(self.evidence / (self.flavor + "-first-window.png"))])
            wait_for(lambda: self.gui.rendered(self.pid, "首次启动设置"), "the first-run page in WebKit", 90)
            field = wait_for(lambda: self.gui.find(self.pid, "本地服务端口"), "the first-run port control")
            self.gui.enter(field, str(self.port))
            button = self.gui.find(self.pid, "保存并启动", {self.gui.atspi.ROLE_PUSH_BUTTON})
            self.gui.click(button)
            self.result["checks"]["first_run_webkit"] = True
        wait_for(lambda: self.gui.rendered(self.pid, "随手记", "文件", "设置")
                 or (not first and self.gui.auth_prompt(self.pid)),
                 "the real application workbench or restart authentication dialog in WebKit", 90)
        if first:
            self.result["checks"]["webkit_rendered"] = True
        def find_backend():
            for candidate in owned_processes(self.marker):
                arguments = candidate["arguments"]
                if candidate["executable"].name == "omnishare" and "--data-dir" in arguments:
                    index = arguments.index("--data-dir")
                    if index + 1 < len(arguments) and arguments[index + 1] == str(self.data):
                        return candidate
            return None
        backend = wait_for(find_backend, "the application's own bundled backend", 30)
        if self.flavor == "deb":
            require(backend["executable"] == INSTALLED_BACKEND, "Backend did not come from the installed DEB resources")
        else:
            appdir = info["environment"].get(b"APPDIR")
            require(appdir is not None, "Extract-and-run did not provide APPDIR")
            extracted = Path(os.fsdecode(appdir)).resolve()
            require(info["executable"].is_relative_to(extracted) and backend["executable"].is_relative_to(extracted),
                    "AppImage did not launch its extracted application and bundled backend")
        arguments = backend["arguments"]
        require("--port" in arguments and arguments[arguments.index("--port") + 1] == str(self.port),
                "Backend port does not match the port selected through the GUI")
        self.backend_pid = backend["pid"]
        self.result["checks"]["bundled_backend"] = True

    def features(self, phase):
        report = self.evidence / (self.flavor + "-features-" + phase + ".json")
        run([sys.executable, str(FEATURE_TEST), "--base-url", "http://127.0.0.1:" + str(self.port),
             "--data-dir", str(self.data), "--version", self.version, "--state", str(self.state),
             "--phase", phase, "--report", str(report)], timeout=180)
        require(report.is_file(), "Core feature test did not produce evidence")
        result = json.loads(report.read_text())
        require(result.get("schema_version") == 1 and result.get("success") is True
                and result.get("version") == self.version and result.get("phase") == phase
                and result.get("scope") == "installed-backend-http-features"
                and isinstance(result.get("checks"), dict) and bool(result["checks"])
                and all(value is True for value in result["checks"].values()),
                "Core feature evidence does not report complete matching success")
        self.result["core_reports"].append(report.name)
        self.result["features"][phase] = result
        state = json.loads(self.state.read_text())
        require(state.get("version") == self.version and isinstance(state.get("access_key"), str)
                and isinstance(state.get("note_content"), str), "Private core feature state is incomplete")
        self.focus()
        # A WebKit view is not a browser chrome: Ctrl+R is not guaranteed to
        # reload it. Trigger a real protected request through application UI.
        if not self.gui.rendered(self.pid, "需要访问密钥"):
            notes = wait_for(lambda: self.gui.find(self.pid, "随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}), "the notes navigation control")
            self.gui.click(notes)
        wait_for(lambda: self.gui.rendered(self.pid, "需要访问密钥"), "the real access-key login dialog", 45)
        field = wait_for(lambda: self.gui.find(self.pid, "访问密钥", {self.gui.atspi.ROLE_PASSWORD_TEXT}), "the access-key input")
        self.gui.enter(field, state["access_key"])
        self.gui.click(self.gui.find(self.pid, "验证并进入", {self.gui.atspi.ROLE_PUSH_BUTTON}))
        wait_for(lambda: not self.gui.rendered(self.pid, "需要访问密钥"), "successful GUI authentication", 45)
        wait_for(lambda: self.gui.rendered(self.pid, "随手记", "文件", "设置"),
                 "the authenticated application workbench in WebKit", 45)
        self.result["checks"]["webkit_rendered"] = True
        notes = wait_for(lambda: self.gui.find(self.pid, "随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}), "the notes navigation control")
        self.gui.click(notes)
        wait_for(lambda: self.gui.rendered(self.pid, state["note_content"]), "the persisted note rendered by WebKit", 45)
        run(["scrot", str(self.evidence / (self.flavor + "-" + phase + ".png"))])
        self.result["checks"]["core_features" if phase == "exercise" else "restart_persistence"] = True
        self.result["checks"]["core_content_in_webkit"] = True

    def stop(self):
        self.focus()
        run(["xdotool", "key", "--clearmodifiers", "ctrl+q"])
        wait_for(lambda: not any(info["pid"] in (self.pid, self.backend_pid) for info in owned_processes(self.marker)),
                 "normal menu Quit to stop the app and its managed backend", 30)
        if self.launcher is not None:
            try:
                require(self.launcher.wait(timeout=15) == 0, "The application launcher did not exit successfully")
            except subprocess.TimeoutExpired as error:
                raise SmokeFailure("AppImage extraction wrapper did not exit after normal Quit") from error
        self.result["checks"]["normal_quit"] = True

    def exercise(self):
        self.start(first=True)
        self.features("exercise")
        self.stop()
        self.start(first=False)
        self.features("verify")
        self.stop()
        return self.result

    def cleanup(self):
        if self.cleaned:
            return
        # Exceptional cleanup never counts as a successful normal-Quit check.
        # Re-read the UID and unpredictable marker before every signal.
        for signum in (signal.SIGTERM, signal.SIGKILL):
            for info in owned_processes(self.marker):
                current = process_info(info["pid"])
                if current and current["environment"].get(MARKER.encode()) == self.marker.encode():
                    try:
                        os.kill(info["pid"], signum)
                    except ProcessLookupError:
                        pass
            time.sleep(0.3)
        if self.launcher is not None:
            try:
                self.launcher.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
        self.log.close()
        self.cleaned = True


def package_status():
    result = run(["dpkg-query", "-W", "-f=${db:Status-Status}", PACKAGE], check=False)
    return result.stdout.strip() if result.returncode == 0 else "not-installed"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--deb", type=Path, required=True)
    parser.add_argument("--appimage", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    args.evidence = args.evidence.resolve()
    args.evidence.mkdir(parents=True, exist_ok=True)
    report = {"schema_version": 1, "success": False, "version": args.version,
              "platform": os_release(), "normal_user": os.getuid() != 0,
              "appimage_scope": "extract-and-run", "fuse_mount_tested": False,
              "checks": {}, "artifacts": {}}
    started = time.monotonic()
    install_attempted = False
    session = None
    phase = "preflight"
    try:
        require(os.getuid() != 0, "GUI installation checks must run as a normal user")
        require(report["platform"]["id"] == "ubuntu" and report["platform"]["version"] in ("22.04", "24.04"),
                "This gate must run on the supported Ubuntu 22.04 or 24.04 image")
        require(bool(os.environ.get("DISPLAY")) and bool(os.environ.get("DBUS_SESSION_BUS_ADDRESS")),
                "Run the test under dbus-run-session and xvfb-run")
        require(not os.environ.get("WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS"), "WebKit sandbox must remain enabled")
        for tool in ("sudo", "apt-get", "dpkg-deb", "dpkg-query", "readelf", "gio", "desktop-file-validate", "xdotool", "scrot"):
            require(shutil.which(tool) is not None, "Missing test dependency: " + tool)
        require(FEATURE_TEST.is_file(), "Installed feature test helper is missing")
        require(package_status() == "not-installed", "Refusing to replace a pre-existing OmniShare package")
        require(not INSTALLED_BINARY.exists() and not INSTALLED_ENTRY.exists() and not INSTALLED_BACKEND.exists(),
                "Refusing to replace existing desktop installation files")
        for label, path in (("deb", args.deb), ("appimage", args.appimage)):
            require(path.is_file() and not path.is_symlink(), label + " must be a regular build artifact")
            report["artifacts"][label] = {"name": path.name, "sha256": sha256(path)}
        args.deb = args.deb.resolve()
        args.appimage = args.appimage.resolve()
        require(run(["dpkg-deb", "-f", str(args.deb), "Package"]).stdout.strip() == PACKAGE, "Unexpected desktop DEB package name")
        require(run(["dpkg-deb", "-f", str(args.deb), "Architecture"]).stdout.strip() == "amd64", "Unexpected DEB architecture")
        deb_version = run(["dpkg-deb", "-f", str(args.deb), "Version"]).stdout.strip()
        require(deb_version.replace("~", "-", 1) == args.version, "DEB version mismatch")
        dependencies = run(["dpkg-deb", "-f", str(args.deb), "Depends"]).stdout
        require(re.search(r"\blibc6\s*\(>=\s*2\.35\)", dependencies) is not None, "DEB must declare the Ubuntu 22.04 glibc baseline")
        require("libwebkit2gtk-4.1-0" in dependencies, "DEB must declare WebKitGTK 4.1")
        gui = AccessibleGUI()
        with ExitStack() as cleanup_stack:
            private = Path(cleanup_stack.enter_context(tempfile.TemporaryDirectory(prefix="omnishare-installed-linux-")))
            phase = "apt-install"
            install_attempted = True
            run(["sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "--yes", "--no-install-recommends", str(args.deb)], timeout=300)
            require(package_status() == "installed", "APT did not install the DEB")
            for executable in (INSTALLED_BINARY, INSTALLED_BACKEND):
                require(executable.is_file() and os.access(executable, os.X_OK), "Installed executable or backend resource is missing")
                require(maximum_glibc(executable) <= (2, 35), "Installed executable exceeds the Ubuntu 22.04 glibc baseline")
            run(["desktop-file-validate", str(INSTALLED_ENTRY)])
            report["checks"]["apt_installed"] = True
            report["checks"]["glibc_baseline"] = True
            report["checks"]["desktop_entry_valid"] = True
            phase = "deb-gui"
            session = ApplicationSession("deb", private, args.evidence, args.version, gui)
            cleanup_stack.callback(session.cleanup)
            report["deb"] = session.result
            session.exercise()
            before_removal = data_fingerprint(session.data)
            phase = "apt-remove"
            run(["sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "--yes", PACKAGE], timeout=180)
            require(package_status() != "installed", "APT did not remove the desktop package")
            require(not INSTALLED_BINARY.exists() and not INSTALLED_ENTRY.exists() and not INSTALLED_BACKEND.exists(), "Uninstall left program files behind")
            require(data_fingerprint(session.data) == before_removal, "Uninstall changed private application data")
            report["checks"]["apt_uninstalled"] = True
            report["checks"]["uninstall_preserves_data"] = True
            install_attempted = False
            session.cleanup()
            session = None
            phase = "appimage-gui"
            image = private / "AppImage 路径 # %" / args.appimage.name
            image.parent.mkdir()
            shutil.copyfile(args.appimage, image)
            image.chmod(0o755)
            require(sha256(image) == report["artifacts"]["appimage"]["sha256"], "AppImage copy changed its bytes")
            session = ApplicationSession("appimage", private, args.evidence, args.version, gui, image)
            cleanup_stack.callback(session.cleanup)
            report["appimage"] = session.result
            session.exercise()
            session.cleanup()
            session = None
            report["success"] = True
    except Exception as error:
        # Never put native logs, state, or credentials into public evidence.
        report["error"] = phase + ": " + (str(error) if isinstance(error, SmokeFailure) else type(error).__name__)
        print("FAIL Linux installed smoke: " + report["error"], file=sys.stderr)
    finally:
        if session is not None:
            try:
                session.cleanup()
            except Exception as error:
                report["success"] = False
                report["cleanup_process_error"] = type(error).__name__
        if install_attempted:
            try:
                removed = run(["sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "remove", "--yes", PACKAGE], timeout=180, check=False)
                report["cleanup_package_removed"] = removed.returncode == 0 and package_status() != "installed"
            except Exception as error:
                report["cleanup_package_removed"] = False
                report["cleanup_package_error"] = type(error).__name__
        report["elapsed_seconds"] = round(time.monotonic() - started, 2)
        (args.evidence / "linux-installed-smoke.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    if report["success"]:
        print("PASS Linux installed DEB and AppImage extract-and-run: WebKit, core features, restart, Quit and uninstall")
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
