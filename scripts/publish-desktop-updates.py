#!/usr/bin/env python3
"""Publish a per-release update feed only after every signed asset is present."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import quote

ROOT = Path(__file__).resolve().parents[1]
REPO = 'Azurefly/omnishare'
spec = importlib.util.spec_from_file_location('prepare', ROOT / 'scripts/prepare-desktop-updates.py')
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)

def gh(*args):
    result = subprocess.run(['gh', *args], check=True, capture_output=True, text=True)
    return json.loads(result.stdout) if result.stdout.strip() else None

def manifest(directory, version, sha, key):
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?', version):
        raise ValueError('Invalid version')
    platforms, packages = {}, {}
    for platform in ['Windows', 'Linux', 'macOS']:
        record = json.loads((directory / f'updater-evidence-{platform}.json').read_text())
        if (record['version'] != version or record['source_commit'] != sha or
                record['public_key_sha256'] != hashlib.sha256(key.encode()).hexdigest()):
            raise ValueError('Update build sources, keys or versions differ')
        for file in record['files']:
            name = file['name']
            if Path(name).name != name or '/' in name or '\\' in name or not name.startswith(f'OmniShare-v{version}-'):
                raise ValueError('Invalid package name')
            package = directory / name
            if package.is_symlink() or not package.is_file() or not 0 < file['size'] <= 512*1024*1024:
                raise ValueError('Invalid package')
            if package.stat().st_size != file['size'] or hashlib.sha256(package.read_bytes()).hexdigest() != file['sha256']:
                raise ValueError('Package differs from build evidence')
            prepare.validate_signature(file['signature'], version, key)
            if file['target'] in platforms:
                raise ValueError('Duplicate updater target')
            platforms[file['target']] = {'url': f'https://github.com/{REPO}/releases/download/v{version}/{quote(name)}', 'signature': file['signature']}
            packages[name] = package
    required = {'windows-x86_64-nsis','windows-x86_64-msi','linux-x86_64-deb','linux-x86_64-appimage','darwin-x86_64','darwin-aarch64'}
    if set(platforms) != required:
        raise ValueError('Incomplete platform update set')
    return {'version':version, 'platforms':platforms}, packages

def main():
    if os.environ.get('GITHUB_REPOSITORY') != REPO:
        raise ValueError('Wrong repository')
    version = (ROOT/'VERSION').read_text().strip()
    sha = subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
    tag = f'v{version}'
    if os.environ.get('UPDATE_TAG') != tag:
        raise ValueError('Release tag and source version differ')
    key = (ROOT/'desktop/src-tauri/updater-public-key.txt').read_text().strip()
    feed, packages = manifest(ROOT/'signed-updates',version,sha,key)
    release = gh('api',f'repos/{REPO}/releases/tags/{tag}')
    if release['draft']:
        raise ValueError('Release has not been published')
    if subprocess.check_output(['git','rev-parse',tag+'^{commit}'],text=True).strip() != sha:
        raise ValueError('Checked out source does not match release tag')
    feed['notes'] = release.get('body') or ''
    feed['pub_date'] = release['published_at']
    out = ROOT/'signed-updates/latest.json'
    out.write_text(json.dumps(feed,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    # Never clobber an existing signed artifact, tag or update feed.
    for name, path in [*packages.items(), ('latest.json',out)]:
        assets = gh('api',f'repos/{REPO}/releases/{release["id"]}/assets?per_page=100')
        old = next((a for a in assets if a['name']==name),None)
        digest = 'sha256:'+hashlib.sha256(path.read_bytes()).hexdigest()
        if old:
            if old.get('digest') != digest or old['size'] != path.stat().st_size:
                raise ValueError('Published updater bytes differ; create a new version instead')
        else:
            subprocess.run(['gh','release','upload',tag,str(path),'--repo',REPO],check=True)
        published = next(a for a in gh('api',f'repos/{REPO}/releases/{release["id"]}/assets?per_page=100') if a['name']==name)
        if published.get('digest') != digest or published['size'] != path.stat().st_size:
            raise ValueError('Uploaded package integrity mismatch; update feed not published')
    print('Verified signed update feed published for '+tag)

if __name__ == '__main__':
    main()
