#!/usr/bin/env python3
"""Safety regressions for the installed Linux gate; no APT or GUI required."""

import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("linux_installed_smoke", Path(__file__).with_name("linux-installed-smoke.py"))
smoke = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke)


class InstalledSmokeSafetyTests(unittest.TestCase):
    def test_restart_auth_modal_does_not_require_its_inert_background(self):
        class ModalGUI:
            atspi = SimpleNamespace(ROLE_PUSH_BUTTON=1)

            def rendered(self, _pid, *texts):
                return texts in (("首次启动设置",), ("需要访问密钥",))

            def auth_prompt(self, _pid):
                return True

            def find(self, *_args, **_kwargs):
                return object()

            def enter(self, *_args):
                pass

            def click(self, *_args):
                pass

        session = smoke.ApplicationSession.__new__(smoke.ApplicationSession)
        session.flavor = "deb"
        session.private = Path("/unused-private-dir")
        session.evidence = session.private / "evidence"
        session.data = session.private / "用户 数据 # %"
        session.env, session.log, session.marker = {}, io.BytesIO(), "test-owned-marker"
        session.gui, session.port = ModalGUI(), 49152
        session.result = {"checks": {}}
        backend = {"pid": 234, "executable": smoke.INSTALLED_BACKEND,
                   "arguments": ["omnishare", "--data-dir", str(session.data), "--port", "49152"]}

        def immediate_wait(predicate, description, timeout=60):
            value = predicate()
            if not value:
                raise smoke.SmokeFailure("Timed out waiting for " + description)
            return value

        with patch.object(smoke.subprocess, "Popen"), patch.object(session, "app_process", return_value={"pid": 123}), \
                patch.object(session, "focus"), patch.object(smoke, "wait_for", side_effect=immediate_wait), \
                patch.object(smoke, "run", return_value=SimpleNamespace(returncode=0, stdout="100\n")), \
                patch.object(smoke, "owned_processes", return_value=[backend]):
            session.start(first=False)
            self.assertEqual(session.backend_pid, 234)
            self.assertNotIn("webkit_rendered", session.result["checks"], "modal alone cannot claim the workbench was visible")
            with self.assertRaisesRegex(smoke.SmokeFailure, "workbench"):
                session.start(first=True)

    def test_restart_auth_prompt_requires_a_document_dialog_and_both_controls(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.atspi = SimpleNamespace(ROLE_DIALOG=1, ROLE_PASSWORD_TEXT=2, ROLE_PUSH_BUTTON=3)
        for missing in (None, "需要访问密钥", "访问密钥", "验证并进入"):
            with self.subTest(missing=missing), patch.object(gui, "rendered", return_value=True), \
                    patch.object(gui, "find", side_effect=lambda _pid, name, _roles: None if name == missing else object()):
                self.assertEqual(gui.auth_prompt(123), missing is None)
        with patch.object(gui, "rendered", return_value=False), patch.object(gui, "find", return_value=object()):
            self.assertFalse(gui.auth_prompt(123), "native dialog text without a WebKit document must not pass")

    def test_auth_modal_opening_during_navigation_lookup_is_rechecked(self):
        visible = iter((False, True))
        session = smoke.ApplicationSession.__new__(smoke.ApplicationSession)
        session.pid = 123
        session.gui = SimpleNamespace(
            atspi=SimpleNamespace(ROLE_PUSH_BUTTON=1),
            rendered=lambda *_args: next(visible),
            find=lambda *_args: None,  # The newly opened modal made the sidebar inert.
            click=lambda *_args: self.fail("an absent/inert navigation control cannot be clicked"))

        def poll(predicate, _description, _timeout):
            self.assertFalse(predicate())
            self.assertTrue(predicate(), "the modal must be observed even though navigation disappeared")
            return True

        with patch.object(smoke, "wait_for", side_effect=poll):
            session.wait_for_auth_prompt()

    @unittest.skipUnless(sys.platform.startswith("linux"), "Linux process ownership uses /proc")
    def test_exception_cleanup_stops_only_its_marker_owned_process(self):
        marker = secrets.token_hex(24)
        child = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"],
                                 env=dict(os.environ, **{smoke.MARKER: marker}))
        unrelated = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"],
                                     env=dict(os.environ, **{smoke.MARKER: secrets.token_hex(24)}))
        try:
            smoke.wait_for(lambda: any(item["pid"] == child.pid for item in smoke.owned_processes(marker)),
                           "private test child", 5)
            session = smoke.ApplicationSession.__new__(smoke.ApplicationSession)
            session.marker = marker
            session.launcher = child
            session.cleaned = False
            session.log = io.BytesIO()
            session.cleanup()
            self.assertIsNotNone(child.poll())
            self.assertIsNone(unrelated.poll(), "cleanup stopped an unrelated normal-user process")
            self.assertTrue(session.log.closed)
            session.cleanup()  # ExitStack and final cleanup may both run.
        finally:
            for process in (child, unrelated):
                if process.poll() is None:
                    process.terminate()
                process.wait(timeout=5)

    def test_accessibility_deadline_also_bounds_children_of_one_node(self):
        clock = [0.0]
        bus = smoke.AtspiDBus.__new__(smoke.AtspiDBus)
        bus.deadline, bus.diagnostic = 3.0, {}
        reads = []

        def request(reference, _interface, _method):
            if reference[0] == "org.a11y.atspi.Registry":
                return ([(":1.42", "/root")],)
            reads.append(reference)
            clock[0] += 1.1
            return ([(":1.42", "/child/" + str(index)) for index in range(1000)],)

        with patch.object(bus, "request", side_effect=request), patch.object(bus, "owner", return_value=123), \
                patch.object(smoke.time, "monotonic", side_effect=lambda: clock[0]):
            bus.nodes({123})
        self.assertLessEqual(len(reads), 3)

    def test_dbus_calls_have_an_explicit_remaining_deadline(self):
        bus = smoke.AtspiDBus.__new__(smoke.AtspiDBus)
        bus.deadline, bus.diagnostic = 10.0, {"calls": 0}
        bus.gio = SimpleNamespace(DBusCallFlags=SimpleNamespace(NONE=0))
        bus.glib = SimpleNamespace(Variant=lambda signature, args: (signature, args))
        connection = SimpleNamespace(call_sync=lambda *args: SimpleNamespace(unpack=lambda: args))
        with patch.object(smoke.time, "monotonic", return_value=9.75):
            args = bus.call(connection, ":1.42", "/root", "org.a11y.atspi.Accessible", "GetRole")
        self.assertEqual(args[-2], 250, "provider timeout must not exceed the query's remaining wall-clock budget")
        with patch.object(smoke.time, "monotonic", return_value=11.0):
            with self.assertRaisesRegex(smoke.SmokeFailure, "deadline"):
                bus.call(connection, ":1.42", "/root", "org.a11y.atspi.Accessible", "GetRole")

    def test_native_tree_excludes_other_application_processes(self):
        bus = smoke.AtspiDBus.__new__(smoke.AtspiDBus)
        bus.deadline, bus.diagnostic = float("inf"), {}
        reads = []

        def request(reference, _interface, method, *_arguments):
            if reference[0] == "org.a11y.atspi.Registry":
                return ([(":1.42", "/owned"), (":1.99", "/unrelated")],)
            if method == "Get":
                return (0,)
            reads.append(reference)
            return ([],)

        with patch.object(bus, "request", side_effect=request), \
                patch.object(bus, "owner", side_effect=lambda name: 123 if name == ":1.42" else 999):
            nodes = bus.nodes({123})
        self.assertEqual([node.reference for node in nodes], [(":1.42", "/owned")])
        self.assertEqual(reads, [(":1.42", "/owned")])

    def test_legacy_atk_socket_exposes_its_owned_webkit_child_by_index(self):
        bus = smoke.AtspiDBus.__new__(smoke.AtspiDBus)
        bus.deadline, bus.diagnostic = float("inf"), {}
        calls = []
        socket, document = (":1.42", "/socket"), (":1.43", "/webkit/document")

        def request(reference, interface, method, signature=None, arguments=()):
            calls.append((reference, method, signature, arguments))
            if reference[0] == "org.a11y.atspi.Registry":
                return ([socket],)
            if method == "GetChildren":
                return ([],)  # The old bridge omits embedded AtkSocket children.
            if interface == "org.freedesktop.DBus.Properties" and method == "Get":
                self.assertEqual(arguments, (smoke.AtspiDBus.ACCESSIBLE, "ChildCount"))
                return (1 if reference == socket else 0,)
            if reference == socket and method == "GetChildAtIndex":
                self.assertEqual((signature, arguments), ("(i)", (0,)))
                return (document,)
            self.fail("Unexpected native provider read")

        with patch.object(bus, "request", side_effect=request), \
                patch.object(bus, "owner", side_effect=lambda name: 123 if name == ":1.42" else 456):
            nodes = bus.nodes({123, 456})
        self.assertEqual([node.reference for node in nodes], [socket, document])
        self.assertIs(nodes[1].parent, nodes[0])
        self.assertEqual(bus.diagnostic["indexed_child_reads"], 1)
        self.assertEqual(len(calls), 6, "the fallback must stop at the advertised child count")

    def test_dbus_state_words_preserve_high_bit_states(self):
        node = smoke.DBusAccessible(None, (":1.42", "/root"))
        node.values["state"] = [1 << 25, 1 << 3]
        self.assertTrue(node.contains(25))
        self.assertTrue(node.contains(35))
        self.assertFalse(node.contains(7))

    def test_blocked_native_provider_is_bounded_and_cannot_report_success(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.isolated = True
        with patch.object(smoke.subprocess, "run", side_effect=subprocess.TimeoutExpired("provider", 8)) as call:
            self.assertFalse(gui.rendered(123, "工作台"))
            self.assertIsNone(gui.find(123, "访问密钥"))
            self.assertEqual(call.call_args.kwargs["timeout"], 8)
            self.assertNotIn("访问密钥", " ".join(call.call_args.args[0]))

    def test_editable_lookup_skips_a_static_label_with_the_same_name(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.isolated = False
        gui.atspi = SimpleNamespace(STATE_EDITABLE=10, ROLE_SPIN_BUTTON=11)
        label = SimpleNamespace(name="本地服务端口", getRole=lambda: 1,
                                getState=lambda: SimpleNamespace(contains=lambda _state: False))
        entry = SimpleNamespace(name="本地服务端口", getRole=lambda: 2,
                                getState=lambda: SimpleNamespace(contains=lambda state: state == 10))
        with patch.object(gui, "nodes", return_value=[label, entry]), patch.object(gui, "showing", return_value=True):
            self.assertIs(gui.find(123, "本地服务端口", editable=True), entry)
            self.assertIs(gui.find(123, "本地服务端口"), label)

    def test_unsaved_editor_and_search_values_cannot_prove_a_saved_note(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.isolated = False
        gui.atspi = SimpleNamespace(ROLE_DOCUMENT_WEB=1, ROLE_DOCUMENT_FRAME=2, ROLE_PASSWORD_TEXT=3,
                                    ROLE_TEXT=4, ROLE_PARAGRAPH=5, ROLE_STATIC=6,
                                    STATE_EDITABLE=7, STATE_SHOWING=8, DESKTOP_COORDS=0)
        marker = "saved-only-after-native-publication"
        provider_reads = []

        def provider_text(text, start, end):
            provider_reads.append((start, end))
            return text

        def node(role, text="", editable=False, parent=None):
            return SimpleNamespace(name=text, parent=parent, has_children=False, getRole=lambda: role,
                                   getState=lambda: SimpleNamespace(contains=lambda state: state == 8 or state == 7 and editable),
                                   getInterfaces=lambda: ["org.a11y.atspi.Document"] if role == 1 else ["org.a11y.atspi.Text"],
                                   queryComponent=lambda: SimpleNamespace(getExtents=lambda _coords: SimpleNamespace(width=800, height=600)),
                                   queryText=lambda: SimpleNamespace(getText=lambda start, end: provider_text(text, start, end)))

        document = node(1)
        editor = node(4, marker, editable=True)
        editor_child = node(6, marker, parent=editor)
        search = node(4, marker, editable=True)
        password = node(3, marker)
        password_child = node(6, marker, parent=password)
        nodes = [document, editor, editor_child, search, password, password_child]
        with patch.object(gui, "nodes", return_value=nodes):
            self.assertFalse(gui.rendered(123, marker), "draft or search text is not persisted note evidence")
        saved_section = node(17, marker)  # A plain div can be a Text-capable SECTION.
        saved_section.name = ""
        with patch.object(gui, "nodes", return_value=nodes + [saved_section]):
            self.assertTrue(gui.rendered(123, marker), "a visible non-editable note body satisfies the assertion")
        self.assertEqual(provider_reads, [(0, -1)], "only read the saved body, through the protocol's end-of-text sentinel")

    def test_document_interface_still_requires_visible_nonzero_webview_geometry(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.isolated = False
        gui.atspi = SimpleNamespace(ROLE_DOCUMENT_WEB=1, ROLE_DOCUMENT_FRAME=2, ROLE_PASSWORD_TEXT=3,
                                    STATE_EDITABLE=7, STATE_SHOWING=8, DESKTOP_COORDS=0)
        document = SimpleNamespace(name="首次启动设置", parent=None, has_children=True, getRole=lambda: 99,
                                   getState=lambda: SimpleNamespace(contains=lambda state: state == 8))
        for width, height, advertised, expected in ((800, 600, True, True), (0, 600, True, False),
                                                    (800, 0, True, False), (800, 600, False, False)):
            with self.subTest(width=width, height=height, advertised=advertised):
                document.getInterfaces = lambda: ["org.a11y.atspi.Document"] if advertised else []
                document.queryComponent = lambda: SimpleNamespace(getExtents=lambda _coords: SimpleNamespace(width=width, height=height))
                with patch.object(gui, "nodes", return_value=[document]):
                    self.assertEqual(gui.rendered(123, "首次启动设置"), expected)

    def test_invalid_native_worker_output_is_not_a_visible_control(self):
        gui = smoke.AccessibleGUI.__new__(smoke.AccessibleGUI)
        gui.isolated = True
        for result in (SimpleNamespace(returncode=1, stdout='{}'), SimpleNamespace(returncode=0, stdout='not json')):
            with patch.object(smoke.subprocess, "run", return_value=result):
                self.assertIsNone(gui.find(123, "设置"))

    def test_failed_commands_do_not_echo_private_arguments_or_output(self):
        private = "ephemeral-credential-must-not-appear"
        with self.assertRaises(smoke.SmokeFailure) as failure:
            smoke.run([sys.executable, "-c", "import sys; print(sys.argv[1]); print(sys.argv[1],file=sys.stderr); sys.exit(7)", private])
        self.assertNotIn(private, str(failure.exception))
        self.assertIn("exited with code 7", str(failure.exception))

    def test_auth_input_diagnostics_never_return_the_entered_value(self):
        private = "synthetic-value-that-must-not-be-published"
        for entered in (private, "wrong-private-value", "\u2022" * len(private), ""):
            with self.subTest(length=len(entered)):
                evidence = smoke.redacted_input_evidence(entered, private)
                self.assertEqual(evidence["matches_expected"], entered == private)
                self.assertEqual(evidence["length"], len(entered))
                self.assertNotIn(private, json.dumps(evidence))
                self.assertNotIn("wrong-private-value", json.dumps(evidence))
                self.assertTrue(all(type(value) in (int, bool) for value in evidence.values()))
        self.assertTrue(smoke.redacted_input_evidence("\u2022" * len(private), private)["masked"])

    def test_unsupported_distribution_fails_before_any_privileged_command(self):
        with tempfile.TemporaryDirectory(prefix="omnishare-preflight-test-") as directory:
            evidence = Path(directory) / "evidence"
            arguments = ["linux-installed-smoke.py", "--deb", "unused.deb", "--appimage", "unused.AppImage",
                         "--version", "0.1.2", "--evidence", str(evidence)]
            with patch.object(sys, "argv", arguments), patch.object(smoke.os, "getuid", return_value=1000), \
                    patch.object(smoke, "os_release", return_value={"id": "debian", "version": "13"}), \
                    patch.object(smoke, "run", side_effect=AssertionError("preflight reached a command")), \
                    patch.object(sys, "stderr", io.StringIO()):
                self.assertEqual(smoke.main(), 1)
            report = json.loads((evidence / "linux-installed-smoke.json").read_text())
            self.assertIs(report["success"], False)
            self.assertIn("supported Ubuntu 22.04 or 24.04", report["error"])
            self.assertEqual(report["checks"], {})
            self.assertNotIn("cleanup_package_removed", report)


if __name__ == "__main__":
    unittest.main(verbosity=2)
