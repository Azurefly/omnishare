import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

root=Path(__file__).resolve().parents[1]
spec=importlib.util.spec_from_file_location('publish',root/'scripts/publish-desktop-updates.py')
publish=importlib.util.module_from_spec(spec);spec.loader.exec_module(publish)

# Deliberately fake structural fixtures. These are NOT cryptographic signature
# tests and are never committed as trusted keys or accepted by native install.
def fixtures(version='0.2.0-rc.3'):
    keyid=b'12345678'
    key=base64.b64encode(b'untrusted comment: fixture\n'+base64.b64encode(b'Ed'+keyid+b'x'*32)+b'\n').decode()
    sig=base64.b64encode(b'untrusted comment: fixture\n'+base64.b64encode(b'ED'+keyid+b'x'*64)+b'\ntrusted comment: timestamp:0\tfile:test\tversion:'+version.encode()+b'\n'+base64.b64encode(b'x'*64)+b'\n').decode()
    return key,sig

class UpdateFeedTest(unittest.TestCase):
    def test_version_and_key_mismatch_rejected(self):
        key,sig=fixtures()
        publish.prepare.validate_signature(sig,'0.2.0-rc.3',key)
        with self.assertRaises(ValueError):publish.prepare.validate_signature(sig,'0.2.0-rc.4',key)
        wrong=base64.b64encode(b'untrusted comment: fixture\n'+base64.b64encode(b'Ed'+b'87654321'+b'x'*32)+b'\n').decode()
        with self.assertRaises(ValueError):publish.prepare.validate_signature(sig,'0.2.0-rc.3',wrong)

    def setup_feed(self, folder):
        key,sig=fixtures();version='0.2.0-rc.3';sha='a'*40
        for kind,targets in [('Windows',['windows-x86_64-nsis','windows-x86_64-msi']),('Linux',['linux-x86_64-deb','linux-x86_64-appimage']),('macOS',['darwin-x86_64','darwin-aarch64'])]:
            records=[]
            for target in targets:
                name=f'OmniShare-v{version}-{target}'
                (folder/name).write_bytes(target.encode())
                records.append({'name':name,'size':len(target),'sha256':hashlib.sha256(target.encode()).hexdigest(),'signature':sig,'target':target})
            (folder/f'updater-evidence-{kind}.json').write_text(json.dumps({'version':version,'source_commit':sha,'public_key_sha256':hashlib.sha256(key.encode()).hexdigest(),'files':records}))
        return version,sha,key

    def test_complete_set_and_hashes_required(self):
        with tempfile.TemporaryDirectory() as path:
            folder=Path(path);version,sha,key=self.setup_feed(folder)
            feed,_=publish.manifest(folder,version,sha,key)
            self.assertEqual(len(feed['platforms']),6)
            file=next(folder.glob('OmniShare*'))
            file.write_bytes(b'tampered')
            with self.assertRaises(ValueError):publish.manifest(folder,version,sha,key)

    def test_sources_versions_and_names_cannot_mix(self):
        for field,value in [('source_commit','b'*40),('version','0.2.0-rc.4'),('public_key_sha256','0'*64)]:
            with self.subTest(field=field),tempfile.TemporaryDirectory() as path:
                folder=Path(path);version,sha,key=self.setup_feed(folder)
                file=folder/'updater-evidence-Windows.json';j=json.loads(file.read_text());j[field]=value;file.write_text(json.dumps(j))
                with self.assertRaises(ValueError):publish.manifest(folder,version,sha,key)

if __name__=='__main__':unittest.main()
