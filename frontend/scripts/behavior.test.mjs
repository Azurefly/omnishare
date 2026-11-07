import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

// Execute the application itself with browser boundary doubles. Suppress only
// automatic startup so each test can drive a specific user interaction.
const source = (await readFile(new URL('../web/app.js', import.meta.url), 'utf8'))
  .replace(/\nstart\(\)\s*$/, '\n')

function createApp(options = {}) {
  const elements = new Map()
  const stored = new Map()
  const downloads = []
  const blobs = new Map()
  const requests = []
  const replacedURLs = []
  const element = selector => {
    if (!elements.has(selector)) elements.set(selector, {
      value: '', textContent: '', innerHTML: '', className: '', disabled: false,
      checked: false, open: false, files: [],
      classList: { add() {}, remove() {}, toggle() {} },
      showModal() { this.open = true }, close() { this.open = false }, focus() {}
    })
    return elements.get(selector)
  }
  class BrowserURL extends URL {
    static createObjectURL(blob) {
      const url = `blob:backup-${blobs.size}`
      blobs.set(url, blob)
      return url
    }
    static revokeObjectURL(url) { blobs.delete(url) }
  }
  class Request {
    constructor() { this.upload = {}; this.headers = {}; requests.push(this) }
    open(method, url) { this.method = method; this.url = url }
    setRequestHeader(name, value) { this.headers[name] = value }
    send(body) { this.body = body }
  }
  const storage = options.storage || {
    getItem: key => stored.get(key) ?? null,
    setItem: (key, value) => stored.set(key, value),
    removeItem: key => stored.delete(key)
  }
  const context = vm.createContext({
    URL: BrowserURL, URLSearchParams, AbortController, Blob, FormData,
    location: new URL(options.url || 'http://localhost/'),
    history: { replaceState: (_state, _title, url) => replacedURLs.push(url) },
    document: {
      querySelector: element, querySelectorAll: () => [],
      createElement: () => ({ click() { downloads.push(this) } })
    },
    window: { prompt: () => '', ...options.window },
    XMLHttpRequest: Request,
    fetch: options.fetch || (async () => { throw new Error('Unexpected fetch') }),
    setTimeout: () => 1, clearTimeout() {},
    console
  })
  Object.defineProperty(context, 'sessionStorage', {
    get: options.storageGetter || (() => storage)
  })
  vm.runInContext(source, context, { filename: 'app.js' })
  const app = vm.runInContext(`({ state, authHeaders, sessionGet, sessionSet,
    sessionRemove, exportBackup, preservePadDraft, performPadSave,
    restoreBackup, openRestoreDialog })`, context)
  return { app, element, stored, downloads, blobs, requests, replacedURLs }
}

test('first-run credentials survive a denied storage accessor and leave the URL', () => {
  const { app, replacedURLs } = createApp({
    url: 'http://localhost/?view=pads&init_token=test-token#editor',
    storageGetter() { throw new DOMException('Storage denied', 'SecurityError') }
  })
  assert.equal(app.state.initToken, 'test-token')
  assert.equal(app.authHeaders()['X-OmniShare-Init-Token'], 'test-token')
  assert.deepEqual(replacedURLs, ['/?view=pads#editor'])
  app.sessionSet('omnishare.accessKey', 'page-only-key')
  assert.equal(app.sessionGet('omnishare.accessKey'), 'page-only-key')
  app.sessionRemove('omnishare.accessKey')
  assert.equal(app.sessionGet('omnishare.accessKey'), null)
})

test('failed storage writes and removals cannot revive an old credential', () => {
  const { app } = createApp({ storage: {
    getItem: () => 'old-key',
    setItem() { throw new DOMException('Full', 'QuotaExceededError') },
    removeItem() { throw new DOMException('Denied', 'SecurityError') }
  } })
  app.sessionSet('omnishare.accessKey', 'new-key')
  assert.equal(app.sessionGet('omnishare.accessKey'), 'new-key')
  app.sessionRemove('omnishare.accessKey')
  assert.equal(app.sessionGet('omnishare.accessKey'), null)
})

test('ordinary sessions persist the bootstrap token and drafts', () => {
  const { app, stored } = createApp({ url: 'http://localhost/?init_token=initial' })
  assert.equal(stored.get('omnishare.initToken'), 'initial')
  app.preservePadDraft({ id: 'other-pad', title: 'Draft', content: 'text', version: 2 })
  assert.equal(JSON.parse(stored.get('omnishare.padDraft.other-pad')).content, 'text')
})

test('a save conflict preserves the latest editor content and returns a failed save', async () => {
  const { app, element, stored } = createApp({
    fetch: async (_url, options) => options?.method === 'PUT'
      ? Response.json({ code: 1, message: 'Version conflict' }, { status: 409 })
      : Response.json({ code: 0, data: [] })
  })
  app.state.currentPad = { id: 'pad-1', version: 3 }
  app.state.currentPadDirty = true
  element('#padTitle').value = 'Current title'
  element('#padContent').value = 'New typing after autosave began'
  const saved = await app.performPadSave({ id: 'pad-1', title: 'Old', content: 'Old typing', version: 3 })
  assert.equal(saved, false)
  assert.equal(app.state.padSaveBlocked, true)
  assert.equal(element('#saveState').textContent, '版本冲突')
  assert.deepEqual(JSON.parse(stored.get('omnishare.padDraft.pad-1')), {
    id: 'pad-1', title: 'Current title', content: 'New typing after autosave began', version: 3
  })
})

test('a failed save still keeps its draft when storage is unavailable', async () => {
  const { app, element } = createApp({
    storageGetter() { throw new Error('Denied') },
    fetch: async () => { throw new Error('Offline') }
  })
  app.state.currentPad = { id: 'pad-2', version: 1 }
  app.state.currentPadDirty = true
  element('#padTitle').value = 'Offline draft'
  element('#padContent').value = 'Unsaved content'
  assert.equal(await app.performPadSave({ id: 'pad-2', version: 1 }), false)
  assert.equal(JSON.parse(app.sessionGet('omnishare.padDraft.pad-2')).content, 'Unsaved content')
  assert.equal(element('#saveState').textContent, '保存失败')
})

test('backup chooses its destination before requesting and streams exact bytes', async () => {
  const calls = []
  const chunks = []
  const { app } = createApp({
    window: { showSaveFilePicker: async options => {
      calls.push('picker')
      assert.match(options.suggestedName, /^omnishare-backup-.*\.zip$/)
      return { createWritable: async () => ({
        write: async chunk => chunks.push(...chunk),
        close: async () => calls.push('close'),
        abort: async () => calls.push('abort')
      }) }
    } },
    fetch: async (_url, options) => {
      calls.push('fetch')
      assert.equal(options.signal.aborted, false)
      return new Response(new Uint8Array([80, 75, 3, 4]))
    }
  })
  await app.exportBackup()
  assert.deepEqual(calls, ['picker', 'fetch', 'close'])
  assert.deepEqual(chunks, [80, 75, 3, 4])
})

test('cancelling the file picker sends no backup request and shows no error', async () => {
  let fetched = false
  const { app, element } = createApp({
    window: { showSaveFilePicker: async () => { throw new DOMException('Cancelled', 'AbortError') } },
    fetch: async () => { fetched = true }
  })
  await app.exportBackup()
  assert.equal(fetched, false)
  assert.equal(element('#toast').textContent, '')
})

test('a backup write error aborts the partial file and the network request', async () => {
  let signal
  let aborted = false
  const { app, element } = createApp({
    window: { showSaveFilePicker: async () => ({ createWritable: async () => ({
      write: async () => { throw new Error('Disk full') },
      abort: async () => { aborted = true },
      close: async () => assert.fail('Must not finish a failed file')
    }) }) },
    fetch: async (_url, options) => {
      signal = options.signal
      return new Response('backup data')
    }
  })
  await app.exportBackup()
  assert.equal(aborted, true)
  assert.equal(signal.aborted, true)
  assert.equal(element('#toast').textContent, 'Disk full')
})

test('the buffered fallback rejects oversized responses and aborts their download', async () => {
  let signal
  const { app, downloads, element } = createApp({ fetch: async (_url, options) => {
    signal = options.signal
    return new Response('body', { headers: { 'content-length': String(2 * 1024 ** 3 + 1) } })
  } })
  await app.exportBackup()
  assert.equal(signal.aborted, true)
  assert.equal(downloads.length, 0)
  assert.match(element('#toast').textContent, /超过 2 GB/)
})

test('browsers without a file picker can download a bounded backup', async () => {
  const { app, downloads, blobs } = createApp({ fetch: async () => new Response('zip bytes') })
  await app.exportBackup()
  assert.equal(downloads.length, 1)
  assert.match(downloads[0].download, /\.zip$/)
  assert.equal(await blobs.get(downloads[0].href).text(), 'zip bytes')
})

test('an unknown-length backup still enforces the buffered download limit', async () => {
  let signal
  let reads = 0
  const { app, downloads, element } = createApp({ fetch: async (_url, options) => {
    signal = options.signal
    return {
      ok: true, headers: new Headers(),
      body: { getReader: () => ({ read: async () => {
        reads++
        // Exercise the byte-count boundary without allocating a 2 GB buffer.
        return { done: false, value: { length: 2 * 1024 ** 3 + 1 } }
      } }) }
    }
  } })
  await app.exportBackup()
  assert.equal(reads, 1)
  assert.equal(signal.aborted, true)
  assert.equal(downloads.length, 0)
  assert.match(element('#toast').textContent, /超过 2 GB/)
})

test('restoring twice sends one request, preserves the password, and unlocks on failure', () => {
  const { app, element, requests } = createApp()
  element('#restoreFile').files = [new Blob(['backup'])]
  element('#restorePassword').value = ' password with spaces '
  element('#restoreConfirm').checked = true
  app.restoreBackup()
  app.openRestoreDialog()
  app.restoreBackup()
  assert.equal(requests.length, 1)
  assert.equal(requests[0].headers['X-OmniShare-Backup-Password'], ' password with spaces ')
  assert.equal(app.state.restoreInFlight, true)
  assert.equal(element('#restoreSubmitBtn').disabled, true)
  requests[0].onerror()
  assert.equal(app.state.restoreInFlight, false)
  assert.equal(element('#restoreSubmitBtn').disabled, false)
  app.restoreBackup()
  assert.equal(requests.length, 2)
  requests[1].onabort()
  assert.equal(app.state.restoreInFlight, false)
})
