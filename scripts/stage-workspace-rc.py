#!/usr/bin/env python3
"""Stage newly built RC packages and bind native evidence to their exact bytes."""
import argparse
import os
from pathlib import Path
import platform
import shutil

from workspace_rc import (NATIVE_REPORTS, candidate_version, current_source, digest,
                          normalize_native_report, package_names, read_json, require,
                          verify_record, write_json)


def macos_checks(root, source, run):
    lifecycle = read_json(root / 'candidate-evidence/macos-window-reopen.json')
    native = ('native_cmd_v_path', 'finder_drag_registered_shortcut', 'native_picker_cancelled_without_write',
              'native_picker_accepted_after_delay', 'picker_preserved_healthy_backend', 'selected_file_contents_verified')
    require(lifecycle.get('success') is True and lifecycle.get('source_commit') == source
            and lifecycle.get('scope') == 'packaged-macos-window-lifecycle'
            and all(lifecycle.get('checks', {}).get(check) is True for check in native),
            'Packaged macOS window and native file checks must pass')
    browser = read_json(root / 'candidate-evidence/macos-device-binding/results.json')
    required = {
        'a discovered device binds reciprocally after its displayed route closes',
        'a file move with a delayed acknowledgement completes in the UI without resubmission',
        'an encrypted backup downloaded in Chromium restores with the exact Unicode passphrase',
        'the cached workbench reopens offline and preserves the open editor when the backend returns',
    }
    require(browser.get('success') is True and browser.get('platform') == 'darwin'
            and browser.get('source_commit') == source and browser.get('workflow_run_id') == run
            and required.issubset(browser.get('checks', [])), 'Packaged macOS workspace checks must pass')
    return lifecycle, browser


def stage(root, kind, env):
    version = candidate_version(root)
    source = current_source(root, env)
    out = root / ('rc-assets-ubuntu24' if kind == 'Ubuntu24' else 'rc-assets')
    out.mkdir(exist_ok=True)
    require(not out.is_symlink(), 'Output directory must not be a symbolic link')
    if kind == 'macOS-arm64':
        require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'Expected an Apple Silicon runner')
        path = out / 'build-evidence-macOS.json'
        report = read_json(path)
        require(report['source_commit'] == source and report['workflow_run_id'] == env['GITHUB_RUN_ID']
                and report['version'] == version, 'Mixed macOS build sources')
        record, = report['files']
        verify_record(out, record, package_names(version)['macOS']['dmg'])
        lifecycle, browser = macos_checks(root, source, env['GITHUB_RUN_ID'])
        for field in ('version', 'icon_sha256', 'executable_sha256'):
            require(lifecycle[field] == report['window_lifecycle'][field], 'Apple Silicon installed a different app')
        report['apple_silicon'] = {'architecture': 'arm64', 'dmg_sha256': record['sha256'],
                                   'window_lifecycle': lifecycle, 'workspace': browser}
        write_json(path, report)
        return
    names = package_names(version)[kind]
    records = []

    def add(pattern, flavor):
        matches = [path for path in root.glob(pattern) if path.is_file() and not path.is_symlink()]
        require(len(matches) == 1, 'Expected exactly one new package for ' + pattern)
        original = matches[0]
        target = out / names[flavor]
        require(not target.exists(), 'Refusing to overwrite a staged package: ' + target.name)
        shutil.copyfile(original, target)
        records.append({'kind': flavor, 'name': target.name, 'source_name': original.name,
                        'size': target.stat().st_size, 'sha256': digest(target)})

    if kind == 'server':
        for system, extension in [('windows', 'zip'), ('linux', 'tar.gz')]:
            for arch in ('amd64', 'arm64'):
                add(f'dist/packages/OmniShare-v{version}-{system}-{arch}.{extension}', system + '-' + arch)
        browser = read_json(root / 'frontend/test-results/workspace/results.json')
        require(browser.get('success') is True, 'Browser verification did not pass')
        browser.update(source_commit=source, workflow_run_id=env['GITHUB_RUN_ID'])
        write_json(out / 'workspace-browser-results.json', browser)
    elif kind == 'Windows':
        add('desktop/src-tauri/target/release/bundle/nsis/*-setup.exe', 'nsis')
        add('desktop/src-tauri/target/release/bundle/msi/*.msi', 'msi')
    elif kind == 'Linux':
        add('desktop/src-tauri/target/release/bundle/deb/*.deb', 'deb')
        add('desktop/src-tauri/target/release/bundle/appimage/*.AppImage', 'appimage')
    elif kind == 'macOS':
        add('macos-rc/*.dmg', 'dmg')
    outcome = env.get('NATIVE_OUTCOME', 'not_run')
    evidence = {
        'schema_version': 2, 'version': version, 'source_commit': source,
        'workflow_run_id': env['GITHUB_RUN_ID'], 'platform': kind, 'build_succeeded': True,
        'native_installation_outcome': outcome, 'native_gui_acceptance_passed': False,
        'apple_notarized': False, 'signed_update_feed_published': False, 'files': records,
        'scope': 'RC candidate; macOS checks packaged backend HTTP and integrity, not native GUI or Apple trust',
    }
    if kind in NATIVE_REPORTS:
        require(outcome == 'success', 'A failed or unrun native check cannot stage a release candidate')
        tested = records
        if kind == 'Ubuntu24':
            linux = read_json(root / 'rc-assets/build-evidence-Linux.json')
            require(linux.get('source_commit') == source and linux.get('version') == version
                    and str(linux.get('workflow_run_id')) == env['GITHUB_RUN_ID'], 'Linux artifacts have different provenance')
            tested = linux['files']
            for record in tested:
                verify_record(root / 'rc-assets', record, package_names(version)['Linux'][record['kind']])
            evidence['tested_files'] = tested
        original = root / ('candidate-evidence/windows-install-smoke.json' if kind == 'Windows'
                           else 'ubuntu-install-evidence/linux-installed-smoke.json')
        report = normalize_native_report(read_json(original), tested, kind, version)
        native = out / NATIVE_REPORTS[kind]
        write_json(native, report)
        evidence.update(native_gui_acceptance_passed=True, native_report_source_sha256=digest(original),
                        native_report_sha256=digest(native))
    elif kind == 'macOS':
        require(outcome == 'success', 'Packaged macOS backend verification must pass')
        evidence['window_lifecycle'], evidence['device_binding'] = macos_checks(root, source, env['GITHUB_RUN_ID'])
        evidence['scope'] = 'macOS integrity, packaged backend HTTP, workspace, transfer and recovery in Chromium, native window and file input checks'
    write_json(out / ('build-evidence-' + kind + '.json'), evidence)
    print('Staged ' + kind + ' from ' + source + ': ' + str(len(records)) + ' packages')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('platform', choices=('server', 'Windows', 'Linux', 'macOS', 'macOS-arm64', 'Ubuntu24'))
    args = parser.parse_args()
    stage(Path(__file__).resolve().parents[1], args.platform, os.environ)


if __name__ == '__main__':
    main()
