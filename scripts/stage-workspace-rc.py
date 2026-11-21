#!/usr/bin/env python3
"""Stage newly built RC packages and bind native evidence to their exact bytes."""
import argparse
import os
from pathlib import Path
import shutil

from workspace_rc import (NATIVE_REPORTS, candidate_version, current_source, digest,
                          normalize_native_report, package_names, read_json, require,
                          verify_record, write_json)


def stage(root, kind, env):
    version = candidate_version(root)
    source = current_source(root, env)
    out = root / ('rc-assets-ubuntu24' if kind == 'Ubuntu24' else 'rc-assets')
    out.mkdir(exist_ok=True)
    require(not out.is_symlink(), 'Output directory must not be a symbolic link')
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
        lifecycle = read_json(root / 'candidate-evidence/macos-window-reopen.json')
        require(lifecycle.get('success') is True and lifecycle.get('scope') == 'packaged-macos-window-lifecycle',
                'Packaged macOS window recovery must pass')
        require(lifecycle.get('source_commit') == source, 'macOS lifecycle evidence has a different source')
        evidence['window_lifecycle'] = lifecycle
        evidence['scope'] = 'macOS integrity, packaged backend HTTP and native window recovery; not full business GUI or Apple trust'
    write_json(out / ('build-evidence-' + kind + '.json'), evidence)
    print('Staged ' + kind + ' from ' + source + ': ' + str(len(records)) + ' packages')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('platform', choices=('server', 'Windows', 'Linux', 'macOS', 'Ubuntu24'))
    args = parser.parse_args()
    stage(Path(__file__).resolve().parents[1], args.platform, os.environ)


if __name__ == '__main__':
    main()
