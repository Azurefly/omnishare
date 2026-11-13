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
    URL: BrowserURL, URLSearchParams, AbortController, Blob, FormData, TextEncoder, btoa,
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
    sessionRemove, keyHeaders, fillSettings, saveSettings, showAuth, updateAccessKeyControls, exportBackup, preservePadDraft, performPadSave,
    restoreBackup, openRestoreDialog, createPad, submitNewPad, refreshCurrentPad, refreshWorkspace, loadRemotePadVersion, savePadDraftCopy, enqueuePadSave, workspacePath, submitPeerBinding })`, context)
  return { app, element, stored, downloads, blobs, requests, replacedURLs }
}

test('obsolete init tokens leave the URL but are never accepted or persisted', () => {
  const { app, replacedURLs } = createApp({
    url: 'http://localhost/?view=pads&init_token=test-token#editor',
    storageGetter() { throw new DOMException('Storage denied', 'SecurityError') }
  })
  assert.equal(app.state.initToken, undefined)
  assert.equal(app.authHeaders()['X-OmniShare-Init-Token'], undefined)
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

test('ordinary sessions remove bootstrap tokens and preserve drafts', () => {
  const { app, stored } = createApp({ url: 'http://localhost/?init_token=initial' })
  assert.equal(stored.has('omnishare.initToken'), false)
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


test('optional key protection transports short and arbitrary UTF-8 keys without trimming', () => {
  const {app} = createApp()
  for (const key of ['1', 'a', '中文🔑', ' ', ' x ', 'CLEAR', '__KEEP__']) {
    const h = app.keyHeaders(key)
    const decoded = h['X-OmniShare-Key'] ?? Buffer.from(h['X-OmniShare-Key-Base64'], 'base64').toString('utf8')
    assert.equal(decoded, key)
  }
  app.state.config = {access_key_enabled:false}
  app.state.accessKey = 'a stored key'
  assert.deepEqual(Object.keys(app.authHeaders()), [])
  app.showAuth()
  assert.equal(app.state.config.access_key_enabled, true)
  assert.equal(Buffer.from(app.authHeaders()['X-OmniShare-Key-Base64'], 'base64').toString('utf8'), 'a stored key')
})

test('optional switch does not require a key, token or clearing confirmation when disabled', async () => {
  const calls=[]
  const cfg={access_key_enabled:false,has_access_key:false,allow_lan:true,listen_address:'0.0.0.0'}
  const {app,element}=createApp({fetch:async(url,options={})=>{
    calls.push({url,options})
    return Response.json({code:0,data:url.includes('/config')?cfg:url.includes('/health')?{version:'test'}:[]})
  }})
  app.state.config=cfg
  app.fillSettings()
  assert.equal(element('#cfgAccessKey').disabled,true)
  assert.equal(element('#cfgAllowLAN').checked,true)
  await app.saveSettings()
  const request=calls.find(x=>x.options.method==='PUT')
  const payload=JSON.parse(request.options.body)
  assert.equal(payload.access_key_enabled,false)
  assert.equal(payload.keep_access_key,true)
  assert.equal(request.options.headers['X-OmniShare-Init-Token'],undefined)
  assert.equal(request.options.headers['X-OmniShare-Confirm'],undefined)
})

test('the enabled switch saves literal CLEAR, __KEEP__ and whitespace keys', async () => {
  for (const key of ['1','CLEAR','__KEEP__',' 🔑 中文 ']) {
    const calls=[]
    const {app,element}=createApp({fetch:async(url,options={})=>{
      calls.push({url,options})
      return Response.json({code:0,data:url.includes('/config')?{has_access_key:true,access_key_enabled:true}:url.includes('/health')?{version:'test'}:[]})
    }})
    app.state.config={has_access_key:false,access_key_enabled:false,allow_lan:true}
    app.fillSettings()
    element('#cfgAccessKeyEnabled').checked=true
    app.updateAccessKeyControls()
    assert.equal(element('#cfgAccessKey').disabled,false)
    element('#cfgAccessKey').value=key
    await app.saveSettings()
    const payload=JSON.parse(calls.find(x=>x.options.method==='PUT').options.body)
    assert.equal(payload.access_key_enabled,true)
    assert.equal(payload.keep_access_key,false)
    assert.equal(payload.access_key,key)
    assert.equal(app.state.accessKey,key)
  }
})


test('native dialog-free document creation opens an in-page form and submits once', async () => {
  let calls = 0, resolveCreate
  const {app, element} = createApp({fetch: async (url, options) => {
    if (options?.method === 'POST') { calls++; return new Promise(resolve => { resolveCreate = resolve }) }
    return Response.json({code:0, data:url.includes('/pads/') ? {id:'pad-new',title:'new',content:'hello',version:1} : url.includes('/pads') ? [] : {}})
  }})
  app.createPad()
  assert.equal(element('#newPadDialog').open, true)
  element('#newPadTitle').value = 'new'
  const first = app.submitNewPad(); const second = app.submitNewPad()
  assert.equal(calls, 1)
  assert.equal(element('#createPadSubmitBtn').disabled, true)
  resolveCreate(Response.json({code:0,data:{id:'pad-new',title:'new',content:'hello',version:1}}, {status:201}))
  await Promise.all([first, second])
  assert.equal(element('#newPadDialog').open, false)
  assert.equal(app.state.currentPad.id,'pad-new')
  assert.equal(element('#createPadSubmitBtn').disabled,false)
})

test('remote document refresh updates a clean editor but preserves dirty drafts', async () => {
  const remote = {id:'remote-pad',title:'New title',content:'remote text',version:2}
  const {app, element, stored} = createApp({fetch:async () => Response.json({code:0,data:remote})})
  app.state.currentPad = {...remote,version:1}
  await app.refreshCurrentPad()
  assert.equal(element('#padContent').value,'remote text')
  app.state.currentPad = {...remote,version:1}; app.state.currentPadDirty = true
  element('#padContent').value='local unsaved text'; element('#padTitle').value='local'
  await app.refreshCurrentPad()
  assert.equal(element('#padContent').value,'local unsaved text')
  assert.equal(app.state.currentPad.version,1)
  assert.equal(app.state.padSaveBlocked,true)
  assert.equal(JSON.parse(stored.get('omnishare.padDraft.remote-pad')).content,'local unsaved text')
})

test('a refresh response arriving after new typing cannot overwrite the editor', async () => {
  let complete
  const {app, element} = createApp({fetch:async () => new Promise(r => {complete=r})})
  app.state.currentPad = {id:'p',title:'old',content:'old',version:1}
  const refresh = app.refreshCurrentPad()
  app.state.currentPadDirty = true; element('#padContent').value='typed while awaiting fetch'; element('#padTitle').value='draft'
  complete(Response.json({code:0,data:{id:'p',title:'remote',content:'remote',version:2}}))
  await refresh
  assert.equal(element('#padContent').value,'typed while awaiting fetch')
  assert.equal(app.state.currentPad.version,1)
})

test('workspace scope is applied only to collections, not local config or writes', () => {
  const {app} = createApp()
  assert.equal(app.workspacePath('/api/v1/notes?q=hello'),'/api/v1/notes?q=hello&scope=all')
  assert.equal(app.workspacePath('/api/v1/notes','POST'),'/api/v1/notes')
  assert.equal(app.workspacePath('/api/v1/config'),'/api/v1/config')
  assert.equal(app.workspacePath('/api/v1/pads/node-test~p'),'/api/v1/pads/node-test~p')
})


test('a late refresh cannot revert a document that finished saving meanwhile', async () => {
  let reply
  const {app, element} = createApp({fetch: () => new Promise(resolve => { reply = resolve })})
  app.state.currentPad = {id:'p', version:1, title:'title', content:'v1'}
  element('#padTitle').value = 'title'; element('#padContent').value = 'v1'
  const pending = app.refreshCurrentPad()
  app.state.currentPad = {id:'p', version:2, title:'title', content:'saved v2'}
  element('#padContent').value = 'saved v2'
  reply(Response.json({code:0,data:{id:'p',version:1,title:'title',content:'v1'}}))
  await pending
  assert.equal(app.state.currentPad.version, 2)
  assert.equal(element('#padContent').value, 'saved v2')
  assert.equal(app.state.padRefreshBusy, false)
})

test('editor refresh continues while aggregate workspace loading is busy', async () => {
  let calls = 0, reply
  const {app, element} = createApp({fetch: () => { calls++; return new Promise(resolve => {reply = resolve}) }})
  app.state.activeTab = 'pads'; app.state.workspaceBusy = true
  app.state.currentPad = {id:'p',version:1,title:'title',content:'old'}
  const pending = app.refreshWorkspace()
  await app.refreshWorkspace() // no overlapping GET of the current editor
  assert.equal(calls, 1)
  reply(Response.json({code:0,data:{id:'p',version:2,title:'title',content:'new'}}))
  await pending
  assert.equal(element('#padContent').value, 'new')
  assert.equal(app.state.workspaceBusy, true)
})

test('loading a remote version must not discard typing that occurred during the request', async () => {
  let reply
  const {app, element, stored} = createApp({fetch: () => new Promise(resolve => {reply = resolve})})
  app.state.currentPad = {id:'p',version:1}; app.state.currentPadDirty = true
  element('#padTitle').value = 'title'; element('#padContent').value = 'original draft'
  const pending = app.loadRemotePadVersion()
  element('#padContent').value = 'new typing'
  reply(Response.json({code:0,data:{id:'p',version:2,title:'title',content:'remote'}}))
  await pending
  assert.equal(element('#padContent').value, 'new typing')
  assert.equal(app.state.currentPadDirty, true)
  assert.equal(JSON.parse(stored.get('omnishare.padDraft.p')).content, 'original draft')
})

test('draft copy is single-flight and does not switch away from newer typing', async () => {
  let copies = 0, reply
  const {app, element, stored} = createApp({fetch: async (_url, options) => {
    if (options?.method === 'POST') { copies++; return new Promise(resolve => {reply = resolve}) }
    return Response.json({code:0,data:[]})
  }})
  app.state.currentPad = {id:'p',version:1}; app.state.currentPadDirty = true
  element('#padTitle').value = 'title'; element('#padContent').value = 'original draft'
  const pending = app.savePadDraftCopy()
  await app.savePadDraftCopy()
  assert.equal(copies, 1); assert.equal(element('#saveDraftCopyBtn').disabled, true)
  element('#padContent').value = 'newer draft'
  reply(Response.json({code:0,data:{id:'copy',version:1,title:'copy',content:'original draft'}}))
  await pending
  assert.equal(app.state.currentPad.id, 'p')
  assert.equal(element('#padContent').value, 'newer draft')
  assert.equal(JSON.parse(stored.get('omnishare.padDraft.p')).content, 'newer draft')
  assert.equal(element('#saveDraftCopyBtn').disabled, false)
})

test('queued autosaves are cancelled after a version conflict', async () => {
  let saves = 0
  const {app, element} = createApp({fetch: async (_url, options) => {
    if (options?.method === 'PUT') { saves++; return Response.json({code:1,message:'conflict'},{status:409}) }
    return Response.json({code:0,data:[]})
  }})
  app.state.currentPad = {id:'p',version:1}; app.state.currentPadDirty = true
  element('#padTitle').value = 'title'; element('#padContent').value = 'new typing'
  const snapshot = {id:'p',version:1,title:'title',content:'draft'}
  const first = app.enqueuePadSave(snapshot, false)
  const queued = app.enqueuePadSave({...snapshot,content:'another draft'}, false)
  assert.equal(await first, false); assert.equal(await queued, false)
  assert.equal(saves, 1)
  assert.equal(element('#padContent').value, 'new typing')
})
