#!/usr/bin/env python3
"""Publish only the explicit 0.2.0-rc.2 candidate after byte verification."""
import hashlib,json,os,re,subprocess,time
from pathlib import Path
REPO='Azurefly/omnishare';VERSION='0.2.0-rc.2';TAG='v'+VERSION
SOURCE=os.environ['SOURCE_COMMIT'];RUN=os.environ['GITHUB_RUN_ID']
root=Path('rc-assets')
def gh(*args,raw=False):
 p=subprocess.run(['gh',*args],capture_output=True,text=True)
 if p.returncode:raise RuntimeError('GitHub operation failed: '+args[0])
 return p.stdout if raw else json.loads(p.stdout)
def digest(p):
 with p.open('rb') as f:return hashlib.file_digest(f,'sha256').hexdigest()
def api(path):return gh('api',f'repos/{REPO}/{path}')
assert re.fullmatch(r'[a-f0-9]{40}',SOURCE)
assert subprocess.check_output(['git','rev-parse','HEAD'],text=True).strip()==SOURCE
assert Path('VERSION').read_text().strip()==VERSION
assert os.environ['GITHUB_REPOSITORY']==REPO and os.environ['GITHUB_REF']=='refs/heads/main'
subprocess.run(['git','merge-base','--is-ancestor',SOURCE,'origin/main'],check=True)
reports={}
files=[]
for kind in ['server','Windows','Linux','macOS']:
 p=root/f'build-evidence-{kind}.json';report=json.loads(p.read_text())
 assert report['source_commit']==SOURCE and report['version']==VERSION and str(report['workflow_run_id'])==RUN
 assert report['build_succeeded'] is True
 reports[kind]=report
 for record in report['files']:
  name=record['name'];assert name==Path(name).name and len(name)<180
  path=root/name
  assert path.is_file() and path.stat().st_size==record['size'] and digest(path)==record['sha256']
  files.append(record)
assert len(files)==9 and len({x['name'] for x in files})==9
browser=json.loads((root/'workspace-browser-results.json').read_text())
assert browser['success'] is True and len(browser['checks'])>=5
manifest={'schema_version':1,'version':VERSION,'tag':TAG,'source_commit':SOURCE,'workflow_run_id':RUN,
 'prerelease':True,'apple_notarized':False,'all_platform_native_acceptance_passed':False,
 'native_results':{k:{'outcome':v['native_installation_outcome'],'gui_verified':v['native_gui_acceptance_passed']} for k,v in reports.items()},
 'browser_workspace_verification':browser,'files':files}
(root/'build-manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2)+'\n')
notes=Path(f'docs/RELEASE_NOTES_v{VERSION}.md').read_text()
notes+='\n## 本次构建结果\n\n'
notes+=f'源码提交：`{SOURCE}`。构建与真实浏览器验证：[Actions {RUN}](https://github.com/{REPO}/actions/runs/{RUN})。\n\n'
for kind in ['Windows','Linux','macOS']:
 r=reports[kind];notes+=f"- {kind}：构建成功；原生检查结果 `{r['native_installation_outcome']}`。"
 notes+=('macOS 仅执行打包后端的 HTTP 检查，不代表原生窗口或 Gatekeeper 验收；未进行 Apple 公证。' if kind=='macOS' else '只有检查成功且报告明确覆盖的场景才计为通过。')+'\n'
notes+='\n真实 Chromium 双实例验证通过；下载附件前请阅读上面的已知限制。\n'
(root/'RELEASE_NOTES.md').write_text(notes)
allowed={x['name'] for x in files}|{f'build-evidence-{k}.json' for k in reports}|{'workspace-browser-results.json','build-manifest.json','RELEASE_NOTES.md'}
assert {p.name for p in root.iterdir() if p.is_file()}==allowed
(root/'SHA256SUMS.txt').write_text(''.join(f'{digest(root/name)}  {name}\n' for name in sorted(allowed)))
allpaths=sorted(p for p in root.iterdir() if p.is_file())
expected={p.name:(p.stat().st_size,'sha256:'+digest(p)) for p in allpaths}
existing=[]
for page in range(1,20):
 data=api(f'releases?per_page=100&page={page}');existing.extend(x for x in data if x['tag_name']==TAG)
 if len(data)<100:break
assert len(existing)<=1
# Never move or overwrite another release's tag, assets, or stable Latest.
refs=api('git/matching-refs/tags/'+TAG)
refs=[x for x in refs if x['ref']=='refs/tags/'+TAG]
if refs:assert len(refs)==1 and refs[0]['object']['type']=='commit' and refs[0]['object']['sha']==SOURCE
else:gh('api','--method','POST',f'repos/{REPO}/git/refs','-f','ref=refs/tags/'+TAG,'-f','sha='+SOURCE)
if existing:
 release=existing[0];assert release['prerelease'] is True
else:
 # Create via REST and use the returned draft ID, rather than public tag lookup.
 payload={'tag_name':TAG,'target_commitish':SOURCE,'name':'OmniShare '+TAG+' · 协同稳定性修复候选版','body':notes,'draft':True,'prerelease':True,'make_latest':'false'}
 Path('/tmp/workspace-release.json').write_text(json.dumps(payload))
 release=gh('api','--method','POST',f'repos/{REPO}/releases','--input','/tmp/workspace-release.json')
assets=api(f"releases/{release['id']}/assets?per_page=100")
for item in assets:
 assert item['name'] in expected and (item['size'],item.get('digest'))==expected[item['name']], 'Existing asset differs; refusing overwrite'
seen={a['name'] for a in assets}
for p in allpaths:
 if p.name not in seen:gh('release','upload',TAG,str(p),'--repo',REPO,raw=True)
assets=api(f"releases/{release['id']}/assets?per_page=100")
assert {a['name'] for a in assets}==set(expected)
for item in assets:assert (item['size'],item.get('digest'))==expected[item['name']]
payload={'body':notes,'draft':False,'prerelease':True,'make_latest':'false'}
Path('/tmp/workspace-release.json').write_text(json.dumps(payload))
release=gh('api','--method','PATCH',f"repos/{REPO}/releases/{release['id']}",'--input','/tmp/workspace-release.json')
assert release['draft'] is False and release['prerelease'] is True
print(json.dumps({'release':release['html_url'],'assets':len(assets),'source_commit':SOURCE,'verified':True}))
