const $ = (selector) => document.querySelector(selector)
const $$ = (selector) => [...document.querySelectorAll(selector)]

function uiIcon(name, extraClass = '') {
  const cls = extraClass ? `ui-icon ${extraClass}` : 'ui-icon'
  return `<svg class="${cls}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><use href="#icon-${name}"></use></svg>`
}

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
  accessKey: sessionGet('omnishare.accessKey') || '', authEpoch: 0,
  deferredInstall: null, editingNote: null,
  uploadQueue: [], activeUploads: 0, uploadSequence: 0, uploadPumping: false, uploadNotice: '',
  requestSeq: {notes:0, files:0, pads:0, devices:0, folders:0}, devicesLoading: false, healthLoading: false, padSaveChain: Promise.resolve(),
  noteTotal: 0, fileTotal: 0, restoreInFlight: false, workspaceBusy: false, padRefreshSeq: 0, padRefreshBusy: false, padSaveEpoch: 0, padSelectSeq: 0, workspaceStatus: null, tailscaleStatus: "", padCreating: false, draftCopyBusy: false, peerBinding: false,
  folders: {
    notes: { list: [], serverItems: [], fallbackCounts: new Map(), selected: '', recursive: true, collapsed: new Set(), loaded: false },
    files: { list: [], serverItems: [], fallbackCounts: new Map(), selected: '', recursive: true, collapsed: new Set(), loaded: false },
    pads:  { list: [], serverItems: [], fallbackCounts: new Map(), selected: '', recursive: true, collapsed: new Set(), loaded: false }
  },
  workspaceTick: 0, folderModal: null, deleteFolderTarget: null, renamingFile: null, movingItem: null,
  noteFormats: new Map(), padPreviewMode: false, editingDeviceAlias: null, shareDialogSession: 0
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

const toast = (message, error=false, duration=2800) => {
  const el = $('#toast')
  el.textContent = message
  el.className = `toast show${error ? ' error' : ''}`
  clearTimeout(el._timer)
  el._timer = setTimeout(() => { el.className = 'toast' }, duration)
}

async function withButtonBusy(btn, busyText, fn) {
  if (!btn) return fn()
  const prevText = btn.textContent
  btn.disabled = true
  if (busyText) btn.textContent = busyText
  try {
    return await fn()
  } finally {
    btn.disabled = false
    btn.textContent = prevText
  }
}

let confirmResolver = null
function askConfirm(message, { title='确认操作', confirmText='确认', danger=false } = {}) {
  const dialog = $('#confirmDialog')
  if (!dialog || typeof dialog.showModal !== 'function' || typeof dialog.addEventListener !== 'function') {
    return Promise.resolve(typeof confirm === 'function' ? confirm(message) : true)
  }
  if (confirmResolver) {
    const prev = confirmResolver
    confirmResolver = null
    prev(false)
    if (dialog.open) dialog.close('cancelled')
  }
  $('#confirmDialogTitle').textContent = title
  $('#confirmDialogMessage').textContent = message
  const submit = $('#confirmSubmitBtn')
  const cancel = $('#confirmCancelBtn')
  submit.textContent = confirmText
  submit.className = `button ${danger ? 'danger' : 'primary'}`
  return new Promise(resolve => {
    let settled = false
    const finish = result => {
      if (settled) return
      settled = true
      if (confirmResolver === finish) confirmResolver = null
      dialog.removeEventListener('close', onClose)
      dialog.removeEventListener('cancel', onCancel)
      cancel?.removeEventListener('click', onCancel)
      submit.removeEventListener('click', onSubmit)
      if (dialog.open) dialog.close(result ? 'confirmed' : 'cancelled')
      resolve(result)
    }
    const onSubmit = e => { e.preventDefault(); finish(true) }
    const onCancel = e => { e?.preventDefault?.(); finish(false) }
    const onClose = () => finish(dialog.returnValue === 'confirmed')
    confirmResolver = finish
    dialog.addEventListener('close', onClose)
    dialog.addEventListener('cancel', onCancel)
    cancel?.addEventListener('click', onCancel)
    submit.addEventListener('click', onSubmit)
    dialog.showModal()
    cancel?.focus?.()
  })
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

function captureAuthContext() {
  const enabled = state.config?.access_key_enabled !== false
  return {epoch: state.authEpoch, enabled, key: enabled ? state.accessKey : ''}
}

function isCurrentAuthContext(context) {
  const current = captureAuthContext()
  return context.epoch === current.epoch && context.enabled === current.enabled && context.key === current.key
}

function workspacePath(path, method='GET') {
  const u = new URL(path, location.origin)
  if (u.origin === location.origin && /^\/api\/v1\/(dashboard|notes|files|pads|folders|shares|trash|audit)$/.test(u.pathname) &&
      (method === 'GET' || (method === 'DELETE' && u.pathname === '/api/v1/trash'))) u.searchParams.set('scope', 'all')
  return u.pathname + u.search
}

function acceptWorkspaceStatus(payload) {
  if (payload.workspace) {
    state.workspaceStatus = payload.workspace
    const unavailable = payload.workspace.nodes.filter(n => !n.online || n.truncated)
    const banner = $('#workspaceStatus')
    if (banner) {
      const text = unavailable.length
        ? `部分设备暂不可用：${unavailable.map(n => n.name + (n.cached ? '（上次快照）' : '')).join('、')}；未取得的数据不计入当前统计。`
        : `统一工作区 · ${payload.workspace.nodes.length} 台设备 · 新内容默认保存在当前设备`
      if (banner.textContent !== text) banner.textContent = text
      banner.classList.toggle('warning', unavailable.length > 0)
    }
  }
  if (payload.discovery) state.tailscaleStatus = payload.discovery.tailscale || ''
}

function isLoopbackHost(host='') {
  const h = String(host).replace(/^\[|\]$/g, '').toLowerCase().replace(/\.$/, '')
  return /^127(?:\.\d{1,3}){3}$/.test(h) || h === '::1' || h === 'localhost' || /^::ffff:7f[0-9a-f]{2}:[0-9a-f]+$/.test(h)
}

function deviceDisplayName(device) {
  const alias = String(device?.alias || '').trim()
  if (alias) return alias
  const host = String(device?.hostname || '').trim()
  if (host && host !== 'OmniShare') return host
  if (device?.is_local) return '本机'
  try {
    const addr = safeDeviceURL(device?.url) || (Array.isArray(device?.addresses) ? device.addresses.map(safeDeviceURL).find(Boolean) : '')
    if (addr) {
      const h = new URL(addr).hostname.replace(/^\[|\]$/g, '')
      if (h && !isLoopbackHost(h)) return h
    }
  } catch { /* ignore */ }
  return host || '设备'
}

function sourceLabel(item) {
  const dev = item?.source_node_id ? state.devices.find(d => d.id === item.source_node_id) : null
  let name = dev ? deviceDisplayName(dev) : (item?.source_node_name || '')
  if (!name) return ''
  if (name === 'OmniShare' && item?.source_node_id && item.source_node_id === state.config?.node_id) {
    name = '本机'
  }
  return `<span class="source-label${item.source_online === false ? ' source-offline' : ''}">来自 ${escapeHTML(name)}${item.source_online === false ? ' · 离线快照' : ''}</span>`
}

function looksLikeMarkdown(text='') {
  const s = String(text || '')
  if (!s.trim()) return false
  if (/```[\s\S]*?```/.test(s)) return true
  if (/^\s{0,3}#{1,4}\s+\S/m.test(s)) return true
  if (/^\s*>\s+\S/m.test(s)) return true
  if (/^\s*\|.*\|\s*\n\s*\|[\s:|-]+\|/m.test(s)) return true
  if (/\[[^\]\n]+\]\((?:https?:\/\/|file:\/\/|<[^>]+>|[A-Za-z]:[\\/]|\/)[^)\n]*\)/.test(s)) return true
  if (/\*\*[^*\n]{1,120}\*\*/.test(s)) return true
  if (/`[^`\n]{1,120}`/.test(s)) return true
  if (/^\s*(?:[-*+]|\d+\.)\s+\S/m.test(s)) return true
  return false
}

function renderInlineMarkdown(escapedText) {
  const codes = []
  let out = escapedText.replace(/`([^`\n]+)`/g, (_, code) => {
    const idx = codes.length
    codes.push(`<code>${code}</code>`)
    return `\u0000IC${idx}\u0000`
  })
  out = out.replace(/\[([^\]\n]+)\]\((?:&lt;([^&\n]+)&gt;|([^)\s]+))\)/g, (_, label, angleTarget, plainTarget) => {
    const target = angleTarget || plainTarget || ''
    const rawTarget = target.replace(/&amp;/g, '&').replace(/&#39;/g, "'").replace(/&quot;/g, '"')
    if (/^https?:\/\//i.test(rawTarget)) {
      return `<a href="${target}" target="_blank" rel="noopener noreferrer">${label}</a>`
    }
    return `<button type="button" class="md-path" data-copy-path="${escapeHTML(rawTarget)}" title="点击复制路径">${uiIcon('folder')}<span>${label}</span></button>`
  })
  out = out.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>')
  out = out.replace(/(^|[^\w*])\*([^*\n]+)\*(?=[^\w*]|$)/g, '$1<em>$2</em>')
  out = out.replace(/\u0000IC(\d+)\u0000/g, (_, idx) => codes[Number(idx)] || '')
  return out
}

function renderMarkdownSafe(src='') {
  const blocks = []
  const normalized = String(src || '').replace(/\r\n?/g, '\n')
  const withPlaceholders = normalized.replace(/```[^\n]*\n([\s\S]*?)```/g, (_, code) => {
    const idx = blocks.length
    blocks.push(`<pre><code>${escapeHTML(code.replace(/\n$/, ''))}</code></pre>`)
    return `\n\u0000CB${idx}\u0000\n`
  })
  const lines = escapeHTML(withPlaceholders).split('\n')
  const html = []
  let i = 0
  const isTableSep = line => /^\s*\|?(?:\s*:?-{2,}:?\s*\|)+\s*(?:\s*:?-{2,}:?\s*)?\|?\s*$/.test(line)
  const parseCells = line => line.trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim())
  while (i < lines.length) {
    const line = lines[i]
    const trimmed = line.trim()
    if (!trimmed) { i++; continue }
    const cbMatch = trimmed.match(/^\u0000CB(\d+)\u0000$/)
    if (cbMatch) { html.push(blocks[Number(cbMatch[1])] || ''); i++; continue }
    if (/^(?:-{3,}|\*{3,}|_{3,})$/.test(trimmed)) { html.push('<hr>'); i++; continue }
    const head = trimmed.match(/^(#{1,4})\s+(.+)$/)
    if (head) {
      const level = head[1].length
      html.push(`<h${level}>${renderInlineMarkdown(head[2])}</h${level}>`)
      i++; continue
    }
    if (trimmed.includes('|') && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      const headers = parseCells(line)
      i += 2
      const rows = []
      while (i < lines.length && lines[i].trim() && lines[i].includes('|')) {
        rows.push(parseCells(lines[i]))
        i++
      }
      html.push(`<div class="md-table-wrap"><table><thead><tr>${headers.map(h => `<th>${renderInlineMarkdown(h)}</th>`).join('')}</tr></thead><tbody>${rows.map(r => `<tr>${headers.map((_, ci) => `<td>${renderInlineMarkdown(r[ci] || '')}</td>`).join('')}</tr>`).join('')}</tbody></table></div>`)
      continue
    }
    if (/^&gt;\s?/.test(trimmed)) {
      const quoteLines = []
      while (i < lines.length && /^&gt;\s?/.test(lines[i].trim())) {
        quoteLines.push(renderInlineMarkdown(lines[i].trim().replace(/^&gt;\s?/, '')))
        i++
      }
      html.push(`<blockquote>${quoteLines.join('<br>')}</blockquote>`)
      continue
    }
    if (/^[-*+]\s+/.test(trimmed)) {
      const items = []
      while (i < lines.length && /^[-*+]\s+/.test(lines[i].trim())) {
        items.push(`<li>${renderInlineMarkdown(lines[i].trim().replace(/^[-*+]\s+/, ''))}</li>`)
        i++
      }
      html.push(`<ul>${items.join('')}</ul>`)
      continue
    }
    if (/^\d+\.\s+/.test(trimmed)) {
      const items = []
      while (i < lines.length && /^\d+\.\s+/.test(lines[i].trim())) {
        items.push(`<li>${renderInlineMarkdown(lines[i].trim().replace(/^\d+\.\s+/, ''))}</li>`)
        i++
      }
      html.push(`<ol>${items.join('')}</ol>`)
      continue
    }
    const para = []
    while (i < lines.length) {
      const t = lines[i].trim()
      if (!t || /^\u0000CB\d+\u0000$/.test(t) || /^(#{1,4})\s+/.test(t) || /^&gt;\s?/.test(t) || /^[-*+]\s+/.test(t) || /^\d+\.\s+/.test(t) || /^(?:-{3,}|\*{3,}|_{3,})$/.test(t) || (t.includes('|') && i + 1 < lines.length && isTableSep(lines[i + 1]))) break
      para.push(renderInlineMarkdown(t))
      i++
    }
    if (para.length) html.push(`<p>${para.join('<br>')}</p>`)
  }
  return html.join('')
}

async function api(path, options={}) {
  path = workspacePath(path, options.method || 'GET')
  const auth = captureAuthContext()
  const res = await fetch(path, {...options, headers: headers(options.headers || {})})
  if (res.status === 401) {
    showAuth(auth)
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
  const auth = captureAuthContext()
  const res = await fetch(path, {headers: headers()})
  if (res.status === 401) {
    showAuth(auth)
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
  const auth = captureAuthContext()
  const res = await fetch(path, {...options, headers: headers(options.headers || {})})
  if (res.status === 401) {
    showAuth(auth)
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

function showAuth(context) {
  // A response sent with older credentials cannot undo a successful login or
  // a later protection change while it was in flight.
  if (context && !isCurrentAuthContext(context)) return
  // A different client may have enabled protection since our last config poll.
  if (state.config) state.config.access_key_enabled = true
  if ($('#authDialog').open) return
  $('#authDialog').showModal()
  $('#authKey').focus()
}

async function verifyAuth() {
  const key = $('#authKey').value
  try {
    const res = await fetch('/api/v1/auth/verify', {method:'POST', headers:keyHeaders(key)})
    if (!res.ok) throw new Error('密钥不正确')
    state.authEpoch++
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
  const auth = captureAuthContext()
  const core = await Promise.allSettled([loadHealth(true), loadConfig(true), loadDashboard(true), loadNotes(true), loadDevices(true)])
  if (core.some(result => result.status === 'rejected')) {
    if (isCurrentAuthContext(auth)) toast('核心服务加载失败，请检查网络连接或访问密钥', true)
    return false
  }
  if (state.folders[state.activeTab]) void loadFolders(state.activeTab)
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
  const auth = captureAuthContext()
  try {
    const config = await api('/api/v1/config')
    // A pre-login response may still describe an unprotected node. It must
    // not clear the new credential or replace settings saved in the meantime.
    if (!isCurrentAuthContext(auth)) return
    state.config = config
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
    await withButtonBusy($('#saveSettingsBtn'), '保存中…', async () => {
      const config = await api('/api/v1/config', {method:'PUT', headers:requestHeaders, body:JSON.stringify(payload)})
      state.authEpoch++
      state.config = config
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
    })
  } catch (err) {
    toast(err.message, true)
  }
}

const FOLDER_LABELS = { notes: '随手记', files: '文件', pads: '协同文档' }
const FOLDER_LOADERS = { notes: () => loadNotes(), files: () => loadFiles(), pads: () => loadPads() }

function normalizeClientFolder(raw = '') {
  return String(raw || '').trim().replace(/\\+/g, '/').replace(/^\/+|\/+$/g, '').split('/').map(s => s.trim()).filter(Boolean).join('/')
}

function joinFolderPaths(base = '', rel = '') {
  const a = normalizeClientFolder(base)
  const b = normalizeClientFolder(rel)
  if (!a) return b
  if (!b) return a
  return `${a}/${b}`
}

function appendFolderQuery(url, kind) {
  const f = state.folders[kind]
  if (!f || !f.selected) return url
  const sep = url.includes('?') ? '&' : '?'
  return `${url}${sep}folder=${encodeURIComponent(f.selected)}&recursive=${f.recursive ? '1' : '0'}`
}

function hasActiveItemFilter(kind) {
  const f = state.folders[kind]
  if (f && f.selected) return true
  if (kind === 'notes') return !!($('#noteSearch')?.value.trim() || $('#noteTagFilter')?.value)
  if (kind === 'files') return !!($('#fileSearch')?.value.trim() || $('#fileType')?.value)
  if (kind === 'pads') return !!$('#padSearch')?.value.trim()
  return false
}

function mergeFolderEntries(kind, serverItems = []) {
  const byPath = new Map()
  const serverNodePaths = new Set()
  const addPath = (rawPath, countDelta = 0) => {
    const clean = normalizeClientFolder(rawPath)
    if (!clean) return
    const parts = clean.split('/')
    for (let i = 1; i <= parts.length; i++) {
      const prefix = parts.slice(0, i).join('/')
      if (!byPath.has(prefix)) byPath.set(prefix, { path: prefix, direct: 0, total: 0 })
    }
    if (countDelta > 0) byPath.get(clean).direct += countDelta
  }
  for (const item of serverItems || []) {
    if (!item || !item.path) continue
    const clean = normalizeClientFolder(item.path)
    if (!clean) continue
    addPath(clean, Number(item.item_count) || 0)
    serverNodePaths.add(`${item.source_node_id || ''}::${clean}`)
  }

  // Merge folder paths from loaded items for legacy peers or optimistic updates not yet in serverItems.
  // Keep unfiltered per-(node, path) fallback counts across folder/search clicks so other folders never vanish.
  const f = state.folders[kind]
  const fallback = new Map()
  if (!hasActiveItemFilter(kind)) {
    for (const item of state[kind] || []) {
      const clean = item && item.folder ? normalizeClientFolder(item.folder) : ''
      if (!clean) continue
      const key = `${item.source_node_id || ''}::${clean}`
      if (!serverNodePaths.has(key)) {
        const prev = fallback.get(key)
        fallback.set(key, { path: clean, count: (prev ? prev.count : 0) + 1 })
      }
    }
    if (f) f.fallbackCounts = fallback
  } else {
    if (f?.fallbackCounts) {
      for (const [key, entry] of f.fallbackCounts.entries()) {
        if (!serverNodePaths.has(key)) fallback.set(key, entry)
      }
    }
    for (const item of state[kind] || []) {
      const clean = item && item.folder ? normalizeClientFolder(item.folder) : ''
      if (!clean) continue
      const key = `${item.source_node_id || ''}::${clean}`
      if (!serverNodePaths.has(key) && !fallback.has(key)) {
        fallback.set(key, { path: clean, count: 1 })
      }
    }
  }
  for (const entry of fallback.values()) {
    addPath(entry.path, entry.count)
  }

  for (const node of byPath.values()) {
    node.total = node.direct
    for (const other of byPath.values()) {
      if (other.path !== node.path && other.path.startsWith(node.path + '/')) {
        node.total += other.direct
      }
    }
  }
  return [...byPath.values()].sort((a, b) => a.path.localeCompare(b.path, 'zh-CN'))
}

function syncFoldersFromState(kind) {
  const f = state.folders[kind]
  if (!f) return
  f.list = mergeFolderEntries(kind, f.serverItems)
  renderFolderUI(kind)
}

async function loadFolders(kind) {
  const f = state.folders[kind]
  if (!f) return
  try {
    const items = await apiCollection(`/api/v1/folders?kind=${encodeURIComponent(kind)}`)
    f.serverItems = items
    f.loaded = true
  } catch {
    f.serverItems = f.serverItems || []
  }
  syncFoldersFromState(kind)
}

function renderFolderUI(kind) {
  const f = state.folders[kind]
  if (!f) return
  const entries = f.list || []
  const present = new Set(entries.map(e => e.path))
  const visible = []
  for (const entry of entries) {
    const parts = entry.path.split('/')
    let hiddenByParent = false
    for (let i = 1; i < parts.length; i++) {
      if (f.collapsed.has(parts.slice(0, i).join('/'))) {
        hiddenByParent = true
        break
      }
    }
    if (hiddenByParent) continue
    const hasChildren = entries.some(other => other.path.startsWith(entry.path + '/'))
    visible.push({ ...entry, depth: parts.length - 1, name: parts[parts.length - 1], hasChildren })
  }
  const treeRows = [
    `<div class="folder-node-row${f.selected === '' ? ' active' : ''}"><span class="folder-toggle spacer"></span><button type="button" class="folder-select" data-folder=""><span class="folder-label">${uiIcon('inbox')}<span class="folder-name">全部${FOLDER_LABELS[kind]}</span></span></button></div>`,
    `<div class="folder-node-row${f.selected === '__root__' ? ' active' : ''}"><span class="folder-toggle spacer"></span><button type="button" class="folder-select" data-folder="__root__"><span class="folder-label">${uiIcon('folder-filled', 'folder-filled')}<span class="folder-name">未归类（根目录）</span></span></button></div>`,
    ...(visible.length ? ['<div class="folder-divider" role="separator"></div>'] : []),
    ...visible.map(node => {
      const collapsed = f.collapsed.has(node.path)
      const count = node.total || 0
      const toggle = node.hasChildren
        ? `<button type="button" class="folder-toggle${collapsed ? '' : ' is-open'}" data-toggle-folder="${escapeHTML(node.path)}" aria-label="${collapsed ? '展开目录' : '折叠目录'}">${uiIcon('chevron-right')}</button>`
        : '<span class="folder-toggle spacer"></span>'
      return `<div class="folder-node-row${node.depth > 0 ? ' is-nested' : ''}${f.selected === node.path ? ' active' : ''}" style="padding-left:${4 + node.depth * 14}px">${toggle}<button type="button" class="folder-select" data-folder="${escapeHTML(node.path)}" title="${escapeHTML(node.path)}"><span class="folder-label">${uiIcon('folder-filled', 'folder-filled')}<span class="folder-name">${escapeHTML(node.name)}</span></span><span class="folder-count${count === 0 ? ' is-empty' : ''}">${count}</span></button></div>`
    })
  ].join('')
  listView(`#${kind}FolderTree`).innerHTML = treeRows

  const datalist = $(`#${kind}FolderOptions`)
  if (datalist) {
    const sig = entries.map(e => e.path).join('\u0000')
    if (datalist.dataset?.signature !== sig) {
      datalist.innerHTML = entries.map(e => `<option value="${escapeHTML(e.path)}"></option>`).join('')
      if (datalist.dataset) datalist.dataset.signature = sig
    }
  }

  let crumbs = `<button type="button" class="folder-crumb${f.selected === '' ? ' current' : ''}" data-folder="">全部</button>`
  if (f.selected === '__root__') {
    crumbs += ' <span>/</span> <span class="folder-crumb current">未归类（根目录）</span>'
  } else if (f.selected) {
    const parts = f.selected.split('/')
    parts.forEach((seg, idx) => {
      const target = parts.slice(0, idx + 1).join('/')
      const last = idx === parts.length - 1
      crumbs += last
        ? ` <span>/</span> <span class="folder-crumb current">${escapeHTML(seg)}</span>`
        : ` <span>/</span> <button type="button" class="folder-crumb" data-folder="${escapeHTML(target)}">${escapeHTML(seg)}</button>`
    })
  }
  const canManage = !!f.selected && f.selected !== '__root__' && present.has(f.selected)
  const actions = [
    f.selected && f.selected !== '__root__'
      ? `<label class="folder-recursive-toggle"><input type="checkbox" class="folder-recursive-input" ${f.recursive ? 'checked' : ''} /> 含子目录</label>`
      : '',
    canManage
      ? `<button type="button" class="text-button create-sub-folder-btn" data-parent-folder="${escapeHTML(f.selected)}">+ 子目录</button><button type="button" class="text-button rename-folder-btn" data-folder-path="${escapeHTML(f.selected)}">重命名</button><button type="button" class="text-button danger delete-folder-btn" data-folder-path="${escapeHTML(f.selected)}">删除目录</button>`
      : ''
  ].filter(Boolean).join('')
  listView(`#${kind}FolderBar`).innerHTML = `<div class="folder-breadcrumbs">${crumbs}</div><div class="folder-bar-actions">${actions}</div>`
}

function selectFolder(kind, path = '') {
  const f = state.folders[kind]
  if (!f) return
  f.selected = path === '__root__' ? '__root__' : normalizeClientFolder(path)
  const targetInput = kind === 'notes' ? $('#noteFolder') : kind === 'files' ? $('#fileUploadFolder') : $('#newPadFolder')
  if (targetInput && (!f.selected || f.selected === '__root__' || f.selected)) {
    targetInput.value = f.selected && f.selected !== '__root__' ? f.selected : ''
  }
  renderFolderUI(kind)
  void FOLDER_LOADERS[kind]?.()
}

function openCreateFolderModal(kind, parentPath = '') {
  state.folderModal = { mode: 'create', kind, oldPath: '' }
  $('#folderModalTitle').textContent = `新建${FOLDER_LABELS[kind] || ''}目录`
  const base = parentPath || (state.folders[kind]?.selected && state.folders[kind].selected !== '__root__' ? state.folders[kind].selected : '')
  $('#folderModalPath').value = base ? `${base}/` : ''
  $('#folderModalError').textContent = ''
  $('#folderModalDialog').showModal()
  $('#folderModalPath').focus()
}

function openRenameFolderModal(kind, oldPath) {
  state.folderModal = { mode: 'rename', kind, oldPath }
  $('#folderModalTitle').textContent = `重命名 / 移动目录`
  $('#folderModalPath').value = oldPath
  $('#folderModalError').textContent = ''
  $('#folderModalDialog').showModal()
  $('#folderModalPath').focus()
}

async function submitFolderModal() {
  const ctx = state.folderModal
  if (!ctx) return
  const path = normalizeClientFolder($('#folderModalPath').value)
  if (!path) { $('#folderModalError').textContent = '请填写有效的目录路径'; return }
  try {
    await withButtonBusy($('#submitFolderModalBtn'), '提交中…', async () => {
      if (ctx.mode === 'create') {
        await api('/api/v1/folders', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({ kind: ctx.kind, path })
        })
        $('#folderModalDialog').close()
        toast(`目录「${path}」已创建`)
        selectFolder(ctx.kind, path)
        await loadFolders(ctx.kind)
      } else {
        if (path === ctx.oldPath) { $('#folderModalDialog').close(); return }
        const res = await api('/api/v1/folders/rename', {
          method: 'PUT',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({ kind: ctx.kind, old_path: ctx.oldPath, new_path: path, scope: 'all' })
        })
        $('#folderModalDialog').close()
        if (state.folders[ctx.kind].selected === ctx.oldPath) state.folders[ctx.kind].selected = path
        const warn = res?.partial && res?.errors?.length ? `（部分离线未同步：${res.errors.join('；')}）` : ''
        toast(`目录已重命名为「${path}」${warn}`)
        await Promise.all([loadFolders(ctx.kind), FOLDER_LOADERS[ctx.kind]?.()])
      }
    })
  } catch (err) {
    $('#folderModalError').textContent = err.message
  }
}

function openDeleteFolderModal(kind, path) {
  state.deleteFolderTarget = { kind, path }
  $('#deleteFolderMessage').textContent = `确定删除目录「${path}」及其子目录节点吗？`
  $('#deleteFolderMode').value = 'move_to_root'
  $('#deleteFolderDialog').showModal()
}

async function submitDeleteFolderModal() {
  const target = state.deleteFolderTarget
  if (!target) return
  const mode = $('#deleteFolderMode').value || 'move_to_root'
  try {
    await withButtonBusy($('#confirmDeleteFolderBtn'), '删除中…', async () => {
      const res = await api('/api/v1/folders/delete', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({ kind: target.kind, path: target.path, mode, scope: 'all' })
      })
      $('#deleteFolderDialog').close()
      const f = state.folders[target.kind]
      if (f.selected === target.path || f.selected.startsWith(target.path + '/')) f.selected = ''
      const warn = res?.partial && res?.errors?.length ? `（部分离线未同步：${res.errors.join('；')}）` : ''
      toast(`已删除目录「${target.path}」${warn}`)
      await Promise.all([loadFolders(target.kind), FOLDER_LOADERS[target.kind]?.(), loadDashboard()])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

function openMoveItemModal(kind, item) {
  state.movingItem = { kind, id: item.id }
  $('#moveItemTargetLabel').textContent = kind === 'notes'
    ? `随手记 ${item.id}`
    : (item.file_name || item.title || item.id)
  $('#moveItemFolder').setAttribute('list', `${kind}FolderOptions`)
  $('#moveItemFolder').value = item.folder || ''
  $('#moveItemDialog').showModal()
  $('#moveItemFolder').focus()
}

async function submitMoveItemModal() {
  const target = state.movingItem
  if (!target) return
  const folder = normalizeClientFolder($('#moveItemFolder').value)
  try {
    await withButtonBusy($('#submitMoveItemBtn'), '移动中…', async () => {
      await api(`/api/v1/${target.kind}/bulk-move`, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({ ids: [target.id], folder })
      })
      $('#moveItemDialog').close()
      toast(folder ? `已移动到「${folder}」` : '已移回根目录')
      await Promise.all([loadFolders(target.kind), FOLDER_LOADERS[target.kind]?.()])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadNotes(strict=false, append=false, preserveWindow=false) {
  const seq = ++state.requestSeq.notes
  const q = encodeURIComponent($('#noteSearch').value.trim())
  const tag = encodeURIComponent($('#noteTagFilter').value)
  const offset = append ? state.notes.length : 0
  const run = async () => {
    const count = preserveWindow ? Math.max(PAGE_SIZE, Math.min(state.notes.length, 10000)) : PAGE_SIZE
    const page = {items:[],total:0}
    for (let start=0; start<count; start+=500) {
      const batch=await apiPage(appendFolderQuery(`/api/v1/notes?q=${q}&tag=${tag}&limit=${Math.min(500,count-start)}&offset=${offset+start}`, 'notes'))
      page.items.push(...batch.items); page.total=batch.total
      if (offset+page.items.length>=batch.total || batch.items.length===0) break
    }
    if (seq !== state.requestSeq.notes) return
    state.notes = append ? [...state.notes, ...page.items] : page.items
    state.noteTotal = page.total
    renderNotes()
    syncFoldersFromState('notes')
    if (append) toast(`已加载 ${state.notes.length} / ${state.noteTotal} 条速记`)
  }
  try {
    await (append ? withButtonBusy($('#loadMoreNotes'), '加载中…', run) : run())
  } catch (err) {
    if (seq === state.requestSeq.notes) reportLoadError(err, strict)
  }
}

// Lists are re-rendered by the workspace poll every few seconds. Re-assigning
// identical markup still discards and re-creates every card (layout, paint, GC
// and GPU tile churn in WebView2), so only touch the DOM when the markup really
// changed or someone else modified the container in between.
function listView(selector) {
  const el = $(selector)
  if (!el) return { set innerHTML(_) {} }
  return {
    set innerHTML(markup) {
      const nodes = el.childNodes?.length
      if (el._listMarkup === markup && el._listNodes === nodes) return
      el.innerHTML = markup
      el._listMarkup = markup
      el._listNodes = el.childNodes?.length
    }
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

  listView('#notesList').innerHTML = state.notes.length ? state.notes.map(n => {
    const restricted = !n.content && !!n.content_redacted
    const restriction = n.is_burn_after_read ? '阅后即焚' : (n.max_read_count > 0 ? `剩余 ${Math.max(0, n.max_read_count - n.read_count)} 次` : '')
    const mode = restricted ? 'plain' : (state.noteFormats.get(n.id) || (looksLikeMarkdown(n.content) ? 'rich' : 'plain'))
    const isRich = mode === 'rich'
    return `
      <article class="card note-card" data-note-id="${escapeHTML(n.id)}">
        <div class="note-head">
          <div class="note-meta">
            ${sourceLabel(n)}
            ${n.folder ? `<button type="button" class="folder-chip" data-open-folder="${escapeHTML(n.folder)}" data-folder-kind="notes">${uiIcon('folder-filled', 'folder-filled')}<span>${escapeHTML(n.folder)}</span></button>` : ''}
            ${n.pinned ? '<span class="badge">置顶</span>' : ''}
            ${n.is_burn_after_read ? '<span class="badge muted">阅后即焚</span>' : ''}
            ${n.max_read_count > 0 ? `<span class="badge muted">阅读 ${n.read_count}/${n.max_read_count}</span>` : ''}
            <span class="note-date" title="ID: ${escapeHTML(n.id)}">${formatDate(n.updated_at)}</span>
          </div>
          <div class="note-actions">
            ${restricted
              ? '<button class="text-button read-note">读取</button><button class="text-button copy-note">读取并复制</button>'
              : `<button class="text-button toggle-note-format" data-format="${isRich ? 'rich' : 'plain'}">${isRich ? '源码' : '富文本'}</button><button class="text-button copy-note">复制</button>`}
            <button class="text-button share-note">分享</button>
            <button class="text-button move-note">移动</button>
            <button class="text-button edit-note">编辑</button>
            <button class="text-button raw-note">Raw</button>
            <button class="text-button danger delete-note">删除</button>
          </div>
        </div>
        <div class="note-content${restricted ? ' restricted' : isRich ? ' rich-markdown' : ''}">${restricted ? `${uiIcon('lock')}<span>受限内容（${escapeHTML(restriction)}），主动读取才会计数。</span>` : isRich ? renderMarkdownSafe(n.content) : escapeHTML(n.content)}</div>
        ${(n.tags || []).length ? `<div class="tag-row">${(n.tags || []).map(t => `<span class="tag">#${escapeHTML(t)}</span>`).join('')}</div>` : ''}
      </article>
    `
  }).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('note')}</div>
      <h3>没有匹配的随手记</h3>
      <p>点击右上角“新建随手记”或点击“从剪贴板发送”以创建第一条随手记</p>
    </div>
  `
}

async function getManagedNote(id) {
  const listed = state.notes.find(x => x.id === id)
  if (!listed) throw new Error('随手记不存在')
  return listed.content_redacted ? api(`/api/v1/notes/${encodeURIComponent(id)}/manage`) : listed
}

async function readRestrictedNote(id, copy=false, btn=null) {
  const listed = state.notes.find(x => x.id === id)
  if (!listed) return
  if (listed.content_redacted && !await askConfirm(listed.is_burn_after_read ? '读取后该内容将被彻底销毁，是否继续？' : '本次读取会消耗一次可读次数，是否继续？', { title: '读取受限内容', confirmText: '继续读取', danger: !!listed.is_burn_after_read })) return
  try {
    await withButtonBusy(btn, '读取中…', async () => {
      const note = listed.content_redacted ? await api(`/api/v1/notes/${encodeURIComponent(id)}`) : listed
      if (copy) {
        await copyText(note.content)
        toast('已复制随手记内容')
      } else {
        $('#viewNoteMeta').textContent = `${note.id} · 已读取 ${note.read_count}${note.max_read_count ? `/${note.max_read_count}` : ''}`
        $('#viewNoteContent').textContent = note.content
        $('#viewNoteDialog').showModal()
        toast('已打开随手记内容')
      }
      await Promise.all([loadNotes(), loadDashboard()])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

function openCreateNoteModal() {
  const f = state.folders.notes
  if ($('#noteFolder') && f) {
    $('#noteFolder').value = f.selected && f.selected !== '__root__' ? f.selected : ''
  }
  $('#createNoteDialog')?.showModal()
  $('#noteContent')?.focus()
}

function openUploadFileModal() {
  const f = state.folders.files
  if ($('#fileUploadFolder') && f) {
    $('#fileUploadFolder').value = f.selected && f.selected !== '__root__' ? f.selected : ''
  }
  $('#uploadFileDialog')?.showModal()
  $('#fileUploadFolder')?.focus()
}

async function createNote() {
  const content = $('#noteContent').value.trim()
  if (!content) {
    toast('请输入内容', true)
    return
  }
  const folder = normalizeClientFolder($('#noteFolder')?.value || '')
  const payload = {
    content,
    ...(folder ? { folder } : {}),
    tags: $('#noteTags').value.split(/[,，]/).map(s => s.trim()).filter(Boolean),
    pinned: $('#notePinned').checked,
    is_burn_after_read: $('#noteBurn').checked,
    max_read_count: Number($('#noteMaxReads').value || 0),
    ttl_seconds: Number($('#noteTTL').value)
  }
  try {
    await withButtonBusy($('#createNoteBtn'), '发布中…', async () => {
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
      if ($('#createNoteDialog')?.open) $('#createNoteDialog').close()
      toast('随手记已发布')
      await Promise.all([loadNotes(), loadFolders('notes'), loadDashboard()])
    })
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
  if (!await askConfirm('将这条随手记移入回收站？', { title: '删除随手记', confirmText: '移入回收站', danger: true })) return
  try {
    await api(`/api/v1/notes/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('已移入回收站')
    await Promise.all([loadNotes(), loadFolders('notes'), loadDashboard()])
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
    if ($('#editNoteFolder')) $('#editNoteFolder').value = n.folder || ''
    $('#editNoteTags').value = (n.tags || []).join(', ')
    $('#editNotePinned').checked = !!n.pinned
    $('#editNoteBurn').checked = !!n.is_burn_after_read
    $('#editNoteMaxReads').value = n.max_read_count || 0
    $('#editNoteDialog').showModal()
    $('#editNoteContent')?.focus()
  } catch (err) {
    toast(err.message, true)
  }
}

async function saveEditNote() {
  if (!state.editingNote) return
  const payload = {
    content: $('#editNoteContent').value,
    folder: normalizeClientFolder($('#editNoteFolder')?.value || ''),
    tags: $('#editNoteTags').value.split(/[,，]/).map(s => s.trim()).filter(Boolean),
    pinned: $('#editNotePinned').checked,
    is_burn_after_read: $('#editNoteBurn').checked,
    max_read_count: Number($('#editNoteMaxReads').value || 0)
  }
  try {
    await withButtonBusy($('#saveEditNoteBtn'), '保存中…', async () => {
      await api(`/api/v1/notes/${encodeURIComponent(state.editingNote.id)}`, {
        method: 'PUT',
        headers: {'Content-Type':'application/json'},
        body: JSON.stringify(payload)
      })
      $('#editNoteDialog').close()
      toast('随手记已更新')
      await Promise.all([loadNotes(), loadFolders('notes')])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadFiles(strict=false, append=false, preserveWindow=false) {
  const seq = ++state.requestSeq.files
  const q = encodeURIComponent($('#fileSearch').value.trim())
  const type = encodeURIComponent($('#fileType').value)
  const offset = append ? state.files.length : 0
  const run = async () => {
    const count = preserveWindow ? Math.max(PAGE_SIZE, Math.min(state.files.length, 10000)) : PAGE_SIZE
    const page = {items:[],total:0}
    for (let start=0; start<count; start+=500) {
      const batch=await apiPage(appendFolderQuery(`/api/v1/files?q=${q}&type=${type}&limit=${Math.min(500,count-start)}&offset=${offset+start}`, 'files'))
      page.items.push(...batch.items); page.total=batch.total
      if (offset+page.items.length>=batch.total || batch.items.length===0) break
    }
    if (seq !== state.requestSeq.files) return
    state.files = append ? [...state.files, ...page.items] : page.items
    state.fileTotal = page.total
    renderFiles()
    syncFoldersFromState('files')
    if (append) toast(`已加载 ${state.files.length} / ${state.fileTotal} 个文件`)
  }
  try {
    await (append ? withButtonBusy($('#loadMoreFiles'), '加载中…', run) : run())
  } catch (err) {
    if (seq === state.requestSeq.files) reportLoadError(err, strict)
  }
}

function fileKindClass(f) {
  if (f.is_video) return ' kind-video'
  if ((f.mime_type || '').startsWith('image/')) return ' kind-image'
  if (/(zip|tar|rar)/.test(f.mime_type || '')) return ' kind-archive'
  return ''
}

function fileIcon(f) {
  let shape = '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6M8 13h8M8 17h5"/>'
  if (f.is_video) shape = '<rect x="3" y="4" width="18" height="16" rx="3"/><path d="m10 8 6 4-6 4Z"/>'
  else if ((f.mime_type || '').startsWith('image/')) shape = '<rect x="3" y="3" width="18" height="18" rx="3"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/>'
  else if (/(zip|tar|rar)/.test(f.mime_type || '')) shape = '<rect x="3" y="5" width="18" height="15" rx="2"/><path d="M3 9h18M10 13h4M7 5V3h10v2"/>'
  return `<svg width="26" height="26" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${shape}</svg>`
}

function fileAvailability(file) {
  const remote = typeof file.id === 'string' && /^node-[0-9a-f]{20}~/.test(file.id)
  if (!remote) return '本机文件'
  return file.source_online === false ? '远端索引 · 来源离线' : '远端索引 · 按需读取'
}

function renderFiles() {
  $('#fileCount').textContent = state.fileTotal > state.files.length
    ? `${state.files.length} / ${state.fileTotal} 个文件`
    : `${state.files.length} 个文件`
  $('#loadMoreFiles').classList.toggle('hidden', state.files.length >= state.fileTotal)
  listView('#filesList').innerHTML = state.files.length ? state.files.map(f => `
    <article class="card file-card" data-file-id="${escapeHTML(f.id)}">
      <div class="file-info">
        <div class="file-icon${fileKindClass(f)}">${fileIcon(f)}</div>
        <div class="file-description">
          <div class="file-meta-row">
            ${sourceLabel(f)}<span class="file-availability">${fileAvailability(f)}</span>
            ${f.folder ? `<button type="button" class="folder-chip" data-open-folder="${escapeHTML(f.folder)}" data-folder-kind="files">${uiIcon('folder-filled', 'folder-filled')}<span>${escapeHTML(f.folder)}</span></button>` : ''}
          </div>
          <h3 title="${escapeHTML(f.file_name)}">${escapeHTML(f.file_name)}</h3>
          <p>${formatBytes(f.file_size)} · ${escapeHTML(f.mime_type)} · 下载 ${f.download_count || 0} 次</p>
        </div>
      </div>
      <div class="file-actions">
        ${f.is_video ? `<button class="button ghost small play-file" ${f.source_online === false ? 'disabled title="来源设备离线，暂时无法读取原文件"' : ''}>${uiIcon('play')}<span>按需播放</span></button>` : ''}
        <button class="button ghost small download-file" ${f.source_online === false ? 'disabled title="来源设备离线，暂时无法下载"' : ''}>下载到本机</button>
        <button class="text-button share-file">分享</button>
        <button class="text-button move-file">移动</button>
        <button class="text-button rename-file">重命名</button>
        <button class="text-button danger delete-file">删除</button>
      </div>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('folder')}</div>
      <h3>没有匹配的文件</h3>
      <p>点击右上角“上传本机文件”或直接拖放文件到此页面；仅在需要时按需传输</p>
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

async function openDownload(f, btn=null) {
  const prevText = btn ? btn.textContent : ''
  let triggered = false
  if (btn) {
    btn.disabled = true
    btn.textContent = '准备下载…'
  }
  toast(`正在准备下载「${f.file_name}」…`)
  try {
    const ticket = await createFileTicket(f, 'attachment')
    const a = document.createElement('a')
    a.href = ticket.url
    a.download = f.file_name
    a.rel = 'noopener'
    document.body?.appendChild?.(a)
    a.click()
    a.remove?.()
    triggered = true
    toast(`已开始下载「${f.file_name}」，请查看系统默认“下载”目录`, false, 4000)
    if (btn) {
      btn.textContent = '✓ 已触发下载'
      setTimeout(() => {
        btn.disabled = false
        btn.textContent = prevText
      }, 1200)
    }
  } catch (err) {
    toast(err.message, true)
  } finally {
    if (btn && !triggered) {
      btn.disabled = false
      btn.textContent = prevText
    }
  }
}

async function playFile(f, btn=null) {
  toast(`正在加载视频「${f.file_name}」…`)
  try {
    await withButtonBusy(btn, '加载中…', async () => {
      const ticket = await createFileTicket(f, 'inline')
      $('#videoTitle').textContent = f.file_name
      const player = $('#videoPlayer')
      player.src = ticket.url
      $('#videoDialog').showModal()
      await player.play().catch(() => {})
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function openRawNote(id, btn=null) {
  try {
    await withButtonBusy(btn, '打开中…', async () => {
      const auth = captureAuthContext()
      const res = await fetch(`/n/${encodeURIComponent(id)}/raw`, {headers: headers()})
      if (res.status === 401) {
        showAuth(auth)
        return
      }
      if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      window.open(url, '_blank', 'noopener')
      toast('已在新窗口打开原始文本')
      setTimeout(() => URL.revokeObjectURL(url), 60000)
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function deleteFile(id) {
  if (!await askConfirm('将该文件移入回收站？磁盘实体会暂时保留。', { title: '删除文件', confirmText: '移入回收站', danger: true })) return
  try {
    await api(`/api/v1/files/${encodeURIComponent(id)}`, {method:'DELETE'})
    toast('文件已移入回收站')
    await Promise.all([loadFiles(), loadFolders('files'), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

function renameFile(file) {
  state.renamingFile = file
  $('#renameFileName').value = file.file_name || ''
  $('#renameFileFolder').value = file.folder || ''
  $('#renameFileDialog').showModal()
  $('#renameFileName').focus()
}

async function submitRenameFile() {
  const file = state.renamingFile
  if (!file) return
  const name = $('#renameFileName').value.trim()
  const folder = normalizeClientFolder($('#renameFileFolder').value)
  if (!name) { toast('文件名不能为空', true); return }
  if (name === file.file_name && folder === (file.folder || '')) {
    $('#renameFileDialog').close()
    return
  }
  try {
    await withButtonBusy($('#submitRenameFileBtn'), '保存中…', async () => {
      await api(`/api/v1/files/${encodeURIComponent(file.id)}`, {
        method: 'PUT',
        headers: {'Content-Type':'application/json'},
        body: JSON.stringify({ file_name: name, folder })
      })
      $('#renameFileDialog').close()
      toast('文件信息已更新')
      await Promise.all([loadFolders('files'), loadFiles()])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function collectDroppedFiles(dataTransfer) {
  const items = [...(dataTransfer?.items || [])]
  const entries = items.map(i => typeof i.webkitGetAsEntry === 'function' ? i.webkitGetAsEntry() : null).filter(Boolean)
  if (!entries.length) return [...(dataTransfer?.files || [])]
  const out = []
  const walk = async (entry, relDir) => {
    if (entry.isFile) {
      await new Promise(resolve => entry.file(file => {
        if (relDir) file._relativeFolder = relDir
        out.push(file)
        resolve()
      }, () => resolve()))
      return
    }
    if (entry.isDirectory) {
      const nextDir = joinFolderPaths(relDir, entry.name)
      const reader = entry.createReader()
      while (true) {
        const batch = await new Promise(resolve => reader.readEntries(resolve, () => resolve([])))
        if (!batch || !batch.length) break
        for (const child of batch) await walk(child, nextDir)
      }
    }
  }
  for (const entry of entries) await walk(entry, '')
  return out
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
  const baseFolder = normalizeClientFolder(options.folder ?? $('#fileUploadFolder')?.value ?? '')
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
    let relDir = normalizeClientFolder(file?._relativeFolder || '')
    if (!relDir && typeof file?.webkitRelativePath === 'string' && file.webkitRelativePath.includes('/')) {
      relDir = normalizeClientFolder(file.webkitRelativePath.split('/').slice(0, -1).join('/'))
    }
    const taskFolder = joinFolderPaths(baseFolder, relDir)
    const task = {
      file, name: String(file.name), size: file.size, ttl, folder: taskFolder, order: ++state.uploadSequence,
      status: 'queued', attempt: 0, loaded: 0, total: 0, progress: null,
      processing: false, error: '', xhr: null, cancelAttempt: null
    }
    state.uploadQueue.push(task)
    createUploadRow(task)
    accepted++
  }
  if (accepted > 0 && $('#uploadFileDialog')?.open) $('#uploadFileDialog').close()
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
  meta.textContent = `${task.folder ? `目录：${task.folder} · ` : ''}${formatBytes(task.size)}${task.attempt > 1 ? ` · 第 ${task.attempt} 次尝试` : ''}`
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
  const panel = $('#uploadActivityPanel')
  if (panel) panel.hidden = state.uploadQueue.length === 0 && !state.uploadNotice
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
    const auth = captureAuthContext()
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
          void Promise.allSettled([loadFiles(), loadFolders('files'), loadDashboard()])
        }
        return
      }
      const message = typeof payload?.message === 'string' ? payload.message : `上传未获确认（HTTP ${xhr.status}），请检查文件列表后再重试。`
      finish('failed', message)
      if (xhr.status === 401) showAuth(auth)
    }
    xhr.onerror = () => finish('failed', '网络连接错误，文件可能已在服务端保存；请检查文件列表后再重试。')
    xhr.ontimeout = () => finish('failed', '上传超时，文件可能已在服务端保存；请检查文件列表后再重试。')
    xhr.onabort = () => finish('cancelled', UPLOAD_CANCEL_NOTICE)
    const form = new FormData()
    form.append('file', task.file)
    form.append('ttl_seconds', task.ttl)
    if (task.folder) form.append('folder', task.folder)
    xhr.send(form)
  } catch (err) {
    finish('failed', `无法开始上传：${err.message}`, true)
  }
}

async function loadPads(strict=false) {
  const seq = ++state.requestSeq.pads
  try {
    const pads = await apiCollection(appendFolderQuery(`/api/v1/pads?q=${encodeURIComponent($('#padSearch').value.trim())}`, 'pads'))
    if (seq !== state.requestSeq.pads) return
    state.pads = pads
    renderPads()
    syncFoldersFromState('pads')
  } catch (err) {
    if (seq === state.requestSeq.pads) reportLoadError(err, strict)
  }
}

function renderPads() {
  if ($('#padCount')) $('#padCount').textContent = `${state.pads.length} 篇文档`
  listView('#padsList').innerHTML = state.pads.length ? state.pads.map(p => `
    <button class="pad-item ${state.currentPad?.id === p.id ? 'active' : ''}" data-pad-id="${escapeHTML(p.id)}">
      <div class="pad-item-main">
        <strong>${escapeHTML(p.title)}</strong>
        <div class="pad-item-meta">
          ${sourceLabel(p)}
          ${p.folder ? `<span class="folder-chip">${uiIcon('folder-filled', 'folder-filled')}<span>${escapeHTML(p.folder)}</span></span>` : ''}
          <span>v${p.version} · ${formatDate(p.updated_at)}</span>
        </div>
      </div>
      ${uiIcon('chevron-right', 'pad-item-chevron')}
    </button>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('document')}</div>
      <h3>没有匹配的协同文档</h3>
      <p>点击右上角“新建协同文档”创建支持版本保护的协作文档</p>
    </div>
  `
}

function applyPadPreviewMode(preview) {
  state.padPreviewMode = !!preview
  const textarea = $('#padContent')
  const previewEl = $('#padPreview')
  const btn = $('#togglePadPreviewBtn')
  if (!textarea || !previewEl || !btn) return
  if (state.padPreviewMode) {
    previewEl.innerHTML = renderMarkdownSafe(textarea.value) || '<p class="muted-text">暂无正文内容</p>'
    textarea.classList.add('hidden')
    previewEl.classList.remove('hidden')
    btn.innerHTML = '<span>编辑</span>'
  } else {
    previewEl.classList.add('hidden')
    textarea.classList.remove('hidden')
    btn.innerHTML = '<span>预览</span>'
  }
}

function renderPadEditor(p) {
  $('#padConflictActions')?.classList.add('hidden')
  $('#padEmpty').classList.add('hidden')
  $('#padForm').classList.remove('hidden')
  $('#padEditorCard')?.classList.remove('hidden')
  $('#padWorkspace')?.classList.add('is-editing')
  $('#padTitle').value = p.title
  if ($('#padFolder')) $('#padFolder').value = p.folder || ''
  $('#padContent').value = p.content
  applyPadPreviewMode(false)
  $('#padMeta').textContent = `${p.id} · 版本 v${p.version} · 更新于 ${formatDate(p.updated_at)}`
  $('#saveState').textContent = '已同步'
  $('#saveState').className = 'badge'
  renderPads()
}

async function closePadEditor() {
  if (state.currentPadDirty) {
    const saved = await savePad(false)
    if (!saved && !await askConfirm('当前文档未能保存，仍要返回列表吗？', { title: '返回文档列表', confirmText: '仍要返回', danger: true })) return
  }
  clearTimeout(state.saveTimer)
  state.currentPad = null
  state.currentPadDirty = false
  $('#padForm')?.classList.add('hidden')
  $('#padEmpty')?.classList.remove('hidden')
  $('#padEditorCard')?.classList.add('hidden')
  $('#padWorkspace')?.classList.remove('is-editing')
  renderPads()
}

async function selectPad(id) {
  const selectSeq = ++state.padSelectSeq
  if (state.currentPad?.id === id) return
  if (state.currentPadDirty) {
    const saved = await savePad(false)
    if (!saved && !await askConfirm('当前文档未能保存，仍要切换吗？', { title: '切换协同文档', confirmText: '仍要切换', danger: true })) return
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
  if ($('#newPadFolder')) {
    const sel = state.folders.pads.selected
    $('#newPadFolder').value = sel && sel !== '__root__' ? sel : ''
  }
  $('#newPadError').textContent = ''
  $('#newPadDialog').showModal()
  $('#newPadTitle').focus()
}

async function submitNewPad() {
  if (state.padCreating) return
  const title = $('#newPadTitle').value.trim()
  if (!title) { $('#newPadError').textContent = '请填写文档标题'; return }
  const folder = normalizeClientFolder($('#newPadFolder')?.value || '')
  state.padCreating = true
  $('#createPadSubmitBtn').disabled = true
  try {
    if (state.currentPadDirty && !await savePad(false)) throw new Error('当前文档未保存，请先处理草稿或冲突')
    const p = await api('/api/v1/pads', {
      method: 'POST', headers: {'Content-Type':'application/json'},
      body: JSON.stringify({title, content: `# ${title}\n\n`, ...(folder ? { folder } : {})})
    })
    $('#newPadDialog').close()
    toast('文档已创建')
    await Promise.all([loadPads(), loadFolders('pads'), loadDashboard()])
    await selectPad(p.id)
  } catch (err) { $('#newPadError').textContent = err.message }
  finally { state.padCreating = false; $('#createPadSubmitBtn').disabled = false }
}

async function refreshCurrentPad() {
  const current = state.currentPad
  if (!current || state.saveInFlight || state.padRefreshBusy) return
  state.padRefreshBusy = true
  const seq = ++state.padRefreshSeq
  try {
    const remote = await api(`/api/v1/pads/${encodeURIComponent(current.id)}`)
    if (seq !== state.padRefreshSeq || state.currentPad?.id !== current.id || state.saveInFlight) return
    // A GET started before a successful save may arrive after that save.
    // Versions are monotonic; never replace the editor with an older reply.
    if (remote.version <= state.currentPad.version) return
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
      if (err.status === 404 && !state.currentPadDirty) {
        sessionRemove(`omnishare.padDraft.${current.id}`)
        state.currentPad = null
        $('#padForm').classList.add('hidden')
        $('#padEmpty').classList.remove('hidden')
        $('#padEditorCard')?.classList.add('hidden')
        $('#padWorkspace')?.classList.remove('is-editing')
        toast('当前协同文档已在其他设备删除')
        void Promise.all([loadPads(), loadDashboard()])
        return
      }
      $('#saveState').textContent = err.status === 404 ? '文档已删除 · 保留当前内容' : '连接中断 · 保留当前内容'
      if (state.currentPadDirty) preservePadDraft()
    }
  } finally { state.padRefreshBusy = false }
}

async function refreshWorkspace() {
  if (document.hidden || $('#authDialog').open || state.restoreInFlight) return
  // Keep the open editor responsive even while a different offline node makes
  // aggregated lists slow. Each editor refresh is single-flight independently.
  const editorRefresh = state.activeTab === 'pads' ? refreshCurrentPad() : Promise.resolve()
  if (state.workspaceBusy) { await editorRefresh; return }
  state.workspaceBusy = true
  try {
    const loaders = {dashboard:()=>loadNotes(false,false,true), notes:()=>loadNotes(false,false,true), files:()=>loadFiles(false,false,true), pads:loadPads, shares:loadShares, trash:loadTrash, activity:loadAudit}
    const tickFolders = state.folders[state.activeTab] ? loadFolders(state.activeTab) : Promise.resolve()
    await Promise.all([loadDashboard(), loaders[state.activeTab]?.(), tickFolders, editorRefresh])
  } finally { state.workspaceBusy = false }
}

function samePadSnapshot(snapshot) {
  const current = padSnapshot()
  return !!current && current.id === snapshot.id && current.version === snapshot.version &&
    current.title === snapshot.title && current.content === snapshot.content && (current.folder || '') === (snapshot.folder || '')
}

async function loadRemotePadVersion() {
  if (!state.currentPad || state.saveInFlight) return
  const draft = padSnapshot()
  preservePadDraft(draft)
  clearTimeout(state.saveTimer)
  ++state.padSaveEpoch
  try {
    const remote = await api(`/api/v1/pads/${encodeURIComponent(draft.id)}`)
    if (!samePadSnapshot(draft) || state.saveInFlight || remote.version < state.currentPad.version) {
      toast('期间已有新的编辑或保存，已保留当前内容，请重新选择远端版本')
      return
    }
    state.currentPad = {...remote}; state.currentPadDirty = false; state.padSaveBlocked = false
    renderPadEditor(remote); $('#padConflictActions').classList.add('hidden')
    toast('已载入远端版本；原草稿仍保留在本次会话中')
  } catch (err) { toast(err.message, true) }
}

async function savePadDraftCopy() {
  const draft = padSnapshot()
  if (!draft || state.draftCopyBusy) return
  state.draftCopyBusy = true
  $('#saveDraftCopyBtn').disabled = true
  clearTimeout(state.saveTimer)
  ++state.padSaveEpoch
  try {
    const copy = await api('/api/v1/pads', {method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({title:(draft.title+'（草稿副本）').slice(0,200),content:draft.content,...(draft.folder ? {folder:draft.folder} : {})})})
    preservePadDraft(draft)
    // An in-flight copy belongs to the snapshot, not to newer typing or a
    // different document selected while the request was travelling.
    if (samePadSnapshot(draft) && !state.saveInFlight) {
      state.currentPad = {...copy}; state.currentPadDirty = false; state.padSaveBlocked = false
      renderPadEditor(copy); $('#padConflictActions').classList.add('hidden')
    }
    await loadPads()
    toast('草稿副本已保存到当前设备；期间的新编辑保留不变')
  } catch (err) { toast(err.message, true) }
  finally { state.draftCopyBusy = false; $('#saveDraftCopyBtn').disabled = false }
}

function padSnapshot() {
  if (!state.currentPad) return null
  const folder = normalizeClientFolder($('#padFolder')?.value || '')
  const snapshot = {
    id: state.currentPad.id,
    title: $('#padTitle').value.trim() || '未命名文档',
    content: $('#padContent').value,
    version: state.currentPad.version
  }
  if (folder || state.currentPad.folder) snapshot.folder = folder
  return snapshot
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
  const epoch = state.padSaveEpoch
  state.padSaveChain = state.padSaveChain.catch(() => false).then(() =>
    epoch === state.padSaveEpoch ? performPadSave(snapshot, showToast) : false)
  return state.padSaveChain
}

async function performPadSave(snapshot, showToast=true) {
  if (state.currentPad?.id === snapshot.id && state.padSaveBlocked) return false
  state.saveInFlight = true
  const isCurrent = state.currentPad?.id === snapshot.id
  if (isCurrent) $('#saveState').textContent = '保存中...'
  const known = isCurrent ? state.currentPad.version : (state.pads.find(p => p.id === snapshot.id)?.version || snapshot.version)
  const payload = {title: snapshot.title, content: snapshot.content, version: known}
  if (snapshot.folder !== undefined) payload.folder = snapshot.folder
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
      const currentFolder = normalizeClientFolder($('#padFolder')?.value || '')
      const unchanged = $('#padTitle').value.trim() === snapshot.title && $('#padContent').value === snapshot.content && (snapshot.folder === undefined || currentFolder === (snapshot.folder || ''))
      state.currentPadDirty = !unchanged
      $('#saveState').textContent = unchanged ? '已保存' : '待保存'
      $('#saveState').className = `badge${unchanged ? '' : ' muted'}`
      $('#padMeta').textContent = `${p.id} · 版本 v${p.version} · 更新于 ${formatDate(p.updated_at)}`
      if (unchanged) sessionRemove(`omnishare.padDraft.${p.id}`)
    }
    renderPads()
    void loadFolders('pads')
    if (showToast) toast('文档已保存')
    return true
  } catch (err) {
    preservePadDraft(snapshot)
    if (err.status === 409) {
      ++state.padSaveEpoch
      if (state.currentPad?.id === snapshot.id) {
        state.padSaveBlocked = true
        $('#padConflictActions')?.classList.remove('hidden')
      }
    }
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
  if (!state.currentPad) return
  if (!await askConfirm('确定将当前协同文档移入回收站吗？所有已绑定设备将同步移除该文档（可在回收站恢复）。', { title: '删除协同文档', confirmText: '移入回收站', danger: true })) return
  clearTimeout(state.saveTimer)
  const id = state.currentPad.id
  try {
    await withButtonBusy($('#deletePadBtn'), '删除中…', async () => {
      await api(`/api/v1/pads/${encodeURIComponent(id)}`, {method:'DELETE'})
      sessionRemove(`omnishare.padDraft.${id}`)
      state.currentPad = null
      state.currentPadDirty = false
      $('#padForm').classList.add('hidden')
      $('#padEmpty').classList.remove('hidden')
      $('#padEditorCard')?.classList.add('hidden')
      $('#padWorkspace')?.classList.remove('is-editing')
      toast('文档已移入回收站')
      await Promise.all([loadPads(), loadFolders('pads'), loadDashboard()])
    })
  } catch (err) {
    toast(err.message, true)
  }
}

async function copyText(value) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(value)
  const ta = document.createElement('textarea')
  ta.value = value
  const container = $('dialog[open]') || document.body
  container.appendChild(ta)
  try {
    ta.select()
    return document.execCommand('copy')
  } finally {
    ta.remove()
  }
}

function openShare(type, id, name) {
  state.shareDialogSession++
  $('#shareObjectType').value = type
  $('#shareObjectId').value = id
  $('#shareObjectName').textContent = name
  $('#shareTitle').textContent = '创建安全外链分享'
  $('#shareCreateFields').hidden = false
  $('#shareTTL').value = '604800'
  $('#shareMaxAccess').value = '0'
  $('#shareCreateError').textContent = ''
  $('#shareResult').hidden = true
  $('#shareResultURL').value = ''
  $('#shareCopyStatus').textContent = ''
  $('#createShareBtn').hidden = false
  $('#copyShareBtn').hidden = true
  $('#closeShareBtn').textContent = '取消'
  $('#shareDialog').showModal()
}

function shareAccessibleURL(link) {
  const raw = String(link?.url || '').trim()
  if (!raw) return { url: '', loopbackOnly: false }
  try {
    const parsed = new URL(raw, location.href)
    if (!isLoopbackHost(parsed.hostname)) return { url: parsed.href, loopbackOnly: false }
    const dev = link.source_node_id
      ? state.devices.find(d => d.id === link.source_node_id)
      : state.devices.find(d => d.is_local)
    const candidates = [
      !link.source_node_id || dev?.is_local ? safeDeviceURL(location.origin) : '',
      ...(dev ? deviceAddresses(dev) : []),
    ].filter(Boolean)
    const reachable = candidates.find(addr => {
      try { return !isLoopbackHost(new URL(addr).hostname) } catch { return false }
    })
    if (reachable) {
      const target = new URL(reachable)
      parsed.protocol = target.protocol
      parsed.hostname = target.hostname
      parsed.port = target.port
      return { url: parsed.href, loopbackOnly: false }
    }
    return { url: parsed.href, loopbackOnly: true }
  } catch {
    return { url: raw, loopbackOnly: false }
  }
}

async function createShare() {
  const dialog = $('#shareDialog')
  const createButton = $('#createShareBtn')
  if (!dialog.open || createButton.disabled || createButton.hidden) return
  const session = state.shareDialogSession
  const payload = {
    object_type: $('#shareObjectType').value,
    object_id: $('#shareObjectId').value,
    ttl_seconds: Number($('#shareTTL').value),
    max_access_count: Number($('#shareMaxAccess').value)
  }
  $('#shareCreateError').textContent = ''
  try {
    await withButtonBusy(createButton, '创建中…', async () => {
      const link = await api('/api/v1/shares', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(payload)
      })
      if (dialog.open && session === state.shareDialogSession) {
        const resolved = shareAccessibleURL(link)
        $('#shareTitle').textContent = '分享链接已创建'
        $('#shareCreateFields').hidden = true
        createButton.hidden = true
        $('#shareResultURL').value = resolved.url
        $('#shareReachability').hidden = !resolved.loopbackOnly
        $('#shareResult').hidden = false
        $('#copyShareBtn').hidden = false
        $('#closeShareBtn').textContent = '完成'
        $('#copyShareBtn').focus()
      }
      await Promise.all([loadShares(), loadDashboard()])
    })
  } catch (err) {
    if (dialog.open && session === state.shareDialogSession) {
      $('#shareCreateError').textContent = err.message
    }
  }
}

async function copyShareLink(url) {
  if (await copyText(url) === false) throw new Error('无法写入剪贴板')
}

async function copyCreatedShareLink() {
  const session = state.shareDialogSession
  const url = $('#shareResultURL')
  const status = $('#shareCopyStatus')
  try {
    await copyShareLink(url.value)
    if (session === state.shareDialogSession) status.textContent = '已复制链接'
  } catch {
    if ($('#shareDialog').open && session === state.shareDialogSession) {
      status.textContent = '剪贴板不可用，已选中链接，可手动复制。'
      url.focus()
      url.select()
    }
  }
}

async function loadShares(strict=false, manual=false) {
  const run = async () => {
    state.shares = await apiCollection('/api/v1/shares')
    renderShares()
    if (manual) toast('分享列表已刷新')
  }
  try {
    await (manual ? withButtonBusy($('#refreshSharesBtn'), '刷新中…', run) : run())
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
  listView('#sharesList').innerHTML = state.shares.length ? state.shares.map(link => {
    const [status, cls] = shareStatus(link)
    const limit = link.max_access_count > 0 ? `${link.access_count}/${link.max_access_count}` : `${link.access_count}/不限`
    const resolved = shareAccessibleURL(link)
    return `
      <article class="card share-card" data-share-id="${escapeHTML(link.id)}">
        <div>
          <div class="note-meta">
            <span class="tag">${labels[link.object_type] || escapeHTML(link.object_type)}</span>
            <span class="badge ${cls === 'danger' ? 'muted' : ''}">${status}</span>
          </div>
          ${sourceLabel(link)}<h3>${escapeHTML(link.name)}</h3>
          <p class="muted-text">访问 ${limit} · 到期 ${link.expires_at ? formatDate(link.expires_at) : '长期有效'}</p>
          <code class="share-url">${escapeHTML(resolved.url)}</code>
          ${resolved.loopbackOnly ? '<p class="share-url-warn">仅来源设备可用。请先在来源设备设置可访问的地址。</p>' : ''}
        </div>
        <div class="file-actions">
          <button class="button ghost small copy-share">${uiIcon('copy')}<span>复制</span></button>
          <button class="button ghost small open-share">${uiIcon('external')}<span>打开</span></button>
          ${link.revoked_at ? '' : `<button class="button danger small revoke-share">${uiIcon('ban')}<span>撤销</span></button>`}
        </div>
      </article>
    `
  }).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('link')}</div>
      <h3>尚未创建安全分享</h3>
      <p>您可以在随手记、文件或协同文档中点击“分享”按钮生成匿名安全访问链接</p>
    </div>
  `
}

async function revokeShare(id) {
  if (!await askConfirm('撤销后该安全外链将立即失效，是否继续？', { title: '撤销分享链接', confirmText: '撤销分享', danger: true })) return
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
  listView('#trashList').innerHTML = state.trash.length ? state.trash.map(item => `
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
        <button class="button ghost small restore-trash">${uiIcon('restore')}<span>恢复</span></button>
        <button class="button danger small purge-trash">${uiIcon('trash')}<span>永久删除</span></button>
      </div>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('trash')}</div>
      <h3>回收站为空</h3>
      <p>被删除的随手记、文件和协同文档会安全暂存在这里</p>
    </div>
  `
}

async function restoreTrash(type, id) {
  try {
    await api(`/api/v1/trash/${encodeURIComponent(type)}/${encodeURIComponent(id)}/restore`, {method:'POST'})
    toast('已成功恢复内容')
    await Promise.all([loadTrash(), loadDashboard(), loadNotes(), loadFiles(), loadPads(), loadFolders('notes'), loadFolders('files'), loadFolders('pads')])
  } catch (err) {
    toast(err.message, true)
  }
}

async function purgeTrash(type, id) {
  if (!await askConfirm('永久删除后将无法恢复，是否确认删除？', { title: '永久删除', confirmText: '永久删除', danger: true })) return
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
  if (!await askConfirm('确定要清空整个回收站吗？所有暂存文件与记录将永久不可恢复！', { title: '清空回收站', confirmText: '确认清空', danger: true })) return
  try {
    await api('/api/v1/trash', {method:'DELETE'})
    toast(state.workspaceStatus?.partial ? '在线设备回收站已处理；离线设备未清空' : '所有已绑定设备的回收站已清空', !!state.workspaceStatus?.partial)
    await Promise.all([loadTrash(), loadDashboard()])
  } catch (err) {
    toast(err.message, true)
  }
}

async function loadAudit(manual=false) {
  const run = async () => {
    state.audits = await api('/api/v1/audit?limit=100')
    renderAudit()
    if (manual) toast('活动记录已刷新')
  }
  try {
    await (manual ? withButtonBusy($('#refreshAuditBtn'), '刷新中…', run) : run())
  } catch (err) {
    toast(err.message, true)
  }
}

function renderAudit() {
  const labels = {create:'创建', update:'更新', delete:'删除', trash:'移入回收站', restore:'恢复', purge:'永久删除', upload:'上传', rename:'重命名', share:'分享', revoke:'撤销', access:'访问', read:'读取', cleanup:'清理'}
  listView('#auditList').innerHTML = state.audits.length ? state.audits.map(e => `
    <article class="timeline-item">
      <span class="timeline-action">${labels[e.action] || escapeHTML(e.action)} · ${escapeHTML(e.object)}</span>
      ${sourceLabel(e)}<div class="timeline-summary">${escapeHTML(e.summary || e.object_id || '—')}</div>
      <time class="timeline-time">${formatDate(e.created_at)}</time>
    </article>
  `).join('') : `
    <div class="empty">
      <div class="empty-icon">${uiIcon('activity')}</div>
      <h3>暂无活动记录</h3>
      <p>所有内容修改、文件上传与分享访问均会通过 SHA-256 哈希链记录于此</p>
    </div>
  `
}

async function loadDevices(strict=false, refresh=false) {
  if (state.devicesLoading) return
  state.devicesLoading = true
  const seq = ++state.requestSeq.devices
  const run = async () => {
    // Routes that just failed are backed off server-side; an explicit refresh
    // asks the server to probe every route again right now.
    const devices = await api(refresh ? '/api/v1/devices?refresh=1' : '/api/v1/devices')
    if (seq !== state.requestSeq.devices) return
    state.devices = devices
    renderDevices()
    if (refresh) toast('设备列表已刷新')
  }
  try {
    await (refresh ? withButtonBusy($('#refreshDevicesBtn'), '刷新中…', run) : run())
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
  const protectedAccess = state.config.access_key_enabled ?? state.config.has_access_key
  if (state.config.allow_lan === false) {
    summary.textContent = protectedAccess ? '仅本机访问 · 访问密钥已启用' : '仅本机访问 · 未启用访问密钥'
    return
  }
  summary.textContent = protectedAccess
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
    await Promise.all([loadDevices(),loadAll(),loadFolders('notes'),loadFolders('files'),loadFolders('pads')]); await refreshWorkspace()
  } catch (err) { $('#bindPeerError').textContent = err.message }
  finally { state.peerBinding = false; $('#bindPeerSubmitBtn').disabled = false }
}

async function unbindDevice(id) {
  try {
    const result = await api(`/api/v1/peers/${encodeURIComponent(id)}`,{method:'DELETE'})
    toast(result.reciprocal_removed ? '双方已解除绑定' : '本机已解除绑定；对方离线，尚未移除其登记')
    await Promise.all([loadDevices(),loadFolders('notes'),loadFolders('files'),loadFolders('pads')]); await refreshWorkspace()
  } catch (err) { toast(err.message,true) }
}

// A device is an identity, not an IP address. Never merge by display name.
function deviceAddresses(device) {
  return [...new Set([device.url, ...(Array.isArray(device.addresses) ? device.addresses : [])]
    .map(safeDeviceURL).filter(Boolean))]
}

function deviceRouteLabel(address) {
  try {
    const host = new URL(address).hostname.replace(/^\[|\]$/g, '').toLowerCase()
    if (host === '127.0.0.1' || host === '::1' || host === 'localhost') return '本机'
    const octets = host.split('.').map(Number)
    if ((octets.length === 4 && octets[0] === 100 && octets[1] >= 64 && octets[1] <= 127) || host.startsWith('fd7a:115c:a1e0:')) return 'Tailscale'
    return '局域网'
  } catch { return '连接' }
}

function displayDevices(devices) {
  const merged = new Map()
  for (const device of devices) {
    const key = device.id || safeDeviceURL(device.url)
    if (!key) continue
    const previous = merged.get(key)
    if (!previous) { merged.set(key, {...device, addresses:deviceAddresses(device)}); continue }
    const priority = d => (d.is_local ? 4 : 0) + (d.bound ? 2 : 0) + (d.online ? 1 : 0)
    const preferred = priority(device) > priority(previous) ? device : previous
    merged.set(key, {...preferred, addresses:[...new Set([...deviceAddresses(preferred), ...deviceAddresses(previous), ...deviceAddresses(device)])]})
  }
  return [...merged.values()]
}

function openDeviceAliasDialog(deviceId) {
  const device = state.devices.find(d => d.id === deviceId)
  if (!device) return
  state.editingDeviceAlias = deviceId
  const current = (device.alias || (device.is_local && device.hostname !== 'OmniShare' ? device.hostname : '') || '').trim()
  const primary = safeDeviceURL(device.url) || (deviceAddresses(device)[0] || '')
  $('#deviceAliasSubtitle').textContent = `${device.is_local ? '当前设备' : '已绑定设备'}${primary ? ` · ${new URL(primary).host}` : ''}`
  $('#deviceAliasInput').value = current
  $('#deviceAliasError').textContent = ''
  $('#deviceAliasDialog').showModal()
  $('#deviceAliasInput').focus()
  $('#deviceAliasInput').select()
}

async function submitDeviceAlias() {
  const id = state.editingDeviceAlias
  if (!id) return
  const alias = $('#deviceAliasInput').value.trim()
  $('#deviceAliasError').textContent = ''
  try {
    await withButtonBusy($('#saveDeviceAliasBtn'), '保存中…', async () => {
      await api(`/api/v1/peers/${encodeURIComponent(id)}/alias`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ alias })
      })
      $('#deviceAliasDialog').close()
      toast(alias ? `已将设备别名设为“${alias}”` : '已恢复设备默认名称')
      await Promise.all([loadDevices(), loadConfig(), loadAll()])
    })
  } catch (err) {
    $('#deviceAliasError').textContent = err.message
  }
}

function renderDevices() {
  const devices = displayDevices(state.devices)
  const groups = [
    {title:'当前设备',devices:devices.filter(d => d.is_local)},
    {title:'已绑定 · 统一工作区',devices:devices.filter(d => !d.is_local && d.bound)},
    {title:'发现的设备',devices:devices.filter(d => !d.is_local && !d.bound && d.online)}
  ]
  const markup = groups.filter(g => g.devices.length).map(g => `<section class="device-group"><h3 class="device-group-title">${g.title}<span>${g.devices.length}</span></h3>${g.devices.map(d => {
    const addresses = deviceAddresses(d)
    // For a local card the address actually opened by this client is the
    // current connection; the first enumerated adapter need not be in use.
    const browserAddress = safeDeviceURL(location.origin)
    const primary = (d.is_local && addresses.includes(browserAddress) ? browserAddress : safeDeviceURL(d.url)) || addresses[0] || ''
    const shortAddress = primary ? new URL(primary).host : '暂无可用地址'
    const displayName = deviceDisplayName(d)
    const note = d.connection_error || (d.is_local ? '本机内容与绑定设备的索引统一展示' : d.bound ? '只展示索引；使用文件时按需读取' : '发现设备不会授予访问权限；绑定后只合并索引')
    return `<div class="device device-compact" data-device-id="${escapeHTML(d.id)}">
      <div class="device-heading"><h3 title="${escapeHTML(displayName)}">${escapeHTML(displayName)}</h3>${(d.is_local || d.bound) ? '<button type="button" class="text-button rename-device" title="设置设备别名">改名</button>' : ''}<span class="device-state ${d.online?'':'offline'}">${d.is_local?'本机':d.online?'在线':escapeHTML(d.status||'不可达')}</span></div>
      <div class="device-primary"><span class="device-route">${primary ? deviceRouteLabel(primary) : '未连接'}</span><code class="device-primary-url" title="${escapeHTML(primary)}">${escapeHTML(shortAddress)}</code></div>
      <p class="device-note">${escapeHTML(note)}</p>
      <details class="device-connections" data-connections-for="${escapeHTML(d.id)}"><summary>连接详情<span>${addresses.length} 个地址</span></summary>
        <div class="device-addresses">${addresses.map(address => `<div class="device-address"><span>${deviceRouteLabel(address)}${address===primary ? (d.online?' · 当前连接':' · 上次连接'):' · 备用'}</span><code class="device-url">${escapeHTML(address)}</code></div>`).join('')}</div>
      </details>
      ${d.is_local?'':d.bound?'<button class="button ghost small unbind-device">解除绑定</button>':`<button class="button primary small bind-device" data-url="${escapeHTML(primary)}">绑定并合并</button>`}
    </div>`
  }).join('')}</section>`).join('')
  const list = $('#deviceList')
  if (list._deviceMarkup !== markup) {
    const expanded = new Set([...(list.querySelectorAll?.('.device-connections[open]') || [])].map(d => d.dataset.connectionsFor))
    list.innerHTML = markup; list._deviceMarkup = markup
    for (const details of list.querySelectorAll?.('.device-connections') || []) details.open = expanded.has(details.dataset.connectionsFor)
  }
  const status = $('#tailscaleStatus'); if (status) status.textContent = state.tailscaleStatus || '局域网自动发现已启用；Tailscale 状态正在检测'
}

const TAB_CONFIG = {
  dashboard: { panel: '#dashboardPanel', title: '工作台' },
  notes: { panel: '#notesPanel', title: '随手记', load: loadNotes },
  files: { panel: '#filesPanel', title: '文件', load: loadFiles },
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
  if (state.folders[tab]) void loadFolders(tab)
}

async function exportBackup() {
  // Backups can take minutes. Passing our own signal keeps the request out of
  // the runtime bridge's 6 s abort watchdog; it only ends on network failure.
  const password = window.prompt('设置备份口令（留空则导出未加密 ZIP；加密包只能用同一口令恢复）', '')
  if (password === null) return
  const controller = new AbortController()
  const suggested = `omnishare-backup-${new Date().toISOString().slice(0, 10)}${password ? '.osb' : '.zip'}`
  await withButtonBusy($('#backupBtn'), '导出中…', async () => {
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
      toast(disposition.includes('.osb') || password ? '加密备份已导出并开始下载，请查看“下载”目录' : '备份已导出并开始下载，请查看“下载”目录', false, 4000)
      setTimeout(() => URL.revokeObjectURL(a.href), 1000)
    } catch (err) {
      // Abort even when rejecting the response by Content-Length or failing to
      // open/write the destination, so its body does not keep downloading.
      controller.abort()
      if (err.name !== 'AbortError') toast(err.message, true)
    }
  })
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
  const auth = captureAuthContext()
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
      showAuth(auth)
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
  $('#deviceAliasForm')?.addEventListener('submit', e => { e.preventDefault(); void submitDeviceAlias() })
  $('#cancelDeviceAliasBtn')?.addEventListener('click', () => $('#deviceAliasDialog')?.close())
  $('#deviceList').addEventListener('click', e => {
    const rename = e.target.closest('.rename-device')
    if (rename) { openDeviceAliasDialog(rename.closest('[data-device-id]').dataset.deviceId); return }
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
  $('#dashNewNoteBtn')?.addEventListener('click', () => { switchTab('notes'); openCreateNoteModal() })
  $('#dashUploadFileBtn')?.addEventListener('click', () => { switchTab('files'); openUploadFileModal() })
  $('#dashNewPadBtn')?.addEventListener('click', () => { switchTab('pads'); void createPad() })
  $('#dashBindBtn')?.addEventListener('click', () => openBindDialog())
  $('#openCreateNoteBtn')?.addEventListener('click', openCreateNoteModal)
  $('#cancelCreateNoteBtn')?.addEventListener('click', () => $('#createNoteDialog')?.close())
  $('#closeCreateNoteBtn')?.addEventListener('click', () => $('#createNoteDialog')?.close())
  $('#createNoteForm')?.addEventListener('submit', e => { e.preventDefault(); void createNote() })
  $('#createNoteBtn').addEventListener('click', () => void createNote())
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
    const pathBtn = e.target.closest('.md-path')
    if (pathBtn) {
      await copyText(pathBtn.dataset.copyPath || '')
      toast('路径已复制到剪贴板')
      return
    }
    const chip = e.target.closest('.folder-chip')
    if (chip) { selectFolder('notes', chip.dataset.openFolder); return }
    const card = e.target.closest('[data-note-id]')
    if (!card) return
    const id = card.dataset.noteId
    const n = state.notes.find(x => x.id === id)
    const toggleFmt = e.target.closest('.toggle-note-format')
    if (toggleFmt) {
      const next = toggleFmt.dataset.format === 'rich' ? 'plain' : 'rich'
      state.noteFormats.set(id, next)
      renderNotes()
      return
    }
    const readBtn = e.target.closest('.read-note')
    const copyBtn = e.target.closest('.copy-note')
    const rawBtn = e.target.closest('.raw-note')
    if (readBtn) await readRestrictedNote(id, false, readBtn)
    else if (copyBtn) await readRestrictedNote(id, true, copyBtn)
    else if (e.target.closest('.share-note')) openShare('note', id, n.content_redacted ? '受限随手记' : n.content.slice(0, 80))
    else if (e.target.closest('.move-note')) openMoveItemModal('notes', n)
    else if (e.target.closest('.edit-note')) await openEditNote(id)
    else if (rawBtn) await openRawNote(id, rawBtn)
    else if (e.target.closest('.delete-note')) deleteNote(id)
  })
  $('#saveEditNoteBtn').addEventListener('click', saveEditNote)

  $('#openUploadFileBtn')?.addEventListener('click', openUploadFileModal)
  $('#cancelUploadFileBtn')?.addEventListener('click', () => $('#uploadFileDialog')?.close())
  $('#closeUploadFileBtn')?.addEventListener('click', () => $('#uploadFileDialog')?.close())
  $('#chooseFilesBtn').addEventListener('click', e => {
    e.stopPropagation()
    $('#fileInput').click()
  })
  $('#chooseFolderBtn')?.addEventListener('click', e => {
    e.stopPropagation()
    $('#folderInput')?.click()
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
  $('#folderInput')?.addEventListener('change', e => {
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
  $('#dropZone').addEventListener('drop', async e => {
    e.stopPropagation()
    const files = await collectDroppedFiles(e.dataTransfer)
    uploadFiles(files)
  })
  ;['dragenter', 'dragover'].forEach(type => $('#filesPanel')?.addEventListener(type, e => {
    if (e.target.closest('#dropZone')) return
    e.preventDefault()
    $('#filesPanel').classList.add('dragging')
  }))
  ;['dragleave', 'drop'].forEach(type => $('#filesPanel')?.addEventListener(type, e => {
    if (e.target.closest('#dropZone')) return
    e.preventDefault()
    $('#filesPanel').classList.remove('dragging')
  }))
  $('#filesPanel')?.addEventListener('drop', async e => {
    if (e.target.closest('#dropZone')) return
    const files = await collectDroppedFiles(e.dataTransfer)
    uploadFiles(files)
  })
  $('#clearFinishedUploadsBtn').addEventListener('click', clearFinishedUploads)
  $('#cancelAllUploadsBtn').addEventListener('click', cancelAllUploads)
  updateUploadSummary()

  $('#fileSearch').addEventListener('input', debounce(() => loadFiles()))
  $('#fileType').addEventListener('change', () => loadFiles())
  $('#filesList').addEventListener('click', e => {
    const chip = e.target.closest('.folder-chip')
    if (chip) { selectFolder('files', chip.dataset.openFolder); return }
    const card = e.target.closest('[data-file-id]')
    if (!card) return
    const f = state.files.find(x => x.id === card.dataset.fileId)
    const dlBtn = e.target.closest('.download-file')
    const playBtn = e.target.closest('.play-file')
    if (dlBtn) void openDownload(f, dlBtn)
    if (playBtn) void playFile(f, playBtn)
    if (e.target.closest('.share-file')) openShare('file', f.id, f.file_name)
    if (e.target.closest('.move-file')) openMoveItemModal('files', f)
    if (e.target.closest('.rename-file')) renameFile(f)
    if (e.target.closest('.delete-file')) deleteFile(f.id)
  })

  $('#newPadBtn').addEventListener('click', createPad)
  $('#backToPadsListBtn')?.addEventListener('click', () => void closePadEditor())
  $('#padSearch').addEventListener('input', debounce(() => loadPads()))
  $('#padsList').addEventListener('click', e => {
    const item = e.target.closest('[data-pad-id]')
    if (item) void selectPad(item.dataset.padId)
  })
  $('#padTitle').addEventListener('input', markPadDirty)
  $('#padFolder')?.addEventListener('input', markPadDirty)
  $('#padContent').addEventListener('input', markPadDirty)

  // 目录侧栏与工具栏交互（随手记、文件、协同文档复用）
  document.addEventListener('click', e => {
    const createBtn = e.target.closest('.create-folder-btn')
    if (createBtn) {
      openCreateFolderModal(createBtn.dataset.folderKind)
      return
    }
    const container = e.target.closest('[data-folder-kind]')
    const kind = container?.dataset?.folderKind
    if (!kind || !state.folders[kind]) return
    const toggleBtn = e.target.closest('[data-toggle-folder]')
    if (toggleBtn) {
      const p = toggleBtn.dataset.toggleFolder
      if (state.folders[kind].collapsed.has(p)) state.folders[kind].collapsed.delete(p)
      else state.folders[kind].collapsed.add(p)
      renderFolderUI(kind)
      return
    }
    const selBtn = e.target.closest('[data-folder]')
    if (selBtn) {
      selectFolder(kind, selBtn.dataset.folder)
      return
    }
    const subBtn = e.target.closest('.create-sub-folder-btn')
    if (subBtn) {
      openCreateFolderModal(kind, subBtn.dataset.parentFolder)
      return
    }
    const renBtn = e.target.closest('.rename-folder-btn')
    if (renBtn) {
      openRenameFolderModal(kind, renBtn.dataset.folderPath)
      return
    }
    const delBtn = e.target.closest('.delete-folder-btn')
    if (delBtn) {
      openDeleteFolderModal(kind, delBtn.dataset.folderPath)
    }
  })
  document.addEventListener('change', e => {
    const rec = e.target.closest('.folder-recursive-input')
    if (!rec) return
    const kind = e.target.closest('[data-folder-kind]')?.dataset?.folderKind
    if (!kind || !state.folders[kind]) return
    state.folders[kind].recursive = !!rec.checked
    void FOLDER_LOADERS[kind]?.()
  })
  $('#folderModalForm')?.addEventListener('submit', e => { e.preventDefault(); void submitFolderModal() })
  $('#cancelFolderModalBtn')?.addEventListener('click', () => $('#folderModalDialog')?.close())
  $('#deleteFolderForm')?.addEventListener('submit', e => { e.preventDefault(); void submitDeleteFolderModal() })
  $('#cancelDeleteFolderBtn')?.addEventListener('click', () => $('#deleteFolderDialog')?.close())
  $('#renameFileForm')?.addEventListener('submit', e => { e.preventDefault(); void submitRenameFile() })
  $('#cancelRenameFileBtn')?.addEventListener('click', () => $('#renameFileDialog')?.close())
  $('#moveItemForm')?.addEventListener('submit', e => { e.preventDefault(); void submitMoveItemModal() })
  $('#cancelMoveItemBtn')?.addEventListener('click', () => $('#moveItemDialog')?.close())
  $('#padContent').addEventListener('keydown', e => {
    if ((e.ctrlKey || e.metaKey) && e.key === 's') {
      e.preventDefault()
      savePad(true)
    }
  })
  $('#togglePadPreviewBtn')?.addEventListener('click', () => applyPadPreviewMode(!state.padPreviewMode))
  $('#padPreview')?.addEventListener('click', async e => {
    const pathBtn = e.target.closest('.md-path')
    if (pathBtn) {
      await copyText(pathBtn.dataset.copyPath || '')
      toast('路径已复制到剪贴板')
    }
  })
  $('#savePadBtn').addEventListener('click', () => savePad(true))
  $('#sharePadBtn').addEventListener('click', () => {
    if (state.currentPad) openShare('pad', state.currentPad.id, state.currentPad.title)
  })
  $('#deletePadBtn').addEventListener('click', deletePad)

  $('#shareForm').addEventListener('submit', e => { e.preventDefault(); void createShare() })
  $('#shareForm').addEventListener('click', e => {
    if (e.target.closest('[data-close-share]')) $('#shareDialog').close()
  })
  $('#copyShareBtn').addEventListener('click', copyCreatedShareLink)
  $('#refreshSharesBtn').addEventListener('click', () => loadShares(false, true))
  $('#sharesList').addEventListener('click', e => {
    const card = e.target.closest('[data-share-id]')
    if (!card) return
    const link = state.shares.find(x => x.id === card.dataset.shareId)
    const resolved = shareAccessibleURL(link)
    if (e.target.closest('.copy-share')) {
      copyShareLink(resolved.url)
        .then(() => toast(resolved.loopbackOnly ? '已复制链接（仅来源设备可用）' : '分享链接已复制', resolved.loopbackOnly))
        .catch(() => toast('剪贴板不可用，请选中卡片中的链接手动复制', true))
    }
    if (e.target.closest('.open-share')) { try { const url = new URL(resolved.url, location.href); if (!['http:','https:'].includes(url.protocol) || url.username || url.password) throw new Error('分享地址无效'); window.open(url.href, '_blank', 'noopener'); toast('已在新窗口打开分享链接') } catch (err) { toast(err.message, true) } }
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

  $('#refreshAuditBtn').addEventListener('click', () => loadAudit(true))
  $('#refreshDevicesBtn').addEventListener('click', () => loadDevices(false, true))
  $('#cfgAccessKeyEnabled').addEventListener('change', updateAccessKeyControls)
  $('#cfgAllowLAN').addEventListener('change', () => {
    $('#cfgListenAddress').value = $('#cfgAllowLAN').checked ? '0.0.0.0' : '127.0.0.1'
  })
  $('#settingsBtn').addEventListener('click', () => {
    fillSettings()
    $('#settingsDialog').showModal()
    $('#cfgNodeName')?.focus({ preventScroll: true })
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
  ;['notes', 'files', 'pads'].forEach(renderFolderUI)
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
  // A background tab must not keep polling the local server on a timer, and a
  // window nobody is looking at (behind other apps, or hidden to the tray where
  // WebView2 can still report the document as visible) polls far less often.
  const workspacePoller = createPoller(refreshWorkspace, 2000, 15000)
  const devicePoller = createPoller(() => loadDevices(), 5000, 30000)
  const healthPoller = createPoller(() => loadHealth(), 15000, 60000)
  document.addEventListener('visibilitychange', () => { if (!document.hidden) { devicePoller.kick(); workspacePoller.kick() } })
  window.addEventListener('focus', () => workspacePoller.kick())
  workspacePoller.start()
  devicePoller.start()
  healthPoller.start()
}

// Self-scheduling poll loop: the next run is planned only after the previous
// one settled (no stacking on slow links), at a cadence that depends on focus.
function createPoller(task, focusedMs, backgroundMs) {
  let timer = 0
  const delay = () => (typeof document.hasFocus === 'function' && !document.hasFocus()) ? backgroundMs : focusedMs
  const run = async () => {
    clearTimeout(timer); timer = 0
    if (!document.hidden) { try { await task() } catch {} }
    if (!timer) timer = setTimeout(run, delay())
  }
  return { start() { if (!timer) timer = setTimeout(run, delay()) }, kick() { return run() } }
}

start()
