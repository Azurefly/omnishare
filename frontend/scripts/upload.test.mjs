import assert from 'node:assert/strict'
import { File } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const source = (await readFile(new URL('../web/app.js', import.meta.url), 'utf8'))
  .replace(/\nstart\(\)\s*$/, '\n')

// Keep request completion and upload progress under test control. In particular,
// aborting a browser request does not prevent an already queued callback from
// being delivered, so tests can replay callbacks from an older attempt.
function createApp(options = {}) {
  class Element {
    constructor(tag = 'div') {
      this.tagName = tag.toUpperCase()
      this.children = []
      this.listeners = new Map()
      this.attributes = new Map()
      this.dataset = {}
      this.className = ''
      this.disabled = false
      this.value = ''
      this._text = ''
      this._html = ''
      this.legacyChildren = new Map()
      this.classList = {
        add: name => this.className = `${this.className} ${name}`.trim(),
        remove: name => this.className = this.className.split(/\s+/).filter(item => item !== name).join(' '),
        contains: name => this.className.split(/\s+/).includes(name),
        toggle: (name, enabled) => enabled ? this.classList.add(name) : this.classList.remove(name)
      }
    }
    appendChild(child) {
      child.remove()
      this.children.push(child)
      child.parentElement = this
      return child
    }
    append(...children) { children.forEach(child => this.appendChild(child)) }
    replaceChildren(...children) {
      this.children.forEach(child => child.parentElement = null)
      this.children = []
      this.append(...children)
    }
    remove() {
      if (this.parentElement) this.parentElement.children = this.parentElement.children.filter(child => child !== this)
      this.parentElement = null
    }
    set textContent(value) { this._text = String(value); this.replaceChildren() }
    get textContent() { return this._text + this.children.map(child => child.textContent).join('') }
    set innerHTML(value) {
      this._html = String(value)
      this.replaceChildren()
      this.legacyChildren.clear()
    }
    get innerHTML() { return this._html }
    querySelector(selector) {
      const className = selector.slice(1)
      for (const child of [...this.children, ...this.legacyChildren.values()]) {
        if (child.classList.contains(className)) return child
        const found = child.querySelector(selector)
        if (found) return found
      }
      // Support the previous implementation's static HTML template as well,
      // so the concurrency regression can be run against its actual buttons.
      const classes = [...this._html.matchAll(/class="([^"]+)"/g)]
        .find(match => match[1].split(/\s+/).includes(className))?.[1]
      if (!classes) return null
      if (!this.legacyChildren.has(selector)) {
        const child = new Element()
        child.className = classes
        this.legacyChildren.set(selector, child)
      }
      return this.legacyChildren.get(selector)
    }
    addEventListener(type, callback) {
      if (!this.listeners.has(type)) this.listeners.set(type, [])
      this.listeners.get(type).push(callback)
    }
    click() {
      if (!this.disabled) this.listeners.get('click')?.forEach(callback => callback({ target: this, preventDefault() {}, stopPropagation() {} }))
    }
    setAttribute(name, value) { this.attributes.set(name, String(value)) }
    getAttribute(name) { return this.attributes.get(name) ?? null }
    removeAttribute(name) { this.attributes.delete(name) }
    showModal() { this.open = true }
    focus() {}
  }
  const elements = new Map()
  const element = selector => {
    if (!elements.has(selector)) elements.set(selector, new Element())
    return elements.get(selector)
  }
  const requests = []
  const live = new Set()
  const fetches = []
  let peak = 0
  class Request {
    constructor() { this.upload = {}; this.headers = {}; requests.push(this) }
    open(method, url) { this.method = method; this.url = url }
    setRequestHeader(name, value) { this.headers[name] = value }
    send(body) {
      if (options.sendError) throw new Error(options.sendError)
      this.body = body
      live.add(this)
      peak = Math.max(peak, live.size)
    }
    abort() { this.aborted = true; live.delete(this); this.onabort?.() }
    respond(status = 201, payload = { code: 0, data: { id: 'uploaded' } }) {
      live.delete(this)
      this.status = status
      this.responseText = typeof payload === 'string' ? payload : JSON.stringify(payload)
      this.onload?.()
    }
    fail(kind = 'error') { live.delete(this); this[`on${kind}`]?.() }
    progress(loaded, total, lengthComputable = true) { this.upload.onprogress?.({ loaded, total, lengthComputable }) }
  }
  const context = vm.createContext({
    URL, URLSearchParams, Blob, File, FormData, AbortController,
    location: new URL('https://node.example/'), history: { replaceState() {} },
    document: { querySelector: element, querySelectorAll: () => [], createElement: tag => new Element(tag) },
    sessionStorage: { getItem: () => null }, window: {}, navigator: {},
    XMLHttpRequest: Request,
    fetch: async url => {
      fetches.push(url)
      return Response.json({ code: 0, data: url.includes('/dashboard') ? {} : [] })
    },
    setTimeout: () => 1, clearTimeout() {}, console
  })
  vm.runInContext(source, context, { filename: 'app.js' })
  const app = vm.runInContext(`({ state, uploadFiles, pumpUploads,
    cancelUpload: typeof cancelUpload === 'function' ? cancelUpload : undefined,
    retryUpload: typeof retryUpload === 'function' ? retryUpload : undefined,
    cancelAllUploads: typeof cancelAllUploads === 'function' ? cancelAllUploads : undefined,
    clearFinishedUploads: typeof clearFinishedUploads === 'function' ? clearFinishedUploads : undefined,
    hasPendingUploads: typeof hasPendingUploads === 'function' ? hasPendingUploads : undefined
  })`, context)
  app.state.config = { max_upload_mb: 32 }
  app.state.accessKey = 'page-key'
  element('#fileTTL').value = '3600'
  return { app, element, requests, fetches, live, peak: () => peak }
}

const file = (name = 'sample.txt', content = 'sample') => new File([content], name, { type: 'text/plain' })
const nextTurn = () => new Promise(resolve => setImmediate(resolve))

test('a failed upload retry waits for the shared three-request scheduler', async () => {
  const { app, element, requests, peak } = createApp()
  app.uploadFiles([file('one'), file('two'), file('three'), file('four')])
  assert.equal(requests.length, 3)
  const failedRow = element('#uploadQueue').children[0]
  requests[0].fail()
  await nextTurn()
  assert.equal(requests.length, 4)
  failedRow.querySelector('.retry-upload').click()
  assert.equal(requests.length, 4, 'retry must queue while all three request slots are occupied')
  assert.equal(peak(), 3)
  requests[1].respond()
  await nextTurn()
  assert.equal(requests.length, 5)
  assert.equal(peak(), 3)
})

test('waiting uploads render immediately and cancelling one never sends its file', () => {
  const { app, element, requests } = createApp()
  assert.equal(app.uploadFiles(Array.from({ length: 5 }, (_, i) => file(`file-${i}`))), 5)
  assert.equal(element('#uploadQueue').children.length, 5)
  assert.equal(requests.length, 3)
  const waiting = app.state.uploadQueue[3]
  assert.equal(waiting.status, 'queued')
  waiting.view.row.querySelector('.cancel-upload').click()
  assert.equal(waiting.status, 'cancelled')
  assert.match(waiting.view.detail.textContent, /尚未发送/)
  assert.equal(app.state.activeUploads, 3)
  requests[0].respond()
  assert.equal(requests.length, 4)
  assert.equal(requests[3].body.get('file').name, 'file-4')
  assert.ok(requests.every(request => request.body.get('file').name !== 'file-3'))
})

test('cancellation settles once and callbacks from the cancelled attempt cannot finish its retry', () => {
  const { app, element, requests, fetches, peak } = createApp()
  app.uploadFiles([file('one'), file('two'), file('three'), file('four')])
  const task = app.state.uploadQueue[0]
  const first = requests[0]
  const late = { load: first.onload, error: first.onerror, abort: first.onabort, progress: first.upload.onprogress }
  task.view.cancel.click()
  assert.equal(first.aborted, true)
  assert.equal(task.status, 'cancelled')
  assert.match(task.view.detail.textContent, /可能已在服务端保存/)
  assert.equal(requests.length, 4)
  assert.equal(app.state.activeUploads, 3)
  first.status = 201
  first.responseText = JSON.stringify({ code: 0, data: { id: 'late' } })
  late.load()
  late.error()
  late.abort()
  late.progress({ loaded: 1, total: 1, lengthComputable: true })
  assert.equal(task.status, 'cancelled')
  assert.equal(app.state.activeUploads, 3)
  assert.equal(fetches.length, 0, 'late success must not refresh or claim completion')
  assert.doesNotMatch(element('#toast').textContent, /上传完成/)
  task.view.retry.click()
  assert.equal(requests.length, 4)
  requests[1].fail()
  assert.equal(requests.length, 5)
  assert.equal(task.status, 'uploading')
  late.load()
  late.error()
  late.abort()
  assert.equal(task.status, 'uploading')
  assert.equal(app.state.activeUploads, 3)
  assert.equal(fetches.length, 0)
  requests[4].respond()
  assert.equal(task.status, 'succeeded')
  assert.equal(app.state.activeUploads, 2)
  assert.equal(peak(), 3)
})

test('request body progress at 100 percent waits for server confirmation', () => {
  const { app, requests, element } = createApp()
  app.uploadFiles([file()])
  const task = app.state.uploadQueue[0]
  requests[0].progress(999, 1000)
  assert.match(task.view.status.textContent, /99%/)
  assert.equal(task.processing, false)
  requests[0].progress(1000, 1000)
  assert.equal(task.status, 'uploading')
  assert.equal(task.view.status.textContent, '服务端处理中')
  assert.match(task.view.detail.textContent, /等待服务器确认/)
  assert.equal(app.state.activeUploads, 1)
  assert.equal(element('#clearFinishedUploadsBtn').disabled, true)
  assert.equal(app.hasPendingUploads(), true)
  requests[0].respond()
  assert.equal(task.status, 'succeeded')
  assert.equal(task.view.status.textContent, '已完成')
  assert.equal(task.file, null, 'successful uploads must release their File references')
  assert.equal(element('#uploadQueue').children.length, 1, 'terminal rows remain available for review')
  assert.equal(app.hasPendingUploads(), false)
  assert.equal(element('#clearFinishedUploadsBtn').disabled, false)
})

test('unknown progress remains indeterminate until the body has been sent', () => {
  const { app, requests } = createApp()
  app.uploadFiles([file()])
  const task = app.state.uploadQueue[0]
  requests[0].progress(5, 0, false)
  assert.equal(task.progress, null)
  assert.equal(task.view.status.textContent, '上传中')
  assert.match(task.view.detail.textContent, /已发送 5 B/)
  requests[0].upload.onload()
  assert.equal(task.view.status.textContent, '服务端处理中')
  assert.equal(task.status, 'uploading')
})

test('cancel all also cancels waiting work before freeing any request slots', () => {
  const { app, element, requests, live, peak } = createApp()
  app.uploadFiles(Array.from({ length: 8 }, (_, i) => file(`file-${i}`)))
  assert.equal(element('#cancelAllUploadsBtn').disabled, false)
  app.cancelAllUploads()
  assert.equal(requests.length, 3, 'cancellation must not start a waiting upload')
  assert.equal(live.size, 0)
  assert.equal(app.state.activeUploads, 0)
  assert.equal(app.hasPendingUploads(), false)
  assert.ok(app.state.uploadQueue.every(task => task.status === 'cancelled'))
  assert.equal(element('#cancelAllUploadsBtn').disabled, true)
  assert.match(element('#transferSummary').textContent, /已取消 8/)
  app.cancelAllUploads()
  assert.equal(app.state.activeUploads, 0)
  assert.equal(peak(), 3)
})

test('oversized inputs are rejected before sending and accepted files keep their chosen retention', () => {
  const { app, element, requests } = createApp()
  app.state.config.max_upload_mb = 1
  const oversized = file('too-large.txt', new Uint8Array(1024 ** 2 + 1))
  assert.equal(app.uploadFiles([oversized]), 0)
  assert.equal(requests.length, 0)
  assert.equal(app.state.uploadQueue.length, 0)
  assert.match(element('#transferSummary').textContent, /超过节点单文件上限 1\.0 MB/)
  assert.equal(app.uploadFiles([oversized, file('accepted.txt')], { ttl: 86400 }), 1)
  assert.equal(requests.length, 1)
  assert.equal(requests[0].body.get('ttl_seconds'), '86400')
  assert.equal(requests[0].body.get('file').name, 'accepted.txt')
  assert.equal(app.uploadFiles([file('default-retention.txt')]), 1)
  assert.equal(requests[1].body.get('ttl_seconds'), '3600')
})

test('queue records stay bounded and clearing terminal records releases retained files', () => {
  const { app, element, requests } = createApp()
  assert.equal(app.uploadFiles(Array.from({ length: 120 }, (_, i) => file(`file-${i}`))), 100)
  assert.equal(app.state.uploadQueue.length, 100)
  assert.equal(element('#uploadQueue').children.length, 100)
  assert.equal(requests.length, 3)
  assert.equal(app.uploadFiles([file('extra')]), 0)
  const retained = [...app.state.uploadQueue]
  app.cancelAllUploads()
  const staleRetry = retained[0].view.retry
  app.clearFinishedUploads()
  assert.equal(app.state.uploadQueue.length, 0)
  assert.equal(element('#uploadQueue').children.length, 0)
  assert.equal(element('#transferSummary').textContent, '等待上传')
  assert.ok(retained.every(task => task.file === null && task.view === null))
  staleRetry.click()
  assert.equal(requests.length, 3, 'a cleared task cannot be revived by an old button')
  assert.equal(app.uploadFiles([file('new')]), 1)
  assert.equal(requests.length, 4)
})

test('clearing finished work preserves active and waiting tasks', () => {
  const { app, element, requests } = createApp()
  app.uploadFiles(Array.from({ length: 5 }, (_, i) => file(`file-${i}`)))
  const first = app.state.uploadQueue[0]
  requests[0].respond()
  app.clearFinishedUploads()
  assert.equal(first.file, null)
  assert.equal(first.view, null)
  assert.equal(app.state.uploadQueue.length, 4)
  assert.equal(element('#uploadQueue').children.length, 4)
  assert.equal(app.state.activeUploads, 3)
  assert.equal(app.hasPendingUploads(), true)
  assert.equal(app.state.uploadQueue.filter(task => task.status === 'queued').length, 1)
})

test('server errors and filenames are displayed as text and never automatically retried', () => {
  const { app, requests } = createApp()
  const name = '<img src=x onerror=alert(1)>.txt'
  const message = '<svg onload=alert(1)> rejected'
  app.uploadFiles([file(name)])
  const task = app.state.uploadQueue[0]
  assert.equal(task.view.row.querySelector('.upload-file-name').textContent, name)
  assert.equal(task.view.row.innerHTML, '')
  requests[0].respond(413, { code: 413, message })
  assert.equal(task.status, 'failed')
  assert.equal(task.view.detail.textContent, message)
  assert.equal(task.view.detail.innerHTML, '')
  assert.equal(requests.length, 1)
  assert.equal(app.state.activeUploads, 0)
})

test('HTTP success without a successful API envelope is not reported as a completed upload', () => {
  const { app, requests, fetches } = createApp()
  app.uploadFiles([file('error-envelope'), file('html-response')])
  requests[0].respond(200, { code: 1, message: 'not saved' })
  requests[1].respond(200, '<html>sign in</html>')
  assert.ok(app.state.uploadQueue.every(task => task.status === 'failed'))
  assert.equal(fetches.length, 0)
  assert.equal(app.state.activeUploads, 0)
})

test('retries use current credentials and always target the same-origin upload endpoint', () => {
  const { app, requests } = createApp()
  app.uploadFiles([file()], { ttl: 60, url: 'https://untrusted.example/upload' })
  assert.equal(requests[0].method, 'POST')
  assert.equal(requests[0].url, '/api/v1/files/upload')
  assert.equal(requests[0].headers['X-OmniShare-Key'], 'page-key')
  requests[0].fail()
  app.state.accessKey = ''
  app.state.initToken = 'new-bootstrap-token'
  app.retryUpload(app.state.uploadQueue[0])
  assert.equal(requests.length, 2)
  assert.equal(requests[1].url, '/api/v1/files/upload')
  assert.equal(requests[1].headers['X-OmniShare-Init-Token'], 'new-bootstrap-token')
  assert.equal(requests[1].headers['X-OmniShare-Key'], undefined)
  assert.equal(requests[1].body.get('ttl_seconds'), '60')
})

test('synchronous send errors release each scheduler slot exactly once', () => {
  const { app, requests, live } = createApp({ sendError: 'browser blocked request' })
  assert.equal(app.uploadFiles(Array.from({ length: 6 }, (_, i) => file(`file-${i}`))), 6)
  assert.equal(requests.length, 6)
  assert.equal(app.state.activeUploads, 0)
  assert.equal(app.hasPendingUploads(), false)
  assert.equal(live.size, 0)
  assert.ok(app.state.uploadQueue.every(task => task.status === 'failed'))
  assert.match(app.state.uploadQueue[0].view.detail.textContent, /browser blocked request/)
})
