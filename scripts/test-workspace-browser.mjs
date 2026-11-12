import assert from 'node:assert/strict';
import {mkdtemp, mkdir, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createServer} from 'node:net';
import {chromium} from '../frontend/node_modules/playwright/index.mjs';

const binary = resolve(process.env.OMNISHARE_BIN || process.argv[2] || 'dist/omnishare-test');
const output=resolve(process.env.WORKSPACE_EVIDENCE || 'frontend/test-results/workspace');
await mkdir(output,{recursive:true});
const root=await mkdtemp(join(tmpdir(),'omnishare-workspace-'));
const children=[], checks=[], errors=[];
let browser, page;
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function freePort(){const s=createServer();s.listen(0,'127.0.0.1');await once(s,'listening');const p=s.address().port;await new Promise(r=>s.close(r));return p;}
async function api(base,path,method='GET',data){const r=await fetch(base+path,{method,headers:{'Content-Type':'application/json'},body:data===undefined?undefined:JSON.stringify(data)});const body=await r.json();if(!r.ok||body.code!==0)throw new Error(`${method} ${path}: ${r.status}`);return body;}
async function start(name){const port=await freePort(),base=`http://127.0.0.1:${port}`;const child=spawn(binary,['--port',String(port),'--name',name,'--data-dir',join(root,name),'--listen','127.0.0.1','--no-browser','--shutdown-on-stdin-close'],{stdio:['pipe','ignore','ignore']});children.push(child);for(let i=0;i<100;i++){try{await api(base,'/api/v1/health');return base;}catch{await delay(100);}}throw new Error('test server failed to start');}
async function waitText(page,selector,value){await page.waitForFunction(({selector,value})=>document.querySelector(selector)?.value===value,{selector,value},{timeout:12000});}
try {
 const a=await start('工作站 A'), b=await start('Mac B');
 await api(a,'/api/v1/notes','POST',{content:'A 本机上的随手记'});
 await api(b,'/api/v1/notes','POST',{content:'另一台设备上的随手记'});
 browser=await chromium.launch();
 const context=await browser.newContext({viewport:{width:1440,height:1000}});
 page=await context.newPage(); page.on('pageerror',e=>errors.push(String(e)));
 await page.goto(a,{waitUntil:'networkidle'});
 await page.locator('#bindDeviceBtn').click();await page.locator('#bindPeerURL').fill(b);await page.locator('#bindPeerSubmitBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#bindPeerDialog').open,null,{timeout:25000});
 await page.locator('.tab[data-tab="notes"]').click();
 await page.getByText('另一台设备上的随手记',{exact:false}).first().waitFor({timeout:15000});
 assert.equal(new URL(page.url()).origin,a);
 const devices=(await api(b,'/api/v1/devices')).data;
 assert.ok(devices.some(d=>d.bound&&!d.is_local));checks.push('one-sided binding creates reciprocal device registration and merged notes');
 const other=await context.newPage();other.on('pageerror',e=>errors.push(String(e)));
 await other.goto(b,{waitUntil:'networkidle'});await other.locator('.tab[data-tab="notes"]').click();
 await other.getByText('A 本机上的随手记',{exact:false}).first().waitFor({timeout:15000});
 assert.equal(new URL(other.url()).origin,b);checks.push('B also displays A content in its own workspace');
 await other.close();await page.bringToFront();
 await page.locator('.tab[data-tab="pads"]').click();await page.locator('#newPadBtn').click();await page.locator('#newPadTitle').fill('跨设备协同验证');await page.locator('#createPadSubmitBtn').click();
 await waitText(page,'#padTitle','跨设备协同验证');checks.push('in-page create dialog works without window.prompt');
 const bpad=(await api(b,'/api/v1/pads','POST',{title:'B 上的文档',content:'initial'})).data;
 await page.getByText('B 上的文档',{exact:true}).waitFor({timeout:12000});await page.getByText('B 上的文档',{exact:true}).click();
 await waitText(page,'#padContent','initial');
 await page.locator('#padContent').fill('从 A 编辑，保存到 B');await page.locator('#savePadBtn').click();
 await page.waitForFunction(()=>document.querySelector('#saveState').textContent.includes('已保存'));
 let current=(await api(b,`/api/v1/pads/${bpad.id}`)).data;assert.equal(current.content,'从 A 编辑，保存到 B');checks.push('editing a remote document in A writes to B without page navigation');
 await api(b,`/api/v1/pads/${bpad.id}`,'PUT',{title:current.title,content:'B 的最新更新',version:current.version});
 await waitText(page,'#padContent','B 的最新更新');checks.push('open editor refreshes remote saves');
 current=(await api(b,`/api/v1/pads/${bpad.id}`)).data;
 await page.locator('#padContent').fill('本地未保存草稿');
 await api(b,`/api/v1/pads/${bpad.id}`,'PUT',{title:current.title,content:'远端冲突版本',version:current.version});
 await page.waitForFunction(()=>document.querySelector('#saveState').textContent.includes('冲突')||document.querySelector('#saveState').textContent.includes('远端已更新'));
 assert.equal(await page.locator('#padContent').inputValue(),'本地未保存草稿');checks.push('concurrent edits preserve local draft and report version conflict');
 await page.screenshot({path:join(output,'unified-workspace-desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});await page.locator('.tab[data-tab="notes"]').click();
 await page.screenshot({path:join(output,'unified-workspace-mobile.png'),fullPage:true});
 assert.deepEqual(errors,[]);
 await writeFile(join(output,'results.json'),JSON.stringify({success:true,checks,scope:'two real SQLite backend processes and Chromium',native_macos_verified:false},null,2));
 console.log(JSON.stringify({success:true,checks}));
} catch(e) {
 if(page)await page.screenshot({path:join(output,'failure.png'),fullPage:true}).catch(()=>{});
 await writeFile(join(output,'results.json'),JSON.stringify({success:false,checks,error:String(e),errors},null,2));throw e;
} finally {
 if(browser)await browser.close();
 for(const child of children){child.stdin.end();const ended=once(child,'exit');await Promise.race([ended,delay(10000)]);if(child.exitCode===null)child.kill('SIGTERM');}
 await rm(root,{recursive:true,force:true});
}
