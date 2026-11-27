import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const source = await readFile(new URL('../web/runtime-bridge.js', import.meta.url), 'utf8')
function bridge(fetcher = async () => Response.json({code:0,data:[]}), timer = setTimeout) {
  const requests = []
  const events = []
  const window = {setTimeout:timer, fetch:async (...args) => {requests.push(args); return fetcher(...args)}}
  const context = vm.createContext({window, URL, Request, Response, Headers, AbortController, CustomEvent:Event,
    location:new URL('http://100.92.203.77:8084/'), console, setTimeout, clearTimeout,
    document:{readyState:'loading',addEventListener(){},querySelector:()=>null,dispatchEvent(e){events.push(e.type)}}})
  vm.runInContext(source, context)
  return {collapse: rows => JSON.parse(JSON.stringify(window.__omnishareRuntimeBridge.collapseDevices(rows))),
    fetch:window.fetch, requests, events}
}

test('native selection, directory walks and shortcut hashing finish without the short API deadline', async () => {
  for (const [path, body] of [['shortcuts',{pick:true}], ['shortcuts',{path:'/sample'}], ['selection',{paths:['/sample']}]]) {
    let finish
    const deadlines = []
    const {fetch, requests, events} = bridge(() => new Promise(resolve => { finish = resolve }),
      (_callback, ms) => { deadlines.push(ms); return 1 })
    const pending = fetch('/api/v1/local-files/' + path, {method:'POST', body:JSON.stringify(body)})
    assert.deepEqual(deadlines, [], 'waiting for the user or disk must not start an abort timer')
    const selected = Response.json({code:0,data:{id:'file'}})
    finish(selected)
    assert.equal(await pending, selected)
    assert.equal(requests.length, 1)
    assert.deepEqual(events, [])
  }
})

test('a failed picker exchange checks health without retrying the file operation', async () => {
  for (const reachable of [true, false]) {
    const {fetch, requests, events} = bridge(async url => {
      if (url === '/api/v1/health' && reachable) return Response.json({code:0})
      throw new TypeError('Failed to fetch')
    }, () => 1)
    await assert.rejects(fetch('/api/v1/local-files/shortcuts', {method:'POST'}),
      reachable ? /未收到操作结果/ : {name:'OmniShareBackendUnavailable'})
    assert.deepEqual(requests.map(([url]) => url), ['/api/v1/local-files/shortcuts','/api/v1/health'])
    assert.deepEqual(events, reachable ? [] : ['omnishare:backend-offline'])
  }
})

test('file moves and public delivery uploads are submitted once and use the server transfer deadline', async () => {
  const deadlines = []
  const {fetch, requests, events} = bridge(async () => Response.json({code:0,data:{moved:true}}),
    (_callback, ms) => { deadlines.push(ms); return 1 })
  for (const path of ['/api/v1/file-transfers', '/api/v1/delivery/items']) {
    const response = await fetch(path, {method:'POST',body:'{}'})
    assert.ok((await response.json()).data.moved)
  }
  assert.deepEqual(deadlines, [])
  assert.equal(requests.length, 2)
  assert.deepEqual(events, [])
})

test('caller cancellation in init or Request preserves the abort and leaves connection status alone', async () => {
  for (const inherited of [false, true]) {
    const {fetch, requests, events} = bridge((_input, init) => new Promise((_resolve, reject) => {
      init.signal.addEventListener('abort', () => reject(init.signal.reason), {once:true})
    }))
    const controller = new AbortController()
    const input = inherited ? new Request('http://100.92.203.77:8084/api/v1/files', {signal:controller.signal}) : '/api/v1/local-files/shortcuts'
    const pending = fetch(input, inherited ? {} : {method:'POST', signal:controller.signal})
    controller.abort()
    await assert.rejects(pending, {name:'AbortError'})
    assert.equal(requests.length, 1)
    assert.deepEqual(events, [])
  }
})
const lan='http://192.168.31.200:8084/'
const tail='http://100.92.203.77:8084/'
const ipv6='http://[fd7a:115c:a1e0::2637:cb4e]:8084/'
const local={id:'node-local',is_local:true,online:true,hostname:'OmniShare',url:lan,addresses:[lan,tail]}

test('runtime keeps every route when duplicate stable local identities collapse', () => {
  const {collapse}=bridge()
  const rows=[local,{...local,url:ipv6,addresses:[ipv6]}]
  const before=JSON.stringify(rows)
  const list=collapse(rows)
  assert.equal(list.length,1)
  assert.deepEqual(new Set(list[0].addresses),new Set([lan,tail,ipv6]))
  assert.equal(list[0].address_count,3)
  assert.equal(JSON.stringify(rows),before,'API input must not be mutated')
})
test('legacy per-interface local entries keep their alternate routes', () => {
  const {collapse}=bridge()
  const list=collapse([{...local,id:'local-lan'},{...local,id:'local-tail',url:tail,addresses:[tail,ipv6]}])
  assert.equal(list.length,1)
  assert.equal(list[0].url,tail)
  assert.deepEqual(new Set(list[0].addresses),new Set([lan,tail,ipv6]))
})
test('the live bound record wins without dropping discovery aliases', () => {
  const {collapse}=bridge()
  const discovery={id:'node-peer',hostname:'OmniShare',url:'http://192.168.31.99:8081',online:false,bound:false}
  const bound={...discovery,url:'http://100.64.162.25:8081/',online:true,bound:true}
  for(const rows of [[discovery,bound],[bound,discovery]]) {
    const [item]=collapse(rows)
    assert.equal(item.bound,true);assert.equal(item.online,true)
    assert.equal(item.url,bound.url)
    assert.deepEqual(new Set(item.addresses),new Set([bound.url,discovery.url+'/']))
  }
})
test('same names or URLs do not merge different stable device identities', () => {
  const {collapse}=bridge()
  const rows=[{...local,id:'node-a'},{...local,id:'node-b'},
    {...local,id:'node-c',is_local:false},{...local,id:'node-d',is_local:false}]
  assert.equal(collapse(rows).length,4)
})
test('unsafe alternate destinations are excluded and safe origins canonicalized', () => {
  const {collapse}=bridge()
  const [item]=collapse([{...local,addresses:[lan,lan.slice(0,-1),null,
    'javascript:alert(1)','file:///tmp/a','http://user:password@host/',
    'https://host/?token=secret','https://host/#secret','https://host/admin',
    'HTTPS://EXAMPLE.COM:443/']}])
  assert.deepEqual(new Set(item.addresses),new Set([lan,'https://example.com/']))
  assert.equal(item.address_count,2)
})
test('unknown records are not collapsed into a single empty identity', () => {
  const {collapse}=bridge()
  const rows=[null,{hostname:'a'},{hostname:'b'},false]
  assert.equal(collapse(rows).length,2)
})
test('actual guarded fetch preserves route union and drops stale body headers', async () => {
  const original={code:0,data:[local,{...local,url:ipv6,addresses:[ipv6]}]}
  const {fetch,requests}=bridge(async()=>Response.json(original,{headers:{
    'Content-Length':'9999','Content-Encoding':'gzip','X-OmniShare-Test':'kept'}}))
  const response=await fetch('/api/v1/devices')
  const payload=await response.json()
  assert.equal(payload.data.length,1)
  assert.deepEqual(new Set(payload.data[0].addresses),new Set([lan,tail,ipv6]))
  assert.equal(response.headers.get('content-length'),null)
  assert.equal(response.headers.get('content-encoding'),null)
  assert.equal(response.headers.get('x-omnishare-test'),'kept')
  assert.equal(requests.length,1,'merging routes never probes or transfers remote content')
})
test('file-index responses pass through untouched and do not fetch file bytes', async () => {
  const response=Response.json({code:0,data:[{id:'node-peer~file',file_name:'movie.mp4'}]})
  const {fetch,requests}=bridge(async()=>response)
  assert.equal(await fetch('/api/v1/files?scope=all'),response)
  assert.equal(requests.length,1)
})
test('installUpdateEntry shows upgrade panel in non-Tauri mode and renders platform download link on click', async () => {
  const makeNode = tag => ({
    tagName: tag.toUpperCase(),
    dataset: {}, style: {}, children: [], attributes: {}, listeners: {},
    className: '', textContent: '', disabled: false, href: '', download: '', rel: '',
    classList: { removed: [], remove(c) { this.removed.push(c) }, contains() { return false } },
    setAttribute(k, v) { this.attributes[k] = v },
    appendChild(child) { this.children.push(child); return child },
    replaceChildren(...nodes) { this.children = nodes },
    addEventListener(type, fn) { this.listeners[type] = fn },
    querySelector(sel) {
      if (sel === '[data-update-status]') return this.children.find(c => 'updateStatus' in (c.dataset || {})) || null
      return null
    }
  })
  const panel = makeNode('div'), button = makeNode('button'), toast = makeNode('div')
  button.textContent = '检查更新'
  const requests = []
  const fetcher = async url => {
    requests.push(url)
    if (url === '/api/v1/health') return Response.json({ code: 0, data: { version: '0.2.0-rc.5' } })
    if (String(url).startsWith('https://api.github.com/repos/Azurefly/omnishare/releases')) {
      return Response.json([{
        tag_name: 'v0.2.0-rc.6', draft: false, prerelease: true,
        html_url: 'https://github.com/Azurefly/omnishare/releases/tag/v0.2.0-rc.6',
        assets: [
          { name: 'OmniShare-v0.2.0-rc.6-macos-universal.dmg', browser_download_url: 'https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.6/OmniShare-v0.2.0-rc.6-macos-universal.dmg' },
          { name: 'OmniShare-v0.2.0-rc.6-windows-x64-setup.exe', browser_download_url: 'https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.6/OmniShare-v0.2.0-rc.6-windows-x64-setup.exe' },
          { name: 'OmniShare-v0.2.0-rc.6-linux-amd64.AppImage', browser_download_url: 'https://github.com/Azurefly/omnishare/releases/download/v0.2.0-rc.6/OmniShare-v0.2.0-rc.6-linux-amd64.AppImage' }
        ]
      }])
    }
    throw new Error(`Unexpected fetch ${url}`)
  }
  const win = { setTimeout, fetch: fetcher }
  const context = vm.createContext({
    window: win, URL, Request, Response, Headers, AbortController,
    MutationObserver: class { observe() {} },
    navigator: { userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64)' },
    location: new URL('http://127.0.0.1:8082/'), console, setTimeout, clearTimeout,
    document: {
      readyState: 'complete',
      addEventListener() {}, dispatchEvent() {},
      createElement: makeNode,
      querySelector: sel => sel === '#desktopUpdates' ? panel : sel === '#checkUpdatesBtn' ? button : sel === '#toast' ? toast : null
    }
  })
  vm.runInContext(source, context)
  assert.deepEqual(panel.classList.removed, ['hidden'])
  assert.equal(requests.length, 0, 'opening page must never contact external release endpoints')
  await button.listeners.click()
  assert.equal(button.disabled, false)
  assert.equal(button.textContent, '检查更新')
  assert.equal(toast.textContent, '发现新版本 v0.2.0-rc.6')
  const box = panel.querySelector('[data-update-status]')
  assert.ok(box)
  assert.match(box.children[0].textContent, /发现新版本 v0\.2\.0-rc\.6/)
  assert.equal(box.children[1].tagName, 'A')
  assert.match(box.children[1].href, /OmniShare-v0\.2\.0-rc\.6-windows-x64-setup\.exe$/)
})
