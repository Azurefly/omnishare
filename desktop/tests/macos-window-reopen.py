#!/usr/bin/env python3
"""Exercise a packaged macOS app, with explicit limits on GUI evidence.

AppKit hide/reopen and visible-window identity are mandatory. The actual close
button is exercised only when the runner already has Accessibility permission;
this script never grants permissions or alters TCC/Gatekeeper policy.
"""
import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import plistlib
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


class Failure(Exception):
    pass


def require(value, message):
    if not value:
        raise Failure(message)


def command(args, timeout=10):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    except subprocess.TimeoutExpired as error:
        raise Failure(f'{Path(args[0]).name} timed out after {timeout}s') from error
    require(result.returncode == 0,
            f'{Path(args[0]).name} exited {result.returncode}: {result.stderr.strip()[:4096]}')
    return result.stdout.strip()


def jxa(script):
    return command(['/usr/bin/osascript', '-l', 'JavaScript', '-e', script])


def observation_script(pid):
    require(isinstance(pid, int) and not isinstance(pid, bool) and pid > 0,
            'Invalid native process ID')
    # CGWindowListCopyWindowInfo returns a CFArrayRef, not a JS array. Read its
    # CFDictionary entries explicitly before deepUnwrap; do not call filter on
    # the unconverted CoreFoundation pointer.
    return """
ObjC.import('AppKit');
ObjC.import('CoreGraphics');
ObjC.import('CoreFoundation');
(function () {
    const pid = __PID__;
    const app = $.NSRunningApplication.runningApplicationWithProcessIdentifier(pid);
    const list = $.CGWindowListCopyWindowInfo(1, 0);
    const windows = [];
    const count = Number($.CFArrayGetCount(list));
    for (let index = 0; index < count; index++) {
        const entry = ObjC.castRefToObject($.CFArrayGetValueAtIndex(list, index));
        const item = ObjC.deepUnwrap(entry);
        const bounds = item.kCGWindowBounds || {};
        if (Number(item.kCGWindowOwnerPID) === pid && Number(item.kCGWindowLayer) === 0 &&
            Number(bounds.Width) >= 400 && Number(bounds.Height) >= 300) {
            windows.push(Number(item.kCGWindowNumber));
        }
    }
    return JSON.stringify({hidden: Boolean(app.isHidden), active: Boolean(app.isActive),
        finishedLaunching: Boolean(app.isFinishedLaunching), windows: windows});
})();
""".replace('__PID__', str(pid))


def wait_for(predicate, message, seconds=25):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.2)
    raise Failure(message)


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as handle:
        for block in iter(lambda: handle.read(1024*1024), b''):
            h.update(block)
    return h.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--app', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    parser.add_argument('--file-inputs', action='store_true', help='Require native file selection, cancellation, Finder drop and Cmd+V')
    parser.add_argument('--build-evidence', type=Path, help='Verify an installed DMG against the original build runner report')
    args = parser.parse_args()
    report = {'schema_version': 1, 'success': False, 'scope': 'packaged-macos-window-lifecycle',
              'checks': {}, 'close_button_tested': False, 'dock_pointer_click_tested': False,
              'apple_notarized': False, 'source_commit': os.environ.get('SOURCE_COMMIT', os.environ.get('GITHUB_SHA', '')),
              'not_tested': ['physical Dock pointer click', 'unsaved editor content', 'Gatekeeper first download']}
    process = None
    report['stage'] = 'package-integrity'
    try:
        require(sys.platform == 'darwin' and os.environ.get('GITHUB_ACTIONS') == 'true',
                'Run this installation probe only on a disposable macOS Actions runner')
        app = args.app.resolve(strict=True)
        info = plistlib.loads((app/'Contents/Info.plist').read_bytes())
        binary = app/'Contents/MacOS'/info['CFBundleExecutable']
        require(binary.is_file() and not binary.is_symlink(), 'Packaged executable is missing')
        icon = app/'Contents/Resources'/info['CFBundleIconFile']
        if not icon.suffix:
            icon = icon.with_suffix('.icns')
        require(icon.is_file(), 'Packaged ICNS is missing')
        report.update(version=info['CFBundleShortVersionString'], executable_sha256=digest(binary), icon_sha256=digest(icon))
        if args.build_evidence:
            built = json.loads(args.build_evidence.read_text())
            require(built.get('success') is True and built.get('checks', {}).get('packaged_icon_matches') is True,
                    'Original build did not verify its generated icon')
            for field in ('source_commit', 'version', 'executable_sha256', 'icon_sha256'):
                require(report[field] == built.get(field), f'Installed package differs from original build: {field}')
        else:
            generated = Path(__file__).resolve().parents[1]/'src-tauri/icons/icon.icns'
            require(report['icon_sha256'] == digest(generated), 'Packaged icon differs from approved generated icon')
        report['checks']['packaged_icon_matches'] = True
        report['stage'] = 'existing-application-check'
        existing = json.loads(jxa("ObjC.import('AppKit'); const a=$.NSRunningApplication.runningApplicationsWithBundleIdentifier(" + json.dumps(info['CFBundleIdentifier']) + "); JSON.stringify(Number(a.count));"))
        require(existing == 0, 'Refusing to interfere with an existing OmniShare application')
        libproc = ctypes.CDLL('/usr/lib/libproc.dylib')
        libproc.proc_pidpath.argtypes = [ctypes.c_int, ctypes.c_void_p, ctypes.c_uint32]
        libproc.proc_pidpath.restype = ctypes.c_int

        def executable(pid):
            buf = ctypes.create_string_buffer(4096)
            return Path(os.fsdecode(buf.value)).resolve() if libproc.proc_pidpath(pid, buf, len(buf)) > 0 else None

        with tempfile.TemporaryDirectory(prefix='omnishare-reopen-') as tmp:
            private = Path(tmp); data = private/'data'; data.mkdir()
            home = private/'home'; home.mkdir()
            with socket.socket() as listener:
                listener.bind(('127.0.0.1',0)); port = listener.getsockname()[1]
            env = dict(os.environ, HOME=str(home), OMNISHARE_DATA_DIR=str(data),
                       OMNISHARE_PORT=str(port), OMNISHARE_DESKTOP_LOG_DIR=str(private/'logs'))
            env.pop('OMNISHARE_BACKEND_BIN',None)
            with (private/'native.log').open('wb') as log:
                report['stage'] = 'native-window-startup'
                process = subprocess.Popen([str(binary)], env=env, stdin=subprocess.DEVNULL, stdout=log, stderr=log)
                pid = process.pid
                def observation():
                    require(process.poll() is None and executable(pid) == binary, 'Owned application exited or changed')
                    return json.loads(jxa(observation_script(pid)))
                wait_for(lambda: (state := observation())['finishedLaunching'] and len(state['windows']) == 1,
                         'Native application did not finish launching with one main window')
                original = observation()['windows'][0]
                report['stage'] = 'bundled-backend-startup'
                wait_for(lambda: (data/'.omnishare.lock').is_file() and (data/'.omnishare.lock').read_text().strip().startswith('pid='), 'Bundled backend did not start')
                def backend_pid():
                    text = (data/'.omnishare.lock').read_text().strip()
                    require(text.startswith('pid='), 'Missing backend process identity')
                    return int(text[4:])
                child = backend_pid()
                child_binary = executable(child)
                require(child_binary and child_binary.is_relative_to(app) and
                        int(command(['/bin/ps','-p',str(child),'-o','ppid='])) == pid,
                        'Backend is not owned by the packaged application')
                def alive():
                    require(backend_pid() == child and executable(child) == child_binary and
                            int(command(['/bin/ps','-p',str(child),'-o','ppid='])) == pid,
                            'Window recovery restarted or lost its bundled backend')
                def reopen():
                    command(['/usr/bin/open', str(app)])
                    wait_for(lambda: observation()['windows'] == [original] and not observation()['hidden'],
                             'LaunchServices Reopen did not restore the same main window')
                    alive()
                # AppKit requests are asynchronous. Only observed window state,
                # original window identity and backend ownership decide success.
                report['stage'] = 'application-hide-reopen'
                for _ in range(3):
                    jxa("ObjC.import('AppKit'); const a=$.NSRunningApplication.runningApplicationWithProcessIdentifier("+str(pid)+"); a.activateWithOptions(2); 'requested';")
                    wait_for(lambda: observation()['active'], 'Owned application did not activate before hide')
                    jxa("ObjC.import('AppKit'); const a=$.NSRunningApplication.runningApplicationWithProcessIdentifier("+str(pid)+"); const accepted=a.hide; $.NSRunLoop.currentRunLoop.runUntilDate($.NSDate.dateWithTimeIntervalSinceNow(0.3)); JSON.stringify({accepted:Boolean(accepted),hidden:Boolean(a.isHidden)});")
                    wait_for(lambda: observation()['hidden'] and not observation()['windows'], 'AppKit hide did not hide the main window')
                    reopen()
                report['checks'].update(application_hide_reopen_three_times=True, same_window_reused=True, backend_pid_preserved=True)
                report['stage'] = 'close-button-reopen'
                trusted = jxa("ObjC.import('ApplicationServices'); Boolean($.AXIsProcessTrusted());") == 'true'
                if trusted:
                    for _ in range(3):
                        command(['/usr/bin/osascript','-e',f'tell application "System Events" to tell (first application process whose unix id is {pid}) to click (first button of window 1 whose subrole is "AXCloseButton")'])
                        wait_for(lambda: not observation()['windows'], 'Close button did not hide the window')
                        reopen()
                    report['close_button_tested'] = True
                    report['checks']['close_button_reopen_three_times'] = True
                else:
                    report['not_tested'].append('close button: runner has no pre-granted Accessibility permission')
                if args.file_inputs:
                    report['stage'] = 'native-file-inputs'
                    require(trusted, 'Native file inputs require pre-granted Accessibility on this runner')
                    directory = private/'OmniShare Finder Test'
                    directory.mkdir()
                    fixture = directory/'访达拖入 #1.txt'
                    fixture.write_bytes(b'OmniShare native Finder drop')
                    picked = directory/'选择本机文件 #2.txt'
                    picked.write_bytes(b'OmniShare native file picker')
                    command(['/usr/bin/swift', '-suppress-warnings', str(Path(__file__).with_name('macos-file-inputs.swift')),
                             str(pid), str(fixture), str(picked), str(port)], timeout=200)
                    base = f'http://127.0.0.1:{port}'
                    client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                    def registered_files():
                        with client.open(base+'/api/v1/files', timeout=5) as response:
                            return json.load(response)['data']
                    expected = sorted([fixture.name, picked.name])
                    wait_for(lambda: sorted(f['file_name'] for f in registered_files() if f.get('source_shortcut')) == expected,
                             'Picker and Finder did not register exactly the two original files')
                    for item in registered_files():
                        with client.open(base+'/api/v1/files/'+item['id']+'/download', timeout=5) as response:
                            require(response.read() == (directory/item['file_name']).read_bytes(), 'Selected file content differs')
                    alive()
                    report['checks'].update(native_cmd_v_path=True, finder_drag_registered_shortcut=True,
                                            native_picker_cancelled_without_write=True, native_picker_accepted_after_delay=True,
                                            picker_preserved_healthy_backend=True, selected_file_contents_verified=True)
                report['stage'] = 'normal-quit'
                jxa("ObjC.import('AppKit'); const a=$.NSRunningApplication.runningApplicationWithProcessIdentifier("+str(pid)+"); a.terminate; $.NSRunLoop.currentRunLoop.runUntilDate($.NSDate.dateWithTimeIntervalSinceNow(0.3)); 'requested';")
                require(process.wait(timeout=25) == 0, 'Native normal quit did not succeed')
                wait_for(lambda: executable(child) is None, 'Bundled backend survived normal quit')
                report['checks']['normal_quit_stops_backend'] = True
                report['success'] = True
                report['stage'] = 'complete'
    except Exception as error:
        report['error'] = str(error) if isinstance(error, Failure) else f'{type(error).__name__}: {error}'
    finally:
        if process is not None and process.poll() is None:
            # Failure cleanup targets only the exact child launched by this script.
            process.terminate()
            try:
                process.wait(timeout=8)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=5)
        args.report.parent.mkdir(parents=True,exist_ok=True)
        args.report.write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n')
        print(json.dumps(report,ensure_ascii=False))
    return 0 if report['success'] else 1


if __name__ == '__main__':
    sys.exit(main())
