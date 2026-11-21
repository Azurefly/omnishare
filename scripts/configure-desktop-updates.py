#!/usr/bin/env python3
"""Owner-only, one-time updater provisioning. Never run on a CI runner.

Private material stays outside the repository and reaches GitHub Secrets only
via the authenticated owner's gh CLI stdin. This script does not commit/push.
"""
import argparse
import base64
import getpass
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PUBLIC = ROOT / 'desktop/src-tauri/updater-public-key.txt'
REPO = 'Azurefly/omnishare'
SECRETS = ('TAURI_SIGNING_PRIVATE_KEY', 'TAURI_SIGNING_PRIVATE_KEY_PASSWORD')


def key_paths(raw):
    candidate = Path(raw).expanduser()
    public = Path(str(candidate) + '.pub')
    if candidate.is_symlink() or public.is_symlink():
        raise ValueError('Key files must not be symbolic links')
    key = candidate.resolve()
    if key == ROOT or ROOT in key.parents:
        raise ValueError('Private signing keys must be stored OUTSIDE this repository')
    return key, Path(str(key) + '.pub')


def public_text(path):
    text = path.read_text(encoding='utf-8').strip()
    lines = base64.b64decode(text, validate=True).decode('utf-8').splitlines()
    if len(lines) != 2 or not lines[0].startswith('untrusted comment:'):
        raise ValueError('Invalid updater public key')
    raw = base64.b64decode(lines[1], validate=True)
    if len(raw) != 42 or raw[:2] != b'Ed':
        raise ValueError('Invalid updater public key payload')
    return text


def safe_run(argv, *, secret_input=None, env=None):
    result = subprocess.run(argv, input=secret_input, text=True, capture_output=True,
                            env=env, timeout=120)
    if result.returncode:
        # Signer/CLI errors can contain sensitive inputs: do not print them.
        raise RuntimeError('Signer or GitHub CLI failed; no secret output was printed')
    return result.stdout


def upload_secrets(key, password):
    env = dict(os.environ, GH_HOST='github.com')
    env.pop('GH_DEBUG', None)
    names = {item['name'] for item in json.loads(safe_run(
        ['gh', 'secret', 'list', '--repo', REPO, '--json', 'name'], env=env))}
    if names.intersection(SECRETS):
        raise ValueError('Updater Secrets already exist; refusing to replace the persistent signing identity')
    # GitHub CLI encrypts values before upload. Neither value enters argv.
    for name, value in [(SECRETS[1], password), (SECRETS[0], key)]:
        safe_run(['gh', 'secret', 'set', name, '--repo', REPO],
                 secret_input=value, env=env)


def install_public(text):
    if PUBLIC.is_symlink():
        raise ValueError('Public-key destination must not be a symbolic link')
    current = PUBLIC.read_text(encoding='utf-8').strip() if PUBLIC.exists() else ''
    if current and current != text:
        raise ValueError('A different public key is already pinned; automatic key rotation is forbidden')
    with tempfile.NamedTemporaryFile(mode='w', encoding='utf-8', dir=PUBLIC.parent,
                                     prefix='.updater-public-', delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(text + '\n')
    try:
        os.replace(temporary, PUBLIC)
    finally:
        temporary.unlink(missing_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--key-file', default=str(Path.home() / '.config/omnishare/updater.key'))
    parser.add_argument('--generate', action='store_true', help='Generate a persistent key interactively, refusing overwrite')
    parser.add_argument('--upload-secrets', action='store_true', help='Upload once to Azurefly/omnishare using the owner gh login')
    args = parser.parse_args()
    if os.environ.get('GITHUB_ACTIONS') == 'true' or not sys.stdin.isatty():
        raise ValueError('Run interactively on the repository owner computer, not in CI or a pipeline')
    key, public = key_paths(args.key_file)
    cli = ROOT / 'desktop/node_modules/@tauri-apps/cli/tauri.js'
    package = cli.with_name('package.json')
    expected = json.loads((ROOT / 'desktop/package.json').read_text())['devDependencies']['@tauri-apps/cli']
    if not shutil.which('node') or not cli.is_file() or json.loads(package.read_text())['version'] != expected:
        raise ValueError('Install the locked CLI first: npm --prefix desktop ci')
    if args.upload_secrets and not shutil.which('gh'):
        raise ValueError('Install GitHub CLI and run gh auth login first')
    if args.generate:
        if key.exists() or public.exists():
            raise ValueError('Existing keys are never overwritten; omit --generate to reuse them')
        key.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
        old_umask = os.umask(0o077)
        try:
            # Password is read by the official CLI from the terminal, not argv.
            subprocess.run(['node', str(cli), 'signer', 'generate', '--write-keys', str(key)], check=True)
        finally:
            os.umask(old_umask)
    if not key.is_file() or not public.is_file():
        raise ValueError('Both the private key and matching .pub file are required')
    if os.name != 'nt' and key.stat().st_mode & 0o077:
        raise ValueError('Restrict private key permissions to the owner (chmod 600) before continuing')
    pin = public_text(public)
    current = PUBLIC.read_text().strip()
    if current and current != pin:
        raise ValueError('Pinned key differs; refusing to break existing client updates')
    private = key.read_text(encoding='utf-8').strip()
    password = getpass.getpass('Signing key password (empty only for an unencrypted key): ')
    version = (ROOT / 'VERSION').read_text().strip()
    env = dict(os.environ, TAURI_SIGNING_PRIVATE_KEY=private, TAURI_SIGNING_PRIVATE_KEY_PASSWORD=password)
    env.pop('TAURI_SIGNING_PRIVATE_KEY_PATH', None)
    with tempfile.TemporaryDirectory(prefix='omnishare-signing-check-') as directory:
        probe = Path(directory) / 'probe.bin'
        probe.write_bytes(os.urandom(64))
        safe_run(['node', str(cli), 'signer', 'sign', '--app-version', version, str(probe)], env=env)
        spec = importlib.util.spec_from_file_location('prepare_updates', ROOT / 'scripts/prepare-desktop-updates.py')
        prepare = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(prepare)
        prepare.validate_signature(Path(str(probe) + '.sig').read_text().strip(), version, pin)
    install_public(pin)
    if args.upload_secrets:
        upload_secrets(private, password)
    print('Public key prepared: desktop/src-tauri/updater-public-key.txt')
    print('Back up your private key and password outside the repository. They were not printed.')
    print('Commit the PUBLIC key and build a new desktop installer; existing RC2 installs need this installer once.')
    if not args.upload_secrets:
        print('GitHub Secrets were NOT changed. Use --upload-secrets after signing in as the owner.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as error:
        print('Setup stopped: ' + str(error), file=sys.stderr)
        sys.exit(1)
