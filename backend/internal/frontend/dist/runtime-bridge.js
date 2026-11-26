(() => {
  'use strict'

  const nativeFetch = window.fetch.bind(window)
  const retryDelays = [250, 900]
  const connectionMessages = /Failed to fetch|后端服务暂不可用|核心服务加载失败|连接失败|网络请求失败/i
  let offline = false
  let healthProbe = null
  let recoveryTimer = 0
  let recoveryAttempt = 0
  let lastConnectionToast = 0
  let normalizingToast = false

  const sleep = delay => new Promise(resolve => setTimeout(resolve, delay))

  function requestInfo(input, init = {}) {
    try {
      const raw = input instanceof Request ? input.url : input
      const url = new URL(raw, location.href)
      const method = String(init.method || (input instanceof Request ? input.method : 'GET')).toUpperCase()
      return {
        url,
        method,
        guarded: url.origin === location.origin && url.pathname.startsWith('/api/')
      }
    } catch {
      return { guarded: false, method: 'GET', url: null }
    }
  }

  function updateBadge(text) {
    const badge = document.querySelector('#healthBadge')
    if (!badge) return
    badge.className = 'badge muted'
    badge.innerHTML = `<i></i>${text}`
  }

  function markOffline() {
    if (!offline) {
      offline = true
      recoveryAttempt = 0
      document.dispatchEvent(new CustomEvent('omnishare:backend-offline'))
    }
    updateBadge('后端重连中')
    scheduleRecovery()
  }

  function markOnline() {
    if (!offline) return
    offline = false
    recoveryAttempt = 0
    if (recoveryTimer) clearTimeout(recoveryTimer)
    recoveryTimer = 0
    updateBadge('连接已恢复')
    document.dispatchEvent(new CustomEvent('omnishare:backend-online'))
  }

  function backendResponds() {
    if (healthProbe) return healthProbe
    const controller = new AbortController()
    const timer = window.setTimeout(() => controller.abort(), 3000)
    healthProbe = nativeFetch('/api/v1/health', {cache:'no-store', signal:controller.signal})
      .then(async response => response.ok && (await response.json())?.code === 0)
      .catch(() => false)
      .finally(() => { clearTimeout(timer); healthProbe = null })
    return healthProbe
  }

  function scheduleRecovery() {
    if (!offline || recoveryTimer) return
    const delay = Math.min(30000, 1500 * (2 ** Math.min(recoveryAttempt, 4)))
    recoveryTimer = window.setTimeout(async () => {
      recoveryTimer = 0
      recoveryAttempt += 1
      if (await backendResponds()) markOnline()
      else scheduleRecovery()
    }, delay)
  }

  function isLinkLocal(value) {
    const ip = String(value || '').toLowerCase()
    return ip.startsWith('169.254.') || ip.startsWith('fe80:')
  }

  function isPrivateIPv4(value) {
    const parts = String(value || '').split('.').map(Number)
    if (parts.length !== 4 || parts.some(part => !Number.isInteger(part) || part < 0 || part > 255)) return false
    return parts[0] === 10 || (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) || (parts[0] === 192 && parts[1] === 168)
  }

  function deviceIP(device) {
    if (device?.ip) return String(device.ip)
    try { return new URL(device?.url || '', location.href).hostname.replace(/^\[|\]$/g, '') } catch { return '' }
  }

  function localDeviceScore(device) {
    const ip = deviceIP(device)
    if (ip === location.hostname) return 1000
    if (isLinkLocal(ip)) return -1
    if (isPrivateIPv4(ip)) return 700
    if (device?.network_type === 'tailscale') return 600
    if (ip === '127.0.0.1' || ip === '::1') return 200
    return ip.includes(':') ? 400 : 500
  }

  function deviceOrigin(value) {
    if (typeof value !== 'string' || !value.trim()) return ''
    try {
      const url = new URL(value)
      if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password ||
          url.search || url.hash || url.pathname !== '/') return ''
      return `${url.origin}/`
    } catch { return '' }
  }

  function deviceRoutes(device) {
    const addresses = Array.isArray(device.addresses) ? device.addresses : []
    return [...new Set([device.url, ...addresses].map(deviceOrigin).filter(Boolean))]
  }

  function collapseDevices(devices) {
    if (!Array.isArray(devices)) return devices

    // This bridge runs BEFORE app.js. Never discard a duplicate row without
    // merging its routes: LAN and Tailscale are paths to one identity, not
    // separate content stores. Names and shared URLs are not stable identities.
    const grouped = new Map()
    for (const [index, device] of devices.entries()) {
      if (!device || typeof device !== 'object' || Array.isArray(device)) continue
      const id = typeof device.id === 'string' ? device.id.trim() : ''
      const legacyLocal = device.is_local && !id.startsWith('node-')
      const key = legacyLocal ? 'local-primary' : id ? `id:${id}` :
        deviceOrigin(device.url) ? `url:${deviceOrigin(device.url)}` : `missing:${index}`
      const previous = grouped.get(key)
      const priority = value => Number(!!value.is_local) * 4 + Number(!!value.bound) * 2 + Number(!!value.online)
      const preferNew = !previous || priority(device) > priority(previous) ||
        (priority(device) === priority(previous) && device.is_local &&
          localDeviceScore(device) > localDeviceScore(previous))
      const preferred = preferNew ? device : previous
      const addresses = [...new Set([
        ...deviceRoutes(preferred), ...(previous ? deviceRoutes(previous) : []), ...deviceRoutes(device)
      ])]
      const primary = { ...preferred, addresses, address_count: addresses.length }
      if (primary.is_local) {
        primary.id = primary.id || 'local-primary'
        primary.network_type = '本机'
        // The current browser route is the best proven local connection, but
        // only select it if the server actually advertised that route.
        const current = deviceOrigin(location.origin)
        primary.url = addresses.includes(current) ? current : deviceOrigin(preferred.url) || addresses[0] || ''
      } else {
        primary.url = deviceOrigin(preferred.url) || addresses[0] || ''
      }
      grouped.set(key, primary)
    }
    return [...grouped.values()]
  }

  async function transformDevicesResponse(response, info) {
    if (!response.ok || info.url?.pathname !== '/api/v1/devices') return response
    try {
      const payload = await response.clone().json()
      if (!Array.isArray(payload?.data)) return response
      payload.data = collapseDevices(payload.data)
      const headers = new Headers(response.headers)
      // The rewritten JSON no longer has the original wire length/encoding.
      headers.delete('content-length')
      headers.delete('content-encoding')
      headers.set('content-type', 'application/json; charset=utf-8')
      return new Response(JSON.stringify(payload), {
        status: response.status,
        statusText: response.statusText,
        headers
      })
    } catch {
      return response
    }
  }

  function requestBudget({method, url}) {
    // A shortcut waits for the native dialog and hashes the chosen file.
    // A directory selection walks the disk before returning its entries.
    if (method === 'POST' && ['/api/v1/local-files/shortcuts', '/api/v1/local-files/selection'].includes(url.pathname)) return 0
    if (url.pathname === '/api/v1/peers/bind') return 25000
    if (url.searchParams.get('scope') === 'all') return 18000
    if (url.pathname === '/api/v1/devices') return 12000
    return 8000
  }

  async function fetchAttempt(input, init, budget) {
    if (!budget || init.signal) return nativeFetch(input, init)
    const controller = new AbortController()
    const inherited = input instanceof Request ? input.signal : null
    const cancel = () => controller.abort(inherited.reason)
    if (inherited?.aborted) cancel()
    else inherited?.addEventListener('abort', cancel, {once:true})
    const timer = window.setTimeout(() => controller.abort(), budget)
    try {
      return await nativeFetch(input, {...init, signal:controller.signal})
    } finally {
      clearTimeout(timer)
      inherited?.removeEventListener('abort', cancel)
    }
  }

  window.fetch = async function guardedFetch(input, init = {}) {
    const info = requestInfo(input, init)
    if (!info.guarded) return nativeFetch(input, init)

    const retries = info.method === 'GET' || info.method === 'HEAD' ? retryDelays.length : 0
    const signal = init.signal || (input instanceof Request ? input.signal : null)
    const budget = requestBudget(info)
    let lastError

    for (let attempt = 0; attempt <= retries; attempt += 1) {
      try {
        const response = await fetchAttempt(input, init, budget)
        markOnline()
        return transformDevicesResponse(response, info)
      } catch (error) {
        if (signal?.aborted) throw error
        lastError = error
        if (attempt < retries) await sleep(retryDelays[attempt])
      }
    }

    if (info.url.pathname !== '/api/v1/health' && await backendResponds()) {
      markOnline()
      throw new Error('未收到操作结果，请刷新后确认', {cause:lastError})
    }
    markOffline()
    const error = new Error('后端服务暂不可用，桌面端正在自动重连')
    error.name = 'OmniShareBackendUnavailable'
    error.cause = lastError
    throw error
  }

  function bridgeToast(message, isError = false) {
    const el = document.querySelector('#toast')
    if (!el) return
    el.textContent = message
    el.className = `toast show${isError ? ' error' : ''}`
    clearTimeout(el._timer)
    el._timer = setTimeout(() => { el.className = 'toast' }, 3200)
  }

  function parseSemver(raw) {
    const m = String(raw || '').trim().replace(/^v/, '').match(/^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?/)
    if (!m) return null
    return {
      major: Number(m[1]),
      minor: Number(m[2]),
      patch: Number(m[3]),
      pre: m[4] ? m[4].split('.').map(p => (/^\d+$/.test(p) ? Number(p) : p)) : []
    }
  }

  function compareSemver(a, b) {
    for (const k of ['major', 'minor', 'patch']) {
      if (a[k] !== b[k]) return a[k] - b[k]
    }
    if (!a.pre.length && !b.pre.length) return 0
    if (!a.pre.length) return 1
    if (!b.pre.length) return -1
    const len = Math.max(a.pre.length, b.pre.length)
    for (let i = 0; i < len; i++) {
      if (i >= a.pre.length) return -1
      if (i >= b.pre.length) return 1
      const x = a.pre[i], y = b.pre[i]
      if (x === y) continue
      if (typeof x === 'number' && typeof y === 'number') return x - y
      if (typeof x === 'number') return -1
      if (typeof y === 'number') return 1
      return String(x) < String(y) ? -1 : 1
    }
    return 0
  }

  function pickPlatformAsset(assets) {
    const ua = String((typeof navigator !== 'undefined' ? navigator.userAgent : '') || '').toLowerCase()
    const suffixes = ua.includes('mac')
      ? ['-macos-universal.dmg']
      : ua.includes('win')
        ? ['-windows-x64-setup.exe', '-windows-x64.msi', '-windows-amd64-portable.zip']
        : ['-linux-amd64.AppImage', '-ubuntu-amd64.deb', '-linux-amd64-portable.tar.gz']
    for (const suffix of suffixes) {
      const hit = (assets || []).find(a => typeof a?.name === 'string' && a.name.endsWith(suffix))
      if (hit) return hit
    }
    return (assets || []).find(a => typeof a?.name === 'string' && /\.(exe|msi|dmg|deb|AppImage|zip|tar\.gz)$/i.test(a.name)) || null
  }

  function setUpdateStatus(panel, text, action = null) {
    let box = panel.querySelector('[data-update-status]')
    if (!box) {
      box = document.createElement('div')
      box.dataset.updateStatus = ''
      box.setAttribute('role', 'status')
      box.style.marginTop = '8px'
      panel.appendChild(box)
    }
    box.replaceChildren()
    const p = document.createElement('p')
    p.className = 'muted-text'
    p.textContent = text
    box.appendChild(p)
    if (action) box.appendChild(action)
  }

  function installUpdateEntry() {
    const invoke = window.__TAURI__?.core?.invoke
    if (invoke || window.__TAURI_INTERNALS__) {
      document.documentElement.dataset.desktop = '1'
    }
    const panel = document.querySelector('#desktopUpdates')
    const button = document.querySelector('#checkUpdatesBtn')
    const quitBtn = document.querySelector('#quitDesktopBtn')
    if (!panel || !button) return
    panel.classList.remove('hidden')
    if (invoke) {
      if (quitBtn) {
        // Windows has no native menu bar; keep the familiar Ctrl+Q shortcut.
        document.addEventListener('keydown', event => {
          if (event.ctrlKey && !event.altKey && !event.shiftKey && (event.key === 'q' || event.key === 'Q')) {
            event.preventDefault()
            void invoke('quit_desktop').catch(() => {})
          }
        })
        quitBtn.addEventListener('click', async () => {
          quitBtn.disabled = true
          quitBtn.textContent = '正在退出…'
          try {
            await invoke('quit_desktop')
          } catch (error) {
            quitBtn.disabled = false
            quitBtn.textContent = '退出应用'
            bridgeToast(String(error?.message || error || '退出应用失败'), true)
          }
        })
      }
      const badge = document.querySelector('#healthBadge')
      if (badge) {
        badge.style.cursor = 'pointer'
        badge.addEventListener('click', () => { void invoke('open_updates').catch(() => {}) })
      }
      button.addEventListener('click', async () => {
        button.disabled = true
        button.textContent = '正在打开…'
        const previousError = panel.querySelector('[data-update-error]')
        if (previousError) previousError.remove()
        try {
          await invoke('open_updates')
          button.textContent = '检查更新'
          bridgeToast('已打开软件更新窗口')
        } catch (error) {
          button.textContent = '无法打开更新窗口，请从桌面菜单重试'
          // Keep failures visible as text; never inject a native error as HTML.
          let status = panel.querySelector('[data-update-error]')
          if (!status) {
            status = document.createElement('p')
            status.dataset.updateError = ''
            status.setAttribute('role', 'status')
            panel.appendChild(status)
          }
          status.textContent = String(error?.message || error || '更新窗口打开失败').slice(0, 500)
          bridgeToast(status.textContent, true)
          console.error(error)
        } finally {
          button.disabled = false
        }
      })
      return
    }
    button.addEventListener('click', async () => {
      button.disabled = true
      button.textContent = '检查中…'
      setUpdateStatus(panel, '正在检查新版本…')
      try {
        const healthRes = await nativeFetch('/api/v1/health', { cache: 'no-store' })
        const healthData = healthRes.ok ? (await healthRes.json())?.data : null
        const currentStr = String(healthData?.version || '').trim()
        const current = parseSemver(currentStr)
        const allowPreview = !currentStr || currentStr.includes('-')
        const relRes = await nativeFetch('https://api.github.com/repos/Azurefly/omnishare/releases?per_page=20', { cache: 'no-store' })
        if (!relRes.ok) throw new Error(`检查更新失败（HTTP ${relRes.status}）`)
        const releases = await relRes.json()
        let best = null
        for (const rel of releases || []) {
          if (!rel || rel.draft) continue
          const v = parseSemver(rel.tag_name)
          if (!v || (!allowPreview && (rel.prerelease || v.pre.length))) continue
          if (current && compareSemver(v, current) <= 0) continue
          if (!best || compareSemver(v, best.v) > 0) best = { rel, v }
        }
        if (!best) {
          const msg = `当前已是最新版本${currentStr ? ' v' + currentStr : ''}`
          setUpdateStatus(panel, msg)
          bridgeToast(msg)
          return
        }
        const tag = best.rel.tag_name
        const asset = pickPlatformAsset(best.rel.assets)
        if (asset && /^https:\/\/github\.com\/Azurefly\/omnishare\/releases\/download\//.test(asset.browser_download_url || '')) {
          const link = document.createElement('a')
          link.className = 'button primary small'
          link.href = asset.browser_download_url
          link.download = asset.name
          link.rel = 'noopener'
          link.textContent = `立即下载升级 ${tag}（${asset.name}）`
          link.addEventListener('click', () => {
            bridgeToast(`已开始下载更新包 ${asset.name}，下载完成后运行安装即可升级`)
          })
          setUpdateStatus(panel, `发现新版本 ${tag}（当前 v${currentStr || '未知'}），点击下方按钮直接下载安装：`, link)
        } else {
          const link = document.createElement('a')
          link.className = 'button primary small'
          link.href = best.rel.html_url || 'https://github.com/Azurefly/omnishare/releases'
          link.target = '_blank'
          link.rel = 'noopener'
          link.textContent = `前往下载 ${tag}`
          setUpdateStatus(panel, `发现新版本 ${tag}，请前往发布页下载对应平台安装包：`, link)
        }
        bridgeToast(`发现新版本 ${tag}`)
      } catch (error) {
        const msg = String(error?.message || error || '检查更新失败')
        setUpdateStatus(panel, msg)
        bridgeToast(msg, true)
      } finally {
        button.disabled = false
        button.textContent = '检查更新'
      }
    })
  }

  function installToastThrottle() {
    const toast = document.querySelector('#toast')
    if (!toast) return
    const observer = new MutationObserver(() => {
      if (normalizingToast) {
        normalizingToast = false
        return
      }
      if (!toast.classList.contains('show') || !connectionMessages.test(toast.textContent || '')) return
      const now = Date.now()
      if (now - lastConnectionToast < 60000) {
        toast.classList.remove('show')
        return
      }
      lastConnectionToast = now
      if (toast.textContent !== '后端连接中断，桌面端正在自动恢复') {
        normalizingToast = true
        toast.textContent = '后端连接中断，桌面端正在自动恢复'
      }
    })
    observer.observe(toast, { attributes: true, childList: true, subtree: true })
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', installToastThrottle, { once: true })
  } else {
    installToastThrottle()
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', installUpdateEntry, {once:true})
  else installUpdateEntry()

  window.__omnishareRuntimeBridge = {
    collapseDevices,
    isOffline: () => offline
  }
})()
