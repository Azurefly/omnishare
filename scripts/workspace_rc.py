"""Shared, read-only provenance checks for workspace release candidates."""
import hashlib
import json
import re
import subprocess

REPOSITORY = 'Azurefly/omnishare'
KINDS = ('server', 'Windows', 'Linux', 'macOS', 'Ubuntu24')
NATIVE_REPORTS = {
    'Windows': 'omnishare-windows-installation.json',
    'Linux': 'omnishare-ubuntu-22.04-installation.json',
    'Ubuntu24': 'omnishare-ubuntu-24.04-installation.json',
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    require(path.is_file() and not path.is_symlink(), 'Expected a regular file: ' + path.name)
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            value.update(block)
    return value.hexdigest()


def read_json(path):
    require(path.is_file() and not path.is_symlink() and 0 < path.stat().st_size <= 1024 * 1024,
            'Missing, unsafe or oversized JSON report: ' + path.name)
    return json.loads(path.read_text(encoding='utf-8-sig'))


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')


def candidate_version(root):
    version = (root / 'VERSION').read_text(encoding='ascii').strip()
    require(re.fullmatch(r'\d+\.\d+\.\d+-rc\.\d+', version), 'An explicit RC version is required')
    manifest = read_json(root / 'release/desktop-release.json')
    require(manifest.get('tag') == 'v' + version and manifest.get('prerelease') is True
            and manifest.get('publication_channel') == 'candidate',
            'Candidate metadata does not match VERSION')
    notes = 'docs/RELEASE_NOTES_v' + version + '.md'
    require(manifest.get('notes') == notes and (root / notes).is_file(), 'Candidate release notes are missing')
    return version


def current_source(root, env):
    source = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=root, text=True).strip()
    require(re.fullmatch(r'[a-f0-9]{40}', source), 'Invalid source commit')
    require(not env.get('SOURCE_COMMIT') or env['SOURCE_COMMIT'] == source, 'Unexpected source commit')
    require(re.fullmatch(r'[0-9]+', env.get('GITHUB_RUN_ID', '')), 'A workflow run ID is required')
    subprocess.run(['git', 'diff', '--quiet', 'HEAD', '--'], cwd=root, check=True)
    return source


def package_names(version):
    prefix = 'OmniShare-v' + version
    return {
        'server': {system + '-' + arch: prefix + '-' + system + '-' + arch + '-portable.' + extension
                   for system, extension in [('windows', 'zip'), ('linux', 'tar.gz')]
                   for arch in ('amd64', 'arm64')},
        'Windows': {'nsis': prefix + '-windows-x64-setup.exe', 'msi': prefix + '-windows-x64.msi'},
        'Linux': {'deb': prefix + '-ubuntu-amd64.deb', 'appimage': prefix + '-linux-amd64.AppImage'},
        'macOS': {'dmg': prefix + '-macos-universal.dmg'},
        'Ubuntu24': {},
    }


def verify_record(directory, record, expected_name):
    require(record.get('name') == expected_name and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', expected_name),
            'Unexpected package filename')
    path = directory / expected_name
    require(type(record.get('size')) is int and record['size'] > 0 and path.is_file()
            and not path.is_symlink() and path.stat().st_size == record['size'], 'Package size mismatch: ' + expected_name)
    require(record.get('sha256') == digest(path), 'Package SHA-256 mismatch: ' + expected_name)


def normalize_native_report(report, records, kind, version):
    """Rename report asset references only after matching original names and bytes."""
    require(report.get('schema_version') == 1 and report.get('success') is True
            and report.get('version') == version, 'Native installation did not succeed for this version')
    by_kind = {record['kind']: record for record in records}

    def bind(value, flavor):
        expected = by_kind[flavor]
        require(value.get('name') in (expected['source_name'], expected['name'])
                and value.get('sha256') == expected['sha256'], 'Native evidence is not bound to the staged package')
        if 'size' in value:
            require(type(value['size']) is int and value['size'] == expected['size'], 'Native evidence size mismatch')
        value['name'] = expected['name']

    if kind == 'Windows':
        require(report.get('platform') == 'windows', 'Wrong Windows report platform')
        for item in report.get('installers', []):
            bind(item['asset'], item['kind'])
        for item in report.get('webview_runtime', {}).get('artifacts', []):
            bind(item, item['kind'])
    else:
        os_version = '22.04' if kind == 'Linux' else '24.04'
        require(report.get('platform', {}).get('id') == 'ubuntu'
                and report['platform'].get('version') == os_version, 'Wrong Ubuntu report platform')
        for flavor, item in report.get('artifacts', {}).items():
            bind(item, flavor)
    return report


def verify_build_inputs(directory, version, source, run):
    """Validate every package and evidence source before any public mutation."""
    reports = {}
    files = []
    names = package_names(version)
    for kind in KINDS:
        report = read_json(directory / ('build-evidence-' + kind + '.json'))
        require(report.get('schema_version') == 2 and report.get('platform') == kind,
                'Unexpected build evidence schema or platform')
        require(report.get('source_commit') == source and report.get('version') == version
                and str(report.get('workflow_run_id')) == str(run), 'Mixed build sources or workflow runs')
        require(report.get('build_succeeded') is True, 'An application build did not succeed')
        records = report.get('files', [])
        require(len(records) == len(names[kind]) and {r.get('kind') for r in records} == set(names[kind]),
                'Incomplete package set for ' + kind)
        for record in records:
            verify_record(directory, record, names[kind][record['kind']])
            files.append(record)
        if kind in NATIVE_REPORTS:
            require(report.get('native_installation_outcome') == 'success'
                    and report.get('native_gui_acceptance_passed') is True, 'Native acceptance failed: ' + kind)
            native = directory / NATIVE_REPORTS[kind]
            require(report.get('native_report_sha256') == digest(native), 'Native evidence was changed after staging')
        elif kind == 'macOS':
            require(report.get('native_installation_outcome') == 'success'
                    and report.get('native_gui_acceptance_passed') is False
                    and report.get('apple_notarized') is False, 'macOS candidate verification scope is invalid')
        if kind == 'macOS' and tuple(map(int, re.findall(r'\d+', version))) >= (0, 2, 0, 4):
            lifecycle = report.get('window_lifecycle', {})
            require(lifecycle.get('success') is True and lifecycle.get('source_commit') == source
                    and lifecycle.get('scope') == 'packaged-macos-window-lifecycle'
                    and all(lifecycle.get('checks', {}).get(check) is True for check in
                            ('packaged_icon_matches', 'application_hide_reopen_three_times',
                             'same_window_reused', 'backend_pid_preserved', 'normal_quit_stops_backend')),
                    'macOS native window recovery evidence is incomplete')
        reports[kind] = report
    require(reports['Ubuntu24'].get('tested_files') == reports['Linux']['files'],
            'Ubuntu 24.04 did not test the same Linux release packages')
    require(len(files) == 9 and len({r['name'].lower() for r in files}) == 9, 'Package names collide')
    browser = read_json(directory / 'workspace-browser-results.json')
    require(browser.get('source_commit') == source and str(browser.get('workflow_run_id')) == str(run),
            'Browser evidence came from a different source or run')
    require(browser.get('success') is True and browser.get('scope') == 'two real SQLite backend processes and Chromium'
            and isinstance(browser.get('checks'), list) and len(set(browser['checks'])) >= 6,
            'Real two-device browser verification did not complete')
    return reports, files, browser
