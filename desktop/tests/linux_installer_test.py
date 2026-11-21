#!/usr/bin/env python3
"""Exercise the portable Linux launchers through the shell and GIO parser."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import unittest


INSTALLER = Path(__file__).resolve().parents[2] / "packaging/linux/install.sh"


class LinuxInstallerTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="omnishare-linux-installer-")
        self.root = Path(self.directory.name)
        self.payload = self.root / "payload"
        self.payload.mkdir()
        shutil.copyfile(INSTALLER, self.payload / "install.sh")
        (self.payload / "omnishare").write_text(
            "#!" + sys.executable + "\n"
            "import json, os, pathlib, sys\n"
            "pathlib.Path(os.environ['OMNISHARE_TEST_CAPTURE']).write_text(json.dumps(sys.argv[1:]))\n"
        )
        self.capture = self.root / "arguments.json"

    def tearDown(self):
        self.directory.cleanup()

    def install(self, component):
        home = self.root / component
        data_home = home / "data 'quoted\" \\ %f $value `pwd`"
        environment = dict(
            os.environ,
            HOME=str(home),
            XDG_DATA_HOME=str(data_home),
            XDG_STATE_HOME=str(home / "state"),
            OMNISHARE_NO_START="1",
            OMNISHARE_TEST_CAPTURE=str(self.capture),
        )
        result = subprocess.run(
            ["sh", str(self.payload / "install.sh")],
            cwd=self.root, env=environment, capture_output=True, text=True, timeout=10,
        )
        return home, data_home, environment, result

    def captured_arguments(self):
        deadline = time.monotonic() + 5
        while not self.capture.exists() and time.monotonic() < deadline:
            time.sleep(0.02)
        self.assertTrue(self.capture.is_file(), "launcher did not execute the installed program")
        return json.loads(self.capture.read_text())

    def test_shell_launcher_preserves_paths_and_arguments_without_expansion(self):
        home, data_home, environment, result = self.install("home 'quote\" \\ 100% $HOME `touch injected`")
        self.assertEqual(result.returncode, 0, result.stderr)
        arguments = ["--example", "a b", "'\"\\%f$HOME`pwd`"]
        subprocess.run([str(home / ".local/bin/omnishare"), *arguments],
                       cwd=self.root, env=environment, check=True, timeout=10)
        self.assertEqual(self.captured_arguments(), ["--data-dir", str(data_home / "omnishare"), *arguments])
        self.assertFalse((self.root / "injected").exists())

    def test_desktop_entry_launches_paths_with_spaces_and_reserved_characters(self):
        self.assertIsNotNone(shutil.which("gio"), "GIO is required to test the real Desktop Entry parser")
        self.assertIsNotNone(shutil.which("desktop-file-validate"))
        for index, component in enumerate(("home with spaces", "home 'quote\" \\ %f%u%F%U%c%k%i $HOME `touch injected`")):
            with self.subTest(component=component):
                self.capture.unlink(missing_ok=True)
                home, data_home, environment, result = self.install(str(index) + component)
                self.assertEqual(result.returncode, 0, result.stderr)
                entry = data_home / "applications/omnishare.desktop"
                validated = subprocess.run(["desktop-file-validate", str(entry)], capture_output=True, text=True, timeout=10)
                self.assertEqual(validated.returncode, 0, validated.stdout + validated.stderr)
                launched = subprocess.run(["gio", "launch", str(entry)], cwd=self.root,
                                          env=environment, capture_output=True, text=True, timeout=10)
                self.assertEqual(launched.returncode, 0, launched.stderr)
                self.assertEqual(self.captured_arguments(), ["--data-dir", str(data_home / "omnishare")])
                self.assertFalse((self.root / "injected").exists())

    def test_line_breaks_are_rejected_before_installing(self):
        for component in ("bad\nExec=other", "bad\rpath"):
            with self.subTest(component=component):
                home, _data_home, _environment, result = self.install(component)
                self.assertEqual(result.returncode, 2)
                self.assertIn("must not contain line breaks", result.stderr)
                self.assertFalse(home.exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
