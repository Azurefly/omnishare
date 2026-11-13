#!/usr/bin/env python3
"""Publish a byte-pinned LAN preview, never a stable or notarized release."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import zipfile

REPO = 'Azurefly/omnishare'
SOURCE = '4e9e809bd5a70e4bbaa4c6bc71adfab05cc56dde'
TAG = 'v0.1.1-lan.1'
TAG_COMMIT = '0892507dd541d2b903beddcf026d789f1360d5d5'
DOC_BRANCH = 'docs/lan-preview-20261112'
NOTES = Path('docs/RELEASE_NOTES_v0.1.1-lan.1.md')


def command(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def api(path, optional=False):
    result = subprocess.run(['gh', 'api', f'repos/{REPO}/{path}'], capture_output=True, text=True)
    if result.returncode:
        if optional and 'HTTP 404' in result.stderr:
            return None
        raise RuntimeError(f'GitHub API request failed: {path}')
    return json.loads(result.stdout)


def find_release():
    # GitHub's by-tag endpoint excludes drafts. List authenticated releases
    # and match exactly; paginate instead of interpreting 404 as absence.
    found = []
    for page in range(1, 101):
        batch = api(f'releases?per_page=100&page={page}')
        found.extend(r for r in batch if r['tag_name'] == TAG)
        if len(batch) < 100:
            if len(found) > 1:
                raise RuntimeError('Multiple releases match this preview tag')
            return found[0] if found else None
    raise RuntimeError('Release inventory exceeded its safety limit')


def digest(path):
    with Path(path).open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def stage(manifest, archives, output, notes, head):
    output.mkdir(parents=True, exist_ok=False)
    for item in manifest['files']:
        target = output / item['file']
        if target.name != item['file']:
            raise RuntimeError('Unsafe asset name')
        with zipfile.ZipFile(archives[item['artifact_id']]) as archive:
            matches = [info for info in archive.infolist() if info.filename == item['member']]
            if len(matches) != 1:
                raise RuntimeError('Archive member missing or duplicated')
            info = matches[0]
            mode = stat.S_IFMT(info.external_attr >> 16)
            if mode not in (0, stat.S_IFREG) or info.file_size != item['size_bytes']:
                raise RuntimeError('Invalid archive member type or size')
            with archive.open(info) as source, target.open('xb') as destination:
                shutil.copyfileobj(source, destination, 128 * 1024)
        if target.stat().st_size != item['size_bytes'] or digest(target) != item['sha256']:
            raise RuntimeError(f'Package hash mismatch: {target.name}')
    public = dict(manifest)
    public.pop('official_release', None)
    public.update(distribution='github-prerelease', stable_release=False, release_commit=head)
    (output / 'build-manifest.json').write_text(json.dumps(public, ensure_ascii=False, indent=2) + '\n')
    shutil.copyfile(notes, output / 'RELEASE_NOTES.md')
    records = {p.name: {'size': p.stat().st_size, 'sha256': digest(p)} for p in sorted(output.iterdir())}
    (output / 'SHA256SUMS.txt').write_text(''.join(f"{v['sha256']}  {k}\n" for k, v in records.items()))
    check = output / 'SHA256SUMS.txt'
    records[check.name] = {'size': check.stat().st_size, 'sha256': digest(check)}
    if len(records) != 12:
        raise RuntimeError('Expected 9 packages and 3 supporting files')
    return records


def verify_assets(release_id, expected, complete):
    assets = api(f'releases/{release_id}/assets?per_page=100')
    seen = set()
    for asset in assets:
        name = asset['name']
        if name not in expected or name in seen:
            raise RuntimeError('Unexpected or duplicate release attachment')
        wanted = expected[name]
        if asset['state'] != 'uploaded' or asset['size'] != wanted['size'] or asset.get('digest') != 'sha256:' + wanted['sha256']:
            raise RuntimeError(f'Uploaded asset is different: {name}')
        seen.add(name)
    if complete and seen != set(expected):
        raise RuntimeError('Release attachments are incomplete')
    return seen


def prepare_main_docs(notes):
    command(['git', 'fetch', 'origin', 'main'])
    exists = subprocess.check_output(['git', 'ls-remote', '--heads', 'origin', f'refs/heads/{DOC_BRANCH}'], text=True).strip()
    if exists:
        print(f'Documentation branch already exists; not overwriting: {DOC_BRANCH}')
        return
    with tempfile.TemporaryDirectory(prefix='omnishare-docs-') as temp:
        work = Path(temp) / 'repo'
        command(['git', 'worktree', 'add', '-b', DOC_BRANCH, str(work), 'origin/main'])
        try:
            readme = work / 'README.md'
            text = readme.read_text()
            anchor = '## 当前版本：0.1.1'
            if text.count(anchor) != 1:
                raise RuntimeError('README version heading changed; refusing a blind edit')
            notice = '''## 局域网预览版：v0.1.1-lan.1

**[下载局域网预览版](https://github.com/Azurefly/omnishare/releases/tag/v0.1.1-lan.1)** · [完整说明与已知问题](docs/RELEASE_NOTES_v0.1.1-lan.1.md)

本版包含默认局域网、可选访问密钥、取消初始化 Token，以及多网卡自动发现。新安装免密直接使用，已有安装保留原配置；开启密钥后可自行使用任意非空内容，不要求最小长度或字符组合。不要将免密服务映射到公网。

| 系统 | 预览版安装包 |
| --- | --- |
| Windows x64 | EXE / MSI，两种任选其一 |
| Ubuntu x64 | DEB；也提供 Linux x64 AppImage |
| macOS 12+，Intel / Apple 芯片 | Universal DMG，未完成 Apple 公证，仍可能被阻止打开 |
| Windows / Linux x64、ARM64 | 后台服务＋浏览器便携包，不是原生桌面窗口 |

**预发布测试包，不代表三平台安装验收通过。** Windows 完整运行验收、Ubuntu 桌面安装验收和 macOS 正式签名/公证仍未完成。发布页提供校验和及构建来源清单。程序内部仍显示 `0.1.1`，附件名包含 `4e9e809`；同版本覆盖安装前先备份，必要时卸载旧应用但保留数据。

局域网功能源码仍在 PR #33，尚未合并 main。**下面的首次配置与网络说明对应现有 main / 旧正式版；使用预览包请按上面的完整说明操作，不需要 init Token。**

## 现有正式版：0.1.1'''
            readme.write_text(text.replace(anchor, notice, 1))
            destination = work / NOTES
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(notes.read_bytes())
            changelog = work / 'CHANGELOG.md'
            history = changelog.read_text()
            anchor = '## 0.1.1 — 2026-11-08'
            if history.count(anchor) != 1:
                raise RuntimeError('CHANGELOG release boundary changed')
            entry = '''## 0.1.1-lan.1（局域网预览版）— 2026-11-12

- 单独发布源码 `4e9e809` 的测试构建，不覆盖旧 `v0.1.1`，不标为 Latest；程序内部版本保持 `0.1.1`。
- 新安装默认局域网、默认关闭密钥保护；密钥为独立开关，不限制最小长度或字符组合；取消 init Token。
- 多网卡自动发现、网络变化后重试和正确的 HTTP/HTTPS 设备地址；升级保留已有设置。
- 提供 Windows EXE/MSI、Ubuntu DEB、Linux AppImage、macOS Universal DMG 和 Windows/Linux 便携包。
- 未完成三平台安装验收或 Apple 公证；保留失败和未测范围，发布源码、文件摘要与构建来源。功能仍位于 PR #33，不表示已合入 main。

详见 [局域网预览版说明](docs/RELEASE_NOTES_v0.1.1-lan.1.md)。

'''
            changelog.write_text(history.replace(anchor, entry + anchor, 1))
            command(['git', '-C', str(work), 'add', 'README.md', 'CHANGELOG.md', str(NOTES)])
            command(['git', '-C', str(work), 'diff', '--cached', '--check'])
            command(['git', '-C', str(work), '-c', 'user.name=github-actions[bot]', '-c', 'user.email=41898282+github-actions[bot]@users.noreply.github.com', 'commit', '-m', 'docs: publish LAN preview download guide and verified support boundaries'])
            command(['git', '-C', str(work), 'push', 'origin', f'HEAD:refs/heads/{DOC_BRANCH}'])
        finally:
            command(['git', 'worktree', 'remove', '--force', str(work)])


def main():
    if os.environ.get('GITHUB_REPOSITORY') != REPO or os.environ.get('GITHUB_REF') != 'refs/heads/fix/cross-platform-install-20261111':
        raise RuntimeError('This publisher is scoped to the reviewed repository and branch')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    if head != os.environ['GITHUB_SHA']:
        raise RuntimeError('Checkout differs from the triggering commit')
    command(['git', 'merge-base', '--is-ancestor', SOURCE, TAG_COMMIT])
    command(['git', 'merge-base', '--is-ancestor', TAG_COMMIT, head])
    command(['git', 'diff', '--exit-code', TAG_COMMIT, head, '--', str(NOTES), 'release/lan-preview-4e9e809.json'])
    command(['git', 'diff', '--exit-code', SOURCE, head, '--', 'backend', 'frontend', 'desktop', 'packaging', 'scripts', 'VERSION', 'release/desktop-release.json'])
    ci = api('actions/runs/43173998528')
    if ci['head_sha'] != SOURCE or ci['conclusion'] != 'success':
        raise RuntimeError('The pinned source CI did not pass')
    manifest = json.loads(Path('release/lan-preview-4e9e809.json').read_text())
    if manifest['source_commit'] != SOURCE or manifest['tag'] != TAG:
        raise RuntimeError('Unexpected preview manifest')
    with tempfile.TemporaryDirectory(prefix='omnishare-preview-') as temp:
        root = Path(temp)
        archives = {}
        for record in manifest['artifacts']:
            metadata = api(f"actions/artifacts/{record['id']}")
            run = metadata['workflow_run']
            if metadata['expired'] or metadata['name'] != record['name'] or run['id'] != record['run'] or run['head_sha'] != record['sha'] or metadata.get('digest') != 'sha256:' + record['digest']:
                raise RuntimeError('Artifact provenance changed')
            archive = root / f"{record['id']}.zip"
            with archive.open('xb') as stream:
                command(['gh', 'api', f"repos/{REPO}/actions/artifacts/{record['id']}/zip"], stdout=stream)
            if archive.stat().st_size != metadata['size_in_bytes'] or digest(archive) != record['digest']:
                raise RuntimeError('Artifact transport checksum mismatch')
            archives[record['id']] = archive
        output = root / 'assets'
        expected = stage(manifest, archives, output, NOTES, TAG_COMMIT)
        tag = api(f'git/ref/tags/{TAG}', optional=True)
        if tag is None or tag['object']['type'] != 'commit' or tag['object']['sha'] != TAG_COMMIT:
            raise RuntimeError('Reviewed tag is missing or changed; refusing to move it')
        release = find_release()
        if release is None:
            command(['gh', 'release', 'create', TAG, '--repo', REPO, '--target', TAG_COMMIT, '--verify-tag', '--draft', '--prerelease', '--latest=false', '--title', 'OmniShare v0.1.1-lan.1 · 局域网预览版', '--notes-file', str(NOTES)])
            release = find_release()
            if release is None:
                raise RuntimeError('Created draft was not found')
        tag = api(f'git/ref/tags/{TAG}')
        if tag['object']['type'] != 'commit' or tag['object']['sha'] != TAG_COMMIT or not release['prerelease'] or release['body'].strip() != NOTES.read_text().strip():
            raise RuntimeError('Existing release/tag differs; refusing to replace it')
        existing = verify_assets(release['id'], expected, complete=not release['draft'])
        if release['draft']:
            missing = [str(output / name) for name in expected if name not in existing]
            if missing:
                command(['gh', 'release', 'upload', TAG, '--repo', REPO, *missing])
            verify_assets(release['id'], expected, complete=True)
            command(['gh', 'release', 'edit', TAG, '--repo', REPO, '--draft=false', '--prerelease', '--latest=false'])
        final = api(f'releases/tags/{TAG}')
        if final['draft'] or not final['prerelease']:
            raise RuntimeError('Preview publication state is incorrect')
        verify_assets(final['id'], expected, complete=True)
        print(json.dumps({'release': final['html_url'], 'assets': len(expected), 'source': SOURCE, 'tag_commit': TAG_COMMIT, 'prerelease': True}))
    prepare_main_docs(NOTES)
    with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as summary:
        summary.write(f'Published [{TAG}]({final["html_url"]}) with 12 byte-verified assets. Stable release unchanged.\n\nDocumentation branch: `{DOC_BRANCH}` (docs-only; ready for PR).\n')


if __name__ == '__main__':
    main()
