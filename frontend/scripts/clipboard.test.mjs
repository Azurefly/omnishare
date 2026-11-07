import assert from 'node:assert/strict'
import { File } from 'node:buffer'
import { readFile } from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const source = (await readFile(new URL('../web/app.js', import.meta.url), 'utf8'))
  .replace(/\nstart\(\)\s*$/, '\n')

function createApp(options = {}) {
  const elements = new Map()
  const requests = []
  const blobs = new Map()
  const element = selector => {
    if (!elements.has(selector)) {
      const attributes = new Map()
      const classes = new Set()
      elements.set(selector, {
        value: '', textContent: '', innerHTML: '', className: '', disabled: false,
        dataset: {}, open: false, checked: false, src: '',
        classList: { add: name => classes.add(name), remove: name => classes.delete(name),
          toggle: (name, active) => active ? classes.add(name) : classes.delete(name),
          contains: name => classes.has(name) },
        showModal() { this.open = true }, close() { this.open = false }, focus() {},
        decode: options.decodeImage || (async () => {}),
        removeAttribute(name) { attributes.delete(name); if (name === 'src') this.src = '' },
        setAttribute: (name, value) => attributes.set(name, value),
        getAttribute: name => attributes.get(name), contains: () => false,
        querySelectorAll: () => []
      })
    }
    return elements.get(selector)
  }
  class BrowserURL extends URL {
    static createObjectURL(blob) { const url = `blob:preview-${blobs.size}`; blobs.set(url, blob); return url }
    static revokeObjectURL(url) { blobs.delete(url) }
  }
  const context = vm.createContext({
    URL: BrowserURL, URLSearchParams, Blob, File, FormData, AbortController,
    location: new URL('https://node.example/'), history: { replaceState() {} },
    document: { querySelector: element, querySelectorAll: () => [], activeElement: null },
    sessionStorage: { getItem: () => null },
    navigator: { clipboard: options.clipboard }, window: {},
    fetch: async (url, init = {}) => {
      requests.push({url, init})
      if (options.fetch) return options.fetch(url, init)
      return Response.json({code:0, data: init.method === 'POST' ? {id:'created'} : url.includes('dashboard') ? {} : []})
    },
    setTimeout: () => 1, clearTimeout() {}, console
  })
  vm.runInContext(source, context, {filename:'app.js'})
  const app = vm.runInContext(`({state, readClipboardPreview, confirmClipboardSend,
    closeClipboardPreview, validateClipboardPNG, safeDeviceURL, renderDevices,
    replaceUploadBoundary(fn) { uploadFiles = fn }})`, context)
  app.state.config = {node_name:'Test node', max_upload_mb:32, has_access_key:true}
  app.state.accessKey = 'session-key-never-in-url'
  return {app, element, requests, blobs}
}

function png(width = 2, height = 2) {
  const bytes = new Uint8Array(24)
  bytes.set([137,80,78,71,13,10,26,10,0,0,0,13,73,72,68,82])
  const view = new DataView(bytes.buffer)
  view.setUint32(16, width)
  view.setUint32(20, height)
  return new Blob([bytes], {type:'image/png'})
}

test('clipboard text remains local until confirmation and is sent to this node only', async () => {
  const text = '<img src=x onerror=alert(1)> private text'
  const {app, element, requests} = createApp({clipboard:{readText:async () => text}})
  await app.readClipboardPreview()
  assert.equal(requests.length, 0)
  assert.equal(element('#clipboardDialog').open, true)
  assert.equal(element('#clipboardPreviewText').value, text)
  assert.equal(element('#clipboardPreviewText').innerHTML, '')
  assert.match(element('#clipboardPreviewMeta').textContent, /https:\/\/node.example/)
  assert.equal(element('#clipboardTTL').value, '86400')
  await app.confirmClipboardSend()
  const writes = requests.filter(request => request.init.method === 'POST')
  assert.equal(writes.length, 1)
  assert.equal(writes[0].url, '/api/v1/notes')
  assert.equal(writes[0].init.headers['X-OmniShare-Key'], 'session-key-never-in-url')
  assert.deepEqual(JSON.parse(writes[0].init.body), {content:text, tags:['剪贴板'], ttl_seconds:86400})
  assert.equal(element('#clipboardDialog').open, false)
  assert.equal(element('#clipboardPreviewText').value, '')
})

test('cancelling a preview erases the content and sends nothing', async () => {
  const {app, element, requests} = createApp({clipboard:{readText:async () => 'sensitive'}})
  await app.readClipboardPreview()
  app.closeClipboardPreview()
  await app.confirmClipboardSend()
  assert.equal(requests.length, 0)
  assert.equal(element('#clipboardPreviewText').value, '')
  assert.equal(element('#clipboardDialog').open, false)
})

test('a denied read does not silently fall back to another permission path', async () => {
  let fallback = false
  const {app, element, requests} = createApp({clipboard:{
    read:async () => { throw new DOMException('Denied', 'NotAllowedError') },
    readText:async () => { fallback = true; return 'private' }
  }})
  await app.readClipboardPreview()
  assert.equal(fallback, false)
  assert.equal(requests.length, 0)
  assert.equal(element('#clipboardDialog').open, false)
  assert.match(element('#toast').textContent, /未获得剪贴板读取权限/)
})

test('HTML-only and active image formats are not accepted as clipboard content', async () => {
  const {app, element, requests} = createApp({clipboard:{read:async () => [
    {types:['text/html', 'image/svg+xml'], getType:async () => assert.fail('Do not read unsupported types')}
  ]}})
  await app.readClipboardPreview()
  assert.equal(requests.length, 0)
  assert.equal(element('#clipboardDialog').open, false)
  assert.match(element('#toast').textContent, /没有纯文本或 PNG/)
})

test('PNG preview is local, confirmation uses the bounded queue, and blob URLs are revoked', async () => {
  const {app, element, requests, blobs} = createApp({clipboard:{read:async () => [
    {types:['image/png', 'text/html'], getType:async type => { assert.equal(type, 'image/png'); return png() }}
  ]}})
  const accepted = []
  app.replaceUploadBoundary((files, options) => { accepted.push({files, options}); return 1 })
  await app.readClipboardPreview()
  assert.equal(requests.length, 0)
  assert.equal(accepted.length, 0)
  assert.equal(blobs.size, 1)
  assert.match(element('#clipboardPreviewImage').src, /^blob:/)
  element('#clipboardTTL').value = '3600'
  await app.confirmClipboardSend()
  assert.equal(accepted.length, 1)
  assert.equal(accepted[0].files[0].type, 'image/png')
  assert.equal(accepted[0].options.ttl, 3600)
  assert.equal(blobs.size, 0)
  assert.equal(element('#clipboardPreviewImage').src, '')
  assert.equal(requests.filter(request => request.init.method === 'POST').length, 0)
})

test('a rejected upload queue keeps the preview available without claiming success', async () => {
  const {app, element, blobs} = createApp({clipboard:{read:async () => [{types:['image/png'], getType:async () => png()}]}})
  app.replaceUploadBoundary(() => 0)
  await app.readClipboardPreview()
  await app.confirmClipboardSend()
  assert.equal(element('#clipboardDialog').open, true)
  assert.match(element('#clipboardPreviewStatus').textContent, /未加入上传队列/)
  assert.equal(blobs.size, 1)
  app.closeClipboardPreview()
  assert.equal(blobs.size, 0)
})

test('an image that the browser cannot decode is discarded without preview or upload', async () => {
  const {app, element, requests, blobs} = createApp({
    clipboard:{read:async () => [{types:['image/png'], getType:async () => png()}]},
    decodeImage:async () => { throw new DOMException('Invalid PNG', 'EncodingError') }
  })
  await app.readClipboardPreview()
  assert.equal(element('#clipboardDialog').open, false)
  assert.equal(element('#clipboardPreviewImage').src, '')
  assert.equal(blobs.size, 0)
  assert.equal(requests.length, 0)
  assert.match(element('#toast').textContent, /无法解码剪贴板图片/)
})

test('PNG byte size, format and decoded dimensions are bounded before preview', async () => {
  const {app} = createApp()
  await assert.rejects(app.validateClipboardPNG(new Blob(['not png'], {type:'image/png'})), /格式无效/)
  await assert.rejects(app.validateClipboardPNG(png(8193, 1)), /尺寸过大/)
  await assert.rejects(app.validateClipboardPNG(png(8192, 8192)), /尺寸过大/)
  await assert.rejects(app.validateClipboardPNG(png(0, 1)), /尺寸过大/)
  app.state.config.max_upload_mb = 0.000001
  await assert.rejects(app.validateClipboardPNG(png()), /超过/)
})

test('duplicate confirmation cannot submit twice while the first request is pending', async () => {
  let finish
  const pending = new Promise(resolve => { finish = resolve })
  const {app, requests, element} = createApp({clipboard:{readText:async () => 'once'}, fetch:async (_url, init) => {
    if (init.method === 'POST') return pending
    return Response.json({code:0, data:[]})
  }})
  await app.readClipboardPreview()
  const first = app.confirmClipboardSend()
  await app.confirmClipboardSend()
  app.closeClipboardPreview()
  assert.equal(element('#clipboardDialog').open, true)
  assert.equal(element('#clipboardCancelBtn').disabled, true)
  assert.equal(requests.length, 1)
  finish(Response.json({code:0, data:{id:'once'}}))
  await first
  assert.equal(requests.filter(request => request.init.method === 'POST').length, 1)
})

test('late clipboard permission results cannot revive a dismissed preview', async () => {
  let finish
  const {app, element, requests} = createApp({clipboard:{readText:() => new Promise(resolve => { finish = resolve })}})
  const reading = app.readClipboardPreview()
  app.closeClipboardPreview()
  finish('late secret')
  await reading
  assert.equal(element('#clipboardDialog').open, false)
  assert.equal(element('#clipboardPreviewText').value, '')
  assert.equal(requests.length, 0)
})

test('clipboard text and the serialized request both respect their size bounds', async () => {
  const oversized = createApp({clipboard:{readText:async () => 'a'.repeat(256 * 1024 + 1)}})
  await oversized.app.readClipboardPreview()
  assert.equal(oversized.element('#clipboardDialog').open, false)
  assert.equal(oversized.requests.length, 0)
  const escaped = createApp({clipboard:{readText:async () => '\u0001'.repeat(250 * 1024)}})
  await escaped.app.readClipboardPreview()
  await escaped.app.confirmClipboardSend()
  assert.equal(escaped.requests.length, 0)
  assert.match(escaped.element('#clipboardPreviewStatus').textContent, /编码后超过请求上限/)
})

test('device links reject embedded credentials, tokens and non-origin destinations', () => {
  const {app} = createApp()
  for (const url of ['javascript:alert(1)', '//other.example', '/api/v1/config',
    'https://user:password@other.example/', 'https://other.example/?init_token=secret',
    'https://other.example/#secret', 'https://other.example/api/v1/backup']) assert.equal(app.safeDeviceURL(url), '')
  assert.equal(app.safeDeviceURL('http://100.100.1.2:8081'), 'http://100.100.1.2:8081/')
  assert.equal(app.safeDeviceURL('https://[::1]:8081'), 'https://[::1]:8081/')
})

test('discovered devices are escaped and are never represented as authorized peers', () => {
  const {app, element} = createApp()
  app.state.devices = [{id:'remote', hostname:'<img src=x>', url:'https://other.example/', online:true, network_type:'lan'}]
  app.renderDevices()
  const markup = element('#deviceList').innerHTML
  assert.match(markup, /&lt;img src=x&gt;/)
  assert.match(markup, /发现设备不会授予访问权限/)
  assert.match(markup, /rel="noopener noreferrer"/)
  assert.doesNotMatch(markup, /session-key-never-in-url|已信任|已配对/)
})
