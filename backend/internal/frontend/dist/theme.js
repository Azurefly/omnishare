(() => {
  'use strict'

  const STORAGE_KEY = 'omnishare.theme'
  const VALID_THEMES = new Set(['light', 'dark', 'system'])
  const systemPreference = window.matchMedia('(prefers-color-scheme: light)')
  let preference = readPreference()

  function readPreference() {
    try {
      const saved = localStorage.getItem(STORAGE_KEY)
      return VALID_THEMES.has(saved) ? saved : 'system'
    } catch {
      return 'system'
    }
  }

  function resolvedTheme(value = preference) {
    if (value === 'system') return systemPreference.matches ? 'light' : 'dark'
    return value
  }

  function updateThemeColor(theme) {
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', theme === 'light' ? '#f4f7fb' : '#0b1020')
  }

  function updateControls() {
    const resolved = resolvedTheme()
    const toggle = document.querySelector('#themeToggle')
    if (toggle) {
      const switchToLight = resolved === 'dark'
      toggle.textContent = switchToLight ? '☀ 白天' : '☾ 黑夜'
      toggle.title = switchToLight ? '切换到浅色主题' : '切换到深色主题'
      toggle.setAttribute('aria-label', toggle.title)
      toggle.setAttribute('aria-pressed', String(resolved === 'dark'))
    }

    const select = document.querySelector('#cfgTheme')
    if (select && select.value !== preference) select.value = preference
  }

  function applyTheme(value, options = {}) {
    preference = VALID_THEMES.has(value) ? value : 'system'
    const resolved = resolvedTheme(preference)
    const root = document.documentElement
    root.dataset.theme = resolved
    root.dataset.themePreference = preference
    root.style.colorScheme = resolved
    updateThemeColor(resolved)

    if (options.persist !== false) {
      try { localStorage.setItem(STORAGE_KEY, preference) } catch { /* storage may be unavailable */ }
    }

    updateControls()
    document.dispatchEvent(new CustomEvent('omnishare:theme-change', {
      detail: { preference, resolved }
    }))
  }

  function toggleTheme() {
    applyTheme(resolvedTheme() === 'dark' ? 'light' : 'dark')
  }

  function bindControls() {
    const toggle = document.querySelector('#themeToggle')
    if (toggle && !toggle.dataset.themeBound) {
      toggle.dataset.themeBound = 'true'
      toggle.addEventListener('click', toggleTheme)
    }

    const select = document.querySelector('#cfgTheme')
    if (select && !select.dataset.themeBound) {
      select.dataset.themeBound = 'true'
      select.addEventListener('change', () => applyTheme(select.value))
    }
    updateControls()
  }

  const onSystemChange = () => {
    if (preference === 'system') applyTheme('system', { persist: false })
  }
  if (typeof systemPreference.addEventListener === 'function') {
    systemPreference.addEventListener('change', onSystemChange)
  } else if (typeof systemPreference.addListener === 'function') {
    systemPreference.addListener(onSystemChange)
  }

  window.OmniShareTheme = {
    getPreference: () => preference,
    getResolvedTheme: () => resolvedTheme(),
    setTheme: value => applyTheme(value),
    toggle: toggleTheme
  }

  applyTheme(preference, { persist: false })
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bindControls, { once: true })
  } else {
    bindControls()
  }
})()
