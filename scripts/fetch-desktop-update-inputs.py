#!/usr/bin/env python3
"""Reuse the exact published packages, never rebuild opaque new installer bytes."""
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
REPO = 'Azurefly/omnishare'

def main():
    kind = sys.argv[1]
    version = (ROOT/'VERSION').read_text().strip()
    tag = 'v'+version
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?',version) or os.environ.get('UPDATE_TAG') != tag:
        raise ValueError('Tag does not match checked-out source')
    if not (ROOT/'desktop/src-tauri/updater-public-key.txt').read_text().strip():
        raise ValueError('No persistent updater public key is configured')
    source = subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()
    if subprocess.check_output(['git','rev-parse',tag+'^{commit}'],text=True).strip()!=source:
        raise ValueError('Tag and source differ')
    incoming = ROOT/'update-inputs'; incoming.mkdir(exist_ok=True)
    def download(name):
        subprocess.run(['gh','release','download',tag,'--repo',REPO,'--pattern',name,'--dir',str(incoming)],check=True)
        return incoming/name
    manifest = json.loads(download('build-manifest.json').read_text())
    if manifest['version'] != version or manifest['source_commit'] != source:
        raise ValueError('Published build manifest refers to different source/version')
    records = {r['name']:r for r in manifest['files']}
    bundle = ROOT/'desktop/src-tauri/target/release/bundle'
    if kind=='Windows':
        names=[(f'OmniShare-v{version}-windows-x64-setup.exe',bundle/'nsis'),(f'OmniShare-v{version}-windows-x64.msi',bundle/'msi')]
    elif kind=='Linux':
        names=[(f'OmniShare-v{version}-ubuntu-amd64.deb',bundle/'deb'),(f'OmniShare-v{version}-linux-amd64.AppImage',bundle/'appimage')]
    elif kind=='macOS':
        names=[(f'OmniShare-v{version}-macos-universal.dmg',incoming)]
    else: raise ValueError('Unsupported platform')
    for name, directory in names:
        record=records[name]
        if not 0 < record['size'] <= 512*1024*1024:raise ValueError('Package size invalid')
        package=download(name)
        with package.open('rb') as stream:
            digest=hashlib.sha256()
            for block in iter(lambda:stream.read(1024*1024),b''):digest.update(block)
        if digest.hexdigest()!=record['sha256'] or package.stat().st_size!=record['size']:
            raise ValueError('Downloaded original package failed integrity check')
        directory.mkdir(parents=True,exist_ok=True)
        if directory != incoming:shutil.copyfile(package,directory/name)
    if kind=='macOS':
        image=incoming/names[0][0]
        result=subprocess.check_output(['hdiutil','attach','-readonly','-nobrowse','-plist',str(image)])
        mounts=[e['mount-point'] for e in plistlib.loads(result).get('system-entities',[]) if e.get('mount-point')]
        if len(mounts)!=1:raise ValueError('Unexpected DMG volume layout')
        mount=Path(mounts[0])
        try:
            app=mount/'OmniShare.app'
            if app.is_symlink() or not app.is_dir():raise ValueError('Missing application bundle')
            subprocess.run(['codesign','--verify','--deep','--strict',str(app)],check=True)
            if '-' not in version:
                # A stable updater must not silently replace a trusted app with
                # a Gatekeeper-rejected build. Candidates remain explicit opt-in.
                subprocess.run(['spctl','--assess','--type','execute',str(app)],check=True)
            target=ROOT/'desktop/src-tauri/target/universal-apple-darwin/release/bundle/macos/OmniShare.app'
            target.parent.mkdir(parents=True,exist_ok=True)
            subprocess.run(['ditto',str(app),str(target)],check=True)
        finally:
            subprocess.run(['hdiutil','detach',str(mount)],check=True)

if __name__=='__main__':main()
