const $ = (selector) => document.querySelector(selector)
const $$ = (selector) => [...document.querySelectorAll(selector)]

// Storage may be disabled or full. Keep this page usable without persisting
// credentials beyond its lifetime, and never fall back to stale stored values.
const sessionValues = new Map()
function sessionGet(key) {
  if (sessionValues.has(key)) return sessionValues.get(key)
  try { return sessionStorage.getItem(key) } catch { return null }
}
function sessionSet(key, value) {
  sessionValues.set(key, String(value))
  try { sessionStorage.setItem(key, String(value)) } catch { /* page memory only */ }
}
function sessionRemove(key) {
  sessionValues.set(key, null)
  try { sessionStorage.removeItem(key) } catch { /* page memory only */ }
}

// Drop obsolete initialization tokens from old links and session storage.
// They are never used as credentials in the LAN-first application.
sessionRemove('omnishare.initToken')
if (new URLSearchParams(location.search).has('init_token')) {
  const clean = new URL(location.href)
  clean.searchParams.delete('init_token')
  history.replaceState(null, '', clean.pathname + clean.search + clean.hash)
}

const state = {
  activeTab: 'dashboard', notes: [], files: [], pads: [], shares: [], trash: [], audits: [], devices: [], stats: {}, config: null,
  currentPad: null, currentPadDirty: false, padSaveBlocked: false, saveTimer: null, saveInFlight: false, queuedPadSave: null,
  accessKey: sessionGet('omnishare.accessKey') || '',
  deferredInstall: null, editingNote: null,
  uploadQueue: [], activeUploads: 0, uploadSequence: 0, uploadPumping: false, uploadNotice: '',
  requestSeq: {notes:0, files:0, pads:0, devices:0}, devicesLoading: false, healthLoading: false, padSaveChain: Promise.resolve(),
  noteTotal: 0, fileTotal: 0, restoreInFlight: false, workspaceBusy: false, padRefreshSeq: 0, padSelectSeq: 0, workspaceStatus: null, tailscaleStatus: "", padCreating: false, draftCopyBusy: false, peerBinding: false
}

// Server default is 100; ask explicitly so the response can be paged instead of
// silently truncated.
const PAGE_SIZE = 100

// Clipboard content is read only after a click, kept in page memory, and sent
// only after a second explicit confirmation. No watcher or background sync.
let clipboardSelection = null
let clipboardPreviewURL = ''
let clipboardReading = false
let clipboardSending = false
let clipboardReadSequence = 0
const CLIPBOARD_TEXT_LIMIT = 256 * 1024
const CLIPBOARD_IMAGE_LIMIT = 16 * 1024 * 1024

const escapeHTML = (value = '') => String(value).replace(/[&<>'"]/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[ch]))
const formatBytes = (bytes = 0) => { if (!bytes) return '0 B'; const units=['B','KB','MB','GB','TB']; const i=Math.min(Math.floor(Math.log(bytes)/Math.log(1024)),units.length-1); return `${(bytes/1024**i).toFixed(i ? 1 : 0)} ${units[i]}` }
const formatDate = (value) => value ? new Date(value).toLocaleString('zh-CN', {month:'numeric',day:'numeric',hour:'2-digit',minute:'2-digit',second:'2-digit',hour12:false}) : '—'
const debounce = (fn, wait=250) => { let t; return (...args) => { clearTimeout(t); t=setTimeout(()=>fn(...args),wait) } }

const toast = (message, error=false) => {
  const el = $('#toast')
  el.textContent = message
  el.className = `toast show${error ? ' error' : ''}`
  clearTimeout(el._timer)
  el._timer = setTimeout(() => { el.className = 'toast' }, 2800)
}

function keyHeaders(key) {
  // Keep ordinary printable ASCII compatible with existing clients. Encode
  // every other value, including leading/trailing spaces, without trimming.
  if (!key) return {}
  if (/^[\x21-\x7e]+$/.test(key)) return {'X-OmniShare-Key': key}
  const bytes = new TextEncoder().encode(key)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return {'X-OmniShare-Key-Base64': btoa(binary)}
}
const authHeaders = () => state.config?.access_key_enabled === false ? {} : keyHeaders(state.accessKey)
const headers = (extra={}) => ({ ...authHeaders(), ...extra })

function workspacePath(path, method='GET') {
  const u = new URL(path, location.origin)
  if (u.origin === location.origin && /^\/api\/v1\/(dashboard|notes|files|pads|shares|trash|audit)$/.test(u.pathname) &&
      (method === 'GET' || (method === 'DELETE' && u.pathname === '/api/v1/trash'))) u.searchParams.set('scope', 'all')
  return u.pathname + u.search
}

function acceptWorkspaceStatus(payload) {
  if (payload.workspace) {
    state.workspaceStatus = payload.workspace
    const unavailable = payload.workspace.nodes.filter(n => !n.online || n.truncated)
    const banner = $('#workspaceStatus')
    if (banner) {
      banner.textContent = unavailable.length
        ? `部分设备暂不可用：${unavailable.map(n => n.name + (n.cached ? '（上次快照）' : '')).join('、')}；未取得的数据不计入当前统计。`
        : `统一工作区 · ${payload.workspace.nodes.length} 台设备 · 新内容默认保存在当前设备`
      banner.classList.toggle('warning', unavailable.length > 0)
    }
  }
  if (payload.discovery) state.tailscaleStatus = payload.discovery.tailscale || ''
}

function sourceLabel(item) {
  if (!item.source_node_name) return ''
  return `<span class="source-label${item.source_online === false ? ' source-offline' : ''}">来自 ${escapeHTML(item.source_node_name)}${item.source_online === false ? ' · 离线快照' : ''}</span>`
}

async function api(path, options={}) {
  path = workspacePath(path, options.method || 'GET')
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
  acceptWorkspaceStatus(payload)
  if (!res.ok || payload.code !== 0) {
    const err = new Error(payload.message || `HTTP ${res.status}`)
    err.status = res.status
    err.data = payload.data
    throw err
  }
  return payload.data
}

// api() returns only data; paged lists also need X-Total-Count to know whether
// the server truncated the result instead of silently dropping the rest.
async function apiPage(path) {
  path = workspacePath(path)
  const res = await fetch(path, {headers: headers()})
  if (res.status === 401) {
    showAuth()
    throw new Error('需要访问密钥')
  }
  const payload = await res.json()
  acceptWorkspaceStatus(payload)
  if (!res.ok) throw new Error(payload.message || `HTTP ${res.status}`)
  return {items: payload.data || [], total: Number(res.headers.get('X-Total-Count') || 0)}
}

async function apiCollection(path) {
  const u = new URL(path, location.origin)
  const items = []
  for (let offset = 0; offset < 10000; offset += 500) {
    u.searchParams.set('limit', '500'); u.searchParams.set('offset', String(offset))
    const page = await apiPage(u.pathname + u.search)
    items.push(...page.items)
    if (!page.items.length || items.length >= page.total) break
  }
  return items
}

async function apiBlob(path, options={}) {
  const res = await fetch(path, {...options, headers: headers(options.headers || {})})
  if (res.status === 401) {
    showAuth()
    throw new Error('需要访问密钥')
  }
  if (!res.ok) {
    const type = res.headers.get('content-type') || ''
    let message = `HTTP ${res.status}`
    let data
    if (type.includes('application/json')) {
      const payload = await res.json()
  acceptWorkspaceStatus(payload)
      message = payload.message || message
      data = payload.data
    } else {
      message = await res.text() || message
    }
    const err = new Error(message)
    err.status = res.status
    err.data = data
    throw err
  }
  return res.blob()
}

function showAuth() {
  // A different client may have enabled protection since our last config poll.
  if (state.config) state.config.access_key_enabled = true
  if (!$('#authDialog').open) $('#authDialog').showModal()
  $('#authKey').focus()
}

async function verifyAuth() {
  const key = $('#authKey').value
  try {
    const res = await fetch('/api/v1/auth/verify', {method:'POST', headers:keyHeaders(key)})
    if (!res.ok) throw new Error('密钥不正确')
    state.accessKey = key
    if (state.config) state.config.access_key_enabled = true
    sessionSet('omnishare.accessKey', key)
    $('#authError').textContent = ''
    $('#authDialog').close()
    await loadAll()
  } catch (err) {
    $('#authError').textContent = err.message
  }
}

async function loadAll() {
  const core = await Promise.allSettled([loadHealth(true), loadConfig(true), loadDashboard(true), loadNotes(true), loadDevices(true)])
  if (core.some(result => result.status === 'rejected')) {
    toast('核心服务加载失败，请检查网络连接或访问密钥', true)
    return false
  }
  return true
}

function reportLoadError(err, strict=false) {
  if (!String(err.message).includes('密钥')) toast(err.message, true)
  if (strict) throw err
}

async function loadHealth(strict=false) {
  // A 15s interval over a slow link otherwise stacks several in-flight polls.
  if (state.healthLoading) return
  state.healthLoading = true
  try {
    const data = await api('/api/v1/health')
    const badge = $('#healthBadge')
    badge.className = 'badge'
    const node = state.config?.node_name || 'OmniShare'
    badge.setAttribute('aria-label', `已连接到 ${node}，版本 ${data.version}`)
    badge.title = `已连接到 ${node} · v${data.version}`
    const markup = `<i></i>${escapeHTML(node)} · v${escapeHTML(data.version)}`
    // Polled every 15s: avoid needless DOM/HTML reparsing when nothing changed.
    if (badge.innerHTML !== markup) badge.innerHTML = markup
  } catch (err) {
    const badge = $('#healthBadge')
    badge.className = 'badge muted'
    badge.setAttribute('aria-label', '节点连接失败')
    badge.title = '节点连接失败'
    const markup = '<i></i>连接失败'
    if (badge.innerHTML !== markup) badge.innerHTML = markup
    reportLoadError(err, strict)
  } finally {
    state.healthLoading = false
  }
}

async function loadDashboard(strict=false) {
  try {
    state.stats = await api('/api/v1/dashboard')
    renderStats()
  } catch (err) {
    reportLoadError(err, strict)
  }
}

function renderStats() {
  const items = [
    ['随手记', state.stats.notes_count || 0],
    ['文件', state.stats.files_count || 0],
    ['协同文档', state.stats.pads_count || 0],
    ['视频', state.stats.videos_count || 0],
    ['安全分享', state.stats.active_shares || 0],
    ['回收站', state.stats.trash_count || 0],
    ['已用存储', formatBytes(state.stats.storage_used || 0)]
  ]
  const stats = $('#stats')
  const markup = items.map(([label, value]) => `
    <article class="stat">
      <span>${label}</span>
      <strong>${escapeHTML(value)}</strong>
    </article>
  `).join('')
  // Dashboard is reloaded after nearly every mutation; skip the reparse when the
  // rendered values are identical (the common case for unrelated actions).
  if (stats.innerHTML !== markup) stats.innerHTML = markup
  $('#storageUsed').textContent = `已用 ${formatBytes(state.stats.storage_used || 0)}`
  $('#storageBar').setAttribute('aria-label', '未设置磁盘总容量配额')
}

async function loadConfig(strict=false) {
  try {
    state.config = await api('/api/v1/config')
    if (state.config.access_key_enabled === false) {
      state.accessKey = ''
      sessionRemove('omnishare.accessKey')
    }
    $('#dataDirText').textContent = '本机私有存储（路径未通过 API 暴露）'
    $('#uploadLimit').textContent = `单文件上限 ${formatBytes(state.config.max_upload_mb * 1024 * 1024)}`
    renderNodeSecurity()
    fillSettings()
    renderStats()
  } catch (err) {
    reportLoadError(err, strict)
  }
}

function fillSettings() {
  if (!state.config) return
  $('#cfgNodeName').value = state.config.node_name || ''
  $('#cfgRetention').value = state.config.retention_days ?? 0
  $('#cfgTrashRetention').value = state.config.trash_retention_days ?? 30
  $('#cfgMaxUpload').value = state.config.max_upload_mb || 4096
  $('#cfgBackupMax').value = state.config.backup_max_mb || 16384
  $('#cfgAutoOpen').checked = !!state.config.auto_open_browser
  $('#cfgAllowLAN').checked = !!state.config.allow_lan
  $('#cfgListenAddress').value = state.config.listen_address || (state.config.allow_lan ? '0.0.0.0' : '127.0.0.1')
  $('#cfgPublicBaseURL').value = state.config.public_base_url || ''
  $('#cfgAllowedOrigins').value = (state.config.allowed_origins || []).join(', ')
  $('#cfgAccessKey').value = ''
  $('#cfgAccessKeyEnabled').checked = state.config.access_key_enabled ?? !!state.config.has_access_key
  updateAccessKeyControls()
  renderPeers(state.config.peers || [])
}

function updateAccessKeyControls() {
  const enabled = $('#cfgAccessKeyEnabled').checked
  $('#cfgAccessKeyRow').classList.toggle('hidden', !enabled)
  $('#cfgAccessKey').disabled = !enabled
  $('#cfgAccessKey').placeholder = state.config?.has_access_key ? '留空保留已设置的密钥' : '自行设置，无长度和字符类型要求'
  $('#cfgAccessKeyWarning').textContent = enabled
    ? '开启后访问本节点需要此密钥；支持中文、数字、空格和符号。'
    : '密钥保护已关闭：能访问本机的设备可读写整个合并工作区。请勿暴露到公网。'
}

function renderPeers(peers) {
  $('#peerRows').innerHTML = peers.map(p => `
    <div class="peer-row" data-id="${escapeHTML(p.id)}">
      <input class="peer-name" value="${escapeHTML(p.name)}" placeholder="节点名称">
      <input class="peer-url" value="${escapeHTML(p.url)}" placeholder="http://100.x.x.x:8081">
      <button type="button" class="button danger small remove-peer">删除</button>
    </div>
  `).join('')
}

function addPeerRow(peer={id:'', name:'', url:''}) {
  $('#peerRows').insertAdjacentHTML('beforeend', `
    <div class="peer-row" data-id="${escapeHTML(peer.id)}">
      <input class="peer-name" value="${escapeHTML(peer.name)}" placeholder="节点名称">
      <input class="peer-url" value="${escapeHTML(peer.url)}" placeholder="http://100.x.x.x:8081">
      <button type="button" class="button danger small remove-peer">删除</button>
    </div>
  `)
}

async function saveSettings() {
  const secret = $('#cfgAccessKey').value
  const protection = $('#cfgAccessKeyEnabled').checked
  if (protection && secret === '' && !state.config?.has_access_key) {
    toast('请先填写访问密钥，或关闭密钥保护', true)
    return
  }
  const peers = $$('#peerRows .peer-row').map(row => ({
    id: row.dataset.id,
    name: row.querySelector('.peer-name').value.trim(),
    url: row.querySelector('.peer-url').value.trim()
  })).filter(p => p.url)

  const payload = {
    node_name: $('#cfgNodeName').value.trim(),
    max_upload_mb: Number($('#cfgMaxUpload').value),
    backup_max_mb: Number($('#cfgBackupMax').value),
    retention_days: Number($('#cfgRetention').value),
    trash_retention_days: Number($('#cfgTrashRetention').value),
    auto_open_browser: $('#cfgAutoOpen').checked,
    allow_lan: $('#cfgAllowLAN').checked,
    listen_address: $('#cfgListenAddress').value.trim(),
    public_base_url: $('#cfgPublicBaseURL').value.trim(),
    allowed_origins: $('#cfgAllowedOrigins').value.split(/[,，]/).map(v => v.trim()).filter(Boolean),
    access_key_enabled: protection,
    keep_access_key: !protection || secret === '',
    access_key: protection ? secret : '',
    peers
  }
  const requestHeaders = { 'Content-Type': 'application/json' }

  try {
    state.config = await api('/api/v1/config', {method:'PUT', headers:requestHeaders, body:JSON.stringify(payload)})
    renderNodeSecurity()
    if (protection && secret !== '') {
      state.accessKey = secret
      sessionSet('omnishare.accessKey', secret)
    }
    if (!protection) {
      state.accessKey = ''
      sessionRemove('omnishare.accessKey')
    }
    $('#settingsDialog').close()
    toast('设置已保存；网络设置需重启服务生效')
    await Promise.all([loadHealth(), loadDevices()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadNotes(strict=false, append=false, preserveWindow=false) {
  const seq = ++state.requestSeq.notes
  const q = encodeURIComponent($('#noteSearch').value.trim())
  const tag = encodeURIComponent($('#noteTagFilter').value)
  const offset = append ? state.notes.length : 0
  try {
    const count = preserveWindow ? Math.max(PAGE_SIZE, Math.min(state.notes.length, 10000)) : PAGE_SIZE
    const page = {items:[],total:0}
    for (let start=0; start<count; start+=500) {
      const batch=await apiPage(`/api/v1/notes?q=${q}&tag=${tag}&limit=${Math.min(500,count-start)}&offset=${offset+start}`)
      page.items.push(...batch.items); page.total=batch.total
      if (offset+page.items.length>=batch.total || batch.items.length===0) break
    }
    if (seq !== state.requestSeq.notes) return
    state.notes = append ? [...state.notes, ...page.items] : page.items
    state.noteTotal = page.total
    renderNotes()
  } catch (err) {
    if (seq === state.requestSeq.notes) reportLoadError(err, strict)
  }
}

function renderNotes() {
  $('#noteCount').textContent = state.noteTotal > state.notes.length
    ? `${state.notes.length} / ${state.noteTotal} 条速记`
    : `${state.notes.length} 条速记`
  $('#loadMoreNotes').classList.toggle('hidden', state.notes.length >= state.noteTotal)
  const tags = [...new Set(state.notes.flatMap(n => n.tags || []))].sort((a, b) => a.localeCompare(b, 'zh-CN'))
  const filter = $('#noteTagFilter')
  const signature = tags.join('\u0000')
  // Rebuilding the option list on every load resets scroll state and forces the
  // browser to reparse HTML; only touch the DOM when the tag set actually changed.
  if (filter.dataset.signature !== signature) {
    const current = filter.value
    filter.innerHTML = '<option value="">全部标签</option>' + tags.map(t => `<option value="${escapeHTML(t)}">#${escapeHTML(t)}</option>`).join('')
    filter.dataset.signature = signature
    filter.value = current
  }

  $('#notesList').innerHTML = state.notes.length ? state.notes.map(n => {
    const restricted = !n.content && !!n.content_redacted
    const restriction = n.is_burn_after_read ? '阅后即焚' : (n.max_read_count > 0 ? `剩余 ${Math.max(0, n.max_read_count - n.read_count)} 次` : '')
    return `
      <article class="card note-card" data-note-id="${escapeHTML(n.id)}">${sourceLabel(n)}
        <div class="note-head">
          <div class="note-meta">
            <span class="tag">${escapeHTML(n.id)}</span>
            ${n.pinned ? '<span class="badge">置顶</span>' : ''}
            ${n.is_burn_after_read ? '<span class="badge muted">阅后即焚</span>' : ''}
            ${n.max_read_count > 0 ? `<span class="badge muted">阅读 ${n.read_count}/${n.max_read_count}</span>` : ''}
            <span>${formatDate(n.updated_at)}</span>
          </div>
          <div class="note-actions">
            ${restricted
              ? '<button class="text-button read-note">读取</button><button class="text-button copy-note">读取并复制</button>'
              : '<button class="text-button copy-note">复制</button>'}
            <button class="text-button share-note">分享</button>
            <button class="text-button edit-note">编辑</button>
            <button class="text-button raw-note">Raw</button>
            <button class="text-button danger delete-note">删除</button>
          </div>
        </div>
        <div class="note-content${restricted ? ' restricted' : ''}">
          ${restricted ? `🔒 受限内容（${escapeHTML(restriction)}），主动读取才会计数。` : escapeHTML(n.content)}
        </div>
        ${(n.tags || []).length ? `<div class="tag-row">${(n.tags || []).map(t => `<span class="tag">#${escapeHTML(t)}</span>`).join('')}</div>` : ''}
      </article>
    `
  }).join('') : `
    <div class="empty">
      <div class="empty-icon">📝</div>
      <h3>没有匹配的随手记</h3>
      <p>在上方输入框编写文本或提取剪贴板以创建第一条随手记</p>
    </div>
  `
}

async function getManagedNote(id) {
  const listed = state.notes.find(x => x.id === id)
  if (!listed) throw new Error('随手记不存在')
  return listed.content_redacted ? api(`/api/v1/notes/${encodeURIComponent(id)}/manage`) : listed
}

async function readRestrictedNote(id, copy=false) {
  const listed = state.notes.find(x => x.id === id)
  if (!listed) return
  if (listed.content_redacted && !confirm(listed.is_burn_after_read ? '读取后该内容将被彻底销毁，是否继续？' : '本次读取会消耗一次可读次数，是否继续？')) return
  try {
    const note = listed.content_redacted ? await api(`/api/v1/notes/${encodeURIComponent(id)}`) : listed
    if (copy) {
      await copyText(note.content)
      toast('已复制随手记内容')
    } else {
      $('#viewNoteMeta').textContent = `${note.id} · 已读取 ${note.read_count}${note.max_read_count ? `/${note.max_read_count}` : ''}`
      $('#viewNoteContent').textContent = note.content
      $('#viewNoteDialog').showModal()
    }
    await Promise.all([loadNotes(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function createNote() {
  const content = $('#noteContent').value.trim()
  if (!content) {
    toast('请输入内容', true)
    return
  }
  const payload = {
    content,
    tags: $('#noteTags').value.split(/[,，]/).map(s => s.trim()).filter(Boolean),
    pinned: $('#notePinned').checked,
    is_burn_after_read: $('#noteBurn').checked,
    max_read_count: Number($('#noteMaxReads').value || 0),
    ttl_seconds: Number($('#noteTTL').value)
  }
  try {
    await api('/api/v1/notes', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    })
    $('#noteContent').value = ''
    $('#noteTags').value = ''
    $('#notePinned').checked = false
    $('#noteBurn').checked = false
    $('#noteMaxReads').value = '0'
    toast('随手记已发布')
    await Promise.all([loadNotes(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

function resetClipboardPreview() {
  clipboardReadSequence++
  clipboardSelection = null
  if (clipboardPreviewURL) URL.revokeObjectURL(clipboardPreviewURL)
  clipboardPreviewURL = ''
  $('#clipboardPreviewText').value = ''
  $('#clipboardPreviewText').classList.add('hidden')
  $('#clipboardPreviewImage').removeAttribute('src')
  $('#clipboardPreviewImage').classList.add('hidden')
  $('#clipboardPreviewMeta').textContent = ''
  $('#clipboardPreviewStatus').textContent = ''
}

function closeClipboardPreview() {
  if (clipboardSending) return
  resetClipboardPreview()
  $('#clipboardDialog').close()
}

async function validateClipboardPNG(blob) {
  const uploadLimit = Number(state.config?.max_upload_mb) * 1024 * 1024
  const limit = Math.min(CLIPBOARD_IMAGE_LIMIT, uploadLimit > 0 ? uploadLimit : CLIPBOARD_IMAGE_LIMIT)
  if (blob.type !== 'image/png' || !blob.size) throw new Error('剪贴板中没有可发送的 PNG 图片')
  if (blob.size > limit) throw new Error(`剪贴板图片超过 ${formatBytes(limit)}，请先缩小图片`)
  // Bound decoded dimensions as well as bytes before displaying a local blob.
  const header = new Uint8Array(await blob.slice(0, 24).arrayBuffer())
  const signature = [137, 80, 78, 71, 13, 10, 26, 10]
  if (header.length !== 24 || signature.some((byte, i) => header[i] !== byte) ||
      header[8] !== 0 || header[9] !== 0 || header[10] !== 0 || header[11] !== 13 ||
      header[12] !== 73 || header[13] !== 72 || header[14] !== 68 || header[15] !== 82) {
    throw new Error('剪贴板图片格式无效')
  }
  const view = new DataView(header.buffer, header.byteOffset, header.byteLength)
  const width = view.getUint32(16)
  const height = view.getUint32(20)
  if (!width || !height || width > 8192 || height > 8192 || width * height > 16 * 1024 * 1024) {
    throw new Error('剪贴板图片尺寸过大，请先缩小图片')
  }
  return { width, height }
}

async function readClipboardPreview() {
  if (clipboardReading || clipboardSending) return
  resetClipboardPreview()
  const sequence = clipboardReadSequence
  clipboardReading = true
  $('#pasteBtn').disabled = true
  try {
    if (!navigator.clipboard) throw new Error('当前浏览器无法读取剪贴板，请使用 HTTPS 或本机地址，或直接粘贴到编辑框')
    let selection
    if (typeof navigator.clipboard.read === 'function') {
      const items = await navigator.clipboard.read()
      // Only one representation is selected. HTML, SVG and arbitrary clipboard
      // formats are never inserted into the document or uploaded implicitly.
      const image = items.find(item => item.types.includes('image/png'))
      if (image) {
        const blob = await image.getType('image/png')
        const dimensions = await validateClipboardPNG(blob)
        const stamp = new Date().toISOString().replace(/[:.]/g, '-')
        selection = { type: 'image', file: new File([blob], `clipboard-${stamp}.png`, {type:'image/png'}), ...dimensions }
      } else {
        const item = items.find(entry => entry.types.includes('text/plain'))
        if (!item) throw new Error('剪贴板中没有纯文本或 PNG 图片，请选择文件上传')
        const blob = await item.getType('text/plain')
        if (blob.size > CLIPBOARD_TEXT_LIMIT) throw new Error('剪贴板文本超过 256 KB，请改为文件上传')
        selection = { type: 'text', text: await blob.text() }
      }
    } else if (typeof navigator.clipboard.readText === 'function') {
      selection = { type: 'text', text: await navigator.clipboard.readText() }
    } else {
      throw new Error('当前浏览器不支持读取剪贴板，请直接粘贴到编辑框')
    }
    if (selection.type === 'text') {
      if (!selection.text.trim()) throw new Error('剪贴板中的文本为空')
      if (new Blob([selection.text]).size > CLIPBOARD_TEXT_LIMIT) throw new Error('剪贴板文本超过 256 KB，请改为文件上传')
    }
    if (sequence !== clipboardReadSequence) return
    const destination = `${state.config?.node_name || '当前节点'} · ${location.origin}`
    if (selection.type === 'image') {
      clipboardPreviewURL = URL.createObjectURL(selection.file)
      const preview = $('#clipboardPreviewImage')
      preview.src = clipboardPreviewURL
      if (typeof preview.decode !== 'function') throw new Error('当前浏览器无法预览此图片，请选择文件上传')
      try { await preview.decode() } catch { throw new Error('无法解码剪贴板图片，请重新复制图片或选择文件上传') }
      if (sequence !== clipboardReadSequence) return
      preview.classList.remove('hidden')
      $('#clipboardPreviewMeta').textContent = `PNG 图片 · ${selection.width} × ${selection.height} · ${formatBytes(selection.file.size)} → ${destination}`
    } else {
      $('#clipboardPreviewText').value = selection.text
      $('#clipboardPreviewText').classList.remove('hidden')
      $('#clipboardPreviewMeta').textContent = `纯文本 · ${formatBytes(new Blob([selection.text]).size)} → ${destination}`
    }
    clipboardSelection = selection
    $('#clipboardTTL').value = '86400'
    $('#clipboardConfirmBtn').disabled = false
    $('#clipboardCancelBtn').disabled = false
    $('#clipboardDialog').showModal()
  } catch (err) {
    if (sequence === clipboardReadSequence) {
      resetClipboardPreview()
      toast(err.name === 'NotAllowedError' ? '未获得剪贴板读取权限；可以直接粘贴到编辑框' : err.message, true)
    }
  } finally {
    clipboardReading = false
    $('#pasteBtn').disabled = false
  }
}

async function confirmClipboardSend() {
  const selection = clipboardSelection
  if (!selection || clipboardReading || clipboardSending) return
  const ttl = Number($('#clipboardTTL').value)
  if (![3600, 86400, 604800].includes(ttl)) {
    $('#clipboardPreviewStatus').textContent = '请选择有效的保留时长'
    return
  }
  clipboardSending = true
  $('#clipboardConfirmBtn').disabled = true
  $('#clipboardCancelBtn').disabled = true
  $('#clipboardPreviewStatus').textContent = '正在发送到当前节点…'
  try {
    if (selection.type === 'image') {
      if (!uploadFiles([selection.file], {ttl})) throw new Error('未加入上传队列，请检查文件上限或清理已结束的任务后再试')
      resetClipboardPreview()
      $('#clipboardDialog').close()
      switchTab('files')
      toast('剪贴板图片已加入传输队列')
    } else {
      const body = JSON.stringify({content:selection.text, tags:['剪贴板'], ttl_seconds:ttl})
      // The API bounds the entire JSON body, including escaped control bytes.
      if (new Blob([body]).size > 1024 * 1024) throw new Error('文本编码后超过请求上限，请改为文件上传')
      await api('/api/v1/notes', {method:'POST', headers:{'Content-Type':'application/json'}, body})
      resetClipboardPreview()
      $('#clipboardDialog').close()
      switchTab('notes')
      toast('剪贴板文本已保存为随手记')
      await loadDashboard()
    }
  } catch (err) {
    $('#clipboardPreviewStatus').textContent = `${err.message}。若连接中断，请先检查节点中的内容，再决定是否重新发送。`
  } finally {
    clipboardSending = false
    $('#clipboardConfirmBtn').disabled = false
    $('#clipboardCancelBtn').disabled = false
  }
}

async function deleteNote(id) {
  if (!confirm('将这条随手记移入回收站？')) return
  try {
    await api(`/api/v1/notes/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('已移入回收站')
    await Promise.all([loadNotes(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function openEditNote(id) {
  try {
    const n = await getManagedNote(id)
    state.editingNote = n
    $('#editNoteId').textContent = n.id
    $('#editNoteContent').value = n.content
    $('#editNoteTags').value = (n.tags || []).join(', ')
    $('#editNotePinned').checked = !!n.pinned
    $('#editNoteBurn').checked = !!n.is_burn_after_read
    $('#editNoteMaxReads').value = n.max_read_count || 0
    $('#editNoteDialog').showModal()
  } catch (err) {
    toast(err.message, true)
  }
}

async function saveEditNote() {
  if (!state.editingNote) return
  const payload = {
    content: $('#editNoteContent').value,
    tags: $('#editNoteTags').value.split(/[,，]/).map(s => s.trim()).filter(Boolean),
    pinned: $('#editNotePinned').checked,
    is_burn_after_read: $('#editNoteBurn').checked,
    max_read_count: Number($('#editNoteMaxReads').value || 0)
  }
  try {
    await api(`/api/v1/notes/${encodeURIComponent(state.editingNote.id)}`, {
      method: 'PUT',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify(payload)
    })
    $('#editNoteDialog').close()
    toast('随手记已更新')
    await loadNotes()
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadFiles(strict=false, append=false, preserveWindow=false) {
  const seq = ++state.requestSeq.files
  const q = encodeURIComponent($('#fileSearch').value.trim())
  const type = encodeURIComponent($('#fileType').value)
  const offset = append ? state.files.length : 0
  try {
    const count = preserveWindow ? Math.max(PAGE_SIZE, Math.min(state.files.length, 10000)) : PAGE_SIZE
    const page = {items:[],total:0}
    for (let start=0; start<count; start+=500) {
      const batch=await apiPage(`/api/v1/files?q=${q}&type=${type}&limit=${Math.min(500,count-start)}&offset=${offset+start}`)
      page.items.push(...batch.items); page.total=batch.total
      if (offset+page.items.length>=batch.total || batch.items.length===0) break
    }
    if (seq !== state.requestSeq.files) return
    state.files = append ? [...state.files, ...page.items] : page.items
    state.fileTotal = page.total
    renderFiles()
  } catch (err) {
    if (seq === state.requestSeq.files) reportLoadError(err, strict)
  }
}

function fileIcon(f) {
  let shape = '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>'
  if (f.is_video) shape = '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="m10 8 6 4-6 4Z"/>'
  else if ((f.mime_type || '').startsWith('image/')) shape = '<rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>'
  else if (/(zip|tar|rar)/.test(f.mime_type || '')) shape = '<rect x="3" y="5" width="18" height="15" rx="2"/><path d="M3 9h18M10 13h4M7 5V3h10v2"/>'
  return `<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${shape}</svg>`
}

function renderFiles() {
  $('#fileCount').textContent = state.fileTotal > state.files.length
    ? `${state.files.length} / ${state.fileTotal} 个文件`
    : `${state.files.length} 个文件`
  $('#loadMoreFiles').classList.toggle('hidden', state.files.length >= state.fileTotal)
  $('#filesList').innerHTML = state.files.length ? state.files.map(f => `
    <article class="card file-card" data-file-id="${escapeHTML(f.id)}">
      <div class="file-info">
        <div class="file-icon">${fileIcon(f)}</div>
        <div class="file-description">
          ${sourceLabel(f)}<h3 title="${escapeHTML(f.file_name)}">${escapeHTML(f.file_name)}</h3>
          <p>${formatBytes(f.file_size)} · ${escapeHTML(f.mime_type)} · 下载 ${f.download_count || 0} 次</p>
        </div>
      </div>
      <div class="file-actions">
        ${f.is_video ? '<button class="button ghost small play-file">▶ 播放</button>' : ''}
        <button class="button ghost small download-file">下载</button>
        <button class="button ghost small share-file">分享</button>
        <button class="button ghost small rename-file">重命名</button>
        <button class="button danger small delete-file">删除</button>
      </div>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">📁</div>
      <h3>没有匹配的文件</h3>
      <p>拖拽文件至上方虚线区域或点击选择文件即可上传</p>
    </div>
  `
}

async function createFileTicket(file, disposition='attachment') {
  return api(`/api/v1/files/${encodeURIComponent(file.id)}/ticket`, {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({disposition})
  })
}

async function openDownload(f) {
  try {
    const ticket = await createFileTicket(f, 'attachment')
    const a = document.createElement('a')
    a.href = ticket.url
    a.download = f.file_name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } catch (err) {
    toast(err.message, true)
  }
}

async function playFile(f) {
  try {
    const ticket = await createFileTicket(f, 'inline')
    $('#videoTitle').textContent = f.file_name
    const player = $('#videoPlayer')
    player.src = ticket.url
    $('#videoDialog').showModal()
    await player.play().catch(() => {})
  } catch (err) {
    toast(err.message, true)
  }
}

async function openRawNote(id) {
  try {
    const res = await fetch(`/n/${encodeURIComponent(id)}/raw`, {headers: headers()})
    if (res.status === 401) {
      showAuth()
      return
    }
    if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    window.open(url, '_blank', 'noopener')
    setTimeout(() => URL.revokeObjectURL(url), 60000)
  } catch (err) {
    toast(err.message, true)
  }
}

async function deleteFile(id) {
  if (!confirm('将该文件移入回收站？磁盘实体会暂时保留。')) return
  try {
    await api(`/api/v1/files/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('文件已移入回收站')
    await Promise.all([loadFiles(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function renameFile(file) {
  const name = prompt('新的文件名', file.file_name)
  if (!name || name === file.file_name) return
  try {
    await api(`/api/v1/files/${encodeURIComponent(file.id)}`, {
      method: 'PUT',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({file_name:name})
    })
    toast('文件已重命名')
    await loadFiles()
  } catch (err) {
    toast(err.message, true)
  }
}

const UPLOAD_CONCURRENCY = 3
const MAX_UPLOAD_TASKS = 100
const UPLOAD_CANCEL_NOTICE = '本次传输已取消，文件可能已在服务端保存；重试前请检查文件列表。'

function hasPendingUploads() {
  return state.uploadQueue.some(task => task.status === 'queued' || task.status === 'uploading')
}

function uploadSizeError(file) {
  if (!file || !Number.isFinite(file.size) || file.size < 0) return '无法读取文件大小'
  const maxMB = Number(state.config?.max_upload_mb)
  if (Number.isFinite(maxMB) && maxMB > 0 && file.size > maxMB * 1024 ** 2) {
    return `超过节点单文件上限 ${formatBytes(maxMB * 1024 ** 2)}`
  }
  return ''
}

function uploadFiles(files, options = {}) {
  const ttl = String(options.ttl ?? $('#fileTTL')?.value ?? 0)
  let accepted = 0
  let rejected = 0
  let firstRejection = ''
  state.uploadNotice = ''
  for (const file of files || []) {
    const reason = uploadSizeError(file) || (state.uploadQueue.length >= MAX_UPLOAD_TASKS
      ? `最多保留 ${MAX_UPLOAD_TASKS} 个任务，请先清除已结束的任务` : '')
    if (reason) {
      rejected++
      if (!firstRejection) firstRejection = `${String(file?.name || '文件')}：${reason}`
      continue
    }
    const task = {
      file, name: String(file.name), size: file.size, ttl, order: ++state.uploadSequence,
      status: 'queued', attempt: 0, loaded: 0, total: 0, progress: null,
      processing: false, error: '', xhr: null, cancelAttempt: null
    }
    state.uploadQueue.push(task)
    createUploadRow(task)
    accepted++
  }
  if (rejected) {
    state.uploadNotice = `${rejected} 个文件未加入：${firstRejection}`
    toast(state.uploadNotice, true)
  }
  updateUploadSummary()
  pumpUploads()
  return accepted
}

function createUploadRow(task) {
  const make = (tag, className, text = '') => {
    const node = document.createElement(tag)
    node.className = className
    node.textContent = text
    return node
  }
  const row = make('div', 'progress-card upload-task')
  const head = make('div', 'upload-task-head')
  const name = make('span', 'upload-file-name', task.name)
  name.title = task.name
  const status = make('span', 'pct upload-status badge muted')
  const meta = make('p', 'upload-meta')
  const progress = make('progress', 'progress-track')
  progress.max = 100
  progress.setAttribute('aria-label', `${task.name} 上传进度`)
  const detail = make('p', 'upload-detail')
  const actions = make('div', 'file-actions upload-actions')
  const cancel = make('button', 'button ghost small cancel-upload', '取消')
  const retry = make('button', 'button ghost small retry-upload', '重试')
  cancel.type = retry.type = 'button'
  cancel.addEventListener('click', () => cancelUpload(task))
  retry.addEventListener('click', () => retryUpload(task))
  head.append(name, status)
  actions.append(cancel, retry)
  row.append(head, meta, progress, detail, actions)
  task.view = { row, status, meta, progress, detail, cancel, retry }
  $('#uploadQueue').appendChild(row)
  renderUploadTask(task)
}

function renderUploadTask(task) {
  const { row, status, meta, progress, detail, cancel, retry } = task.view
  const pending = task.status === 'queued' || task.status === 'uploading'
  row.dataset.uploadState = task.status
  const labels = { queued: '排队中', succeeded: '已完成', failed: '失败', cancelled: '已取消' }
  status.textContent = task.status === 'uploading'
    ? (task.processing ? '服务端处理中' : task.progress === null ? '上传中' : `上传中 ${task.progress}%`)
    : labels[task.status]
  meta.textContent = `${formatBytes(task.size)}${task.attempt > 1 ? ` · 第 ${task.attempt} 次尝试` : ''}`
  if (task.status === 'uploading' && task.progress === null) progress.removeAttribute('value')
  else progress.value = task.status === 'succeeded' || task.processing ? 100 : task.progress || 0
  detail.textContent = task.status === 'queued' ? '等待开始上传，可随时取消。'
    : task.status === 'succeeded' ? '服务器已确认保存。'
    : task.status === 'failed' || task.status === 'cancelled' ? task.error
    : task.processing ? '文件内容已发送，正在等待服务器确认保存。'
    : task.total > 0 ? `已发送 ${formatBytes(task.loaded)} / ${formatBytes(task.total)}`
    : task.loaded > 0 ? `已发送 ${formatBytes(task.loaded)}，等待进度信息。` : '正在上传，等待进度信息。'
  cancel.hidden = cancel.disabled = !pending
  retry.hidden = retry.disabled = !((task.status === 'failed' || task.status === 'cancelled') && task.file)
}

function updateUploadSummary() {
  const counts = { queued: 0, uploading: 0, succeeded: 0, failed: 0, cancelled: 0 }
  for (const task of state.uploadQueue) counts[task.status]++
  const summary = $('#transferSummary')
  if (summary) {
    const text = state.uploadQueue.length
      ? `上传中 ${counts.uploading} · 排队 ${counts.queued} · 已完成 ${counts.succeeded} · 失败 ${counts.failed} · 已取消 ${counts.cancelled}`
      : '等待上传'
    summary.textContent = state.uploadNotice ? `${text}。${state.uploadNotice}` : text
  }
  const clear = $('#clearFinishedUploadsBtn')
  if (clear) clear.disabled = counts.succeeded + counts.failed + counts.cancelled === 0
  const cancel = $('#cancelAllUploadsBtn')
  if (cancel) cancel.disabled = counts.queued + counts.uploading === 0
}

function clearFinishedUploads() {
  state.uploadQueue = state.uploadQueue.filter(task => {
    if (task.status === 'queued' || task.status === 'uploading') return true
    task.file = null
    task.view.row.remove()
    task.view = null
    return false
  })
  state.uploadNotice = ''
  updateUploadSummary()
}

function cancelUpload(task) {
  if (!state.uploadQueue.includes(task)) return
  if (task.status === 'queued') {
    task.status = 'cancelled'
    task.error = task.attempt ? UPLOAD_CANCEL_NOTICE : '已取消，文件尚未发送。'
    renderUploadTask(task)
    updateUploadSummary()
  } else if (task.status === 'uploading') {
    task.cancelAttempt?.()
  }
}

function cancelAllUploads() {
  // Cancel waiting tasks first, so aborting a running request cannot start one.
  state.uploadQueue.filter(task => task.status === 'queued').forEach(cancelUpload)
  state.uploadQueue.filter(task => task.status === 'uploading').forEach(cancelUpload)
}

function retryUpload(task) {
  if (!state.uploadQueue.includes(task) || !task.file || !['failed', 'cancelled'].includes(task.status)) return false
  const reason = uploadSizeError(task.file)
  if (reason) {
    task.status = 'failed'
    task.error = reason
    renderUploadTask(task)
    updateUploadSummary()
    return false
  }
  Object.assign(task, { status: 'queued', order: ++state.uploadSequence, loaded: 0, total: 0, progress: null, processing: false, error: '' })
  renderUploadTask(task)
  updateUploadSummary()
  pumpUploads()
  return true
}

function pumpUploads() {
  if (state.uploadPumping) return
  state.uploadPumping = true
  try {
    while (state.activeUploads < UPLOAD_CONCURRENCY) {
      const task = state.uploadQueue.filter(item => item.status === 'queued').sort((a, b) => a.order - b.order)[0]
      if (!task) break
      uploadOne(task)
    }
  } finally {
    state.uploadPumping = false
    updateUploadSummary()
  }
}

function uploadOne(task) {
  if (task.status !== 'queued' || state.activeUploads >= UPLOAD_CONCURRENCY) return
  task.status = 'uploading'
  const attempt = ++task.attempt
  state.activeUploads++
  let xhr = null
  const current = () => task.status === 'uploading' && task.attempt === attempt && task.xhr === xhr
  const finish = (status, message = '', abort = false) => {
    if (!current()) return false
    // Invalidate the attempt before abort(): synchronous or queued events from
    // this request must never finish a cancellation or a later retry again.
    task.status = status
    task.error = message
    task.xhr = null
    task.cancelAttempt = null
    if (xhr) {
      xhr.onload = xhr.onerror = xhr.ontimeout = xhr.onabort = null
      xhr.upload.onprogress = xhr.upload.onload = null
      if (abort) { try { xhr.abort() } catch { /* the attempt is already invalidated */ } }
    }
    state.activeUploads--
    if (status === 'succeeded') task.file = null
    renderUploadTask(task)
    updateUploadSummary()
    pumpUploads()
    return true
  }
  task.cancelAttempt = () => finish('cancelled', UPLOAD_CANCEL_NOTICE, true)
  renderUploadTask(task)
  try {
    xhr = new XMLHttpRequest()
    task.xhr = xhr
    // Only this node receives the file or session headers. Discovery URLs and
    // other caller-supplied destinations are never upload targets.
    xhr.open('POST', '/api/v1/files/upload')
    xhr.timeout = Math.max(5 * 60 * 1000, Math.ceil(task.size / (64 * 1024)) * 1000)
    Object.entries(authHeaders()).forEach(([name, value]) => xhr.setRequestHeader(name, value))
    xhr.upload.onprogress = event => {
      if (!current() || task.processing) return
      task.loaded = Math.max(0, event.loaded)
      task.total = event.lengthComputable && event.total > 0 ? event.total : 0
      task.progress = task.total ? Math.min(100, Math.floor(task.loaded / task.total * 100)) : null
      task.processing = task.progress === 100
      renderUploadTask(task)
    }
    xhr.upload.onload = () => {
      if (!current()) return
      task.processing = true
      task.progress = 100
      renderUploadTask(task)
    }
    xhr.onload = () => {
      if (!current()) return
      let payload
      try { payload = JSON.parse(xhr.responseText) } catch { /* invalid responses are failures */ }
      if (xhr.status >= 200 && xhr.status < 300 && payload?.code === 0) {
        if (finish('succeeded')) {
          toast(`${task.name} 上传完成`)
          void Promise.allSettled([loadFiles(), loadDashboard()])
        }
        return
      }
      const message = typeof payload?.message === 'string' ? payload.message : `上传未获确认（HTTP ${xhr.status}），请检查文件列表后再重试。`
      finish('failed', message)
      if (xhr.status === 401) showAuth()
    }
    xhr.onerror = () => finish('failed', '网络连接错误，文件可能已在服务端保存；请检查文件列表后再重试。')
    xhr.ontimeout = () => finish('failed', '上传超时，文件可能已在服务端保存；请检查文件列表后再重试。')
    xhr.onabort = () => finish('cancelled', UPLOAD_CANCEL_NOTICE)
    const form = new FormData()
    form.append('file', task.file)
    form.append('ttl_seconds', task.ttl)
    xhr.send(form)
  } catch (err) {
    finish('failed', `无法开始上传：${err.message}`, true)
  }
}

async function loadPads(strict=false) {
  const seq = ++state.requestSeq.pads
  try {
    const pads = await apiCollection(`/api/v1/pads?q=${encodeURIComponent($('#padSearch').value.trim())}`)
    if (seq !== state.requestSeq.pads) return
    state.pads = pads
    renderPads()
  } catch (err) {
    if (seq === state.requestSeq.pads) reportLoadError(err, strict)
  }
}

function renderPads() {
  $('#padsList').innerHTML = state.pads.length ? state.pads.map(p => `
    <button class="pad-item ${state.currentPad?.id === p.id ? 'active' : ''}" data-pad-id="${escapeHTML(p.id)}">
      <strong>${escapeHTML(p.title)}</strong>${sourceLabel(p)}
      <span>v${p.version} · ${formatDate(p.updated_at)}</span>
    </button>
  `).join('') : `
    <div class="empty">
      <p>暂无协同文档</p>
    </div>
  `
}

function renderPadEditor(p) {
  $('#padConflictActions')?.classList.add('hidden')
  $('#padEmpty').classList.add('hidden')
  $('#padForm').classList.remove('hidden')
  $('#padTitle').value = p.title
  $('#padContent').value = p.content
  $('#padMeta').textContent = `${p.id} · 版本 v${p.version} · 更新于 ${formatDate(p.updated_at)}`
  $('#saveState').textContent = '已同步'
  $('#saveState').className = 'badge'
  renderPads()
}

async function selectPad(id) {
  const selectSeq = ++state.padSelectSeq
  if (state.currentPad?.id === id) return
  if (state.currentPadDirty) {
    const saved = await savePad(false)
    if (!saved && !confirm('当前文档未能保存，仍要切换吗？')) return
  }
  clearTimeout(state.saveTimer)
  try {
    const p = await api(`/api/v1/pads/${encodeURIComponent(id)}`)
    if (selectSeq !== state.padSelectSeq) return
    state.currentPad = { ...p }
    state.currentPadDirty = false
    state.padSaveBlocked = false
    renderPadEditor(p)
  } catch (err) {
    toast(err.message, true)
  }
}

function createPad() {
  $('#newPadTitle').value = '新协同文档'
  $('#newPadError').textContent = ''
  $('#newPadDialog').showModal()
  $('#newPadTitle').focus()
}

async function submitNewPad() {
  if (state.padCreating) return
  const title = $('#newPadTitle').value.trim()
  if (!title) { $('#newPadError').textContent = '请填写文档标题'; return }
  state.padCreating = true
  $('#createPadSubmitBtn').disabled = true
  try {
    if (state.currentPadDirty && !await savePad(false)) throw new Error('当前文档未保存，请先处理草稿或冲突')
    const p = await api('/api/v1/pads', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({title, content: `# ${title}\n\n`})
    })
    $('#newPadDialog').close()
    toast('文档已创建')
    await Promise.all([loadPads(), loadDashboard()])
    await selectPad(p.id)
  } catch (err) { $('#newPadError').textContent = err.message }
  finally { state.padCreating = false; $('#createPadSubmitBtn').disabled = false }
}

async function refreshCurrentPad() {
  const current = state.currentPad
  if (!current || state.saveInFlight) return
  const seq = ++state.padRefreshSeq
  try {
    const remote = await api(`/api/v1/pads/${encodeURIComponent(current.id)}`)
    if (seq !== state.padRefreshSeq || state.currentPad?.id !== current.id || state.saveInFlight) return
    if (remote.version === state.currentPad.version) return
    if (state.currentPadDirty) {
      preservePadDraft()
      state.padSaveBlocked = true
      clearTimeout(state.saveTimer)
      $('#saveState').textContent = '远端已更新 · 本地草稿保留'
      $('#padConflictActions').classList.remove('hidden')
      return
    }
    const editor = $('#padContent'), start = editor.selectionStart, end = editor.selectionEnd, scroll = editor.scrollTop
    state.currentPad = {...remote}
    renderPadEditor(remote)
    if (typeof editor.setSelectionRange === 'function' && Number.isInteger(start)) editor.setSelectionRange(Math.min(start, remote.content.length), Math.min(end, remote.content.length))
    editor.scrollTop = scroll
  } catch (err) {
    if (state.currentPad?.id === current.id) {
      $('#saveState').textContent = err.status === 404 ? '文档已删除 · 保留当前内容' : '连接中断 · 保留当前内容'
      if (state.currentPadDirty) preservePadDraft()
    }
  }
}

async function refreshWorkspace() {
  if (document.hidden || state.workspaceBusy || $('#authDialog').open || state.restoreInFlight) return
  state.workspaceBusy = true
  try {
    await loadDashboard()
    const loaders = {dashboard:()=>loadNotes(false,false,true), notes:()=>loadNotes(false,false,true), files:()=>loadFiles(false,false,true), pads:loadPads, shares:loadShares, trash:loadTrash, activity:loadAudit}
    if (loaders[state.activeTab]) await loaders[state.activeTab]()
    if (state.activeTab === 'pads') await refreshCurrentPad()
  } finally { state.workspaceBusy = false }
}

async function loadRemotePadVersion() {
  if (!state.currentPad || state.saveInFlight) return
  preservePadDraft()
  const id = state.currentPad.id
  try {
    const remote = await api(`/api/v1/pads/${encodeURIComponent(id)}`)
    if (state.currentPad?.id !== id) return
    state.currentPad = {...remote}; state.currentPadDirty = false; state.padSaveBlocked = false
    renderPadEditor(remote); $('#padConflictActions').classList.add('hidden')
    toast('已载入远端版本；原草稿仍保留在本次会话中')
  } catch (err) { toast(err.message, true) }
}

async function savePadDraftCopy() {
  const draft = padSnapshot(); if (!draft) return
  try {
    const copy = await api('/api/v1/pads', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title:(draft.title+'（草稿副本）').slice(0,200),content:draft.content})})
    preservePadDraft(draft)
    state.currentPad = {...copy}; state.currentPadDirty = false; state.padSaveBlocked = false
    renderPadEditor(copy); $('#padConflictActions').classList.add('hidden'); await loadPads()
    toast('草稿副本已保存到当前设备')
  } catch (err) { toast(err.message, true) }
}

function padSnapshot() {
  if (!state.currentPad) return null
  return {
    id: state.currentPad.id,
    title: $('#padTitle').value.trim() || '未命名文档',
    content: $('#padContent').value,
    version: state.currentPad.version
  }
}

function preservePadDraft(snapshot = padSnapshot()) {
  if (!snapshot) return
  // An autosave may fail after the user has typed more. Preserve the editor's
  // latest contents instead of overwriting them with the older request body.
  const draft = state.currentPad?.id === snapshot.id && state.currentPadDirty
    ? padSnapshot() : snapshot
  sessionSet(`omnishare.padDraft.${draft.id}`, JSON.stringify(draft))
}

function markPadDirty() {
  if (!state.currentPad) return
  state.currentPadDirty = true
  if (state.padSaveBlocked) {
    // After a 409 the local draft is intentionally kept; keep retrying the same
    // stale version would only produce a conflict toast and a full pad reload on
    // every keystroke.
    $('#saveState').textContent = '版本冲突'
    $('#saveState').className = 'badge muted'
    return
  }
  $('#saveState').textContent = '待保存'
  $('#saveState').className = 'badge muted'
  clearTimeout(state.saveTimer)
  const snapshot = padSnapshot()
  state.saveTimer = setTimeout(() => enqueuePadSave(snapshot, false), 900)
}

function enqueuePadSave(snapshot, showToast=true) {
  if (!snapshot) return Promise.resolve(true)
  state.padSaveChain = state.padSaveChain.catch(() => false).then(() => performPadSave(snapshot, showToast))
  return state.padSaveChain
}

async function performPadSave(snapshot, showToast=true) {
  state.saveInFlight = true
  const isCurrent = state.currentPad?.id === snapshot.id
  if (isCurrent) $('#saveState').textContent = '保存中...'
  const known = isCurrent ? state.currentPad.version : (state.pads.find(p => p.id === snapshot.id)?.version || snapshot.version)
  const payload = {title: snapshot.title, content: snapshot.content, version: known}
  try {
    const p = await api(`/api/v1/pads/${encodeURIComponent(snapshot.id)}`, {
      method: 'PUT',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    })
    const index = state.pads.findIndex(item => item.id === p.id)
    if (index >= 0) state.pads[index] = { ...p }
    else state.pads.unshift({ ...p })
    if (state.currentPad?.id === snapshot.id) {
      state.currentPad = { ...p }
      const unchanged = $('#padTitle').value.trim() === snapshot.title && $('#padContent').value === snapshot.content
      state.currentPadDirty = !unchanged
      $('#saveState').textContent = unchanged ? '已保存' : '待保存'
      $('#saveState').className = `badge${unchanged ? '' : ' muted'}`
      $('#padMeta').textContent = `${p.id} · 版本 v${p.version} · 更新于 ${formatDate(p.updated_at)}`
      if (unchanged) sessionRemove(`omnishare.padDraft.${p.id}`)
    }
    renderPads()
    if (showToast) toast('文档已保存')
    return true
  } catch (err) {
    preservePadDraft(snapshot)
    if (err.status === 409) { state.padSaveBlocked = true; $('#padConflictActions')?.classList.remove('hidden') }
    if (state.currentPad?.id === snapshot.id) {
      $('#saveState').textContent = err.status === 409 ? '版本冲突' : '保存失败'
      $('#saveState').className = 'badge muted'
    }
    toast(err.status === 409 ? '发现远端新版本，本地草稿已保留在会话中' : err.message, true)
    if (err.status === 409) {
      // Refresh the list once so the UI shows the remote version, then wait for
      // an explicit user action instead of retrying the stale version forever.
      await loadPads()
    }
    return false
  } finally { state.saveInFlight = false }
}

async function savePad(showToast=true) {
  clearTimeout(state.saveTimer)
  state.padSaveBlocked = false
  return enqueuePadSave(padSnapshot(), showToast)
}

async function deletePad() {
  if (!state.currentPad || !confirm('将当前协同文档移入回收站？')) return
  clearTimeout(state.saveTimer)
  const id = state.currentPad.id
  try {
    await api(`/api/v1/pads/${encodeURIComponent(id)}`, {method:'DELETE'})
    sessionRemove(`omnishare.padDraft.${id}`)
    state.currentPad = null
    state.currentPadDirty = false
    $('#padForm').classList.add('hidden')
    $('#padEmpty').classList.remove('hidden')
    toast('文档已移入回收站')
    await Promise.all([loadPads(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

function copyText(value) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value)
  const ta = document.createElement('textarea')
  ta.value = value
  document.body.appendChild(ta)
  ta.select()
  document.execCommand('copy')
  ta.remove()
  return Promise.resolve()
}

function openShare(type, id, name) {
  $('#shareObjectType').value = type
  $('#shareObjectId').value = id
  $('#shareObjectName').textContent = name
  $('#shareTTL').value = '604800'
  $('#shareMaxAccess').value = '0'
  $('#shareDialog').showModal()
}

async function createShare() {
  const payload = {
    object_type: $('#shareObjectType').value,
    object_id: $('#shareObjectId').value,
    ttl_seconds: Number($('#shareTTL').value),
    max_access_count: Number($('#shareMaxAccess').value)
  }
  try {
    const link = await api('/api/v1/shares', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload)
    })
    await copyText(link.url)
    $('#shareDialog').close()
    toast('分享链接已创建并自动复制到剪贴板')
    await Promise.all([loadShares(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadShares(strict=false) {
  try {
    state.shares = await apiCollection('/api/v1/shares')
    renderShares()
  } catch (err) {
    reportLoadError(err, strict)
  }
}

function shareStatus(link) {
  const labels = {active:'有效', revoked:'已撤销', expired:'已过期', exhausted:'次数用尽', target_missing:'目标不存在', target_deleted:'目标已删除', target_expired:'目标已过期'}
  const status = link.status || 'active'
  return [labels[status] || status, status === 'active' ? '' : 'danger']
}

function renderShares() {
  const labels = {note:'随手记', file:'文件', pad:'协同文档'}
  $('#sharesList').innerHTML = state.shares.length ? state.shares.map(link => {
    const [status, cls] = shareStatus(link)
    const limit = link.max_access_count > 0 ? `${link.access_count}/${link.max_access_count}` : `${link.access_count}/不限`
    return `
      <article class="card share-card" data-share-id="${escapeHTML(link.id)}">
        <div>
          <div class="note-meta">
            <span class="tag">${labels[link.object_type] || escapeHTML(link.object_type)}</span>
            <span class="badge ${cls === 'danger' ? 'muted' : ''}">${status}</span>
          </div>
          ${sourceLabel(link)}<h3>${escapeHTML(link.name)}</h3>
          <p class="muted-text">访问 ${limit} · 到期 ${link.expires_at ? formatDate(link.expires_at) : '长期有效'}</p>
          <code class="share-url">${escapeHTML(link.url)}</code>
        </div>
        <div class="file-actions">
          <button class="button ghost small copy-share">复制</button>
          <button class="button ghost small open-share">🔗 打开</button>
          ${link.revoked_at ? '' : '<button class="button danger small revoke-share">🚫 撤销</button>'}
        </div>
      </article>
    `
  }).join('') : `
    <div class="empty">
      <div class="empty-icon">🔗</div>
      <h3>尚未创建安全分享</h3>
      <p>您可以在随手记、文件或协同文档中点击“分享”按钮生成匿名安全访问链接</p>
    </div>
  `
}

async function revokeShare(id) {
  if (!confirm('撤销后该安全外链将立即失效，是否继续？')) return
  try {
    await api(`/api/v1/shares/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('分享链接已撤销')
    await Promise.all([loadShares(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadTrash(strict=false) {
  try {
    state.trash = await apiCollection(`/api/v1/trash?type=${encodeURIComponent($('#trashType')?.value || '')}`)
    renderTrash()
  } catch (err) {
    reportLoadError(err, strict)
  }
}

function renderTrash() {
  const labels = {note:'随手记', file:'文件', pad:'协同文档'}
  $('#trashList').innerHTML = state.trash.length ? state.trash.map(item => `
    <article class="card trash-card" data-trash-type="${escapeHTML(item.object_type)}" data-trash-id="${escapeHTML(item.id)}">
      <div>
        <div class="note-meta">
          <span class="tag">${labels[item.object_type] || escapeHTML(item.object_type)}</span>
          <span>删除于 ${formatDate(item.deleted_at)}</span>
        </div>
        ${sourceLabel(item)}<h3>${escapeHTML(item.name)}</h3>
        ${item.size ? `<p class="muted-text">${formatBytes(item.size)}</p>` : ''}
      </div>
      <div class="file-actions">
        <button class="button ghost small restore-trash">♻️ 恢复</button>
        <button class="button danger small purge-trash">🗑️ 永久删除</button>
      </div>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">🧹</div>
      <h3>回收站为空</h3>
      <p>被删除的随手记、文件和协同文档会安全暂存在这里</p>
    </div>
  `
}

async function restoreTrash(type, id) {
  try {
    await api(`/api/v1/trash/${encodeURIComponent(type)}/${encodeURIComponent(id)}/restore`, {method:'POST'})
    toast('已成功恢复内容')
    await Promise.all([loadTrash(), loadDashboard(), loadNotes(), loadFiles(), loadPads()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function purgeTrash(type, id) {
  if (!confirm('永久删除后将无法恢复，是否确认删除？')) return
  try {
    await api(`/api/v1/trash/${encodeURIComponent(type)}/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('已彻底永久删除')
    await Promise.all([loadTrash(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function emptyTrash() {
  if (!state.trash.length) return
  if (!confirm('确定要清空整个回收站吗？所有暂存文件与记录将永久不可恢复！')) return
  try {
    await api('/api/v1/trash', {method:'DELETE'})
    toast(state.workspaceStatus?.partial ? '在线设备回收站已处理；离线设备未清空' : '所有已绑定设备的回收站已清空', !!state.workspaceStatus?.partial)
    await Promise.all([loadTrash(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadAudit() {
  try {
    state.audits = await api('/api/v1/audit?limit=100')
    renderAudit()
  } catch (err) {
    toast(err.message, true)
  }
}

function renderAudit() {
  const labels = {create:'创建', update:'更新', delete:'删除', trash:'移入回收站', restore:'恢复', purge:'永久删除', upload:'上传', rename:'重命名', share:'分享', revoke:'撤销', access:'访问', read:'读取', cleanup:'清理'}
  $('#auditList').innerHTML = state.audits.length ? state.audits.map(e => `
    <article class="timeline-item">
      <span class="timeline-action">${labels[e.action] || escapeHTML(e.action)} · ${escapeHTML(e.object)}</span>
      ${sourceLabel(e)}<div class="timeline-summary">${escapeHTML(e.summary || e.object_id || '—')}</div>
      <time class="timeline-time">${formatDate(e.created_at)}</time>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">⚡</div>
      <h3>暂无活动记录</h3>
      <p>所有内容修改、文件上传与分享访问均会通过 SHA-256 哈希链记录于此</p>
    </div>
  `
}

async function loadDevices(strict=false) {
  if (state.devicesLoading) return
  state.devicesLoading = true
  const seq = ++state.requestSeq.devices
  try {
    const devices = await api('/api/v1/devices')
    if (seq !== state.requestSeq.devices) return
    state.devices = devices
    renderDevices()
  } catch (err) {
    if (seq === state.requestSeq.devices) reportLoadError(err, strict)
  } finally {
    state.devicesLoading = false
  }
}

function safeDeviceURL(value) {
  try {
    // Device discovery supplies an origin, never a login link or executable
    // path. Reject credentials and token-bearing queries rather than displaying
    // a misleading label for a different destination.
    const u = new URL(value)
    if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password ||
        u.search || u.hash || u.pathname !== '/') return ''
    return `${u.origin}/`
  } catch {
    return ''
  }
}

function renderNodeSecurity() {
  const summary = $('#nodeSecuritySummary')
  if (!summary || !state.config) return
  summary.textContent = (state.config.access_key_enabled ?? state.config.has_access_key)
    ? '访问密钥已启用 · 绑定设备通过身份签名协作'
    : '局域网免密访问 · 可在设置中开启密钥保护'
}

function openBindDialog(url='') {
  $('#bindPeerURL').value = url
  $('#bindPeerKey').value = ''
  $('#bindPeerError').textContent = ''
  $('#bindPeerDialog').showModal()
  $('#bindPeerURL').focus()
}

async function submitPeerBinding() {
  if (state.peerBinding) return
  state.peerBinding = true; $('#bindPeerSubmitBtn').disabled = true
  $('#bindPeerError').textContent = '正在连接并完成双向绑定…'
  try {
    await api('/api/v1/peers/bind', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({url:$('#bindPeerURL').value.trim(),access_key:$('#bindPeerKey').value})})
    $('#bindPeerKey').value = ''; $('#bindPeerDialog').close()
    toast('双向绑定成功，两端工作区已合并')
    await Promise.all([loadDevices(),loadAll()]); await refreshWorkspace()
  } catch (err) { $('#bindPeerError').textContent = err.message }
  finally { state.peerBinding = false; $('#bindPeerSubmitBtn').disabled = false }
}

async function unbindDevice(id) {
  try {
    const result = await api(`/api/v1/peers/${encodeURIComponent(id)}`,{method:'DELETE'})
    toast(result.reciprocal_removed ? '双方已解除绑定' : '本机已解除绑定；对方离线，尚未移除其登记')
    await loadDevices(); await refreshWorkspace()
  } catch (err) { toast(err.message,true) }
}

function renderDevices() {
  const groups = [
    {title:'当前设备',devices:state.devices.filter(d => d.is_local)},
    {title:'已绑定 · 统一工作区',devices:state.devices.filter(d => !d.is_local && d.bound)},
    {title:'发现的设备',devices:state.devices.filter(d => !d.is_local && !d.bound)}
  ]
  const markup = groups.filter(g => g.devices.length).map(g => `<section class="device-group"><h3 class="device-group-title">${g.title}<span>${g.devices.length}</span></h3>${g.devices.map(d => {
    const addresses = [...new Set([d.url,...(d.addresses||[])].map(safeDeviceURL).filter(Boolean))]
    return `<div class="device" data-device-id="${escapeHTML(d.id)}">
      <div class="device-heading"><h3>${escapeHTML(d.hostname || 'OmniShare')}</h3><span class="device-state ${d.online?'':'offline'}">${d.is_local?'本机':d.online?'在线':escapeHTML(d.status||'不可达')}</span></div>
      ${addresses.map(address => `<code class="device-url">${escapeHTML(address)}${address===safeDeviceURL(d.url)&&d.online?' · 当前连接':''}</code>`).join('')}
      <p class="device-note">${d.connection_error ? escapeHTML(d.connection_error) : d.is_local?'新内容默认保存于此；列表包含所有已绑定设备':d.bound?'内容已合并，直接在当前界面查看和操作':'发现设备不会授予访问权限；绑定后双方合并工作区'}</p>
      ${d.is_local?'':d.bound?'<button class="button ghost small unbind-device">解除绑定</button>':`<button class="button primary small bind-device" data-url="${escapeHTML(d.url||'')}">绑定并合并</button>`}
    </div>`
  }).join('')}</section>`).join('')
  const list = $('#deviceList')
  if (list._deviceMarkup !== markup) { list.innerHTML = markup; list._deviceMarkup = markup }
  const status = $('#tailscaleStatus'); if (status) status.textContent = state.tailscaleStatus || '局域网自动发现已启用；Tailscale 状态正在检测'
}

const TAB_CONFIG = {
  dashboard: { panel: '#dashboardPanel', title: '工作台' },
  notes: { panel: '#notesPanel', title: '随手记', load: loadNotes },
  files: { panel: '#filesPanel', title: '文件与传输', load: loadFiles },
  pads: { panel: '#padsPanel', title: '协同文档', load: loadPads },
  shares: { panel: '#sharesPanel', title: '安全分享', load: loadShares },
  trash: { panel: '#trashPanel', title: '回收站', load: loadTrash },
  activity: { panel: '#activityPanel', title: '活动记录', load: loadAudit }
}

function switchTab(tab) {
  const config = TAB_CONFIG[tab]
  if (!config) return
  if (state.activeTab === 'pads' && tab !== 'pads' && state.currentPadDirty && !state.padSaveBlocked) savePad(false)
  state.activeTab = tab
  $$('.tab').forEach(b => {
    const active = b.dataset.tab === tab
    b.classList.toggle('active', active)
    if (active) b.setAttribute('aria-current', 'page')
    else b.removeAttribute('aria-current')
  })
  $$('.panel-view').forEach(p => p.classList.remove('active'))
  $(config.panel).classList.add('active')
  if ($('#workspaceTitle')) $('#workspaceTitle').textContent = config.title
  document.title = `${config.title} · OmniShare`
  if (config.load) config.load()
}

async function exportBackup() {
  // Backups can take minutes. Passing our own signal keeps the request out of
  // the runtime bridge's 6 s abort watchdog; it only ends on network failure.
  const password = window.prompt('设置备份口令（留空则导出未加密 ZIP；加密包只能用同一口令恢复）', '')
  if (password === null) return
  const controller = new AbortController()
  const suggested = `omnishare-backup-${new Date().toISOString().slice(0, 10)}${password ? '.osb' : '.zip'}`
  try {
    // The picker needs the click's transient user activation. Awaiting the
    // backup response first can exhaust it, especially for a large archive.
    // Cancelling here also avoids generating a backup the user does not want.
    const handle = window.showSaveFilePicker
      ? await window.showSaveFilePicker({ suggestedName: suggested }) : null
    toast('正在生成备份，请稍候…')
    const extra = password ? { 'X-OmniShare-Backup-Password': password } : {}
    const res = await fetch('/api/v1/backup', {signal: controller.signal, headers: headers(extra)})
    if (!res.ok) {
      let message = `HTTP ${res.status}`
      try {
        const payload = await res.json()
  acceptWorkspaceStatus(payload)
        message = payload.message || message
      } catch { /* non-JSON error */ }
      throw new Error(message)
    }
    const disposition = res.headers.get('content-disposition') || ''
    const total = Number(res.headers.get('content-length') || 0)

    // Prefer the File System Access API so a 16 GB backup is written chunk by
    // chunk instead of being materialised as one Blob in memory.
    if (handle) {
      const writable = await handle.createWritable()
      let loaded = 0
      let lastShown = -10
      try {
        const reader = res.body.getReader()
        while (true) {
          const { done, value } = await reader.read()
          if (done) break
          await writable.write(value)
          loaded += value.length
          if (total) {
            const percent = Math.floor((loaded / total) * 100)
            if (percent >= lastShown + 10) {
              lastShown = percent
              toast(`正在写入备份文件 ${percent}%`)
            }
          }
        }
        await writable.close()
      } catch (err) {
        try { await writable.abort() } catch { /* already closed */ }
        throw err
      }
      toast(disposition.includes('.osb') ? '加密备份已保存' : '备份已保存')
      return
    }

    // Fallback browsers have to buffer the whole response. Refuse instead of
    // silently exhausting memory on multi-gigabyte archives.
    const MAX_BUFFERED = 2 * 1024 * 1024 * 1024
    if (total > MAX_BUFFERED) {
      throw new Error('当前浏览器不支持流式下载，备份超过 2 GB。请使用 Chrome/Edge，或降低“备份上限”')
    }
    const reader = res.body.getReader()
    const chunks = []
    let received = 0
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      chunks.push(value)
      received += value.length
      if (received > MAX_BUFFERED) {
        controller.abort()
        throw new Error('备份超过 2 GB 且浏览器不支持流式下载，已中止')
      }
    }
    const blob = new Blob(chunks, { type: 'application/octet-stream' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = suggested
    a.click()
    setTimeout(() => URL.revokeObjectURL(a.href), 1000)
  } catch (err) {
    // Abort even when rejecting the response by Content-Length or failing to
    // open/write the destination, so its body does not keep downloading.
    controller.abort()
    if (err.name !== 'AbortError') toast(err.message, true)
  }
}

function openRestoreDialog() {
  if (state.restoreInFlight) {
    if (!$('#restoreDialog').open) $('#restoreDialog').showModal()
    return
  }
  $('#restoreFile').value = ''
  $('#restorePassword').value = ''
  $('#restoreConfirm').checked = false
  $('#restoreProgress').classList.add('hidden')
  $('#restoreProgress').value = 0
  $('#restoreStatus').textContent = ''
  $('#restoreSubmitBtn').disabled = false
  $('#restoreDialog').showModal()
}

function restoreBackup() {
  if (state.restoreInFlight) return
  const file = $('#restoreFile').files?.[0]
  const status = $('#restoreStatus')
  const progress = $('#restoreProgress')
  const submit = $('#restoreSubmitBtn')
  if (!file) {
    status.textContent = '请先选择备份文件'
    return
  }
  if (!$('#restoreConfirm').checked) {
    status.textContent = '请先勾选确认覆盖当前数据'
    return
  }
  const restoreHeaders = { 'X-OmniShare-Confirm': 'RESTORE-BACKUP' }
  Object.assign(restoreHeaders, authHeaders())
  const restorePassword = $('#restorePassword').value
  if (restorePassword) restoreHeaders['X-OmniShare-Backup-Password'] = restorePassword
  const form = new FormData()
  form.append('backup', file)
  submit.disabled = true
  progress.classList.remove('hidden')
  progress.value = 0
  status.textContent = '正在上传并校验备份…'
  const xhr = new XMLHttpRequest()
  // Backups are capped by the server's backup limit, not by a fixed duration;
  // a slow link must not turn a valid restore into a timeout.
  // 0 means "no total deadline". The server enforces an idle timeout instead,
  // so a dead connection can still be reclaimed while a slow upload completes.
  xhr.timeout = 0
  xhr.open('POST', '/api/v1/restore')
  Object.entries(restoreHeaders).forEach(([name, value]) => xhr.setRequestHeader(name, value))
  xhr.upload.onprogress = event => {
    if (event.lengthComputable) progress.value = Math.round((event.loaded / event.total) * 100)
  }
  const finishRestore = () => {
    state.restoreInFlight = false
    submit.disabled = false
  }
  xhr.onerror = () => {
    status.textContent = '网络错误，恢复未完成'
    finishRestore()
  }
  xhr.onabort = () => {
    status.textContent = '恢复请求已中止'
    finishRestore()
  }
  xhr.onload = () => {
    finishRestore()
    let payload = {}
    try { payload = JSON.parse(xhr.responseText) } catch { /* non-JSON error page */ }
    if (xhr.status >= 200 && xhr.status < 300) {
      status.textContent = '恢复完成，正在刷新数据…'
      toast('备份已恢复，建议重启程序')
      $('#restoreDialog').close()
      loadAll()
      return
    }
    if (xhr.status === 401) {
      status.textContent = '需要访问密钥'
      showAuth()
    } else {
      status.textContent = payload.message || ('恢复失败：HTTP ' + xhr.status)
    }
  }
  state.restoreInFlight = true
  try {
    xhr.send(form)
  } catch (err) {
    status.textContent = err.message
    finishRestore()
  }
}

function bindEvents() {
  $('#newPadCreateForm').addEventListener('submit', e => { e.preventDefault(); void submitNewPad() })
  $('#cancelNewPadBtn').addEventListener('click', () => $('#newPadDialog').close())
  $('#loadRemotePadBtn').addEventListener('click', () => void loadRemotePadVersion())
  $('#saveDraftCopyBtn').addEventListener('click', () => void savePadDraftCopy())
  $('#bindDeviceBtn').addEventListener('click', () => openBindDialog())
  $('#bindPeerForm').addEventListener('submit', e => { e.preventDefault(); void submitPeerBinding() })
  $('#cancelBindPeerBtn').addEventListener('click', () => $('#bindPeerDialog').close())
  $('#deviceList').addEventListener('click', e => {
    const bind = e.target.closest('.bind-device')
    if (bind) { openBindDialog(bind.dataset.url); return }
    const unbind = e.target.closest('.unbind-device')
    if (unbind) void unbindDevice(unbind.closest('[data-device-id]').dataset.deviceId)
  })
  const sidebar = $('.sidebar-disclosure')
  if (sidebar && window.matchMedia) {
    const narrow = window.matchMedia('(max-width: 980px)')
    const applySidebarDefault = () => { sidebar.open = !narrow.matches }
    applySidebarDefault()
    // Keep the user's choice while they remain within the same layout. Only
    // crossing the desktop/mobile boundary applies the new layout's default.
    if (typeof narrow.addEventListener === 'function') narrow.addEventListener('change', applySidebarDefault)
    else if (typeof narrow.addListener === 'function') narrow.addListener(applySidebarDefault)
  }
  $$('.tab').forEach(btn => btn.addEventListener('click', () => switchTab(btn.dataset.tab)))
  $$('.dashboard-link').forEach(btn => btn.addEventListener('click', () => switchTab(btn.dataset.targetTab)))
  $('#createNoteBtn').addEventListener('click', createNote)
  $('#noteContent').addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
      e.preventDefault()
      createNote()
    }
  })
  $('#pasteBtn').addEventListener('click', readClipboardPreview)
  $('#clipboardConfirmBtn').addEventListener('click', confirmClipboardSend)
  $('#clipboardCancelBtn').addEventListener('click', closeClipboardPreview)
  $('#clipboardDialog').addEventListener('cancel', event => {
    if (clipboardSending) event.preventDefault()
  })
  $('#clipboardDialog').addEventListener('close', resetClipboardPreview)
  window.addEventListener('pagehide', resetClipboardPreview)
  $('#noteSearch').addEventListener('input', debounce(() => loadNotes()))
  $('#noteTagFilter').addEventListener('change', () => loadNotes())
  $('#notesList').addEventListener('click', async e => {
    const card = e.target.closest('[data-note-id]')
    if (!card) return
    const id = card.dataset.noteId
    const n = state.notes.find(x => x.id === id)
    if (e.target.closest('.read-note')) await readRestrictedNote(id, false)
    else if (e.target.closest('.copy-note')) await readRestrictedNote(id, true)
    else if (e.target.closest('.share-note')) openShare('note', id, n.content_redacted ? '受限随手记' : n.content.slice(0, 80))
    else if (e.target.closest('.edit-note')) await openEditNote(id)
    else if (e.target.closest('.raw-note')) openRawNote(id)
    else if (e.target.closest('.delete-note')) deleteNote(id)
  })
  $('#saveEditNoteBtn').addEventListener('click', saveEditNote)

  $('#chooseFilesBtn').addEventListener('click', e => {
    e.stopPropagation()
    $('#fileInput').click()
  })
  $('#dropZone').addEventListener('click', event => {
    if (!event.target.closest('button, input, select, label, a')) $('#fileInput').click()
  })
  $('#dropZone').addEventListener('keydown', event => {
    if (event.target === $('#dropZone') && ['Enter', ' '].includes(event.key)) {
      event.preventDefault()
      $('#fileInput').click()
    }
  })
  $('#fileInput').addEventListener('change', e => {
    uploadFiles(e.target.files)
    e.target.value = ''
  })
  ;['dragenter', 'dragover'].forEach(type => $('#dropZone').addEventListener(type, e => {
    e.preventDefault()
    $('#dropZone').classList.add('dragging')
  }))
  ;['dragleave', 'drop'].forEach(type => $('#dropZone').addEventListener(type, e => {
    e.preventDefault()
    $('#dropZone').classList.remove('dragging')
  }))
  $('#dropZone').addEventListener('drop', e => uploadFiles(e.dataTransfer.files))
  $('#clearFinishedUploadsBtn').addEventListener('click', clearFinishedUploads)
  $('#cancelAllUploadsBtn').addEventListener('click', cancelAllUploads)
  updateUploadSummary()

  $('#fileSearch').addEventListener('input', debounce(() => loadFiles()))
  $('#fileType').addEventListener('change', () => loadFiles())
  $('#filesList').addEventListener('click', e => {
    const card = e.target.closest('[data-file-id]')
    if (!card) return
    const f = state.files.find(x => x.id === card.dataset.fileId)
    if (e.target.closest('.download-file')) openDownload(f)
    if (e.target.closest('.play-file')) playFile(f)
    if (e.target.closest('.share-file')) openShare('file', f.id, f.file_name)
    if (e.target.closest('.rename-file')) renameFile(f)
    if (e.target.closest('.delete-file')) deleteFile(f.id)
  })

  $('#newPadBtn').addEventListener('click', createPad)
  $('#padSearch').addEventListener('input', debounce(() => loadPads()))
  $('#padsList').addEventListener('click', e => {
    const item = e.target.closest('[data-pad-id]')
    if (item) void selectPad(item.dataset.padId)
  })
  $('#padTitle').addEventListener('input', markPadDirty)
  $('#padContent').addEventListener('input', markPadDirty)
  $('#padContent').addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault()
      savePad(true)
    }
  })
  $('#savePadBtn').addEventListener('click', () => savePad(true))
  $('#sharePadBtn').addEventListener('click', () => {
    if (state.currentPad) openShare('pad', state.currentPad.id, state.currentPad.title)
  })
  $('#deletePadBtn').addEventListener('click', deletePad)

  $('#createShareBtn').addEventListener('click', createShare)
  $('#refreshSharesBtn').addEventListener('click', loadShares)
  $('#sharesList').addEventListener('click', e => {
    const card = e.target.closest('[data-share-id]')
    if (!card) return
    const link = state.shares.find(x => x.id === card.dataset.shareId)
    if (e.target.closest('.copy-share')) copyText(link.url).then(() => toast('分享链接已复制'))
    if (e.target.closest('.open-share')) { try { const url = new URL(link.url, location.href); if (!['http:','https:'].includes(url.protocol) || url.username || url.password) throw new Error('分享地址无效'); window.open(url.href, '_blank', 'noopener') } catch (err) { toast(err.message, true) } }
    if (e.target.closest('.revoke-share')) revokeShare(link.id)
  })

  $('#trashType').addEventListener('change', loadTrash)
  $('#emptyTrashBtn').addEventListener('click', emptyTrash)
  $('#trashList').addEventListener('click', e => {
    const card = e.target.closest('[data-trash-id]')
    if (!card) return
    const type = card.dataset.trashType
    const id = card.dataset.trashId
    if (e.target.closest('.restore-trash')) restoreTrash(type, id)
    if (e.target.closest('.purge-trash')) purgeTrash(type, id)
  })

  $('#refreshAuditBtn').addEventListener('click', loadAudit)
  $('#refreshDevicesBtn').addEventListener('click', () => loadDevices())
  $('#cfgAccessKeyEnabled').addEventListener('change', updateAccessKeyControls)
  $('#cfgAllowLAN').addEventListener('change', () => {
    $('#cfgListenAddress').value = $('#cfgAllowLAN').checked ? '0.0.0.0' : '127.0.0.1'
  })
  $('#settingsBtn').addEventListener('click', () => {
    fillSettings()
    $('#settingsDialog').showModal()
  })
  $('#backupBtn').addEventListener('click', exportBackup)
  $('#loadMoreNotes').addEventListener('click', () => loadNotes(false, true))
  $('#loadMoreFiles').addEventListener('click', () => loadFiles(false, true))
  $('#restoreBtn').addEventListener('click', openRestoreDialog)
  $('#restoreSubmitBtn').addEventListener('click', restoreBackup)
  $('#saveSettingsBtn').addEventListener('click', saveSettings)
  $('#addPeerBtn').addEventListener('click', () => { $('#settingsDialog').close(); openBindDialog() })
  $('#peerRows').addEventListener('click', e => {
    if (e.target.closest('.remove-peer')) e.target.closest('.peer-row').remove()
  })

  $('#authSubmitBtn').addEventListener('click', verifyAuth)
  $('#authKey').addEventListener('keydown', e => {
    if (e.key === 'Enter') {
      e.preventDefault()
      verifyAuth()
    }
  })

  $('#closeVideoBtn').addEventListener('click', () => {
    $('#videoPlayer').pause()
    $('#videoPlayer').removeAttribute('src')
    $('#videoDialog').close()
  })

  const closeViewed = () => $('#viewNoteDialog').close()
  $('#closeViewNoteBtn').addEventListener('click', closeViewed)
  $('#closeViewedNoteBtn').addEventListener('click', closeViewed)
  $('#copyViewedNoteBtn').addEventListener('click', () => copyText($('#viewNoteContent').textContent).then(() => toast('已复制随手记内容')))

  window.addEventListener('beforeinstallprompt', e => {
    e.preventDefault()
    state.deferredInstall = e
    $('#installBtn').classList.remove('hidden')
  })
  $('#installBtn').addEventListener('click', async () => {
    if (!state.deferredInstall) return
    state.deferredInstall.prompt()
    await state.deferredInstall.userChoice
    state.deferredInstall = null
    $('#installBtn').classList.add('hidden')
  })
}

async function start() {
  bindEvents()
  window.addEventListener('beforeunload', event => {
    if (state.currentPadDirty) preservePadDraft()
    if (state.currentPadDirty || clipboardSending || hasPendingUploads()) {
      event.preventDefault()
      event.returnValue = ''
    }
  })
  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/service-worker.js').catch(() => {})
  await loadAll()
  switchTab(state.activeTab)
  // A background tab must not keep polling the local server on a timer.
  document.addEventListener('visibilitychange', () => { if (!document.hidden) { loadDevices(); void refreshWorkspace() } })
  window.addEventListener('focus', () => void refreshWorkspace())
  setInterval(() => { void refreshWorkspace() }, 2000)
  setInterval(() => { if (!document.hidden) loadHealth() }, 15000)
  setInterval(() => {
    if (!document.hidden) loadDevices()
  }, 5000)
}

start()
