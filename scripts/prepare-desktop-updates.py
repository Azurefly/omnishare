#!/usr/bin/env python3
"""Sign final desktop packages, never server/portable binaries, for native updating."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def validate_signature(signature, version, public_key):
    """Structural/version/key-id check; the native updater also verifies cryptography."""
    lines = base64.b64decode(signature, validate=True).decode('utf-8').splitlines()
    key = base64.b64decode(public_key, validate=True).decode('utf-8').splitlines()
    if len(lines) != 4 or len(key) != 2 or not lines[0].startswith('untrusted comment:'):
        raise ValueError('Invalid updater signature/public key format')
    signature_bytes = base64.b64decode(lines[1], validate=True)
    key_bytes = base64.b64decode(key[1], validate=True)
    if len(signature_bytes) != 74 or len(key_bytes) != 42 or signature_bytes[2:10] != key_bytes[2:10]:
        raise ValueError('Signature was made by a different update key')
    if not lines[2].startswith('trusted comment: ') or f'version:{version}' not in lines[2].split('\t'):
        raise ValueError('Signature is not bound to this application version')
    if len(base64.b64decode(lines[3], validate=True)) != 64:
        raise ValueError('Invalid global signature')


def one(pattern):
    matches = list(ROOT.glob(pattern))
    if len(matches) != 1:
        raise ValueError(f'Expected one final package: {pattern}; found {len(matches)}')
    return matches[0]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('platform', choices=['Windows', 'Linux', 'macOS'])
    args = parser.parse_args()
    version = (ROOT / 'VERSION').read_text().strip()
    if not re.fullmatch(r'\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?', version):
        raise ValueError('Invalid app version')
    key = (ROOT / 'desktop/src-tauri/updater-public-key.txt').read_text().strip()
    if not key or not os.environ.get('TAURI_SIGNING_PRIVATE_KEY'):
        raise ValueError('Persistent updater public/private key is not configured; refusing unsigned update')
    out = ROOT / 'signed-updates'
    out.mkdir(exist_ok=True)
    prefix = f'OmniShare-v{version}'
    records = []
    if args.platform == 'Windows':
        inputs = [('windows-x86_64-nsis', one('desktop/src-tauri/target/release/bundle/nsis/*-setup.exe'), f'{prefix}-windows-x64-updater-setup.exe'),
                  ('windows-x86_64-msi', one('desktop/src-tauri/target/release/bundle/msi/*.msi'), f'{prefix}-windows-x64-updater.msi')]
    elif args.platform == 'Linux':
        inputs = [('linux-x86_64-deb', one('desktop/src-tauri/target/release/bundle/deb/*.deb'), f'{prefix}-ubuntu-amd64-updater.deb'),
                  ('linux-x86_64-appimage', one('desktop/src-tauri/target/release/bundle/appimage/*.AppImage'), f'{prefix}-linux-amd64-updater.AppImage')]
    else:
        app = one('desktop/src-tauri/target/universal-apple-darwin/release/bundle/macos/OmniShare.app')
        subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
        archive = out / f'{prefix}-macos-universal-updater.app.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            tar.add(app, arcname='OmniShare.app')
        inputs = [('darwin-x86_64', archive, archive.name), ('darwin-aarch64', archive, archive.name)]
    signed = {}
    for target, source, name in inputs:
        dest = out / name
        if name not in signed:
            if source.resolve() != dest.resolve():
                shutil.copyfile(source, dest)
            # The key/password stay in environment, never in argv or workflow logs.
            command = ['node', str(ROOT / 'desktop/node_modules/@tauri-apps/cli/tauri.js'),
                       'signer', 'sign', '--app-version', version, str(dest)]
            result = subprocess.run(command, capture_output=True, text=True)
            if result.returncode:
                raise ValueError('Update signing failed; verify key, password and signer CLI version')
            signature = Path(str(dest) + '.sig').read_text().strip()
            validate_signature(signature, version, key)
            signed[name] = {'name': name, 'sha256': hashlib.sha256(dest.read_bytes()).hexdigest(),
                            'size': dest.stat().st_size, 'signature': signature}
        records.append({'target': target, **signed[name]})
    (out / f'updater-evidence-{args.platform}.json').write_text(json.dumps({
        'version': version, 'source_commit': subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip(),
        'public_key_sha256': hashlib.sha256(key.encode()).hexdigest(), 'files': records,
    }, indent=2) + '\n')

if __name__ == '__main__':
    main()
