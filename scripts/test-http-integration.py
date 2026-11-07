"""Exercise a built OmniShare binary over HTTP using isolated temporary data.

Usage: python3 scripts/test-http-integration.py /path/to/omnishare
"""

import argparse
import json
import pathlib
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

def run(binary):
    with tempfile.TemporaryDirectory(prefix='omnishare-integration-') as root:
        root = pathlib.Path(root)
        with socket.socket() as probe:
            probe.bind(('127.0.0.1', 0))
            port = probe.getsockname()[1]
        base = f'http://127.0.0.1:{port}'
        process = None
        log = (root / 'backend.log').open('wb')
        auth = {}

        def request(method, path, data=None, headers=None):
            if isinstance(data, dict):
                data = json.dumps(data).encode()
                headers = {'Content-Type': 'application/json', **(headers or {})}
            req = urllib.request.Request(base + path, data=data, method=method,
                                         headers={**auth, **(headers or {})})
            try:
                response = urllib.request.urlopen(req, timeout=15)
            except urllib.error.HTTPError as error:
                response = error
            with response:
                return response.status, response.headers, response.read()

        def data(method, path, body=None, expected=200, headers=None):
            status, _, raw = request(method, path, body, headers)
            assert status == expected, (method, path, status, raw[:300])
            return json.loads(raw).get('data')

        def multipart(name, filename, payload):
            boundary = 'omnishare-integration-boundary'
            body = (f'--{boundary}\r\nContent-Disposition: form-data; name="{name}"; filename="{filename}"\r\n'
                    'Content-Type: application/octet-stream\r\n\r\n').encode()
            body += payload + f'\r\n--{boundary}--\r\n'.encode()
            return body, {'Content-Type': f'multipart/form-data; boundary={boundary}'}

        def start():
            nonlocal process
            process = subprocess.Popen([binary, '--no-browser', '--port', str(port), '--data-dir', str(root / 'data')], stdout=log, stderr=log)
            for _ in range(100):
                if process.poll() is not None:
                    raise AssertionError('backend exited during startup')
                try:
                    if request('GET', '/api/v1/health')[0] == 200:
                        return
                except (OSError, urllib.error.URLError):
                    pass
                time.sleep(.05)
            raise AssertionError('backend did not become ready')

        def stop():
            if process is not None and process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()

        try:
            start()
            assert request('GET', '/api/v1/notes')[0] == 401
            assert data('GET', '/api/v1/health')['storage'] == 'sqlite'
            token = (root / 'data' / 'init-token').read_text().strip()
            auth['X-OmniShare-Init-Token'] = token
            original = data('POST', '/api/v1/notes', {'content': '集成测试：保留的原始笔记'}, 201)
            body, headers = multipart('file', 'sample.txt', b'omnishare range payload')
            file = data('POST', '/api/v1/files/upload', body, 201, headers)
            status, _, raw = request('GET', '/api/v1/files/' + file['id'] + '/download', headers={'Range': 'bytes=0-8'})
            assert status == 206 and raw == b'omnishare'
            status, headers, backup = request('GET', '/api/v1/backup', headers={'X-OmniShare-Backup-Password': 'integration-backup-password'})
            assert status == 200 and backup.startswith(b'OMNISHARE-ENC-BK')
            magic_len = len(b'OMNISHARE-ENC-BK')
            header_len = int.from_bytes(backup[magic_len:magic_len + 4], 'big')
            assert json.loads(backup[magic_len + 4:magic_len + 4 + header_len])['format_version'] == 3
            later = data('POST', '/api/v1/notes', {'content': '备份后临时新增'}, 201)
            body, headers = multipart('backup', 'snapshot.osb', backup)
            headers.update({'X-OmniShare-Confirm': 'RESTORE-BACKUP', 'X-OmniShare-Backup-Password': 'wrong-password'})
            assert request('POST', '/api/v1/restore', body, headers)[0] == 400
            data('GET', '/api/v1/notes/' + later['id'] + '/manage')
            headers['X-OmniShare-Backup-Password'] = 'integration-backup-password'
            data('POST', '/api/v1/restore', body, 200, headers)
            assert request('GET', '/api/v1/notes/' + later['id'] + '/manage')[0] == 404
            assert data('GET', '/api/v1/notes/' + original['id'] + '/manage')['content'] == original['content']
            assert request('GET', '/api/v1/files/' + file['id'] + '/download')[2] == b'omnishare range payload'
            second = subprocess.run([binary, '--no-browser', '--port', str(port), '--data-dir', str(root / 'data')], capture_output=True, timeout=10)
            assert second.returncode != 0 and b'instance lock' in second.stderr
            stop()
            start()
            assert data('GET', '/api/v1/notes/' + original['id'] + '/manage')['content'] == original['content']
            print(json.dumps({'result': 'passed', 'checks': ['default-deny first run', 'SQLite startup', 'Chinese note creation', 'file upload and HTTP Range', 'encrypted v3 backup', 'wrong-password restore preserves state', 'encrypted restore round trip', 'single-instance lock', 'restart persistence']}, ensure_ascii=False))
        finally:
            stop()
            log.close()

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('binary', type=pathlib.Path)
    args = parser.parse_args()
    run(str(args.binary.resolve(strict=True)))
