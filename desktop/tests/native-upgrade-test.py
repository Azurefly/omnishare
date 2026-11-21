#!/usr/bin/env python3
"""CI-only old->new native upgrade with disposable identities and real signatures.

Builds a COPY of the actual OmniShare application twice. The only injected
code is the test driver and a local CA/DNS override for the fixed GitHub hosts.
Shipping sources, public key, endpoint rules and production Secrets are never
changed. Installs only a uniquely-named fixture on a disposable Actions runner.
"""
import argparse
import hashlib
import gzip
import http.server
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]
OLD, NEW = '0.0.0-rc.1', '0.0.0-rc.2'

def checked(args, cwd=None, env=None, timeout=1200, quiet=False):
    result = subprocess.run([str(x) for x in args], cwd=cwd, env=env, timeout=timeout,
                            capture_output=quiet, text=quiet)
    if result.returncode:
        # Signer output can contain keys. Never echo captured signer stdout.
        raise RuntimeError('Command failed: ' + str(args[0]) + ' (exit ' + str(result.returncode) + ')')
    return result

def patch(path, old, new):
    text = path.read_text(encoding='utf-8')
    if text.count(old) != 1:
        raise ValueError('Fixture patch anchor changed in ' + str(path.name))
    path.write_text(text.replace(old, new), encoding='utf-8')

def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]

def api(base, path, data=None):
    raw = None if data is None else json.dumps(data, ensure_ascii=False).encode()
    req = urllib.request.Request(base+path, data=raw, headers={'Content-Type':'application/json'})
    with urllib.request.urlopen(req, timeout=15) as response:
        value=json.load(response)
        assert value['code']==0, value
        return value['data']

def upload_file(base, name, payload):
    boundary='omnishare-upgrade-'+uuid.uuid4().hex
    body=(f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{name}"\r\n'
          'Content-Type: application/octet-stream\r\n\r\n').encode('utf-8')
    body+=payload+f'\r\n--{boundary}--\r\n'.encode()
    request=urllib.request.Request(base+'/api/v1/files/upload', data=body,
        headers={'Content-Type':f'multipart/form-data; boundary={boundary}'})
    with urllib.request.urlopen(request,timeout=15) as response:
        result=json.load(response)
        assert response.status==201 and result['code']==0,result
        return result['data']

def download_file(base, file_id):
    with urllib.request.urlopen(base+'/api/v1/files/'+file_id+'/download',timeout=15) as response:
        assert response.status==200
        return response.read()

def wait_file(path, root, timeout=180):
    end = time.monotonic()+timeout
    while not path.exists():
        if (root/'failure.txt').exists():
            raise AssertionError((root/'failure.txt').read_text(encoding='utf-8'))
        if time.monotonic()>end: raise TimeoutError('Timed out waiting for '+path.name)
        time.sleep(.1)

def serve(root):
    state=json.loads((root/'server.json').read_text())
    payload=Path(state['package']).read_bytes()
    prefix='/Azurefly/omnishare/releases/download/v'+NEW+'/'
    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version='HTTP/1.1'
        def log_message(self, *_): pass
        def do_GET(self):
            mode=(root/'mode').read_text().strip() if (root/'mode').exists() else 'normal'
            signature=state['signatures'].get(mode,state['signatures']['normal'])
            active_payload=Path(state['invalid_package']).read_bytes() if mode=='invalid-installer' else payload
            url='https://github.com'+prefix+'package.'+state['extension']
            manifest=json.dumps({'version':NEW,'platforms':{target:{'url':url,'signature':signature} for target in state['targets']}}).encode()
            if self.path.startswith('/repos/Azurefly/omnishare/releases?'):
                body=json.dumps([{'tag_name':'v'+NEW,'draft':False,'prerelease':True,'body':'Isolated CI upgrade fixture','assets':[
                    {'name':'latest.json','size':len(manifest),'browser_download_url':'https://github.com'+prefix+'latest.json'},
                    {'name':'package.'+state['extension'],'size':len(active_payload),'browser_download_url':url}]}]).encode()
            elif self.path==prefix+'latest.json':
                body=manifest
                if mode=='oversized-manifest':
                    self.send_response(200);self.send_header('Content-Length',str(4*1024*1024+1));self.end_headers()
                    self.close_connection=True;return
            elif self.path==prefix+'package.'+state['extension']:
                body=active_payload
                if mode=='tampered':body=bytes([payload[0]^1])+payload[1:]
                self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers()
                if mode=='slow':time.sleep(3)
                if mode=='truncated':body=body[:len(body)//2];self.close_connection=True
                try:self.wfile.write(body)
                except (BrokenPipeError,ConnectionResetError,ssl.SSLError):pass
                return
            else:
                self.send_response(404);self.send_header('Content-Length','0');self.end_headers();return
            self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers()
            try:self.wfile.write(body)
            except (BrokenPipeError,ConnectionResetError,ssl.SSLError):pass
    server=http.server.ThreadingHTTPServer(('127.0.0.1',443),Handler)
    context=ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(root/'fixture-server.pem', root/'fixture-server.key')
    server.socket=context.wrap_socket(server.socket,server_side=True)
    (root/'server-ready').write_text('yes')
    def stop_when_requested():
        while not (root/'server-stop').exists():time.sleep(.1)
        server.shutdown()
    threading.Thread(target=stop_when_requested,daemon=True).start()
    server.serve_forever()
    server.server_close()

def create_test_tls(root, openssl):
    """Generate a single-extension CA and leaf without system OpenSSL defaults.

    OpenSSL 1.1 can append -addext to its default v3_ca extension set, creating
    duplicate basicConstraints that newer verifiers correctly reject. Explicit
    local config keeps this fixture valid and never changes system trust.
    """
    config = root/'fixture-openssl.cnf'
    config.write_text(
        '[req]\nprompt=no\ndistinguished_name=ca_dn\n'
        '[ca_dn]\nCN=OmniShare CI Fixture\n'
        '[root_ca]\nbasicConstraints=critical,CA:TRUE,pathlen:0\n'
        'keyUsage=critical,keyCertSign,cRLSign\nsubjectKeyIdentifier=hash\n'
        'authorityKeyIdentifier=keyid:always\n', encoding='ascii', newline='\n')
    checked([openssl,'req','-x509','-newkey','rsa:2048','-nodes','-sha256',
             '-days','1','-config',config,'-extensions','root_ca',
             '-keyout',root/'fixture-ca.key','-out',root/'fixture-ca.pem'],quiet=True)
    checked([openssl,'req','-new','-newkey','rsa:2048','-nodes','-sha256',
             '-config',config,'-subj','/CN=api.github.com',
             '-keyout',root/'fixture-server.key','-out',root/'fixture-server.csr'],quiet=True)
    (root/'fixture-server.ext').write_text(
        'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\n'
        'extendedKeyUsage=serverAuth\nsubjectAltName=DNS:api.github.com,DNS:github.com\n'
        'subjectKeyIdentifier=hash\nauthorityKeyIdentifier=keyid,issuer\n',
        encoding='ascii', newline='\n')
    checked([openssl,'x509','-req','-in',root/'fixture-server.csr',
             '-CA',root/'fixture-ca.pem','-CAkey',root/'fixture-ca.key','-CAcreateserial',
             '-days','1','-sha256','-extfile',root/'fixture-server.ext',
             '-out',root/'fixture-server.pem'],quiet=True)
    checked([openssl,'verify','-x509_strict','-purpose','sslserver',
             '-verify_hostname','api.github.com','-CAfile',root/'fixture-ca.pem',
             root/'fixture-server.pem'],quiet=True)

def prepare_copy(root, cli, backend_port):
    source=root/'source'
    shutil.copytree(ROOT,source,ignore=shutil.ignore_patterns('.git','node_modules','target','.review','test-results','dist','signed-updates','__pycache__'))
    stem='OmniShareUpdateTest'+uuid.uuid4().hex[:8]
    conf=source/'desktop/src-tauri/tauri.conf.json'
    value=json.loads(conf.read_text());value['productName']=stem;value['identifier']='io.omnishare.upgrade-test.'+stem.lower()
    conf.write_text(json.dumps(value,indent=2)+'\n')
    key=root/'test-updater.key'
    checked(['node',cli,'signer','generate','--ci','--write-keys',key],quiet=True)
    public=Path(str(key)+'.pub').read_text().strip()
    (source/'desktop/src-tauri/updater-public-key.txt').write_text(public+'\n')
    openssl=shutil.which('openssl')
    if not openssl and os.name=='nt':openssl=r'C:\Program Files\Git\usr\bin\openssl.exe'
    create_test_tls(root, openssl)
    module=source/'desktop/src-tauri/src/updates'
    shutil.copyfile(ROOT/'desktop/tests/native-upgrade-driver.rs',module/'fixture.rs')
    shutil.copyfile(root/'fixture-ca.pem',module/'fixture-ca.pem')
    (module/'fixture-config.json').write_text(json.dumps({'root':str(root),'port':backend_port}))
    patch(module/'mod.rs','mod transfer;','mod transfer;\npub mod fixture;')
    patch(module/'mod.rs','reqwest::Client::builder()', '''reqwest::Client::builder()
        .no_proxy()
        .tls_certs_only([reqwest::Certificate::from_pem(include_bytes!("fixture-ca.pem")).unwrap()])
        .resolve("api.github.com", "127.0.0.1:443".parse().unwrap())
        .resolve("github.com", "127.0.0.1:443".parse().unwrap())''')
    main=source/'desktop/src-tauri/src/main.rs'
    patch(main,'fn main() {','fn main() {\n    updates::fixture::configure();')
    patch(main,'start_backend_watchdog(app.handle().clone());','start_backend_watchdog(app.handle().clone());\n            updates::fixture::start(app.handle().clone());')
    # The driver calls native commands directly. Prevent the regular UI's
    # automatic check from racing these scripted failure cases.
    patch(source/'desktop/frontend/updates.mjs','controller.initialize().then(ok=>{if(ok)controller.check()})','controller.initialize()')
    return source,stem,key

def build(source, root, version, kind):
    original=(source/'VERSION').read_text().strip()
    # Keep normal version/resource contracts active in the fixture build.
    for name in ['VERSION','frontend/package.json','frontend/package-lock.json','frontend/web/service-worker.js',
                 'backend/internal/buildinfo/version.go','desktop/package.json','desktop/package-lock.json',
                 'desktop/src-tauri/Cargo.toml','desktop/src-tauri/Cargo.lock','desktop/src-tauri/tauri.conf.json']:
        path=source/name
        if path.exists():path.write_text(path.read_text(encoding='utf-8').replace(original,version),encoding='utf-8')
    checked(['npm','--prefix','frontend','run','build'],cwd=source) if os.name!='nt' else checked(['cmd','/c','npm','--prefix','frontend','run','build'],cwd=source)
    resources=source/'desktop/src-tauri/resources';resources.mkdir(exist_ok=True)
    backend=resources/('omnishare.exe' if os.name=='nt' else 'omnishare')
    checked(['go','build','-trimpath','-o',backend,'./cmd/omnishare'],cwd=source/'backend')
    checked(['node',source/'desktop/scripts/ensure-icons.mjs'],cwd=source/'desktop')
    cli=ROOT/'desktop/node_modules/@tauri-apps/cli/tauri.js'
    options={}
    if kind=='msi':options={'bundle':{'windows':{'wix':{'version':'0.0.'+('601' if version==OLD else '602')}}}}
    overlay=root/'overlay.json';overlay.write_text(json.dumps(options))
    checked(['node',cli,'build','--ci','--bundles',kind,'--config',overlay],cwd=source/'desktop')
    bundle=source/'desktop/src-tauri/target/release/bundle'
    patterns={'nsis':'nsis/*-setup.exe','msi':'msi/*.msi','appimage':'appimage/*.AppImage','app':'macos/*.app','deb':'deb/*.deb'}
    matches=list(bundle.glob(patterns[kind]));assert len(matches)==1,matches
    out=root/('old' if version==OLD else 'new');out.mkdir(exist_ok=True)
    dest=out/matches[0].name
    if dest.is_dir():shutil.rmtree(dest)
    if matches[0].is_dir():
        checked(['ditto',matches[0],dest]);
        nested=list((dest/'Contents').rglob('omnishare'))
        for binary in nested:checked(['codesign','--force','--sign','-','--timestamp=none',binary])
        checked(['codesign','--force','--sign','-','--timestamp=none',dest])
        checked(['codesign','--verify','--deep','--strict',dest])
    else:shutil.copy2(matches[0],dest)
    # Remove bundle outputs only, retaining the incremental compilation cache.
    shutil.rmtree(bundle)
    return dest,backend

def sign(cli,key,package,version):
    env=dict(os.environ,TAURI_SIGNING_PRIVATE_KEY=key.read_text().strip(),TAURI_SIGNING_PRIVATE_KEY_PASSWORD='')
    checked(['node',cli,'signer','sign','--app-version',version,package],env=env,quiet=True)
    return Path(str(package)+'.sig').read_text().strip()

def main(kind,evidence):
    if os.environ.get('GITHUB_ACTIONS')!='true':raise RuntimeError('Run only on a disposable GitHub Actions runner')
    assert kind in {'appimage','app','nsis','msi','deb'}
    evidence.mkdir(parents=True,exist_ok=True)
    root=Path(tempfile.mkdtemp(prefix='os-upgrade-',dir=os.environ.get('RUNNER_TEMP'))).resolve()
    processes=[];checks=[];started=None;old=None;source=None
    result={'success':False,'kind':kind,'os':platform.platform(),'production_keys_used':False,
            'scope':'real OmniShare native updater and installer, disposable test identity; not OS signing/notarization or an interactive user-click test',
            'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip()}
    try:
        cli=ROOT/'desktop/node_modules/@tauri-apps/cli/tauri.js'
        backend_port=port();source,stem,key=prepare_copy(root,cli,backend_port)
        old,backend=build(source,root,OLD,kind)
        new,backend=build(source,root,NEW,kind)
        package=new
        if kind=='app':
            package=root/'new'/'update.app.tar.gz'
            with tarfile.open(package,'w:gz') as archive:archive.add(new,arcname=new.name)
        signatures={'normal':sign(cli,key,package,NEW),'wrong-signed-version':sign(cli,key,package,'0.0.0-rc.3')}
        wrong=root/'wrong.key';checked(['node',cli,'signer','generate','--ci','--write-keys',wrong],quiet=True)
        signatures['wrong-key']=sign(cli,wrong,package,NEW)
        invalid=root/'invalid-package.bin'
        invalid.write_bytes((b'MZ'+bytes(510)) if os.name=='nt' else gzip.compress(b'not a tar archive'))
        signatures['invalid-installer']=sign(cli,key,invalid,NEW)
        osname={'app':'darwin','appimage':'linux','nsis':'windows','msi':'windows','deb':'linux'}[kind]
        arch='aarch64' if platform.machine().lower() in {'arm64','aarch64'} else 'x86_64'
        targets=[f'{osname}-{arch}'+('' if kind=='app' else '-'+kind)]
        extension={'app':'app.tar.gz','appimage':'AppImage','nsis':'exe','msi':'msi','deb':'deb'}[kind]
        (root/'server.json').write_text(json.dumps({'package':str(package),'invalid_package':str(invalid),'extension':extension,'targets':targets,'signatures':signatures}))
        command=[sys.executable,str(Path(__file__).resolve()),'--serve',str(root)]
        if os.name!='nt':command=['sudo','-n',*command]
        server_log=(root/'https-server.log').open('wb')
        server=subprocess.Popen(command,stdout=server_log,stderr=server_log);processes.append(server)
        wait_file(root/'server-ready',root,20)
        # Prove trust chain and hostname validity before testing the updater.
        context=ssl.create_default_context(cafile=str(root/'fixture-ca.pem'))
        with socket.create_connection(('127.0.0.1',443),timeout=10) as raw:
            with context.wrap_socket(raw,server_hostname='api.github.com') as tls:
                tls.sendall(b'GET /health HTTP/1.1\r\nHost: api.github.com\r\nConnection: close\r\n\r\n')
                assert tls.recv(1024).startswith(b'HTTP/1.1 404'), 'TLS fixture did not answer'
        checks.append('private HTTPS fixture with verified CA/leaf chain and hostname, no TLS bypass')
        install=root/'installed';install.mkdir()
        if kind=='app':
            installed=install/old.name;checked(['ditto',old,installed]);executable=installed/'Contents/MacOS/omnishare-desktop'
        elif kind=='appimage':
            executable=install/'OmniShareTest.AppImage';shutil.copy2(old,executable);executable.chmod(0o755)
        elif kind=='deb':
            # Dedicated hosted runner only: refuse to replace any existing app.
            executable=Path('/usr/bin/omnishare-desktop')
            assert not executable.exists(), 'Existing desktop install must not be overwritten by this test'
            deb_package=subprocess.check_output(['dpkg-deb','--field',str(old),'Package'],text=True).strip()
            assert ''.join(c for c in deb_package if c.isalnum())==stem.lower(), 'Unexpected fixture package identity'
            checked(['sudo','-n','dpkg','--install',old],timeout=240)
            result['deb_package']=deb_package
            result['privilege_scope']='disposable hosted runner with sudo; interactive polkit/UAC prompts are not covered'
        elif kind=='nsis':
            checked([old,'/S','/D='+str(install)],timeout=240);executable=install/'omnishare-desktop.exe'
        else:
            checked(['msiexec','/i',old,'/qn','/norestart','INSTALLDIR='+str(install)],timeout=240);executable=install/'omnishare-desktop.exe'
        assert executable.is_file(),'Fixture install did not create '+str(executable)
        env=dict(os.environ,APPIMAGE_EXTRACT_AND_RUN='1')
        log=(root/'app.log').open('wb')
        started=subprocess.Popen([str(executable)],env=env,stdout=log,stderr=log);processes.append(started)
        wait_file(root/'started.json',root,60)
        before=json.loads((root/'started.json').read_text());assert before['version']==OLD
        base='http://127.0.0.1:'+str(before['port'])
        note=api(base,'/api/v1/notes',{'content':'升级前的笔记 • preserve me'})
        pad=api(base,'/api/v1/pads',{'title':'升级持久化','content':'upgrade content'})
        file_payload=os.urandom(8192)
        uploaded=upload_file(base,'upgrade-persistence.bin',file_payload)
        assert download_file(base,uploaded['id'])==file_payload
        peer_port=port();peer_base=f'http://127.0.0.1:{peer_port}'
        peer=subprocess.Popen([str(backend),'--no-browser','--listen','127.0.0.1','--port',str(peer_port),'--data-dir',str(root/'peer'),'--shutdown-on-stdin-close'],stdin=subprocess.PIPE,stdout=log,stderr=log);processes.append(peer)
        deadline=time.monotonic()+30
        while True:
            try:api(peer_base,'/api/v1/health');break
            except Exception:
                if time.monotonic()>deadline:raise
                time.sleep(.1)
        api(base,'/api/v1/peers/bind',{'url':peer_base,'access_key':''})
        devices=api(base,'/api/v1/devices');peer_ids=sorted(d['id'] for d in devices if d.get('bound'))
        assert peer_ids,'No durable peer binding was created'
        sentinel=root/'data'/'upgrade-preserve.bin';sentinel.write_bytes(os.urandom(8192));digest=hashlib.sha256(sentinel.read_bytes()).hexdigest()
        (root/'begin').touch()
        wait_file(root/'ready',root,150)
        # Verify check/download/rejections left the old app, data and peer alive.
        assert api(base,'/api/v1/notes/'+note['id']+'/manage')['content']==note['content']
        assert peer.poll() is None
        checks.append('tampered bytes, wrong key, signed-version mismatch, truncation and oversize rejected without stopping backend')
        checks.append('signed but invalid native installer fails and restarts the owned backend without exiting permanently')
        checks.append('real native update-window close cancels download; reopen and retry succeeds')
        (root/'confirm').touch()
        wait_file(root/'restarted.json',root,240)
        after=json.loads((root/'restarted.json').read_text());assert after['version']==NEW;assert after['pid']!=before['pid']
        base='http://127.0.0.1:'+str(after['port']);assert after['port']==before['port']
        assert api(base,'/api/v1/notes/'+note['id']+'/manage')['content']==note['content']
        assert api(base,'/api/v1/pads/'+pad['id'])['content']==pad['content']
        assert download_file(base,uploaded['id'])==file_payload, 'uploaded file or SQLite index was lost'
        assert sorted(d['id'] for d in api(base,'/api/v1/devices') if d.get('bound'))==peer_ids
        assert hashlib.sha256(sentinel.read_bytes()).hexdigest()==digest
        assert peer.poll() is None
        if kind=='deb':
            installed_version=subprocess.check_output(['dpkg-query','-W','-f=${Version}',deb_package],text=True).strip()
            expected_version=subprocess.check_output(['dpkg-deb','--field',str(new),'Version'],text=True).strip()
            assert installed_version==expected_version, 'dpkg database does not report the new version'
            checks.append('actual DEB installation and dpkg package version updated through native updater')
        checks.extend(['explicit consent required before native installation','installed application restarted at new version',
                       'SQLite note and collaborative document, API-uploaded file and index, data file, port and peer binding preserved','other deployed node remained running'])
        (root/'finish').touch()
        result.update(success=True,checks=checks,old_version=OLD,new_version=NEW,package_sha256=hashlib.sha256(package.read_bytes()).hexdigest())
    except Exception as error:
        result.update(error=str(error),checks=checks)
        raise
    finally:
        (evidence/'result.json').write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
        for name in ['events.jsonl','failure.txt','app.log','https-server.log']:
            if (root/name).exists():shutil.copyfile(root/name,evidence/name)
        # Stop only recorded fixture processes. Never terminate by app name.
        (root/'server-stop').touch()
        if processes:
            try:processes[0].wait(timeout=10)
            except subprocess.TimeoutExpired:pass
        for child in reversed(processes):
            if child.stdin:child.stdin.close()
            if child.poll() is None:
                child.terminate()
                try:child.wait(timeout=10)
                except subprocess.TimeoutExpired:child.kill();child.wait()
        # Private keys/certificates are disposable and never uploaded.
        for key in root.glob('*.key'):key.unlink(missing_ok=True)
        if source:shutil.rmtree(source,ignore_errors=True)
        print(json.dumps(result,ensure_ascii=False))

if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--serve',type=Path);parser.add_argument('--kind',choices=['appimage','app','nsis','msi','deb']);parser.add_argument('--evidence',type=Path,default=Path('upgrade-evidence'))
    args=parser.parse_args()
    if args.serve:serve(args.serve)
    else:main(args.kind,args.evidence.resolve())
