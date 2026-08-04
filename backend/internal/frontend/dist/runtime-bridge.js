(() => {
  'use strict'

  const nativeFetch = window.fetch.bind(window)
  const retryDelays = [250, 900]
  const connectionMessages = /Failed to fetch|后端服务暂不可用|核心服务加载失败|连接失败|网络请求失败/i
  let offline = false
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
    setTimeout(() => location.reload(), 350)
  }

  function scheduleRecovery() {
    if (!offline || recoveryTimer) return
    const delay = Math.min(30000, 1500 * (2 ** Math.min(recoveryAttempt, 4)))
    recoveryTimer = window.setTimeout(async () => {
      recoveryTimer = 0
      recoveryAttempt += 1
      try {
        const response = await nativeFetch('/api/v1/health', { cache: 'no-store' })
        if (response.ok) {
          const payload = await response.json()
          if (payload?.code === 0) {
            markOnline()
            return
          }
        }
      } catch {
        // The watchdog or external service may still be recovering.
      }
      scheduleRecovery()
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

  function collapseDevices(devices) {
    if (!Array.isArray(devices)) return devices

    const local = devices
      .filter(device => device?.is_local && !isLinkLocal(deviceIP(device)))
      .sort((a, b) => localDeviceScore(b) - localDeviceScore(a) || String(a.url).localeCompare(String(b.url)))
    const remote = devices.filter(device => !device?.is_local)
    const output = []
    const seen = new Set()

    if (local.length) {
      const primary = { ...local[0], id: 'local-primary', network_type: '本机' }
      primary.address_count = local.length
      output.push(primary)
      seen.add(`url:${String(primary.url || '').toLowerCase().replace(/\/$/, '')}`)
    }

    for (const device of remote) {
      const normalizedURL = String(device?.url || '').toLowerCase().replace(/\/$/, '')
      const key = normalizedURL ? `url:${normalizedURL}` : `id:${device?.id || ''}`
      if (seen.has(key)) continue
      seen.add(key)
      output.push(device)
    }

    return output
  }

  async function transformDevicesResponse(response, info) {
    if (!response.ok || info.url?.pathname !== '/api/v1/devices') return response
    try {
      const payload = await response.clone().json()
      if (!Array.isArray(payload?.data)) return response
      payload.data = collapseDevices(payload.data)
      const headers = new Headers(response.headers)
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

  window.fetch = async function guardedFetch(input, init = {}) {
    const info = requestInfo(input, init)
    if (!info.guarded) return nativeFetch(input, init)

    const retries = info.method === 'GET' || info.method === 'HEAD' ? retryDelays.length : 0
    let lastError

    for (let attempt = 0; attempt <= retries; attempt += 1) {
      let timeout = 0
      let options = init
      try {
        if (!init.signal) {
          const controller = new AbortController()
          timeout = window.setTimeout(() => controller.abort(), 6000)
          options = { ...init, signal: controller.signal }
        }
        const response = await nativeFetch(input, options)
        if (timeout) clearTimeout(timeout)
        markOnline()
        return transformDevicesResponse(response, info)
      } catch (error) {
        if (timeout) clearTimeout(timeout)
        lastError = error
        if (attempt < retries) await sleep(retryDelays[attempt])
      }
    }

    markOffline()
    const error = new Error('后端服务暂不可用，桌面端正在自动重连')
    error.name = 'OmniShareBackendUnavailable'
    error.cause = lastError
    throw error
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

  window.__omnishareRuntimeBridge = {
    collapseDevices,
    isOffline: () => offline
  }
})()
