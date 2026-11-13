#!/usr/bin/env python3
"""Stage real newly built candidates; preserve failed/unrun native evidence."""
import hashlib,json,os,shutil,subprocess,sys
from pathlib import Path

root=Path(__file__).resolve().parents[1]
version=(root/'VERSION').read_text().strip()
if version!='0.2.0-rc.2': raise SystemExit('Unexpected candidate version')
kind=sys.argv[1]
out=root/'rc-assets';out.mkdir(exist_ok=True)
records=[]
def digest(path):
 h=hashlib.sha256()
 with path.open('rb') as stream:
  for block in iter(lambda:stream.read(1024*1024),b''): h.update(block)
 return h.hexdigest()
def add(source,name):
 p=out/name
 if p.exists():raise RuntimeError('duplicate output: '+name)
 shutil.copyfile(source,p)
 records.append({'name':name,'sha256':digest(p),'size':p.stat().st_size})
def one(pattern):
 xs=[p for p in root.glob(pattern) if p.is_file()]
 if len(xs)!=1:raise RuntimeError(f'Expected one built file for {pattern}, found {len(xs)}')
 return xs[0]
if kind=='server':
 for system,ext in [('windows','zip'),('linux','tar.gz')]:
  for arch in ['amd64','arm64']:
   add(one(f'dist/packages/OmniShare-v{version}-{system}-{arch}.{ext}'),f'OmniShare-v{version}-{system}-{arch}-portable.{ext}')
elif kind=='Windows':
 add(one('desktop/src-tauri/target/release/bundle/nsis/*-setup.exe'),f'OmniShare-v{version}-windows-x64-setup.exe')
 add(one('desktop/src-tauri/target/release/bundle/msi/*.msi'),f'OmniShare-v{version}-windows-x64.msi')
elif kind=='Linux':
 add(one('desktop/src-tauri/target/release/bundle/deb/*.deb'),f'OmniShare-v{version}-ubuntu-amd64.deb')
 add(one('desktop/src-tauri/target/release/bundle/appimage/*.AppImage'),f'OmniShare-v{version}-linux-amd64.AppImage')
elif kind=='macOS':
 add(one('macos-rc/*.dmg'),f'OmniShare-v{version}-macos-universal.dmg')
else:raise SystemExit('Unknown platform')
evidence={
 'schema_version':1,'version':version,'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip(),
 'workflow_run_id':os.environ['GITHUB_RUN_ID'],'platform':kind,'build_succeeded':True,
 'native_installation_outcome':os.environ.get('NATIVE_OUTCOME','not_run'),
 'native_gui_acceptance_passed':os.environ.get('NATIVE_OUTCOME')=='success' and kind in ('Windows','Linux'),
 'apple_notarized':False,'files':records,
 'scope':'candidate build, not stable acceptance; macOS HTTP check tests packaged backend only, not native GUI',
}
for path in ['candidate-evidence/windows-install-smoke.json','ubuntu-install-evidence/linux-installed-smoke.json']:
 p=root/path
 if p.is_file():evidence['native_report']=json.loads(p.read_text(encoding='utf-8-sig'))
if kind in ('Windows','Linux'):
 evidence['native_gui_acceptance_passed'] = evidence['native_gui_acceptance_passed'] and evidence.get('native_report',{}).get('success') is True
(out/f'build-evidence-{kind}.json').write_text(json.dumps(evidence,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
print(json.dumps({'platform':kind,'files':records,'native_outcome':evidence['native_installation_outcome']}))
# Candidate evidence is UTF-8 and compatible with Ubuntu 22.04's Python 3.10.
