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

            def find(self, *_args):
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

    def test_failed_commands_do_not_echo_private_arguments_or_output(self):
        private = "ephemeral-credential-must-not-appear"
        with self.assertRaises(smoke.SmokeFailure) as failure:
            smoke.run([sys.executable, "-c", "import sys; print(sys.argv[1]); print(sys.argv[1],file=sys.stderr); sys.exit(7)", private])
        self.assertNotIn(private, str(failure.exception))
        self.assertIn("exited with code 7", str(failure.exception))

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
