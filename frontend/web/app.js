const $ = (selector) => document.querySelector(selector)
const $$ = (selector) => [...document.querySelectorAll(selector)]
const state = {
  activeTab: 'notes', notes: [], files: [], pads: [], shares: [], trash: [], audits: [], devices: [], stats: {}, config: null,
  currentPad: null, currentPadDirty: false, saveTimer: null, saveInFlight: false, queuedPadSave: null,
  accessKey: sessionStorage.getItem('omnishare.accessKey') || '', deferredInstall: null, editingNote: null,
  uploadQueue: [], activeUploads: 0, requestSeq: {notes:0, files:0, pads:0, devices:0}, devicesLoading: false, padSaveChain: Promise.resolve()
}

const escapeHTML = (value = '') => String(value).replace(/[&<>'"]/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[ch]))
const formatBytes = (bytes = 0) => { if (!bytes) return '0 B'; const units=['B','KB','MB','GB','TB']; const i=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),units.length-1); return `${(bytes/1024**i).toFixed(i ? 1 : 0)} ${units[i]}` }
const formatDate = (value) => value ? new Date(value).toLocaleString() : '—'
const debounce = (fn, wait=250) => { let t; return (...args) => { clearTimeout(t); t=setTimeout(()=>fn(...args),wait) } }
const toast = (message, error=false) => { const el=$('#toast'); el.textContent=message; el.className=`toast show${error?' error':''}`; clearTimeout(el._timer); el._timer=setTimeout(()=>el.className='toast',2600) }
const headers = (extra={}) => ({ ...(state.accessKey ? {'X-OmniShare-Key':state.accessKey} : {}), ...extra })

async function api(path, options={}) {
  const res = await fetch(path, {...options, headers: headers(options.headers || {})})
  if (res.status === 401) {
    showAuth()
    throw new Error('需要访问密钥')
  }
  const type = res.headers.get('content-type') || ''
  if (!type.includes('application/json')) {
    if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
    return res
  }
  const payload = await res.json()
  if (!res.ok || payload.code !== 0) { const err=new Error(payload.message || `HTTP ${res.status}`);err.status=res.status;err.data=payload.data;throw err }
  return payload.data
}

function showAuth() {
  if (!$('#authDialog').open) $('#authDialog').showModal()
  $('#authKey').focus()
}

async function verifyAuth() {
  const key = $('#authKey').value.trim()
  try {
    const res = await fetch('/api/v1/auth/verify', {method:'POST', headers:{'X-OmniShare-Key':key}})
    if (!res.ok) throw new Error('密钥不正确')
    state.accessKey = key
    sessionStorage.setItem('omnishare.accessKey', key)
    $('#authError').textContent = ''
    $('#authDialog').close()
    await loadAll()
  } catch (err) { $('#authError').textContent = err.message }
}

async function loadAll() {
  const core = await Promise.allSettled([loadHealth(), loadConfig()])
  if (core.some(result => result.status === 'rejected')) return
  const rest = await Promise.allSettled([loadDashboard(), loadNotes(), loadFiles(), loadPads(), loadShares(), loadTrash(), loadDevices()])
  const failed = rest.filter(result => result.status === 'rejected')
  if (failed.length) toast(`${failed.length} 个模块加载失败，请重试`, true)
}

async function loadHealth() {
  try {
    const data = await api('/api/v1/health')
    const badge=$('#healthBadge'); badge.className='badge'; const node=state.config?.node_name||'OmniShare'; badge.innerHTML=`<i></i>${escapeHTML(node)} · v${escapeHTML(data.version)}`
  } catch { const badge=$('#healthBadge'); badge.className='badge muted'; badge.innerHTML='<i></i>连接失败' }
}

async function loadDashboard() {
  try { state.stats = await api('/api/v1/dashboard'); renderStats() } catch (err) { if (!String(err.message).includes('密钥')) toast(err.message,true) }
}
function renderStats() {
  const items=[['随手记',state.stats.notes_count||0],['文件',state.stats.files_count||0],['协同文档',state.stats.pads_count||0],['视频',state.stats.videos_count||0],['安全分享',state.stats.active_shares||0],['回收站',state.stats.trash_count||0],['已用存储',formatBytes(state.stats.storage_used||0)]]
  $('#stats').innerHTML=items.map(([label,value])=>`<article class="stat"><span>${label}</span><strong>${escapeHTML(value)}</strong></article>`).join('')
  $('#storageUsed').textContent=`已用 ${formatBytes(state.stats.storage_used||0)}`
  $('#storageBar').setAttribute('aria-label','未设置磁盘总容量配额')
}

async function loadConfig() {
  try {
    state.config=await api('/api/v1/config')
    $('#dataDirText').textContent=state.config.data_dir
    $('#uploadLimit').textContent=`单文件上限 ${formatBytes(state.config.max_upload_mb*1024*1024)}`
    fillSettings()
    renderStats()
  } catch (err) { if (!String(err.message).includes('密钥')) toast(err.message,true) }
}

function fillSettings() {
  if (!state.config) return
  $('#cfgNodeName').value=state.config.node_name||''
  $('#cfgRetention').value=state.config.retention_days??0
  $('#cfgTrashRetention').value=state.config.trash_retention_days??30
  $('#cfgMaxUpload').value=state.config.max_upload_mb||4096
  $('#cfgAutoOpen').checked=!!state.config.auto_open_browser
  $('#cfgAllowLAN').checked=!!state.config.allow_lan
  $('#cfgListenAddress').value=state.config.listen_address||'127.0.0.1'
  $('#cfgPublicBaseURL').value=state.config.public_base_url||''
  $('#cfgAllowedOrigins').value=(state.config.allowed_origins||[]).join(', ')
  $('#cfgAccessKey').value=''
  renderPeers(state.config.peers||[])
}
function renderPeers(peers) {
  $('#peerRows').innerHTML=peers.map(p=>`<div class="peer-row" data-id="${escapeHTML(p.id)}"><input class="peer-name" value="${escapeHTML(p.name)}" placeholder="节点名称"><input class="peer-url" value="${escapeHTML(p.url)}" placeholder="http://100.x.x.x:8081"><button type="button" class="button danger small remove-peer">删除</button></div>`).join('')
}
function addPeerRow(peer={id:'',name:'',url:''}) {
  $('#peerRows').insertAdjacentHTML('beforeend',`<div class="peer-row" data-id="${escapeHTML(peer.id)}"><input class="peer-name" value="${escapeHTML(peer.name)}" placeholder="节点名称"><input class="peer-url" value="${escapeHTML(peer.url)}" placeholder="http://100.x.x.x:8081"><button type="button" class="button danger small remove-peer">删除</button></div>`)
}
async function saveSettings() {
  const secret=$('#cfgAccessKey').value.trim()
  const peers=$$('#peerRows .peer-row').map(row=>({id:row.dataset.id,name:row.querySelector('.peer-name').value.trim(),url:row.querySelector('.peer-url').value.trim()})).filter(p=>p.url)
  const payload={
    node_name:$('#cfgNodeName').value.trim(), max_upload_mb:Number($('#cfgMaxUpload').value), retention_days:Number($('#cfgRetention').value), trash_retention_days:Number($('#cfgTrashRetention').value),
    auto_open_browser:$('#cfgAutoOpen').checked, allow_lan:$('#cfgAllowLAN').checked, listen_address:$('#cfgListenAddress').value.trim(),
    public_base_url:$('#cfgPublicBaseURL').value.trim(), allowed_origins:$('#cfgAllowedOrigins').value.split(/[,，]/).map(v=>v.trim()).filter(Boolean),
    access_key:secret===''?'__KEEP__':(secret==='CLEAR'?'':secret), peers
  }
  const requestHeaders={'Content-Type':'application/json'}
  if(secret==='CLEAR') requestHeaders['X-OmniShare-Confirm']='CLEAR-ACCESS-KEY'
  try {
    state.config=await api('/api/v1/config',{method:'PUT',headers:requestHeaders,body:JSON.stringify(payload)})
    if (secret && secret!=='CLEAR') { state.accessKey=secret; sessionStorage.setItem('omnishare.accessKey',secret) }
    if (secret==='CLEAR') { state.accessKey=''; sessionStorage.removeItem('omnishare.accessKey') }
    $('#settingsDialog').close(); toast('设置已保存；网络设置需重启应用生效'); await Promise.all([loadHealth(),loadDevices()])
  } catch(err){toast(err.message,true)}
}

async function loadNotes() {
  const seq=++state.requestSeq.notes
  const q=encodeURIComponent($('#noteSearch').value.trim()); const tag=encodeURIComponent($('#noteTagFilter').value)
  try { const notes=await api(`/api/v1/notes?q=${q}&tag=${tag}`); if(seq!==state.requestSeq.notes)return; state.notes=notes; renderNotes() } catch(err){ if(seq===state.requestSeq.notes&&!String(err.message).includes('密钥')) toast(err.message,true) }
}
function renderNotes() {
  $('#noteCount').textContent=`${state.notes.length} 条`
  const tags=[...new Set(state.notes.flatMap(n=>n.tags||[]))].sort((a,b)=>a.localeCompare(b,'zh-CN'))
  const current=$('#noteTagFilter').value
  $('#noteTagFilter').innerHTML='<option value="">全部标签</option>'+tags.map(t=>`<option value="${escapeHTML(t)}">#${escapeHTML(t)}</option>`).join('')
  $('#noteTagFilter').value=current
  $('#notesList').innerHTML=state.notes.length?state.notes.map(n=>{const restricted=!!n.content_redacted;const restriction=n.is_burn_after_read?'阅后即焚':(n.max_read_count>0?`剩余 ${Math.max(0,n.max_read_count-n.read_count)} 次`:'');return `<article class="card note-card" data-note-id="${escapeHTML(n.id)}"><div class="note-head"><div class="note-meta"><span class="tag">${escapeHTML(n.id)}</span>${n.pinned?'<span>📌 置顶</span>':''}${n.is_burn_after_read?'<span>🔥 阅后即焚</span>':''}${n.max_read_count>0?`<span>👁 ${n.read_count}/${n.max_read_count}</span>`:''}<span>${formatDate(n.updated_at)}</span></div><div class="note-actions">${restricted?'<button class="text-button read-note">读取</button><button class="text-button copy-note">读取并复制</button>':'<button class="text-button copy-note">复制</button>'}<button class="text-button share-note">分享</button><button class="text-button edit-note">编辑</button><button class="text-button raw-note">Raw</button><button class="text-button danger delete-note">删除</button></div></div><div class="note-content${restricted?' restricted':''}">${restricted?`🔒 受限内容（${escapeHTML(restriction)}），主动读取才会计数。`:escapeHTML(n.content)}</div><div class="tag-row">${(n.tags||[]).map(t=>`<span class="tag">#${escapeHTML(t)}</span>`).join('')}</div></article>`}).join(''):'<div class="empty">没有匹配的随手记</div>'
}
async function getManagedNote(id){const listed=state.notes.find(x=>x.id===id);if(!listed)throw new Error('随手记不存在');return listed.content_redacted?api(`/api/v1/notes/${encodeURIComponent(id)}/manage`):listed}
async function readRestrictedNote(id,copy=false){const listed=state.notes.find(x=>x.id===id);if(!listed)return;if(listed.content_redacted&&!confirm(listed.is_burn_after_read?'读取后该内容将被销毁，是否继续？':'本次读取会消耗一次可读次数，是否继续？'))return;try{const note=listed.content_redacted?await api(`/api/v1/notes/${encodeURIComponent(id)}`):listed;if(copy){await copyText(note.content);toast('已复制')}else{$('#viewNoteMeta').textContent=`${note.id} · 已读取 ${note.read_count}${note.max_read_count?`/${note.max_read_count}`:''}`;$('#viewNoteContent').textContent=note.content;$('#viewNoteDialog').showModal()}await Promise.all([loadNotes(),loadDashboard()])}catch(err){toast(err.message,true)}}
async function createNote() {
  const content=$('#noteContent').value.trim(); if(!content){toast('请输入内容',true);return}
  const payload={content,tags:$('#noteTags').value.split(/[,，]/).map(s=>s.trim()).filter(Boolean),pinned:$('#notePinned').checked,is_burn_after_read:$('#noteBurn').checked,max_read_count:Number($('#noteMaxReads').value||0),ttl_seconds:Number($('#noteTTL').value)}
  try { await api('/api/v1/notes',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)}); $('#noteContent').value='';$('#noteTags').value='';$('#notePinned').checked=false;$('#noteBurn').checked=false;$('#noteMaxReads').value='0';toast('随手记已发布');await Promise.all([loadNotes(),loadDashboard()]) } catch(err){toast(err.message,true)}
}
async function deleteNote(id) { if(!confirm('将这条随手记移入回收站？'))return; try{await api(`/api/v1/notes/${encodeURIComponent(id)}`,{method:'DELETE'});toast('已移入回收站');await Promise.all([loadNotes(),loadDashboard()])}catch(err){toast(err.message,true)} }
async function openEditNote(id){try{const n=await getManagedNote(id);state.editingNote=n;$('#editNoteId').textContent=n.id;$('#editNoteContent').value=n.content;$('#editNoteTags').value=(n.tags||[]).join(', ');$('#editNotePinned').checked=!!n.pinned;$('#editNoteBurn').checked=!!n.is_burn_after_read;$('#editNoteMaxReads').value=n.max_read_count||0;$('#editNoteDialog').showModal()}catch(err){toast(err.message,true)}}
async function saveEditNote(){if(!state.editingNote)return;const payload={content:$('#editNoteContent').value,tags:$('#editNoteTags').value.split(/[,，]/).map(s=>s.trim()).filter(Boolean),pinned:$('#editNotePinned').checked,is_burn_after_read:$('#editNoteBurn').checked,max_read_count:Number($('#editNoteMaxReads').value||0)};try{await api(`/api/v1/notes/${encodeURIComponent(state.editingNote.id)}`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});$('#editNoteDialog').close();toast('随手记已更新');await loadNotes()}catch(err){toast(err.message,true)}}

async function loadFiles() {
  const seq=++state.requestSeq.files
  const q=encodeURIComponent($('#fileSearch').value.trim());const type=encodeURIComponent($('#fileType').value)
  try{const files=await api(`/api/v1/files?q=${q}&type=${type}`);if(seq!==state.requestSeq.files)return;state.files=files;renderFiles()}catch(err){if(seq===state.requestSeq.files&&!String(err.message).includes('密钥'))toast(err.message,true)}
}
function fileIcon(f){if(f.is_video)return'🎬';if((f.mime_type||'').startsWith('image/'))return'🖼';if((f.mime_type||'').includes('zip'))return'🗜';return'📄'}
function renderFiles(){
  $('#fileCount').textContent=`${state.files.length} 个文件`
  $('#filesList').innerHTML=state.files.length?state.files.map(f=>`<article class="card file-card" data-file-id="${escapeHTML(f.id)}"><div class="file-info"><div class="file-icon">${fileIcon(f)}</div><div><h3 title="${escapeHTML(f.file_name)}">${escapeHTML(f.file_name)}</h3><p>${formatBytes(f.file_size)} · ${escapeHTML(f.mime_type)} · 下载 ${f.download_count||0} 次</p></div></div><div class="file-actions">${f.is_video?'<button class="button ghost small play-file">播放</button>':''}<button class="button ghost small download-file">下载</button><button class="button ghost small share-file">分享</button><button class="button ghost small rename-file">重命名</button><button class="button danger small delete-file">删除</button></div></article>`).join(''):'<div class="empty">没有匹配的文件</div>'
}
async function createFileTicket(file,disposition='attachment'){return api(`/api/v1/files/${encodeURIComponent(file.id)}/ticket`,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({disposition})})}
async function openDownload(f){try{const ticket=await createFileTicket(f,'attachment');const a=document.createElement('a');a.href=ticket.url;a.download=f.file_name;a.rel='noopener';document.body.appendChild(a);a.click();a.remove()}catch(err){toast(err.message,true)}}
async function playFile(f){try{const ticket=await createFileTicket(f,'inline');$('#videoTitle').textContent=f.file_name;const player=$('#videoPlayer');player.src=ticket.url;$('#videoDialog').showModal();await player.play().catch(()=>{})}catch(err){toast(err.message,true)}}
async function openRawNote(id){try{const res=await fetch(`/n/${encodeURIComponent(id)}/raw`,{headers:headers()});if(res.status===401){showAuth();return}if(!res.ok)throw new Error(await res.text()||`HTTP ${res.status}`);const blob=await res.blob();const url=URL.createObjectURL(blob);window.open(url,'_blank','noopener');setTimeout(()=>URL.revokeObjectURL(url),60000)}catch(err){toast(err.message,true)}}
async function deleteFile(id){if(!confirm('将该文件移入回收站？磁盘实体会暂时保留。'))return;try{await api(`/api/v1/files/${encodeURIComponent(id)}`,{method:'DELETE'});toast('文件已移入回收站');await Promise.all([loadFiles(),loadDashboard()])}catch(err){toast(err.message,true)}}

async function renameFile(file){const name=prompt('新的文件名',file.file_name);if(!name||name===file.file_name)return;try{await api(`/api/v1/files/${encodeURIComponent(file.id)}`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({file_name:name})});toast('文件已重命名');await loadFiles()}catch(err){toast(err.message,true)}}

function uploadFiles(files){for(const file of files)state.uploadQueue.push({file,ttl:$('#fileTTL').value});pumpUploads()}
function pumpUploads(){while(state.activeUploads<3&&state.uploadQueue.length){const task=state.uploadQueue.shift();state.activeUploads++;uploadOne(task).finally(()=>{state.activeUploads--;pumpUploads()})}}
function uploadOne({file,ttl}){return new Promise(resolve=>{
  const row=document.createElement('div');row.className='progress-card';row.innerHTML=`<div class="note-head"><span>${escapeHTML(file.name)}</span><span class="pct">排队</span></div><progress class="progress-track" max="100" value="0"></progress>`;$('#uploadQueue').appendChild(row)
  const xhr=new XMLHttpRequest();xhr.open('POST','/api/v1/files/upload');if(state.accessKey)xhr.setRequestHeader('X-OmniShare-Key',state.accessKey)
  row.querySelector('.pct').textContent='0%'
  xhr.upload.onprogress=e=>{if(e.lengthComputable){const p=Math.round(e.loaded/e.total*100);row.querySelector('.pct').textContent=`${p}%`;row.querySelector('.progress-track').value=p}}
  xhr.onload=async()=>{if(xhr.status>=200&&xhr.status<300){toast(`${file.name} 上传完成`);row.remove();await Promise.all([loadFiles(),loadDashboard()])}else{let message=`HTTP ${xhr.status}`;try{message=JSON.parse(xhr.responseText).message||message}catch{}row.querySelector('.pct').textContent='失败';toast(`${file.name}：${message}`,true)}resolve()}
  xhr.onerror=()=>{row.querySelector('.pct').textContent='失败';toast(`${file.name}：网络错误`,true);resolve()}
  xhr.onabort=()=>{row.querySelector('.pct').textContent='已取消';resolve()}
  const form=new FormData();form.append('file',file);form.append('ttl_seconds',ttl);xhr.send(form)
})}

async function loadPads(){const seq=++state.requestSeq.pads;try{const pads=await api(`/api/v1/pads?q=${encodeURIComponent($('#padSearch').value.trim())}`);if(seq!==state.requestSeq.pads)return;state.pads=pads;renderPads()}catch(err){if(seq===state.requestSeq.pads&&!String(err.message).includes('密钥'))toast(err.message,true)}}
function renderPads(){
  $('#padsList').innerHTML=state.pads.length?state.pads.map(p=>`<button class="pad-item ${state.currentPad?.id===p.id?'active':''}" data-pad-id="${escapeHTML(p.id)}"><strong>${escapeHTML(p.title)}</strong><span>v${p.version} · ${formatDate(p.updated_at)}</span></button>`).join(''):'<div class="empty">暂无文档</div>'
}
function renderPadEditor(p){$('#padEmpty').classList.add('hidden');$('#padForm').classList.remove('hidden');$('#padTitle').value=p.title;$('#padContent').value=p.content;$('#padMeta').textContent=`${p.id} · 版本 ${p.version} · 更新于 ${formatDate(p.updated_at)}`;$('#saveState').textContent='已同步';$('#saveState').className='badge';renderPads()}
async function selectPad(id){if(state.currentPad?.id===id)return;if(state.currentPadDirty){const saved=await savePad(false);if(!saved&&!confirm('当前文档未能保存，仍要切换吗？'))return}clearTimeout(state.saveTimer);try{const p=await api(`/api/v1/pads/${encodeURIComponent(id)}`);state.currentPad={...p};state.currentPadDirty=false;renderPadEditor(p)}catch(err){toast(err.message,true)}}
async function createPad(){const title=prompt('文档标题','新协同文档');if(!title)return;try{const p=await api('/api/v1/pads',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title,content:`# ${title}

`})});toast('文档已创建');await Promise.all([loadPads(),loadDashboard()]);await selectPad(p.id)}catch(err){toast(err.message,true)}}
function padSnapshot(){if(!state.currentPad)return null;return{id:state.currentPad.id,title:$('#padTitle').value.trim()||'未命名文档',content:$('#padContent').value,version:state.currentPad.version}}
function markPadDirty(){if(!state.currentPad)return;state.currentPadDirty=true;$('#saveState').textContent='待保存';$('#saveState').className='badge muted';clearTimeout(state.saveTimer);const snapshot=padSnapshot();state.saveTimer=setTimeout(()=>enqueuePadSave(snapshot,false),900)}
function enqueuePadSave(snapshot,showToast=true){if(!snapshot)return Promise.resolve(true);state.padSaveChain=state.padSaveChain.catch(()=>false).then(()=>performPadSave(snapshot,showToast));return state.padSaveChain}
async function performPadSave(snapshot,showToast=true){const isCurrent=state.currentPad?.id===snapshot.id;if(isCurrent)$('#saveState').textContent='保存中';const known=isCurrent?state.currentPad.version:(state.pads.find(p=>p.id===snapshot.id)?.version||snapshot.version);const payload={title:snapshot.title,content:snapshot.content,version:known};try{const p=await api(`/api/v1/pads/${encodeURIComponent(snapshot.id)}`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});const index=state.pads.findIndex(item=>item.id===p.id);if(index>=0)state.pads[index]={...p};else state.pads.unshift({...p});if(state.currentPad?.id===snapshot.id){state.currentPad={...p};const unchanged=$('#padTitle').value.trim()===snapshot.title&&$('#padContent').value===snapshot.content;state.currentPadDirty=!unchanged;$('#saveState').textContent=unchanged?'已保存':'待保存';$('#saveState').className=`badge${unchanged?'':' muted'}`;$('#padMeta').textContent=`${p.id} · 版本 ${p.version} · 更新于 ${formatDate(p.updated_at)}`;if(unchanged)sessionStorage.removeItem(`omnishare.padDraft.${p.id}`)}renderPads();if(showToast)toast('文档已保存');return true}catch(err){sessionStorage.setItem(`omnishare.padDraft.${snapshot.id}`,JSON.stringify({...snapshot,saved_at:new Date().toISOString()}));if(state.currentPad?.id===snapshot.id){$('#saveState').textContent=err.status===409?'版本冲突':'保存失败';$('#saveState').className='badge muted'}toast(err.status===409?'发现新版本，本地草稿已保留在当前会话':err.message,true);await loadPads();return false}}
async function savePad(showToast=true){clearTimeout(state.saveTimer);return enqueuePadSave(padSnapshot(),showToast)}
async function deletePad(){if(!state.currentPad||!confirm('将当前文档移入回收站？'))return;clearTimeout(state.saveTimer);const id=state.currentPad.id;try{await api(`/api/v1/pads/${encodeURIComponent(id)}`,{method:'DELETE'});sessionStorage.removeItem(`omnishare.padDraft.${id}`);state.currentPad=null;state.currentPadDirty=false;$('#padForm').classList.add('hidden');$('#padEmpty').classList.remove('hidden');toast('文档已移入回收站');await Promise.all([loadPads(),loadDashboard()])}catch(err){toast(err.message,true)}}


function copyText(value){if(navigator.clipboard?.writeText)return navigator.clipboard.writeText(value);const ta=document.createElement('textarea');ta.value=value;document.body.appendChild(ta);ta.select();document.execCommand('copy');ta.remove();return Promise.resolve()}
function openShare(type,id,name){$('#shareObjectType').value=type;$('#shareObjectId').value=id;$('#shareObjectName').textContent=name;$('#shareTTL').value='604800';$('#shareMaxAccess').value='0';$('#shareDialog').showModal()}
async function createShare(){const payload={object_type:$('#shareObjectType').value,object_id:$('#shareObjectId').value,ttl_seconds:Number($('#shareTTL').value),max_access_count:Number($('#shareMaxAccess').value)};try{const link=await api('/api/v1/shares',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});await copyText(link.url);$('#shareDialog').close();toast('分享链接已创建并复制');await Promise.all([loadShares(),loadDashboard()])}catch(err){toast(err.message,true)}}
async function loadShares(){try{state.shares=await api('/api/v1/shares');renderShares()}catch(err){if(!String(err.message).includes('密钥'))toast(err.message,true)}}
function shareStatus(link){const labels={active:'有效',revoked:'已撤销',expired:'已过期',exhausted:'次数用尽',target_missing:'目标不存在',target_deleted:'目标已删除',target_expired:'目标已过期'};const status=link.status||'active';return[labels[status]||status,status==='active'?'':'danger']}
function renderShares(){const labels={note:'随手记',file:'文件',pad:'协同文档'};$('#sharesList').innerHTML=state.shares.length?state.shares.map(link=>{const [status,cls]=shareStatus(link);const limit=link.max_access_count>0?`${link.access_count}/${link.max_access_count}`:`${link.access_count}/不限`;return `<article class="card share-card" data-share-id="${escapeHTML(link.id)}"><div><div class="note-meta"><span class="tag">${labels[link.object_type]||escapeHTML(link.object_type)}</span><span class="badge ${cls==='danger'?'muted':''}">${status}</span></div><h3>${escapeHTML(link.name)}</h3><p class="muted-text">访问 ${limit} · 到期 ${link.expires_at?formatDate(link.expires_at):'长期有效'}</p><code class="share-url">${escapeHTML(link.url)}</code></div><div class="file-actions"><button class="button ghost small copy-share">复制</button><button class="button ghost small open-share">打开</button>${link.revoked_at?'':`<button class="button danger small revoke-share">撤销</button>`}</div></article>`}).join(''):'<div class="empty">尚未创建安全分享</div>'}
async function revokeShare(id){if(!confirm('撤销后该链接将立即失效，是否继续？'))return;try{await api(`/api/v1/shares/${encodeURIComponent(id)}`,{method:'DELETE'});toast('分享已撤销');await Promise.all([loadShares(),loadDashboard()])}catch(err){toast(err.message,true)}}
async function loadTrash(){try{state.trash=await api(`/api/v1/trash?type=${encodeURIComponent($('#trashType')?.value||'')}`);renderTrash()}catch(err){if(!String(err.message).includes('密钥'))toast(err.message,true)}}
function renderTrash(){const labels={note:'随手记',file:'文件',pad:'协同文档'};$('#trashList').innerHTML=state.trash.length?state.trash.map(item=>`<article class="card trash-card" data-trash-type="${escapeHTML(item.object_type)}" data-trash-id="${escapeHTML(item.id)}"><div><div class="note-meta"><span class="tag">${labels[item.object_type]||escapeHTML(item.object_type)}</span><span>删除于 ${formatDate(item.deleted_at)}</span></div><h3>${escapeHTML(item.name)}</h3>${item.size?`<p class="muted-text">${formatBytes(item.size)}</p>`:''}</div><div class="file-actions"><button class="button ghost small restore-trash">恢复</button><button class="button danger small purge-trash">永久删除</button></div></article>`).join(''):'<div class="empty">回收站为空</div>'}
async function restoreTrash(type,id){try{await api(`/api/v1/trash/${encodeURIComponent(type)}/${encodeURIComponent(id)}/restore`,{method:'POST'});toast('已恢复');await Promise.all([loadTrash(),loadDashboard(),loadNotes(),loadFiles(),loadPads()])}catch(err){toast(err.message,true)}}
async function purgeTrash(type,id){if(!confirm('永久删除后无法恢复，是否继续？'))return;try{await api(`/api/v1/trash/${encodeURIComponent(type)}/${encodeURIComponent(id)}`,{method:'DELETE'});toast('已永久删除');await Promise.all([loadTrash(),loadDashboard()])}catch(err){toast(err.message,true)}}
async function emptyTrash(){if(!state.trash.length)return;if(!confirm('永久清空整个回收站？此操作不可恢复。'))return;try{await api('/api/v1/trash',{method:'DELETE'});toast('回收站已清空');await Promise.all([loadTrash(),loadDashboard()])}catch(err){toast(err.message,true)}}

async function loadAudit(){try{state.audits=await api('/api/v1/audit?limit=100');renderAudit()}catch(err){toast(err.message,true)}}
function renderAudit(){const labels={create:'创建',update:'更新',delete:'删除',trash:'移入回收站',restore:'恢复',purge:'永久删除',upload:'上传',rename:'重命名',share:'分享',revoke:'撤销',access:'访问',read:'读取',cleanup:'清理'};$('#auditList').innerHTML=state.audits.length?state.audits.map(e=>`<article class="timeline-item"><span class="timeline-action">${labels[e.action]||escapeHTML(e.action)} · ${escapeHTML(e.object)}</span><div class="timeline-summary">${escapeHTML(e.summary||e.object_id||'—')}</div><time class="timeline-time">${formatDate(e.created_at)}</time></article>`).join(''):'<div class="empty">暂无活动记录</div>'}

async function loadDevices(){if(state.devicesLoading)return;state.devicesLoading=true;const seq=++state.requestSeq.devices;try{const devices=await api('/api/v1/devices');if(seq!==state.requestSeq.devices)return;state.devices=devices;renderDevices()}catch(err){if(seq===state.requestSeq.devices&&!String(err.message).includes('密钥'))toast(err.message,true)}finally{state.devicesLoading=false}}
function safeDeviceURL(value){try{const u=new URL(value,location.origin);return ['http:','https:'].includes(u.protocol)?u.href:''}catch{return''}}
function renderDevices(){$('#deviceList').innerHTML=state.devices.length?state.devices.map(d=>{const url=safeDeviceURL(d.url);return `<div class="device"><div class="device-main"><h3>${escapeHTML(d.hostname)}</h3>${url?`<a href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer">${escapeHTML(url)}</a>`:'<span class="muted-text">无有效地址</span>'}</div><div><div class="device-state ${d.online?'':'offline'}">${d.online?'在线':'离线'}</div><div class="muted-text">${escapeHTML(d.network_type)}${d.latency_ms?` · ${d.latency_ms}ms`:''}</div></div></div>`}).join(''):'<div class="empty">未发现可用地址</div>'}

function switchTab(tab){if(state.activeTab==='pads'&&tab!=='pads'&&state.currentPadDirty)savePad(false);state.activeTab=tab;$$('.tab').forEach(b=>b.classList.toggle('active',b.dataset.tab===tab));$$('.panel-view').forEach(p=>p.classList.remove('active'));$(`#${tab}Panel`).classList.add('active');if(tab==='activity')loadAudit();if(tab==='shares')loadShares();if(tab==='trash')loadTrash();if(tab==='pads')loadPads();if(tab==='files')loadFiles();if(tab==='notes')loadNotes()}
async function exportBackup(){try{const res=await fetch('/api/v1/backup',{headers:headers()});if(res.status===401){showAuth();return}if(!res.ok)throw new Error(await res.text());const blob=await res.blob();const a=document.createElement('a');a.href=URL.createObjectURL(blob);a.download=`omnishare-backup-${new Date().toISOString().slice(0,10)}.zip`;a.click();setTimeout(()=>URL.revokeObjectURL(a.href),1000)}catch(err){toast(err.message,true)}}

function bindEvents(){
  $$('.tab').forEach(btn=>btn.addEventListener('click',()=>switchTab(btn.dataset.tab)))
  $('#createNoteBtn').addEventListener('click',createNote);$('#pasteBtn').addEventListener('click',async()=>{try{$('#noteContent').value=await navigator.clipboard.readText()}catch{toast('浏览器未授予剪贴板权限',true)}})
  $('#noteSearch').addEventListener('input',debounce(loadNotes));$('#noteTagFilter').addEventListener('change',loadNotes)
  $('#notesList').addEventListener('click',async e=>{const card=e.target.closest('[data-note-id]');if(!card)return;const id=card.dataset.noteId,n=state.notes.find(x=>x.id===id);if(e.target.closest('.read-note'))await readRestrictedNote(id,false);else if(e.target.closest('.copy-note'))await readRestrictedNote(id,true);else if(e.target.closest('.share-note'))openShare('note',id,n.content_redacted?'受限随手记':n.content.slice(0,80));else if(e.target.closest('.edit-note'))await openEditNote(id);else if(e.target.closest('.raw-note'))openRawNote(id);else if(e.target.closest('.delete-note'))deleteNote(id)})
  $('#saveEditNoteBtn').addEventListener('click',saveEditNote)
  $('#chooseFilesBtn').addEventListener('click',e=>{e.stopPropagation();$('#fileInput').click()});$('#dropZone').addEventListener('click',()=>$('#fileInput').click());$('#fileInput').addEventListener('change',e=>{uploadFiles(e.target.files);e.target.value='' });['dragenter','dragover'].forEach(type=>$('#dropZone').addEventListener(type,e=>{e.preventDefault();$('#dropZone').classList.add('dragging')}));['dragleave','drop'].forEach(type=>$('#dropZone').addEventListener(type,e=>{e.preventDefault();$('#dropZone').classList.remove('dragging')}));$('#dropZone').addEventListener('drop',e=>uploadFiles(e.dataTransfer.files))
  $('#fileSearch').addEventListener('input',debounce(loadFiles));$('#fileType').addEventListener('change',loadFiles);$('#filesList').addEventListener('click',e=>{const card=e.target.closest('[data-file-id]');if(!card)return;const f=state.files.find(x=>x.id===card.dataset.fileId);if(e.target.closest('.download-file'))openDownload(f);if(e.target.closest('.play-file'))playFile(f);if(e.target.closest('.share-file'))openShare('file',f.id,f.file_name);if(e.target.closest('.rename-file'))renameFile(f);if(e.target.closest('.delete-file'))deleteFile(f.id)})
  $('#newPadBtn').addEventListener('click',createPad);$('#padSearch').addEventListener('input',debounce(loadPads));$('#padsList').addEventListener('click',e=>{const item=e.target.closest('[data-pad-id]');if(item)void selectPad(item.dataset.padId)});$('#padTitle').addEventListener('input',markPadDirty);$('#padContent').addEventListener('input',markPadDirty);$('#savePadBtn').addEventListener('click',()=>savePad(true));$('#sharePadBtn').addEventListener('click',()=>{if(state.currentPad)openShare('pad',state.currentPad.id,state.currentPad.title)});$('#deletePadBtn').addEventListener('click',deletePad)
  $('#createShareBtn').addEventListener('click',createShare);$('#refreshSharesBtn').addEventListener('click',loadShares);$('#sharesList').addEventListener('click',e=>{const card=e.target.closest('[data-share-id]');if(!card)return;const link=state.shares.find(x=>x.id===card.dataset.shareId);if(e.target.closest('.copy-share'))copyText(link.url).then(()=>toast('链接已复制'));if(e.target.closest('.open-share'))window.open(link.url,'_blank','noopener');if(e.target.closest('.revoke-share'))revokeShare(link.id)});$('#trashType').addEventListener('change',loadTrash);$('#emptyTrashBtn').addEventListener('click',emptyTrash);$('#trashList').addEventListener('click',e=>{const card=e.target.closest('[data-trash-id]');if(!card)return;const type=card.dataset.trashType,id=card.dataset.trashId;if(e.target.closest('.restore-trash'))restoreTrash(type,id);if(e.target.closest('.purge-trash'))purgeTrash(type,id)});$('#refreshAuditBtn').addEventListener('click',loadAudit);$('#refreshDevicesBtn').addEventListener('click',loadDevices);$('#settingsBtn').addEventListener('click',()=>{fillSettings();$('#settingsDialog').showModal()});$('#backupBtn').addEventListener('click',exportBackup);$('#saveSettingsBtn').addEventListener('click',saveSettings);$('#addPeerBtn').addEventListener('click',()=>addPeerRow());$('#peerRows').addEventListener('click',e=>{if(e.target.closest('.remove-peer'))e.target.closest('.peer-row').remove()})
  $('#authSubmitBtn').addEventListener('click',verifyAuth);$('#authKey').addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();verifyAuth()}});$('#closeVideoBtn').addEventListener('click',()=>{$('#videoPlayer').pause();$('#videoPlayer').removeAttribute('src');$('#videoDialog').close()});const closeViewed=()=>$('#viewNoteDialog').close();$('#closeViewNoteBtn').addEventListener('click',closeViewed);$('#closeViewedNoteBtn').addEventListener('click',closeViewed);$('#copyViewedNoteBtn').addEventListener('click',()=>copyText($('#viewNoteContent').textContent).then(()=>toast('已复制')))
  window.addEventListener('beforeinstallprompt',e=>{e.preventDefault();state.deferredInstall=e;$('#installBtn').classList.remove('hidden')});$('#installBtn').addEventListener('click',async()=>{if(!state.deferredInstall)return;state.deferredInstall.prompt();await state.deferredInstall.userChoice;state.deferredInstall=null;$('#installBtn').classList.add('hidden')})
}

async function start(){bindEvents();window.addEventListener('beforeunload',event=>{if(state.currentPadDirty){event.preventDefault();event.returnValue=''}});if('serviceWorker'in navigator)navigator.serviceWorker.register('/service-worker.js').catch(()=>{});await loadAll();setInterval(loadHealth,15000);setInterval(()=>{if(!document.hidden)loadDevices()},30000)}
start()
