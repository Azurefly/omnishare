import assert from 'node:assert/strict';
import {mkdtemp, mkdir, readFile, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createConnection, createServer} from 'node:net';
import {chromium} from '../frontend/node_modules/playwright/index.mjs';

const binary = resolve(process.env.OMNISHARE_BIN || process.argv[2] || 'dist/omnishare-test');
const bindingOnly = process.argv.includes('--binding-only');
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

async function checkDiscoveredBinding(page, a, b) {
 const remote = (await api(b, '/api/v1/peers/identity')).data;
 await api(a, '/api/v1/peers/' + remote.id, 'DELETE');
 const relay = createServer(client => {
   const upstream = createConnection({host:'127.0.0.1', port:Number(new URL(b).port)});
   client.on('error', () => upstream.destroy());
   upstream.on('error', () => client.destroy());
   client.on('close', () => upstream.destroy());
   upstream.on('close', () => client.destroy());
   client.pipe(upstream).pipe(client);
 });
 relay.listen(0, '127.0.0.1');
 await once(relay, 'listening');
 const primary = 'http://127.0.0.1:' + relay.address().port;
 try {
   await api(a, '/api/v1/config', 'PUT', {allow_lan:false, keep_access_key:true, peers:[{name:remote.name, url:primary}]});
   await page.goto(a, {waitUntil:'networkidle'});
   const card = page.locator(`[data-device-id="${remote.id}"]`);
   await card.locator('.bind-device').click();
   assert.equal(await page.locator('#bindPeerURL').inputValue(), primary + '/');
   // The signed identity came from B. Now remove only the route displayed when the form opened.
   const closed = once(relay, 'close');
   relay.close();
   await closed;
   await page.locator('#bindPeerSubmitBtn').click();
   await page.locator('#bindPeerDialog').waitFor({state:'hidden', timeout:25000});
   await card.locator('.unbind-device').waitFor();
   const local = (await api(a, '/api/v1/peers/identity')).data;
   const peer = (await api(a, '/api/v1/devices')).data.find(d => d.id === remote.id);
   assert.equal(new URL(peer.url).origin, b);
   assert.ok(peer.bound && (await api(b, '/api/v1/devices')).data.some(d => d.id === local.id && d.bound));
   checks.push('a discovered device binds reciprocally after its displayed route closes');
 } finally {
   if (relay.listening) relay.close();
   await api(a, '/api/v1/config', 'PUT', {allow_lan:false, keep_access_key:true, peers:[]});
 }
}
async function assertWritingViewport(page, sourceSelector, saveSelector, minSourceHeight) {
 const {source,save,height}=await page.evaluate(({sourceSelector,saveSelector}) => {
   const rect=selector=>document.querySelector(selector).getBoundingClientRect().toJSON();
   return {source:rect(sourceSelector),save:rect(saveSelector),height:innerHeight};
 }, {sourceSelector,saveSelector});
 assert.ok(source.height>=minSourceHeight && source.top>=0 && source.bottom<=height,
   `Expected a visible writing area of at least ${minSourceHeight}px, got ${JSON.stringify(source)} in ${height}px`);
 assert.ok(save.top>=0 && save.bottom<=height && save.height>0, 'The save action must be visible without scrolling');
}

async function checkBackupAndRecovery(base) {
 const context = await browser.newContext();
 try {
   // Exercise the actual download fallback used by browsers without a save picker.
   await context.addInitScript(() => Object.defineProperty(window, 'showSaveFilePicker', {value:undefined}));
   const client = await context.newPage();
   client.on('pageerror', error => errors.push(String(error)));
   await client.goto(base + '/icon.svg');
   await client.evaluate(() => caches.open('other-application-assets').then(() => true));
   await client.goto(base);
   await client.waitForFunction(() => !!navigator.serviceWorker.controller);
   assert.ok(await client.evaluate(() => caches.has('other-application-assets')), 'Activation removed an unrelated cache');
   await client.locator('#settingsBtn').click();
   await client.locator('#backupBtn').click();
   await client.locator('#backupFormat').selectOption('osb');
   const passphrase = '  中文备份 🔑 café  ';
   await client.locator('#backupPassword').fill(passphrase);
   let backupRequests = 0;
   client.on('request', req => { if (new URL(req.url()).pathname === '/api/v1/backup') backupRequests++; });
   await client.locator('#backupDialog').getByRole('button', {name:'取消', exact:true}).click();
   assert.equal(backupRequests, 0);
   await client.locator('#backupBtn').click();
   await client.locator('#backupFormat').selectOption('osb');
   assert.equal(await client.locator('#backupPassword').inputValue(), '');
   await client.locator('#backupPassword').fill(passphrase);
   const downloading = client.waitForEvent('download');
   await client.locator('#backupForm').getByRole('button', {name:'导出备份', exact:true}).click();
   const downloaded = await downloading;
   assert.match(downloaded.suggestedFilename(), /\.osb$/);
   const data = await readFile(await downloaded.path());
   assert.equal(data.subarray(0, 16).toString(), 'OMNISHARE-ENC-BK');
   assert.equal(backupRequests, 1);
   const originalNotes = (await api(base, '/api/v1/notes')).data.map(n => n.id).sort();
   const extra = (await api(base, '/api/v1/notes', 'POST', {content:'备份后的临时数据'})).data;
   await client.locator('#restoreBtn').click();
   await client.locator('#restoreFile').setInputFiles({name:'回归.osb',mimeType:'application/octet-stream',buffer:data});
   await client.locator('#restorePassword').fill(passphrase.trim());
   await client.locator('#restoreConfirm').check();
   await client.locator('#restoreSubmitBtn').click();
   await client.locator('#restoreStatus').filter({hasText:'口令不正确'}).waitFor();
   assert.ok((await api(base, '/api/v1/notes')).data.some(n => n.id === extra.id));
   await client.locator('#restorePassword').fill(passphrase);
   await client.locator('#restoreSubmitBtn').click();
   await client.locator('#restoreDialog').waitFor({state:'hidden'});
   assert.deepEqual((await api(base, '/api/v1/notes')).data.map(n => n.id).sort(), originalNotes);
   checks.push('an encrypted backup downloaded in Chromium restores with the exact Unicode passphrase');

   await context.setOffline(true);
   await client.reload({waitUntil:'domcontentloaded'});
   await client.locator('[data-workbench-ready="1"]').waitFor();
   await client.waitForFunction(() => window.__omnishareRuntimeBridge?.isOffline());
   await client.locator('.tab[data-tab="notes"]').click();
   await client.locator('#openCreateNoteBtn').click();
   await client.locator('#noteContent').fill('恢复连接期间继续编辑');
   await context.setOffline(false);
   await client.waitForFunction(() => window.__omnishareRuntimeBridge?.isOffline() === false);
   assert.equal(await client.locator('#noteContent').inputValue(), '恢复连接期间继续编辑');
   assert.ok((await client.locator('#createNoteDialog').isVisible()));
   checks.push('the cached workbench reopens offline and preserves the open editor when the backend returns');
 } finally {
   await context.close();
 }
}

async function exerciseWorkspace() {
 const a=await start('工作站 A'), b=await start('Mac B');
 await api(a,'/api/v1/notes','POST',{content:'A 本机上的随手记'});
 await api(b,'/api/v1/notes','POST',{content:'另一台设备上的随手记'});
 browser=await chromium.launch({...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? {executablePath:process.env.PLAYWRIGHT_EXECUTABLE_PATH} : {})});
 const context=await browser.newContext({viewport:{width:1440,height:1000},serviceWorkers:'block'});
 page=await context.newPage(); page.on('pageerror',e=>errors.push(String(e)));
 await page.goto(a,{waitUntil:'networkidle'});
 await page.waitForFunction(() => document.querySelector('#nodeSecuritySummary').textContent.includes('仅本机访问'));
 await page.locator('#tailscaleStatus').filter({hasText:'自动发现已关闭'}).waitFor();
 checks.push('a loopback-only node reports local-only access');
 await page.locator('#bindDeviceBtn').click();
 await page.locator('#bindPeerURL').fill(b);
 const bindWrites = [];
 const captureBind = request => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/v1/peers/bind') bindWrites.push(request.url()); };
 page.on('request', captureBind);
 await page.locator('#bindPeerDialog').getByRole('button', {name:'关闭', exact:true}).click();
 await page.locator('#bindPeerDialog').waitFor({state:'hidden'});
 page.off('request', captureBind);
 assert.equal(bindWrites.length, 0, 'Closing the binding dialog must not send a binding request');
 checks.push('closing the binding dialog before confirmation sends no request');
 await page.locator('#bindDeviceBtn').click();await page.locator('#bindPeerURL').fill(b);await page.locator('#bindPeerSubmitBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#bindPeerDialog').open,null,{timeout:25000});
 await page.locator('.tab[data-tab="notes"]').click();
 await page.getByText('另一台设备上的随手记',{exact:false}).first().waitFor({timeout:15000});
 assert.equal(new URL(page.url()).origin,a);
 const devices=(await api(b,'/api/v1/devices')).data;
 assert.ok(devices.some(d=>d.bound&&!d.is_local));checks.push('one-sided binding creates reciprocal device registration and merged notes');
 await checkDiscoveredBinding(page, a, b);
 await page.locator('.device').filter({has:page.locator('.unbind-device')}).locator('.rename-device').click();
 await page.locator('#deviceAliasInput').fill('unsaved alias');
 await page.locator('#deviceAliasDialog').getByRole('button', {name:'关闭', exact:true}).click();
 await page.locator('#deviceAliasDialog').waitFor({state:'hidden'});
 assert.ok(!(await api(a, '/api/v1/devices')).data.some(d => d.alias === 'unsaved alias'));
 await page.locator('.tab[data-tab="pads"]').click();
 await page.locator('#newPadBtn').click();
 await page.locator('#newPadTitle').fill('unsaved document');
 await page.locator('#newPadDialog').getByRole('button', {name:'关闭', exact:true}).click();
 await page.locator('#newPadDialog').waitFor({state:'hidden'});
 assert.ok(!(await api(a, '/api/v1/pads?scope=all')).data.some(d => d.title === 'unsaved document'));
 checks.push('closing alias and new document dialogs preserves data');
 if (bindingOnly) return;
 await page.locator('.tab[data-tab="notes"]').click();
 // Folder workflow in unified workspace: create folder, create note in folder, create empty folder, verify cross-device visibility & filter stability.
 await page.locator('#notesPanel .create-folder-btn').click();
 await page.locator('#folderModalDialog[open]').waitFor({timeout:5000});
 await page.locator('#folderModalPath').fill('项目/方案');
 await page.locator('#submitFolderModalBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#folderModalDialog').open);
 await page.locator('#openCreateNoteBtn').click();
 await page.locator('#createNoteDialog[open]').waitFor({timeout:5000});
 const noteSource=page.locator('#noteContent');
 const noteEditor=page.locator('#createNoteMarkdownEditor');
 await noteSource.fill('目录归档随手记验证');
 await noteSource.press('ControlOrMeta+a');
 await noteEditor.getByRole('button',{name:'加粗',exact:true}).click();
 assert.equal(await noteSource.inputValue(),'**目录归档随手记验证**');
 await noteSource.press('ControlOrMeta+z');
 assert.equal(await noteSource.inputValue(),'目录归档随手记验证');
 await noteSource.press('ControlOrMeta+a');
 await noteSource.press('ControlOrMeta+b');
 await noteEditor.getByRole('button',{name:'分栏',exact:true}).click();
 await noteEditor.locator('.markdown-preview strong').getByText('目录归档随手记验证').waitFor();
 await noteSource.dispatchEvent('keydown',{key:'Enter',ctrlKey:true,isComposing:true});
 assert.ok(await page.locator('#createNoteDialog').isVisible());
 await page.locator('#createNoteBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#createNoteDialog').open);
 await page.getByText('目录归档随手记验证',{exact:false}).first().waitFor({timeout:12000});
 await page.locator('#notesPanel .create-folder-btn').click();
 await page.locator('#folderModalDialog[open]').waitFor({timeout:5000});
 await page.locator('#folderModalPath').fill('跨设备空目录');
 await page.locator('#submitFolderModalBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#folderModalDialog').open);
 await page.locator('#notesFolderTree [data-folder=""]').click();
 checks.push('notes folder create, publish, and tree filter work in Chromium');
 const other=await context.newPage();other.on('pageerror',e=>errors.push(String(e)));
 await other.goto(b,{waitUntil:'networkidle'});await other.locator('.tab[data-tab="notes"]').click();
 await other.getByText('A 本机上的随手记',{exact:false}).first().waitFor({timeout:15000});
 await other.locator('#notesFolderTree [data-folder="跨设备空目录"]').waitFor({timeout:12000});
 await other.locator('#notesFolderTree [data-folder="项目/方案"]').waitFor({timeout:12000});
 assert.equal((await other.locator('#notesFolderTree [data-folder="项目/方案"] .folder-count').textContent()).trim(), '1');
 assert.equal((await other.locator('#notesFolderTree [data-folder="跨设备空目录"] .folder-count').textContent()).trim(), '0');
 await other.locator('#notesFolderTree [data-folder="项目/方案"]').click();
 await other.getByText('目录归档随手记验证',{exact:false}).first().waitFor({timeout:12000});
 // Clicking a folder filter on machine B must keep other remote/empty folders and counts visible.
 await other.locator('#notesFolderTree [data-folder="跨设备空目录"]').waitFor({timeout:5000});
 assert.equal((await other.locator('#notesFolderTree [data-folder="项目/方案"] .folder-count').textContent()).trim(), '1');
 await other.locator('#notesFolderTree [data-folder=""]').click();
 assert.equal(new URL(other.url()).origin,b);checks.push('B sees empty and non-empty folders created on A with stable counts when filtering');
 await other.close();await page.bringToFront();
 await page.setViewportSize({width:960,height:640});
 await page.locator('.note-card').filter({hasText:'目录归档随手记验证'}).locator('.edit-note').click();
 const editEditor=page.locator('#editNoteMarkdownEditor');
 const editSource=page.locator('#editNoteContent');
 await page.locator('#editNoteDialog.is-writing-expanded').waitFor();
 await assertWritingViewport(page,'#editNoteContent','#saveEditNoteBtn',365);
 await editSource.fill('目录归档随手记验证\n\n上线准备');
 await page.keyboard.press('Escape');
 await page.locator('#confirmDialog[open]').waitFor();
 await page.locator('#confirmCancelBtn').click();
 assert.equal(await editSource.inputValue(),'目录归档随手记验证\n\n上线准备');
 assert.ok(await page.locator('#editNoteDialog').isVisible());
 await editEditor.getByRole('button',{name:'待办清单',exact:true}).click();
 await editEditor.getByRole('button',{name:'预览',exact:true}).click();
 await editEditor.locator('.markdown-task').getByText('上线准备').waitFor();
 assert.ok(await editSource.isHidden());
 await page.keyboard.press('ControlOrMeta+s');
 await page.locator('#editNoteDialog').waitFor({state:'hidden'});
 assert.ok((await api(a,'/api/v1/notes?scope=local')).data.some(n=>n.content.endsWith('- [ ] 上线准备')));
 checks.push('shared note editor formats, previews, supports undo and saves Markdown without intercepting IME');
 await page.setViewportSize({width:1440,height:1000});
 await page.locator('.tab[data-tab="pads"]').click();await page.locator('#newPadBtn').click();await page.locator('#newPadTitle').fill('跨设备协同验证');await page.locator('#createPadSubmitBtn').click();
 await waitText(page,'#padTitle','跨设备协同验证');checks.push('in-page create dialog works without window.prompt');
 const bpad=(await api(b,'/api/v1/pads','POST',{title:'B 上的文档',content:'initial'})).data;
 await page.locator('#backToPadsListBtn').click();
 await page.getByText('B 上的文档',{exact:true}).waitFor({timeout:12000});await page.getByText('B 上的文档',{exact:true}).click();
 await waitText(page,'#padContent','initial');
 const padEditor=page.locator('#padMarkdownEditor');
 await padEditor.getByRole('button',{name:'分栏',exact:true}).click();
 await page.locator('#padFolderOptionsMenu summary').click();
 await page.locator('#padFolder').fill('测试/协作');
 await page.locator('#padFolder').press('Escape');
 assert.equal(await page.locator('#padFolderOptionsMenu[open]').count(),0);
 await page.locator('#padContent').fill('从 A 编辑，保存到 B');
 await padEditor.locator('.markdown-preview').getByText('从 A 编辑，保存到 B').waitFor();
 await page.locator('#padContent').press('ControlOrMeta+s');
 await page.waitForFunction(()=>document.querySelector('#saveState').textContent.includes('已保存'));
 let current=(await api(b,`/api/v1/pads/${bpad.id}`)).data;assert.equal(current.content,'从 A 编辑，保存到 B');assert.equal(current.folder,'测试/协作');checks.push('editing a remote document in A writes to B without page navigation');
 await api(b,`/api/v1/pads/${bpad.id}`,'PUT',{title:current.title,content:'B 的最新更新',version:current.version});
 await waitText(page,'#padContent','B 的最新更新');
 await padEditor.locator('.markdown-preview').getByText('B 的最新更新').waitFor();
 assert.equal(await padEditor.getAttribute('data-mode'),'split');
 checks.push('open document keeps split mode while live preview follows remote saves');
 await page.setViewportSize({width:960,height:640});
 await page.locator('#padEditorCard.is-writing-expanded').waitFor();
 await assertWritingViewport(page,'#padContent','#savePadBtn',340);
 await page.setViewportSize({width:390,height:460});
 await padEditor.getByRole('button',{name:'写作',exact:true}).click();
 await assertWritingViewport(page,'#padContent','#savePadBtn',225);
 const formatToggle=padEditor.getByRole('button',{name:'格式',exact:true});
 await formatToggle.click();
 const compactButtons=await padEditor.locator('.markdown-format').evaluate(toolbar => {
   const right=toolbar.getBoundingClientRect().right;
   return [...toolbar.querySelectorAll('button')].every(button => {
     const bounds=button.getBoundingClientRect();
     return bounds.width>0 && bounds.right<=right;
   });
 });
 assert.ok(compactButtons, 'Compact format controls should wrap into view');
 await formatToggle.click();
 await padEditor.getByRole('button',{name:'分栏',exact:true}).click();
 const splitFits=await padEditor.locator('.markdown-preview').evaluate(preview =>
   preview.getBoundingClientRect().bottom<=preview.parentElement.getBoundingClientRect().bottom+1);
 assert.ok(splitFits, 'The split preview should fit the visible sheet');
 await padEditor.getByRole('button',{name:'写作',exact:true}).click();
 await page.setViewportSize({width:720,height:390});
 await assertWritingViewport(page,'#padContent','#savePadBtn',165);
 await page.setViewportSize({width:1440,height:1000});
 await padEditor.getByRole('button',{name:'铺满窗口',exact:true}).click();
 assert.equal(await page.locator('#padEditorCard.is-writing-expanded').count(),1);
 await padEditor.getByRole('button',{name:'还原窗口',exact:true}).click();
 assert.equal(await page.locator('#padEditorCard.is-writing-expanded').count(),0);
 assert.equal(await page.locator('#padContent').inputValue(),'B 的最新更新');
 current=(await api(b,`/api/v1/pads/${bpad.id}`)).data;
 await page.locator('#padContent').fill('本地未保存草稿');
 await api(b,`/api/v1/pads/${bpad.id}`,'PUT',{title:current.title,content:'远端冲突版本',version:current.version});
 await page.waitForFunction(()=>document.querySelector('#saveState').textContent.includes('冲突')||document.querySelector('#saveState').textContent.includes('远端已更新'));
 assert.equal(await page.locator('#padContent').inputValue(),'本地未保存草稿');checks.push('concurrent edits preserve local draft and report version conflict');
 await page.screenshot({path:join(output,'unified-workspace-desktop.png'),fullPage:true});
 await page.locator('#deletePadBtn').click();
 await page.locator('#confirmDialog[open]').waitFor({timeout:5000});
 await page.locator('#confirmSubmitBtn').click();
 await page.waitForFunction(()=>!document.querySelector('#confirmDialog').open && document.querySelector('#padForm').classList.contains('hidden'));
 const bPadsAfterDelete=(await api(b,'/api/v1/pads')).data;
 assert.equal(bPadsAfterDelete.length,0);checks.push('visible bound device can delete a remote collaborative pad via in-app confirm');
 await page.setViewportSize({width:390,height:460});
 await page.locator('.tab[data-tab="notes"]').click();
 await page.locator('#openCreateNoteBtn').click();
 await page.locator('#createNoteDialog.is-writing-expanded').waitFor();
 await page.locator('#noteContent').fill('小窗口输入验证');
 await assertWritingViewport(page,'#noteContent','#createNoteBtn',235);
 await page.locator('#createNoteDialog summary').click();
 await page.locator('#noteTTL').selectOption('3600');
 await page.locator('#closeCreateNoteBtn').click();
 await page.locator('#confirmDialog[open]').waitFor();
 await page.locator('#confirmSubmitBtn').click();
 await page.locator('#createNoteDialog').waitFor({state:'hidden'});
 await page.locator('#openCreateNoteBtn').click();
 assert.equal(await page.locator('#noteContent').inputValue(),'');
 assert.equal(await page.locator('#noteTTL').inputValue(),'0');
 await page.locator('#cancelCreateNoteBtn').click();
 await page.locator('#createNoteDialog').waitFor({state:'hidden'});
 checks.push('dirty notes offer continue editing or discard, and new notes reset options');
 checks.push('compact note and document editors keep enough space to type and keep the save action in view');
 await page.setViewportSize({width:390,height:844});await page.locator('.tab[data-tab="notes"]').click();
 await page.screenshot({path:join(output,'unified-workspace-mobile.png'),fullPage:true});
 const remoteNote=page.locator('.note-card').filter({hasText:'另一台设备上的随手记'});
 assert.ok(await remoteNote.locator('.share-note').isDisabled());
 await page.locator('.tab[data-tab="shares"]').click();
 await page.getByRole('heading',{name:'公开交付未启用'}).waitFor();
 await page.locator('#deliverySettingsBtn').click();
 const deliverySettings=page.locator('#deliverySettingsDialog[open]');
 await deliverySettings.waitFor();
 assert.equal(await deliverySettings.getByLabel('服务器 HTTPS 地址').inputValue(),'');
 await deliverySettings.getByLabel('发布设备名').waitFor();
 await deliverySettings.getByLabel('发布密钥').waitFor();
 const advanced=deliverySettings.locator('#deliveryAdvancedSettings');
 assert.equal(await advanced.getAttribute('open'),null);
 await page.setViewportSize({width:390,height:460});
 await advanced.getByText('高级设置').click();
 assert.equal(await deliverySettings.getByLabel('单独的管理地址').inputValue(),'');
 const bounds=await deliverySettings.getByRole('button',{name:'保存并连接'}).boundingBox();
 assert.ok(bounds && bounds.y>=0 && bounds.y+bounds.height<=460,'Save remains visible with advanced settings expanded');
 await deliverySettings.getByRole('button',{name:'取消'}).click();
 await page.setViewportSize({width:390,height:844});
 await page.locator('.tab[data-tab="notes"]').click();
 checks.push('public delivery stays disabled until this device configures a server');

 await page.setViewportSize({width:1440,height:1000});
 const sample = 'file reference verification';
 const upload = new FormData();
 upload.append('file',new Blob([sample]),'跨设备引用验收.txt');
 const posted = await fetch(b+'/api/v1/files/upload',{method:'POST',body:upload});
 assert.equal(posted.status,201);
 const original = (await posted.json()).data;
 const name='跨设备引用验收.txt';
 await page.locator('#openCreateNoteBtn').click();
 await page.locator('#noteContent').fill('验收附件\n\n');
 const createEditor=page.locator('#createNoteMarkdownEditor');
 await createEditor.getByRole('button',{name:'引用工作区文件'}).click();
 await page.locator('#workspaceFilePicker').getByRole('button',{name:new RegExp(name)}).click();
 await createEditor.getByRole('button',{name:'预览',exact:true}).click();
 await createEditor.locator('.md-workspace-file').getByText(name,{exact:false}).waitFor();
 await page.locator('#createNoteBtn').click();
 await page.locator('#createNoteDialog').waitFor({state:'hidden'});
 const noteReference=page.locator('.note-card').filter({hasText:'验收附件'}).locator('.md-workspace-file');
 await noteReference.waitFor();
 await page.locator('.tab[data-tab="pads"]').click();
 await page.locator('.pad-item').filter({hasText:'跨设备协同验证'}).click();
 const documentEditor=page.locator('#padMarkdownEditor');
 await documentEditor.getByRole('button',{name:'引用工作区文件'}).click();
 await page.locator('#workspaceFilePicker').getByRole('button',{name:new RegExp(name)}).click();
 await documentEditor.getByRole('button',{name:'预览',exact:true}).click();
 await documentEditor.locator('.md-workspace-file').waitFor();
 await page.locator('#savePadBtn').click();
 await page.waitForFunction(()=>document.querySelector('#saveState').textContent.includes('已保存'));
 await page.locator('#backToPadsListBtn').click();
 await page.locator('.tab[data-tab="files"]').click();
 const remoteFile=page.locator('.file-card').filter({hasText:name});
 let moveRequests = 0;
 await page.route('**/api/v1/file-transfers', async route => {
   moveRequests++;
   // The peer really moves and verifies the file. Hold the response longer
   // than the former UI deadline to exercise a slow acknowledgement.
   const result = await route.fetch();
   await delay(9500);
   await route.fulfill({response:result});
 });
 await remoteFile.getByRole('button',{name:'转移到本机'}).click();
 await page.locator('#confirmSubmitBtn').click();
 await page.locator('#toast').filter({hasText:'文件已转移到本机'}).waitFor({timeout:20000});
 assert.equal(moveRequests, 1);
 checks.push('a file move with a delayed acknowledgement completes in the UI without resubmission');
 await page.waitForFunction(name => [...document.querySelectorAll('.file-card')].some(card =>
   card.textContent.includes(name) && !card.querySelector('.transfer-file')),name);
 assert.deepEqual((await api(b,'/api/v1/files?scope=local')).data,[]);
 await assert.rejects(readFile(join(root,'Mac B',original.storage_path)),{code:'ENOENT'});
 const [local]=(await api(a,'/api/v1/files?scope=local')).data;
 assert.equal(await readFile(join(root,'工作站 A',local.storage_path),'utf8'),sample);
 await page.locator('.tab[data-tab="notes"]').click();
 const downloadPromise=page.waitForEvent('download');
 await noteReference.click();
 const download=await downloadPromise;
 assert.equal(download.suggestedFilename(),name);
 assert.equal(await readFile(await download.path(),'utf8'),sample);
 checks.push('note and document references share the picker; a real file move removes the source and preserves the note reference');
 await checkBackupAndRecovery(a);
}

try {
 await exerciseWorkspace();
 assert.deepEqual(errors,[]);
 const report = {success:true, checks, platform:process.platform, scope:'two real folder-backed processes and Chromium',
   source_commit:process.env.SOURCE_COMMIT, workflow_run_id:process.env.GITHUB_RUN_ID, native_macos_verified:false};
 await writeFile(join(output,'results.json'),JSON.stringify(report,null,2));
 console.log(JSON.stringify({success:true,checks}));
} catch(e) {
 if(page)await page.screenshot({path:join(output,'failure.png'),fullPage:true}).catch(()=>{});
 await writeFile(join(output,'results.json'),JSON.stringify({success:false,checks,error:String(e),errors},null,2));throw e;
} finally {
 if(browser)await browser.close();
 for(const child of children){child.stdin.end();const ended=once(child,'exit');await Promise.race([ended,delay(10000)]);if(child.exitCode===null)child.kill('SIGTERM');}
 await rm(root,{recursive:true,force:true});
}
