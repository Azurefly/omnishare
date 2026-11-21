#!/usr/bin/env python3
"""Exercise real signed updates in isolated native fixtures, never a user's install.

The production updater methods are called from a cfg(test) Rust test binary.
Temporary signer keys, fixture apps and installers are destroyed afterwards.
Only JSON evidence/logs are exported; no production key, Release or Secrets change.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import subprocess
import tarfile
import tempfile
import time
import uuid
from xml.sax.saxutils import escape

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT/'desktop/node_modules/@tauri-apps/cli/tauri.js'
TEST_PREFIX = 'updates::integration_tests::'


def run(command, *, env=None, cwd=ROOT, timeout=180, secret=False):
    result = subprocess.run([str(x) for x in command], cwd=cwd, env=env,
                            text=True, encoding='utf-8', errors='replace', capture_output=True, timeout=timeout)
    if result.returncode:
        if secret:
            raise RuntimeError('Test-only signing failed; secret output suppressed')
        raise RuntimeError(f'{command[0]} failed ({result.returncode}):\n{result.stdout[-12000:]}\n{result.stderr[-16000:]}')
    return result.stdout


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def compile_fixture(root, version):
    name = 'fixture.exe' if os.name == 'nt' else 'fixture'
    output = root/version/name
    output.parent.mkdir()
    source = root/'fixture.rs'
    source.write_text('''fn main() {
    let receipt = std::path::Path::new(env!("UPDATE_TEST_RECEIPT"));
    std::fs::write(receipt, env!("UPDATE_TEST_VERSION")).unwrap();
    println!("{}", env!("UPDATE_TEST_VERSION"));
}\n''', encoding='utf-8')
    env=dict(os.environ, UPDATE_TEST_VERSION=version, UPDATE_TEST_RECEIPT=str(root/'restarted.txt'))
    run(['rustc','--edition','2021','-O',source,'-o',output],env=env)
    return output


def mac_app(directory, executable, version):
    app=directory/'UpdaterTest.app'
    binary=app/'Contents/MacOS/fixture'
    binary.parent.mkdir(parents=True)
    shutil.copyfile(executable,binary);binary.chmod(0o755)
    (app/'Contents/Info.plist').write_bytes(plistlib.dumps({
        'CFBundleIdentifier':'com.azurefly.omnishare.updater-test',
        'CFBundleExecutable':'fixture','CFBundleName':'UpdaterTest',
        'CFBundlePackageType':'APPL','CFBundleVersion':version,'CFBundleShortVersionString':version}))
    run(['codesign','--force','--sign','-','--timestamp=none',app])
    return app,binary


def windows_installers(root, old, new):
    cache=Path(os.environ['LOCALAPPDATA'])/'tauri'
    nsis=cache/'NSIS/makensis.exe'
    wix=cache/'WixTools314'
    assert nsis.is_file() and (wix/'candle.exe').is_file(), 'Run prepare-windows-bundle-tools.py first'
    upgrade=str(uuid.uuid4()).upper();component=str(uuid.uuid4()).upper()
    outputs=[]
    for kind in ('nsis','msi'):
        installed=root/('installed-'+kind)
        pairs=[]
        for version,binary in [('1.0.0',old),('1.0.1',new)]:
            dest=root/f'{kind}-{version}'
            dest.mkdir()
            if kind=='nsis':
                installer=dest/'setup.exe';script=dest/'setup.nsi'
                script.write_text(f'''Unicode true
Name "OmniShare isolated updater test"
OutFile "{installer}"
InstallDir "{installed}"
RequestExecutionLevel user
SilentInstall silent
Section
  SetOutPath "$INSTDIR"
  File /oname=fixture.exe "{binary}"
  Exec '"$INSTDIR\\fixture.exe"'
SectionEnd
''',encoding='utf-8')
                run([nsis,'/V2',script])
            else:
                installer=dest/'fixture.msi';script=dest/'fixture.wxs'
                x=lambda s:escape(str(s),{'"':'&quot;'})
                script.write_text(f'''<Wix xmlns="http://schemas.microsoft.com/wix/2006/wi">
<Product Id="*" Name="OmniShareUpdaterTest-{upgrade}" Language="1033" Version="{version}" Manufacturer="OmniShare test" UpgradeCode="{upgrade}">
<Package InstallerVersion="500" Compressed="yes" InstallScope="perUser" InstallPrivileges="limited"/>
<MajorUpgrade DowngradeErrorMessage="No test downgrade"/><MediaTemplate EmbedCab="yes"/>
<Property Id="INSTALLDIR" Value="{x(installed)}"/>
<Directory Id="TARGETDIR" Name="SourceDir"><Directory Id="INSTALLDIR" Name="UpdaterTest">
<Component Id="Main" Guid="{component}"><File Id="FixtureExe" Source="{x(binary)}" KeyPath="yes"/></Component>
</Directory></Directory><Feature Id="MainFeature" Level="1"><ComponentRef Id="Main"/></Feature>
<CustomAction Id="LaunchUpdatedTest" FileKey="FixtureExe" ExeCommand="" Return="asyncNoWait" Impersonate="yes"/>
<InstallExecuteSequence><Custom Action="LaunchUpdatedTest" After="InstallFinalize">AUTOLAUNCHAPP AND NOT REMOVE</Custom></InstallExecuteSequence>
</Product></Wix>''',encoding='utf-8')
                run([wix/'candle.exe','-nologo','-out',dest/'fixture.wixobj',script])
                run([wix/'light.exe','-nologo','-sval','-out',installer,dest/'fixture.wixobj'])
            pairs.append(installer)
        outputs.append((kind,pairs[0],pairs[1],installed/'fixture.exe'))
    return outputs


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--evidence',default='native-updater-evidence')
    args=parser.parse_args()
    evidence=Path(args.evidence).resolve();evidence.mkdir(parents=True,exist_ok=True)
    protected=ROOT/'desktop/src-tauri/updater-public-key.txt';production=protected.read_bytes()
    report={'platform':platform.system(),'production_key_changed':False,'fixture_only':True,'passed':False,'native_installs':[], 'source_commit':os.environ.get('GITHUB_SHA','local')}
    env={k:v for k,v in os.environ.items() if not k.startswith('TAURI_SIGNING_')}
    try:
        with tempfile.TemporaryDirectory(prefix='omnishare-update-test-') as directory:
            # This is a byte-exact safety marker read by Rust, not platform text.
            # Never let Windows text translation turn its LF into CRLF.
            root=Path(directory).resolve();(root/'TEST-ONLY').write_bytes(b'OmniShare ephemeral updater fixtures\n')
            for name in ('updater.key','wrong.key'):
                run(['node',CLI,'signer','generate','--ci','--password','','--write-keys',root/name],env=env,secret=True)
            signing=dict(env,TAURI_SIGNING_PRIVATE_KEY=(root/'updater.key').read_text().strip(),TAURI_SIGNING_PRIVATE_KEY_PASSWORD='')
            def sign(package, version='1.0.1'):
                command=['node',CLI,'signer','sign']
                if version:command+=['--app-version',version]
                run([*command,package],env=signing,cwd=root,secret=True)
            payload=b'OmniShare signed updater acceptance\n'*128
            (root/'payload.bin').write_bytes(payload);sign(root/'payload.bin')
            (root/'unversioned.bin').write_bytes(payload);sign(root/'unversioned.bin',None)
            old=compile_fixture(root,'1.0.0');new=compile_fixture(root,'1.0.1')
            data=root/'user-data';data.mkdir()
            for name,value in {'config.json':'{"port":8084,"key_enabled":false}',
                               'notes.json':'[{"text":"升级不丢失"}]',
                               'peers.json':'[{"id":"persistent-bound-peer"}]',
                               'file.bin':'local file remains intact'}.items():
                (data/name).write_text(value,encoding='utf-8')
            before={p.name:digest(p) for p in data.iterdir()}
            installers=[]
            if os.name=='nt':
                installers=windows_installers(root,old,new)
                initial=installers[0][3]
            elif platform.system()=='Darwin':
                _,initial=mac_app(root/'installed',old,'1.0.0')
                app,new=mac_app(root/'incoming',new,'1.0.1')
                package=root/'update.app.tar.gz'
                with tarfile.open(package,'w:gz') as tar:tar.add(app,arcname='UpdaterTest.app')
                installers=[('app-bundle',None,package,initial)]
            else:
                initial=root/'installed/Fixture.AppImage';initial.parent.mkdir();shutil.copyfile(old,initial);initial.chmod(0o755)
                package=root/'update.AppImage';shutil.copyfile(new,package)
                installers=[('elf-appimage-replacement',None,package,initial)]
            (root/'executable.txt').write_text(str(initial),encoding='utf-8')
            testenv=dict(env,OMNISHARE_UPDATER_TEST_FIXTURES=str(root))
            output=run(['cargo','test','--locked','--no-run','--message-format=json'],env=testenv,cwd=ROOT/'desktop/src-tauri',timeout=1200)
            candidates=[json.loads(line).get('executable') for line in output.splitlines() if line.startswith('{') and json.loads(line).get('reason')=='compiler-artifact' and json.loads(line).get('profile',{}).get('test')]
            binary=next(p for p in candidates if p and 'omnishare_desktop' in Path(p).name)
            signed=run([binary,'--exact',TEST_PREFIX+'signed_download_and_manifest_acceptance','--ignored','--nocapture'],env=testenv)
            (evidence/'signed-tests.log').write_text(signed,encoding='utf-8')
            report['signed_tests']=json.loads((root/'signed-acceptance.json').read_text())
            for kind,old_installer,package,installed in installers:
                if old_installer:
                    if kind=='msi':run(['msiexec','/i',old_installer,'/qn','/norestart'],env=testenv)
                    else:run([old_installer,'/S'],env=testenv)
                assert installed.is_file(),f'{kind}: previous fixture install is missing'
                run([installed],env=testenv)
                assert (root/'restarted.txt').read_text()=='1.0.0'
                (root/'restarted.txt').unlink()
                sign(package)
                (root/'install.json').write_text(json.dumps({'executable':str(installed),'package':str(package)}),encoding='utf-8')
                text=run([binary,'--exact',TEST_PREFIX+'native_install_probe','--ignored','--nocapture'],env=testenv)
                (evidence/f'{kind}.log').write_text(text,encoding='utf-8')
                if os.name!='nt':run([installed],env=testenv)
                deadline=time.monotonic()+60
                while time.monotonic()<deadline:
                    if (root/'restarted.txt').exists() and (root/'restarted.txt').read_text()=='1.0.1':break
                    time.sleep(.1)
                else:raise AssertionError(f'{kind}: updated executable did not restart with new version')
                assert digest(installed)==digest(new),f'{kind}: installed executable differs'
                assert {p.name:digest(p) for p in data.iterdir()}==before,'user data changed'
                report['native_installs'].append({'kind':kind,'old_version':'1.0.0','new_version':'1.0.1','signed':True,'restarted':True,'data_and_binding_sentinels_unchanged':True,'package_sha256':digest(package)})
                if kind=='msi':run(['msiexec','/x',package,'/qn','/norestart'],env=testenv)
            assert protected.read_bytes()==production,'Production trust anchor changed!'
            report['passed']=True
    except BaseException as error:
        report['error']=str(error)
        raise
    finally:
        report['production_key_changed']=protected.read_bytes()!=production
        (evidence/'result.json').write_text(json.dumps(report,ensure_ascii=False,indent=2)+'\n',encoding='utf-8')
    print(json.dumps(report,ensure_ascii=False))

if __name__=='__main__':main()
