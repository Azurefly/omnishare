#!/usr/bin/env python3
"""Fast bridge/diagnostic regressions; packaged macOS acceptance stays separate."""
import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('macos_window_reopen', Path(__file__).with_name('macos-window-reopen.py'))
PROBE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PROBE)


class WindowProbeTests(unittest.TestCase):
    def observe(self, entries, hidden=False):
        fixture = r'''
const vm = require('node:vm');
const fixture = JSON.parse(process.argv[1]);
const opaque = {kind: 'CFArrayRef'};
const bridge = {
  import() {},
  castRefToObject(ref) { return ref.dictionary; },
  deepUnwrap(value) {
    if (value === opaque) throw new Error('CFArray pointer must not be deepUnwrapped');
    return value;
  },
};
const api = {
  NSRunningApplication: {runningApplicationWithProcessIdentifier(pid) {
    if (pid !== 1234) throw new Error('Wrong application identity');
    return {isHidden: fixture.hidden, isActive: !fixture.hidden, isFinishedLaunching: true};
  }},
  CGWindowListCopyWindowInfo() { return opaque; },
  CFArrayGetCount(ref) { if(ref !== opaque) throw new Error('Wrong CFArray'); return fixture.entries.length; },
  CFArrayGetValueAtIndex(ref, index) { if(ref !== opaque) throw new Error('Wrong CFArray'); return {dictionary: fixture.entries[index]}; },
};
process.stdout.write(vm.runInNewContext(fixture.script, {ObjC: bridge, $: api}));
'''
        result = subprocess.run(['node', '-e', fixture, json.dumps({
            'script': PROBE.observation_script(1234), 'entries': entries, 'hidden': hidden,
        })], capture_output=True, text=True, timeout=10, check=True)
        return json.loads(result.stdout)

    @staticmethod
    def window(number=42, pid=1234, layer=0, width=900, height=600):
        return {'kCGWindowNumber': number, 'kCGWindowOwnerPID': pid,
                'kCGWindowLayer': layer, 'kCGWindowBounds': {'Width': width, 'Height': height}}

    def test_opaque_cfarray_entries_are_unwrapped_individually(self):
        self.assertEqual(self.observe([self.window()]), {'hidden': False, 'active': True, 'finishedLaunching': True, 'windows': [42]})

    def test_only_owned_normal_sized_windows_are_selected(self):
        rows = [self.window(1, pid=999), self.window(2, layer=3),
                self.window(3, width=40), self.window(4, height=20), self.window(5)]
        self.assertEqual(self.observe(rows)['windows'], [5])

    def test_hidden_application_has_no_visible_windows(self):
        self.assertEqual(self.observe([], hidden=True), {'hidden': True, 'active': False, 'finishedLaunching': True, 'windows': []})

    def test_missing_bounds_do_not_create_a_false_main_window(self):
        self.assertEqual(self.observe([{'kCGWindowOwnerPID': 1234, 'kCGWindowLayer': 0}])['windows'], [])

    def test_pid_must_be_a_positive_integer(self):
        for pid in (None, True, 0, -1, 1.5, '1234; throw Error()'):
            with self.subTest(pid=pid), self.assertRaises(PROBE.Failure):
                PROBE.observation_script(pid)

    def test_command_keeps_exit_code_and_stderr(self):
        result = subprocess.CompletedProcess(['osascript'], 1, '', 'TypeError: w.filter is not a function\n')
        with patch.object(PROBE.subprocess, 'run', return_value=result):
            with self.assertRaisesRegex(PROBE.Failure, 'osascript exited 1: TypeError: w.filter'):
                PROBE.command(['/usr/bin/osascript'])

    def test_command_timeout_is_actionable(self):
        with patch.object(PROBE.subprocess, 'run', side_effect=subprocess.TimeoutExpired('osascript', 3)):
            with self.assertRaisesRegex(PROBE.Failure, 'osascript timed out after 3s'):
                PROBE.command(['/usr/bin/osascript'], timeout=3)

    def test_success_returns_clean_stdout(self):
        with patch.object(PROBE.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, ' true\n', '')):
            self.assertEqual(PROBE.command(['/usr/bin/osascript']), 'true')

    def test_wait_does_not_swallow_real_observation_errors(self):
        def failed_observation():
            raise PROBE.Failure('bridge failed')
        with self.assertRaisesRegex(PROBE.Failure, 'bridge failed'):
            PROBE.wait_for(failed_observation, 'no window', seconds=1)

    def test_probe_refuses_non_macos_and_records_failure_stage(self):
        with tempfile.TemporaryDirectory() as folder:
            report = Path(folder) / 'report.json'
            with patch.object(sys, 'argv', ['probe', '--app', folder, '--report', str(report)]), \
                 patch.object(PROBE.sys, 'platform', 'linux'), \
                 patch.object(PROBE.subprocess, 'Popen') as start, \
                 contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(PROBE.main(), 1)
                start.assert_not_called()
            evidence = json.loads(report.read_text())
            self.assertFalse(evidence['success'])
            self.assertEqual(evidence['stage'], 'package-integrity')
            self.assertFalse(evidence['close_button_tested'])


if __name__ == '__main__':
    unittest.main()
