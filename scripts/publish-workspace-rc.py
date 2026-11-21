#!/usr/bin/env python3
"""Publish an immutable RC only after exact-source and native package validation."""
import json
import os
from pathlib import Path
import subprocess
import tempfile

from workspace_rc import (KINDS, NATIVE_REPORTS, REPOSITORY, candidate_version,
                          current_source, digest, read_json, require, verify_build_inputs, write_json)


class GitHub:
    def invoke(self, *args, raw=False):
        result = subprocess.run(['gh', *args], capture_output=True, text=True, timeout=600)
        if result.returncode:
            raise RuntimeError('GitHub operation failed: ' + args[0])
        return result.stdout if raw else json.loads(result.stdout)

    def get(self, path):
        return self.invoke('api', 'repos/' + REPOSITORY + '/' + path)

    def mutate(self, method, path, payload):
        with tempfile.TemporaryDirectory(prefix='omnishare-candidate-payload-') as folder:
            request = Path(folder) / 'request.json'
            write_json(request, payload)
            return self.invoke('api', '--method', method, 'repos/' + REPOSITORY + '/' + path,
                               '--input', str(request))

    def upload(self, tag, path):
        self.invoke('release', 'upload', tag, str(path), '--repo', REPOSITORY, raw=True)


def verify_remote_assets(assets, expected, *, complete):
    require(isinstance(assets, list) and len({item['name'] for item in assets}) == len(assets),
            'Remote asset names collide')
    if complete:
        require({item['name'] for item in assets} == set(expected), 'Published attachment set is incomplete or unexpected')
    for item in assets:
        require(item['name'] in expected and item.get('state') == 'uploaded'
                and (item.get('size'), item.get('digest')) == expected[item['name']],
                'Existing release asset differs; refusing overwrite: ' + item['name'])


def publish_assets(directory, version, source, notes, expected, github, title=None):
    """Resume matching drafts; never move tags or modify already-public releases."""
    tag = 'v' + version
    existing = []
    for page in range(1, 20):
        items = github.get(f'releases?per_page=100&page={page}')
        existing.extend(item for item in items if item['tag_name'] == tag)
        if len(items) < 100:
            break
    else:
        raise ValueError('Release listing exceeded its bound; no publication was attempted')
    require(len(existing) <= 1, 'Duplicate release tag')
    refs = [item for item in github.get('git/matching-refs/tags/' + tag) if item['ref'] == 'refs/tags/' + tag]
    if refs:
        require(len(refs) == 1 and refs[0]['object']['type'] == 'commit' and refs[0]['object']['sha'] == source,
                'Existing tag points to another source; refusing to move it')
    release = existing[0] if existing else None
    if release:
        require(refs and release.get('prerelease') is True, 'Existing release is not this candidate')
        assets = github.get(f"releases/{release['id']}/assets?per_page=100")
        verify_remote_assets(assets, expected, complete=release.get('draft') is False)
        if release.get('draft') is False:
            return {'release': release['html_url'], 'assets': len(assets), 'source_commit': source,
                    'verified': True, 'already_published': True}
    else:
        assets = []
    # Every local validation and existing-publication check precedes mutations.
    if not refs:
        github.mutate('POST', 'git/refs', {'ref': 'refs/tags/' + tag, 'sha': source})
    if not release:
        release = github.mutate('POST', 'releases', {
            'tag_name': tag, 'target_commitish': source,
            'name': title or 'OmniShare ' + tag + ' · 桌面与统一工作区候选版',
            'body': notes, 'draft': True, 'prerelease': True, 'make_latest': 'false',
        })
    seen = {item['name'] for item in assets}
    for name in sorted(expected):
        if name not in seen:
            github.upload(tag, directory / name)
    assets = github.get(f"releases/{release['id']}/assets?per_page=100")
    verify_remote_assets(assets, expected, complete=True)
    github.mutate('PATCH', f"releases/{release['id']}", {
        'body': notes, 'draft': False, 'prerelease': True, 'make_latest': 'false',
    })
    published = github.get(f"releases/{release['id']}")
    require(published.get('tag_name') == tag and published.get('draft') is False
            and published.get('prerelease') is True, 'Final candidate release state was not confirmed')
    verify_remote_assets(github.get(f"releases/{release['id']}/assets?per_page=100"), expected, complete=True)
    return {'release': published['html_url'], 'assets': len(assets), 'source_commit': source, 'verified': True}


def prepare_publication(root, directory, version, source, run):
    require(directory.is_dir() and not directory.is_symlink(), 'Asset directory must be a real directory')
    reports, files, browser = verify_build_inputs(directory, version, source, run)
    # Reuse the full independent report validator: every native check, feature
    # exercise, restart, runtime signature and exact package hash must agree.
    command = (
        "const {verifyInstallationEvidence}=require('./scripts/verify-installation-evidence.cjs');"
        "process.stdout.write(JSON.stringify(verifyInstallationEvidence({"
        "root:process.argv[1],version:process.argv[2],assetLayout:'workspace-candidate'})));"
    )
    result = subprocess.run(['node', '-e', command, str(directory), version], cwd=root,
                            capture_output=True, text=True, timeout=120)
    require(result.returncode == 0, 'Full native installation evidence validation failed: ' + result.stderr.strip())
    native_hashes = dict(json.loads(result.stdout))
    require(set(native_hashes) == set(NATIVE_REPORTS.values()), 'Native evidence set is incomplete')
    allowed_inputs = ({item['name'] for item in files}
                      | {'build-evidence-' + kind + '.json' for kind in KINDS}
                      | set(NATIVE_REPORTS.values()) | {'workspace-browser-results.json'})
    derived = {'build-manifest.json', 'RELEASE_NOTES.md', 'SHA256SUMS.txt'}
    entries = list(directory.iterdir())
    require(all(path.is_file() and not path.is_symlink() for path in entries)
            and {path.name for path in entries} - derived == allowed_inputs, 'Unexpected candidate asset or directory')
    manifest = {
        'schema_version': 2, 'version': version, 'tag': 'v' + version, 'source_commit': source,
        'workflow_run_id': run, 'prerelease': True, 'apple_notarized': False,
        'signed_update_feed_published': False, 'all_platform_native_acceptance_passed': False,
        'windows_and_ubuntu_native_acceptance_passed': True,
        'native_results': {kind: {'outcome': report['native_installation_outcome'],
                                 'gui_verified': report['native_gui_acceptance_passed']}
                           for kind, report in reports.items()},
        'native_evidence_sha256': native_hashes, 'browser_workspace_verification': browser, 'files': files,
    }
    write_json(directory / 'build-manifest.json', manifest)
    notes = (root / f'docs/RELEASE_NOTES_v{version}.md').read_text(encoding='utf-8')
    notes += '\n## 本次构建与验收结果\n\n'
    notes += (f'源码提交：`{source}`。构建及原生验证：[Actions {run}]'
              f'(https://github.com/{REPOSITORY}/actions/runs/{run})。\n\n')
    notes += ('- Windows：MSI 与 EXE/NSIS 实际安装、原生界面、功能、退出、重启与卸载保留数据检查通过。\n'
              '- Ubuntu：同一组 DEB / AppImage 字节在 22.04 与 24.04 分别完成安装和原生界面检查；'
              'AppImage 使用 extract-and-run，未验证 FUSE 挂载。\n'
              '- macOS：Universal 包架构、ad-hoc 完整性签名与打包后端 HTTP 检查通过；'
              '另见 macOS 构建报告中的窗口恢复检查；不代表 Apple 公证、Gatekeeper 或完整业务 GUI 验收。\n'
              '- 双设备真实 Chromium 工作区验证通过。所有附件的 SHA-256 见 `SHA256SUMS.txt`。\n'
              '- 候选包提供 GitHub Release 的 `SHA256SUMS.txt` 校验通道。应用内在线升级的实际验证范围以独立升级记录为准。\n')
    (directory / 'RELEASE_NOTES.md').write_text(notes, encoding='utf-8')
    checksum_names = sorted(allowed_inputs | {'build-manifest.json', 'RELEASE_NOTES.md'})
    (directory / 'SHA256SUMS.txt').write_text(''.join(digest(directory / name) + '  ' + name + '\n'
                                                  for name in checksum_names), encoding='utf-8')
    expected = {name: ((directory / name).stat().st_size, 'sha256:' + digest(directory / name))
                for name in checksum_names + ['SHA256SUMS.txt']}
    return notes, expected


def main():
    root = Path(__file__).resolve().parents[1]
    require(os.environ.get('GITHUB_REPOSITORY') == REPOSITORY
            and os.environ.get('GITHUB_REF') == 'refs/heads/main', 'Publication requires the trusted main workflow')
    source = current_source(root, os.environ)
    require(os.environ.get('SOURCE_COMMIT') == source, 'An explicit matching source commit is required')
    subprocess.run(['git', 'merge-base', '--is-ancestor', source, 'origin/main'], cwd=root, check=True)
    version = candidate_version(root)
    directory = root / 'rc-assets'
    notes, expected = prepare_publication(root, directory, version, source, os.environ['GITHUB_RUN_ID'])
    title = read_json(root / 'release/desktop-release.json')['name']
    require(isinstance(title, str) and title.startswith('OmniShare v' + version), 'Candidate title version mismatch')
    print(json.dumps(publish_assets(directory, version, source, notes, expected, GitHub(), title)))


if __name__ == '__main__':
    main()
