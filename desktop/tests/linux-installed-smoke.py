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


def redacted_input_evidence(text, expected):
    return {"length": len(text), "expected_length": len(expected), "matches_expected": text == expected,
            "masked": bool(text) and all(character in "\u2022\u25cf*" for character in text)}


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


class AtspiDBus:
    """Read AT-SPI directly without libatspi's re-entrant cache dispatch.

    Each provider call has a real D-Bus timeout, in addition to the worker's
    wall-clock limit. The tree still comes from the installed application's
    accessibility providers; no HTTP or browser debug surface is consulted.
    """

    ACCESSIBLE = "org.a11y.atspi.Accessible"
    ROOT_PATH = "/org/a11y/atspi/accessible/root"

    def __init__(self, diagnostic):
        from gi.repository import Gio, GLib
        self.gio, self.glib = Gio, GLib
        self.diagnostic = diagnostic
        self.diagnostic.update(driver="bounded-atspi-dbus", calls=0, nodes=0)
        self.deadline = time.monotonic() + 6
        self.owners = {}
        self.diagnostic["phase"] = "connect-session"
        self.session = Gio.bus_get_sync(Gio.BusType.SESSION, None)
        address = self.call(self.session, "org.a11y.Bus", "/org/a11y/bus", "org.a11y.Bus", "GetAddress")[0]
        require(isinstance(address, str) and address.startswith("unix:"), "Accessibility bus is not local")
        self.address = address
        self.address_kind = "path" if address.startswith("unix:path=") else "abstract" if address.startswith("unix:abstract=") else "unix"
        self.diagnostic["phase"] = "connect-accessibility"
        self.connection = Gio.DBusConnection.new_for_address_sync(
            address, Gio.DBusConnectionFlags.AUTHENTICATION_CLIENT | Gio.DBusConnectionFlags.MESSAGE_BUS_CONNECTION,
            None, None)

    def register_client(self):
        # WebKit activates its AX tree for registered assistive-technology
        # clients. Keep this connection alive across disposable read workers;
        # otherwise older WebKit providers can expose only the GTK shell.
        self.call(self.session, "org.a11y.Bus", "/org/a11y/bus", "org.freedesktop.DBus.Properties", "Set",
                  "(ssv)", ("org.a11y.Status", "IsEnabled", self.glib.Variant("b", True)))
        for event in ("object:state-changed", "document:load-complete"):
            self.request(("org.a11y.atspi.Registry", "/org/a11y/atspi/registry"),
                         "org.a11y.atspi.Registry", "RegisterEvent", "(sass)", (event, [], ""))

    def call(self, connection, destination, path, interface, method, signature=None, arguments=()):
        remaining = int((self.deadline - time.monotonic()) * 1000)
        require(remaining > 0, "Accessibility query exhausted its deadline")
        self.diagnostic["phase"] = interface.rsplit(".", 1)[-1] + "." + method
        self.diagnostic["calls"] += 1
        value = connection.call_sync(destination, path, interface, method,
                                     self.glib.Variant(signature, arguments) if signature else None,
                                     None, self.gio.DBusCallFlags.NONE, min(750, remaining), None)
        return value.unpack()

    def request(self, reference, interface, method, signature=None, arguments=()):
        return self.call(self.connection, reference[0], reference[1], interface, method, signature, arguments)

    def owner(self, name):
        if name not in self.owners:
            self.owners[name] = self.call(self.connection, "org.freedesktop.DBus", "/org/freedesktop/DBus",
                                          "org.freedesktop.DBus", "GetConnectionUnixProcessID", "(s)", (name,))[0]
        return self.owners[name]

    def children(self, reference):
        children = self.request(reference, self.ACCESSIBLE, "GetChildren")[0]
        if children:
            return children[:1000]
        # Ubuntu 22.04's ATK bridge predates socket support in GetChildren
        # (added in at-spi2-core 2.47.1). Its ChildCount/GetChildAtIndex do
        # expose the embedded WebKit root. Read that protocol directly, with
        # the same per-call and worker bounds as every other provider read.
        count = self.request(reference, "org.freedesktop.DBus.Properties", "Get", "(ss)",
                             (self.ACCESSIBLE, "ChildCount"))[0]
        require(type(count) is int and count >= 0, "Invalid accessibility child count")
        children = []
        for index in range(min(count, 1000)):
            if time.monotonic() >= self.deadline:
                break
            children.append(self.request(reference, self.ACCESSIBLE, "GetChildAtIndex", "(i)", (index,))[0])
            self.diagnostic["indexed_child_reads"] = self.diagnostic.get("indexed_child_reads", 0) + 1
        return children

    def nodes(self, allowed_pids):
        roots = self.request(("org.a11y.atspi.Registry", self.ROOT_PATH), self.ACCESSIBLE, "GetChildren")[0]
        self.diagnostic.update(registry_roots=len(roots), rejected_owners=0, provider_errors=0)
        pending = [(tuple(reference), 0, None) for reference in reversed(roots)]
        visited, result = set(), []
        while pending and len(visited) < 1000 and time.monotonic() < self.deadline:
            reference, depth, parent = pending.pop()
            if reference in visited or depth > 40 or reference[1] == "/org/a11y/atspi/null":
                continue
            visited.add(reference)
            try:
                if self.owner(reference[0]) not in allowed_pids:
                    self.diagnostic["rejected_owners"] += 1
                    continue
                node = DBusAccessible(self, reference, parent)
                result.append(node)
                children = self.children(reference)
                node.has_children = bool(children)
                pending.extend((tuple(child), depth + 1, node) for child in reversed(children[:1000]))
            except Exception as error:
                # An unrelated/defunct provider cannot block the owned tree.
                # Its missing controls still cannot satisfy the GUI checks.
                self.diagnostic["provider_errors"] += 1
                self.diagnostic["last_provider_error"] = type(error).__name__
                continue
        self.diagnostic["nodes"] = len(result)
        self.diagnostic["deadline_reached"] = time.monotonic() >= self.deadline
        return result


class DBusAccessible:
    """A fresh, bounded view of one real AT-SPI object within one query."""

    def __init__(self, bus, reference, parent=None):
        self.bus, self.reference = bus, reference
        self.parent = parent
        self.has_children = True
        self.values = {}

    def request(self, interface, method, signature=None, arguments=()):
        return self.bus.request(self.reference, interface, method, signature, arguments)

    @property
    def name(self):
        if "name" not in self.values:
            self.values["name"] = self.request("org.freedesktop.DBus.Properties", "Get", "(ss)",
                                                (AtspiDBus.ACCESSIBLE, "Name"))[0]
        return self.values["name"]

    def getRole(self):
        if "role" not in self.values:
            self.values["role"] = self.request(AtspiDBus.ACCESSIBLE, "GetRole")[0]
        return self.values["role"]

    def getRoleName(self):
        return self.request(AtspiDBus.ACCESSIBLE, "GetRoleName")[0]

    def getInterfaces(self):
        if "interfaces" not in self.values:
            self.values["interfaces"] = self.request(AtspiDBus.ACCESSIBLE, "GetInterfaces")[0]
        return self.values["interfaces"]

    def getState(self):
        if "state" not in self.values:
            self.values["state"] = self.request(AtspiDBus.ACCESSIBLE, "GetState")[0]
        return self

    def contains(self, state):
        words = self.values["state"]
        index = int(state)
        return index // 32 < len(words) and bool(words[index // 32] & (1 << (index % 32)))

    def queryComponent(self):
        return self

    def scrollTo(self, position):
        result = self.request("org.a11y.atspi.Component", "ScrollTo", "(u)", (int(position),))[0]
        self.values.pop("state", None)
        return result

    def getExtents(self, coordinate_type):
        from types import SimpleNamespace
        response = self.request("org.a11y.atspi.Component", "GetExtents", "(u)", (int(coordinate_type),))
        self.values["extents_shape"] = [type(response).__name__, len(response)]
        rect = response[0]
        self.values["extents_shape"].extend([type(rect).__name__, len(rect) if isinstance(rect, (tuple, list)) else None])
        return SimpleNamespace(**dict(zip(("x", "y", "width", "height"), rect)))

    def queryText(self):
        return self

    def getText(self, start, end):
        return self.request("org.a11y.atspi.Text", "GetText", "(ii)", (start, end))[0]


class AccessibleGUI:
    def __init__(self, isolated=True, diagnostic=None):
        try:
            import gi
            gi.require_version("Atspi", "2.0")
            from gi.repository import Atspi
            from types import SimpleNamespace
            # Import only enum metadata. pyatspi/libatspi's process-wide cache
            # machinery is deliberately not initialized in either process.
            roles = ("DOCUMENT_WEB", "DOCUMENT_FRAME", "PASSWORD_TEXT", "TEXT", "PARAGRAPH", "STATIC",
                     "DIALOG", "PUSH_BUTTON", "SPIN_BUTTON")
            self.atspi = SimpleNamespace(**{"ROLE_" + name: getattr(Atspi.Role, name) for name in roles},
                                         STATE_SHOWING=Atspi.StateType.SHOWING, STATE_EDITABLE=Atspi.StateType.EDITABLE,
                                         STATE_FOCUSED=Atspi.StateType.FOCUSED, STATE_ENABLED=Atspi.StateType.ENABLED,
                                         DESKTOP_COORDS=Atspi.CoordType.SCREEN, SCROLL_ANYWHERE=Atspi.ScrollType.ANYWHERE)
            self.isolated = isolated
            if isolated:
                # The long-lived connection talks only to the session bus and
                # registry, never to a potentially blocked app provider.
                self.client = AtspiDBus({})
                self.client.register_client()
                # Native providers can hang inside C/D-Bus despite Python loop
                # deadlines. Isolate each read in a disposable bounded process.
                return
            self.bus = AtspiDBus(diagnostic if diagnostic is not None else {})
        except (ImportError, ValueError, AttributeError) as error:
            raise SmokeFailure("System python3-pyatspi and Atspi 2.0 are required for real WebKit verification") from error

    def query(self, pid, action, **arguments):
        request = dict(pid=pid, action=action, **arguments)
        try:
            completed = subprocess.run(
                [sys.executable, str(Path(__file__).resolve()), "--a11y-query"],
                input=json.dumps(request), capture_output=True, text=True,
                timeout=8, check=False)
            if completed.returncode != 0 or len(completed.stdout) > 65536:
                self.last_query_diagnostic = {"returncode": completed.returncode, "oversize_output": len(completed.stdout) > 65536}
                return None
            response = json.loads(completed.stdout)
            self.last_query_diagnostic = response.get("diagnostic", {})
            return response.get("value")
        except (subprocess.TimeoutExpired, ValueError, OSError) as error:
            self.last_query_diagnostic = {"error_type": type(error).__name__}
            if isinstance(error, subprocess.TimeoutExpired) and error.stderr:
                trace = error.stderr.decode("utf-8", "replace") if isinstance(error.stderr, bytes) else error.stderr
                # Extract only function names and source line numbers from the
                # timed worker; never publish provider data or raw stderr.
                self.last_query_diagnostic["blocked_frames"] = re.findall(r'line (\d+) in ([A-Za-z_][A-Za-z_0-9]*)', trace)[-12:]
            return None

    def nodes(self, pid):
        # WebKit may register its web-process accessibility root separately
        # from GTK. Accept only this test instance's marker-owned processes.
        allowed_pids = {pid}
        owner = process_info(pid)
        if owner:
            marker = owner["environment"].get(MARKER.encode())
            if marker:
                allowed_pids.update(item["pid"] for item in owned_processes(os.fsdecode(marker)))
        return self.bus.nodes(allowed_pids)

    def showing(self, node):
        try:
            return node.getState().contains(self.atspi.STATE_SHOWING)
        except Exception:
            return False

    def text(self, pid):
        documents = []
        fragments = []
        editors = set()
        deadline = time.monotonic() + 5
        for node in self.nodes(pid):
            if time.monotonic() >= deadline:
                break
            try:
                role = node.getRole()
                if (id(getattr(node, "parent", None)) in editors
                        or role == self.atspi.ROLE_PASSWORD_TEXT
                        or node.getState().contains(self.atspi.STATE_EDITABLE)):
                    # Unsaved editor/search values, including their text
                    # descendants, cannot prove a saved note was displayed.
                    editors.add(id(node))
                    continue
                if not self.showing(node):
                    continue
                interfaces = node.getInterfaces()
                if (role in (self.atspi.ROLE_DOCUMENT_WEB, self.atspi.ROLE_DOCUMENT_FRAME)
                        or "org.a11y.atspi.Document" in interfaces):
                    rect = node.queryComponent().getExtents(self.atspi.DESKTOP_COORDS)
                    if rect.width > 50 and rect.height > 50:
                        documents.append(node)
                if node.name:
                    fragments.append(node.name)
                try:
                    # WebKit exposes plain div contents as a Text-capable
                    # section, not necessarily a TEXT/PARAGRAPH/STATIC role.
                    # Read only real text leaves: container text can include
                    # an unsaved descendant editor even if the container is
                    # not itself editable.
                    if not node.has_children and "org.a11y.atspi.Text" in interfaces:
                        # AT-SPI uses -1 for end-of-text. Passing a fixed end
                        # beyond a short leaf's length is invalid in WebKit.
                        fragments.append(node.queryText().getText(0, -1)[:4096])
                except Exception:
                    pass
            except Exception:
                continue
        return bool(documents), " ".join(" ".join(fragments).split())

    def rendered(self, pid, *texts):
        if getattr(self, "isolated", False):
            return self.query(pid, "rendered", texts=list(texts)) is True
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

    def find(self, pid, label, roles=None, editable=False, reveal=False):
        if getattr(self, "isolated", False):
            return self.query(pid, "find", label=label, roles=list(roles) if roles else None, editable=editable, reveal=reveal)
        deadline = time.monotonic() + 5
        for node in self.nodes(pid):
            if time.monotonic() >= deadline:
                break
            try:
                if label not in node.name or (roles is not None and node.getRole() not in roles):
                    continue
                if editable and not (node.getState().contains(self.atspi.STATE_EDITABLE)
                                     or node.getRole() == self.atspi.ROLE_SPIN_BUTTON):
                    continue
                if reveal and node.queryComponent().scrollTo(self.atspi.SCROLL_ANYWHERE) is not True:
                    continue
                if self.showing(node):
                    return node
            except Exception:
                continue
        return None

    def coordinates(self, node):
        require(node is not None, "Required visible WebKit control was not found")
        try:
            if isinstance(node, dict):
                from types import SimpleNamespace
                require(process_info(node["pid"]) is not None, "Native control owner exited")
                rect = SimpleNamespace(**node["rect"])
            else:
                rect = node.queryComponent().getExtents(self.atspi.DESKTOP_COORDS)
        except Exception as error:
            raise SmokeFailure("WebKit control has no screen coordinates") from error
        require(rect.width > 0 and rect.height > 0, "WebKit control is not visibly rendered")
        return str(rect.x + rect.width // 2), str(rect.y + rect.height // 2)

    def click(self, node):
        x, y = self.coordinates(node)
        run(["xdotool", "mousemove", "--sync", x, y, "click", "1"])

    def scroll_down(self, anchor):
        # Anchor the real wheel input inside the page using a control that
        # was just clicked, rather than whichever sidebar retains focus.
        x, y = self.coordinates(anchor)
        run(["xdotool", "mousemove", "--sync", x, y, "click", "--repeat", "3", "--delay", "80", "5"])

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
                         "ACCESSIBILITY_ENABLED": "1", "AT_SPI_BUS_ADDRESS": self.gui.client.address})
        self.log = (self.private / "private-process.log").open("wb")
        self.launcher = None
        self.pid = None
        self.backend_pid = None
        self.window = None
        self.cleaned = False
        self.native_note = "native-ui-" + secrets.token_hex(16)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            self.port = listener.getsockname()[1]
        self.result = {"checks": {}, "core_reports": [], "features": {},
                       "accessibility_transport": self.gui.client.address_kind}

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
            try:
                wait_for(lambda: self.gui.rendered(self.pid, "首次启动设置"), "the first-run page in WebKit", 90)
            except SmokeFailure:
                # Capture after the wait too: a screenshot taken at X11 map
                # time cannot tell a delayed first paint from a blank WebKit.
                run(["scrot", str(self.evidence / (self.flavor + "-first-run-failed.png"))], check=False)
                self.result["first_run_query_diagnostic"] = getattr(self.gui, "last_query_diagnostic", {})
                self.result["first_run_accessibility"] = self.gui.query(self.pid, "diagnostic")
                self.result["first_run_owned_executables"] = sorted({item["executable"].name for item in owned_processes(self.marker)})
                native_log = (self.private / "private-process.log").read_text(errors="replace")
                self.result["first_run_native_error_counts"] = {
                    name: len(re.findall(pattern, native_log, re.IGNORECASE)) for name, pattern in {
                        "a11y_bus_connection": r"(?:can't|failed to) connect to (?:the )?a11y bus",
                        "a11y_bus_discovery": r"can't find a11y bus",
                        "a11y_bus_ownership": r"can't own name .*? on a11y bus",
                        "atspi_registration": r"(?:failed|unable) to (?:register|embed).*?(?:atspi|at-spi|accessib)",
                        "dbus_access_denied": r"org\.freedesktop\.DBus\.Error\.AccessDenied",
                        "sandbox_error": r"(?:bwrap|bubblewrap).*?(?:error|denied|failed)",
                    }.items()}
                raise
            run(["scrot", str(self.evidence / (self.flavor + "-first-window-ready.png"))])
            field = wait_for(lambda: self.gui.find(self.pid, "本地服务端口", editable=True), "the first-run port control")
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
        self.wait_for_auth_prompt()
        field = wait_for(lambda: self.gui.find(self.pid, "访问密钥", {self.gui.atspi.ROLE_PASSWORD_TEXT}), "the access-key input")
        self.gui.enter(field, state["access_key"])
        authentication = {"before_submit": self.gui.query(self.pid, "auth_state", expected=state["access_key"]),
                          "dialog_closed_observed": False}
        self.result[phase + "_authentication"] = authentication
        run(["scrot", str(self.evidence / (self.flavor + "-" + phase + "-auth-before-submit.png"))])
        submit = self.gui.find(self.pid, "验证并进入", {self.gui.atspi.ROLE_PUSH_BUTTON})
        authentication["submit_control"] = submit
        try:
            self.gui.click(submit)
            authentication["after_submit"] = self.gui.query(self.pid, "auth_state", expected=state["access_key"])
            wait_for(lambda: not self.gui.rendered(self.pid, "需要访问密钥"), "successful GUI authentication", 45)
            authentication["dialog_closed_observed"] = True
            wait_for(lambda: self.gui.rendered(self.pid, "随手记", "文件", "设置"),
                     "the authenticated application workbench in WebKit", 45)
        except Exception:
            authentication["failure_state"] = self.gui.query(self.pid, "auth_state", expected=state["access_key"])
            authentication["query_diagnostic"] = getattr(self.gui, "last_query_diagnostic", {})
            run(["scrot", str(self.evidence / (self.flavor + "-" + phase + "-auth-failed.png"))], check=False)
            raise
        self.result["checks"]["webkit_rendered"] = True
        notes = wait_for(lambda: self.gui.find(self.pid, "随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}), "the notes navigation control")
        self.gui.click(notes)
        # The feature fixture ends in a unique ASCII token. X11 keyboard
        # mappings can alter CJK punctuation during synthesized typing, so
        # filter by that token but still assert the complete Unicode body.
        query = state["note_content"].rsplit(" ", 1)[-1]
        require(re.fullmatch(r"[0-9a-f]{12}", query) is not None, "Feature note lacks its unique search token")
        self.search_note(query, state["note_content"], phase)
        run(["scrot", str(self.evidence / (self.flavor + "-" + phase + ".png"))])
        self.result["checks"]["core_features" if phase == "exercise" else "restart_persistence"] = True
        self.result["checks"]["core_content_in_webkit"] = True

    def wait_for_auth_prompt(self):
        def prompt_or_request():
            if self.gui.rendered(self.pid, "需要访问密钥"):
                return True
            # A background request can open the modal between these two
            # reads and make navigation inert. Re-check the modal on the
            # next poll instead of getting stuck waiting for the sidebar.
            notes = self.gui.find(self.pid, "随手记", {self.gui.atspi.ROLE_PUSH_BUTTON})
            if notes is not None:
                self.gui.click(notes)
            return False
        try:
            wait_for(prompt_or_request, "the real access-key login dialog", 45)
        except SmokeFailure:
            # No credential has been typed into this prompt yet.
            run(["scrot", str(self.evidence / (self.flavor + "-auth-prompt-failed.png"))], check=False)
            self.result["authentication_query_diagnostic"] = getattr(self.gui, "last_query_diagnostic", {})
            raise

    def wait_for_note(self, text, description, anchor):
        def reveal():
            if self.gui.rendered(self.pid, text):
                return True
            self.gui.scroll_down(anchor)
            return False
        wait_for(reveal, description)

    def search_note(self, query, text, phase):
        try:
            # Scroll the actual accessible input into view before obtaining
            # fresh screen coordinates. SHOWING alone allows a clipped input.
            search = wait_for(lambda: self.gui.find(self.pid, "搜索随手记", editable=True, reveal=True),
                              "native note search fully in view")
            self.gui.enter(search, query)
            wait_for(lambda: (self.gui.query(self.pid, "search_state", expected=query) or {}).get("matches_expected") is True,
                     "exact text in the native search input", 15)
            self.wait_for_note(text, "the persisted note rendered by WebKit", search)
        except Exception:
            # Authentication has finished and the notes page contains only
            # this isolated test's synthetic fixtures. Never return editor
            # values, access credentials, or the private feature state.
            run(["scrot", str(self.evidence / (self.flavor + "-" + phase + "-search-failed.png"))], check=False)
            self.result[phase + "_search_query_diagnostic"] = getattr(self.gui, "last_query_diagnostic", {})
            self.result[phase + "_search_state"] = self.gui.query(self.pid, "search_state", expected=query)
            raise

    def native_note_edit(self):
        self.focus()
        notes_nav = wait_for(lambda: self.gui.find(self.pid, "随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}),
                             "native notes navigation")
        self.gui.click(notes_nav)
        open_create = wait_for(lambda: self.gui.find(self.pid, "新建随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}),
                               "native new note button")
        self.gui.click(open_create)
        field = wait_for(lambda: self.gui.find(self.pid, "随手记正文", editable=True), "native note composer")
        self.gui.enter(field, self.native_note)
        publish = wait_for(lambda: self.gui.find(self.pid, "发布随手记", {self.gui.atspi.ROLE_PUSH_BUTTON}),
                           "native note publish button")
        self.gui.click(publish)
        self.wait_for_note(self.native_note, "note created through native GUI", notes_nav)
        self.result["checks"]["native_note_created"] = True
        self.gui.click(wait_for(lambda: self.gui.find(self.pid, "编辑", {self.gui.atspi.ROLE_PUSH_BUTTON}),
                                "native note edit button"))
        edited = self.native_note + "-edited-through-native-window"
        self.gui.enter(wait_for(lambda: self.gui.find(self.pid, "随手记正文", editable=True), "native note edit field"), edited)
        save = wait_for(lambda: self.gui.find(self.pid, "保存更新", {self.gui.atspi.ROLE_PUSH_BUTTON}), "native note save button")
        self.gui.click(save)
        self.wait_for_note(edited, "edited note saved through native GUI", save)
        self.native_note = edited
        self.result["checks"]["native_note_edited"] = True
        run(["scrot", str(self.evidence / (self.flavor + "-native-note-edited.png"))])

    def native_note_verify(self):
        self.focus()
        self.search_note(self.native_note, self.native_note, "native-restart")
        self.result["checks"]["native_note_restart_verified"] = True
        run(["scrot", str(self.evidence / (self.flavor + "-native-note-restart.png"))])

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
        try:
            self.native_note_edit()
        except Exception:
            # This phase contains only a fresh profile and our synthetic note;
            # access credentials have not yet been generated or entered.
            run(["scrot", str(self.evidence / (self.flavor + "-native-note-failed.png"))], check=False)
            self.result["native_note_query_diagnostic"] = getattr(self.gui, "last_query_diagnostic", {})
            self.result["native_note_accessibility"] = self.gui.query(self.pid, "diagnostic")
            raise
        self.features("exercise")
        self.stop()
        self.start(first=False)
        self.features("verify")
        self.native_note_verify()
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
            report["native_runtime_packages"] = run(
                ["dpkg-query", "-W", "-f=${binary:Package}=${Version}\n", "libwebkit2gtk-4.1-0", "libatk-bridge2.0-0", "at-spi2-core"],
                check=False).stdout.splitlines()
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


def accessibility_worker():
    import faulthandler
    faulthandler.dump_traceback_later(4)
    request = json.loads(sys.stdin.read(65536))
    pid = request["pid"]
    require(isinstance(pid, int) and process_info(pid) is not None, "Invalid native control owner")
    value, diagnostic = None, {}
    try:
        gui = AccessibleGUI(isolated=False, diagnostic=diagnostic)
        if request["action"] == "rendered":
            value = gui.rendered(pid, *request["texts"])
        elif request["action"] == "diagnostic":
            value = []
            for node in gui.nodes(pid)[:160]:
                try:
                    role, interfaces = node.getRole(), node.getInterfaces()
                    row = {"role": node.getRoleName(), "role_id": role, "interfaces": interfaces,
                           "name": node.name[:160], "showing": gui.showing(node),
                           "editable": node.getState().contains(gui.atspi.STATE_EDITABLE), "has_children": node.has_children}
                    if (role in (gui.atspi.ROLE_DOCUMENT_WEB, gui.atspi.ROLE_DOCUMENT_FRAME)
                            or "org.a11y.atspi.Document" in interfaces):
                        try:
                            rect = node.queryComponent().getExtents(gui.atspi.DESKTOP_COORDS)
                            row["rect"] = {name: getattr(rect, name) for name in ("x", "y", "width", "height")}
                        except Exception as error:
                            row["rect_error"] = type(error).__name__
                        row["rect_shape"] = node.values.get("extents_shape")
                    if not row["editable"] and not node.has_children and "org.a11y.atspi.Text" in interfaces:
                        try:
                            row["text"] = node.queryText().getText(0, -1)[:160]
                        except Exception as error:
                            row["text_error"] = type(error).__name__
                    value.append(row)
                except Exception:
                    continue
        elif request["action"] == "find":
            node = gui.find(pid, request["label"], request.get("roles"), request.get("editable", False), request.get("reveal", False))
            if node is not None:
                rect = node.queryComponent().getExtents(gui.atspi.DESKTOP_COORDS)
                value = {"pid": pid, "rect": {name: getattr(rect, name) for name in ("x", "y", "width", "height")}}
        elif request["action"] == "search_state":
            node = gui.find(pid, "搜索随手记", editable=True)
            if node is not None:
                text = node.queryText().getText(0, -1)
                rect = node.queryComponent().getExtents(gui.atspi.DESKTOP_COORDS)
                value = {"matches_expected": text == request["expected"], "length": len(text), "ascii_only": text.isascii(),
                         "rect": {name: getattr(rect, name) for name in ("x", "y", "width", "height")}}
        elif request["action"] == "auth_state":
            value = {"dialog_present": gui.rendered(pid, "需要访问密钥"),
                     "incorrect_value_error": gui.rendered(pid, "密钥不正确")}
            field = gui.find(pid, "访问密钥", {gui.atspi.ROLE_PASSWORD_TEXT})
            if field is not None:
                value["input_state"] = redacted_input_evidence(field.queryText().getText(0, -1), request["expected"])
                value["input_state"]["focused"] = field.getState().contains(gui.atspi.STATE_FOCUSED)
                rect = field.queryComponent().getExtents(gui.atspi.DESKTOP_COORDS)
                value["input_state"]["rect"] = {name: getattr(rect, name) for name in ("x", "y", "width", "height")}
            submit = gui.find(pid, "验证并进入", {gui.atspi.ROLE_PUSH_BUTTON})
            if submit is not None:
                value["submit_state"] = {"focused": submit.getState().contains(gui.atspi.STATE_FOCUSED),
                                         "enabled": submit.getState().contains(gui.atspi.STATE_ENABLED)}
        else:
            raise SmokeFailure("Unsupported native query")
    except Exception as error:
        # Provider exceptions may include private document text or addresses.
        # Retain only the bounded protocol phase and exception class.
        diagnostic["error_type"] = type(error).__name__
    finally:
        faulthandler.cancel_dump_traceback_later()
    print(json.dumps({"value": value, "diagnostic": diagnostic}), flush=True)


if __name__ == "__main__":
    if sys.argv[1:] == ["--a11y-query"]:
        accessibility_worker()
    else:
        sys.exit(main())
