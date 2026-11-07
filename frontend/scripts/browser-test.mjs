import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { readFile, mkdir, writeFile } from 'node:fs/promises'
import { resolve, extname, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'

// Exercise the real HTML/CSS/application in Chromium with synthetic API data.
// Backend authentication and persistence are tested separately by the Go and
// real-process HTTP suites. This server binds loopback and never uses user data.
const root = fileURLToPath(new URL('../web/', import.meta.url))
const output = fileURLToPath(new URL('../test-results/browser/', import.meta.url))
const {version} = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'))
const apiSource = await readFile(new URL('../../backend/internal/api/api.go', import.meta.url), 'utf8')
const csp = apiSource.match(/Set\("Content-Security-Policy", "([^"]+)"/)[1]
const stamp = '2026-11-07T08:30:00Z'
const notes = [{id:'n-demo', content:'今天的灵感，随时接着写。\n把文本、代码和文件留在自己的设备上。', tags:['灵感','工作'], updated_at:stamp, pinned:true}]
const files = [{id:'f-demo', file_name:'产品设计讨论记录.md', file_size:12680, mime_type:'text/markdown', download_count:2}]
const writes = []
let failDevices = false
let expectedDeviceConsoleErrors = 0
const config = {node_name:'我的工作空间', has_access_key:true, max_upload_mb:32, backup_max_mb:16384,
  retention_days:0, trash_retention_days:30, auto_open_browser:false, allow_lan:false,
  listen_address:'127.0.0.1', public_base_url:'', allowed_origins:[], peers:[]}
let origin
const respond = (res, data, status = 200) => {
  res.writeHead(status, {'Content-Type':'application/json; charset=utf-8', 'Cache-Control':'no-store',
    'X-Total-Count':Array.isArray(data) ? String(data.length) : '0'})
  res.end(JSON.stringify({code:status === 200 ? 0 : status, data}))
}
const server = createServer(async (req, res) => {
  try {
    res.setHeader('Content-Security-Policy', csp)
    res.setHeader('Referrer-Policy', 'no-referrer')
    res.setHeader('X-Content-Type-Options', 'nosniff')
    const url = new URL(req.url, origin)
    if (url.pathname.startsWith('/api/')) {
      if (req.method === 'POST') {
        const chunks = []
        for await (const chunk of req) chunks.push(chunk)
        const body = Buffer.concat(chunks)
        writes.push({path:url.pathname, body})
        if (url.pathname === '/api/v1/notes') {
          const note = {...JSON.parse(body), id:`n-${notes.length}`, updated_at:stamp}
          notes.unshift(note)
          return respond(res, note)
        }
        if (url.pathname === '/api/v1/files/upload') return respond(res, {id:'uploaded'})
        return respond(res, {})
      }
      if (url.pathname === '/api/v1/health') return respond(res, {version})
      if (url.pathname === '/api/v1/config') return respond(res, config)
      if (url.pathname === '/api/v1/dashboard') return respond(res, {notes_count:notes.length,
        files_count:files.length, pads_count:2, videos_count:0, active_shares:1, trash_count:0, storage_used:38948864})
      if (url.pathname === '/api/v1/devices' && failDevices) return respond(res, [], 400)
      if (url.pathname === '/api/v1/devices') return respond(res, [
        {id:'local', hostname:'我的工作空间', url:origin, is_local:true, online:true, network_type:'本机'},
        {id:'remote', hostname:'书房的笔记本', url:'https://desk.example', is_local:false, online:true, network_type:'manual', latency_ms:8},
        {id:'offline', hostname:'备用设备', url:'http://100.64.0.8:8081', is_local:false, online:false, network_type:'tailscale'}
      ])
      if (url.pathname === '/api/v1/notes') return respond(res, notes)
      if (url.pathname === '/api/v1/files') return respond(res, files)
      if (url.pathname === '/api/v1/pads') return respond(res, [{id:'pad-demo', title:'项目协作笔记', version:3, updated_at:stamp}])
      return respond(res, [])
    }
    const path = resolve(root, `.${decodeURIComponent(url.pathname === '/' ? '/index.html' : url.pathname)}`)
    if (!path.startsWith(root.endsWith(sep) ? root : root + sep)) { res.writeHead(403); return res.end() }
    const mime = {'.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml', '.webmanifest':'application/manifest+json'}[extname(path)] || 'application/octet-stream'
    const content = await readFile(path)
    res.writeHead(200, {'Content-Type':`${mime}; charset=utf-8`, 'Cache-Control':'no-store'})
    res.end(content)
  } catch (error) {
    res.writeHead(500, {'Content-Type':'text/plain'})
    res.end(String(error.message))
  }
})
await new Promise(resolveListen => server.listen(0, '127.0.0.1', resolveListen))
origin = `http://127.0.0.1:${server.address().port}`
await mkdir(output, {recursive:true})
let browser
let page
const results = []
const errors = []
const external = []
try {
  browser = await chromium.launch({headless:true})
  const context = await browser.newContext({viewport:{width:1440, height:1000}, serviceWorkers:'block',
    locale:'zh-CN', reducedMotion:'reduce'})
  await context.addInitScript(() => {
    if (!localStorage.getItem('omnishare.theme')) localStorage.setItem('omnishare.theme', 'light')
  })
  page = await context.newPage()
  page.setDefaultTimeout(10000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('console', message => {
    if (message.type() !== 'error') return
    // This one deliberate 400 tests the application's failure handling. Keep
    // every other resource/CSP error, plus all unhandled page errors, fatal.
    if (expectedDeviceConsoleErrors > 0 && failDevices &&
        message.location().url === `${origin}/api/v1/devices` &&
        /^Failed to load resource:.*\b400\b/.test(message.text())) {
      expectedDeviceConsoleErrors--
      return
    }
    errors.push(message.text())
  })
  page.on('request', request => {
    if (/^https?:/.test(request.url()) && !request.url().startsWith(origin + '/')) external.push(request.url())
  })
  const check = async (name, fn) => { await fn(); results.push(name); console.log(`PASS ${name}`) }
  const screenshot = name => page.screenshot({path:resolve(output, `${name}.png`), fullPage:true})
  const assertNoOverflow = async label => {
    const metrics = await page.evaluate(() => ({width:innerWidth, content:document.documentElement.scrollWidth}))
    assert.ok(metrics.content <= metrics.width + 1, `${label}: content ${metrics.content}px exceeds viewport ${metrics.width}px`)
  }
  await page.goto(origin, {waitUntil:'networkidle'})
  await page.waitForFunction(() => document.querySelector('#healthBadge')?.textContent.includes('v'))

  await check('desktop dashboard renders with device groups and no overflow', async () => {
    assert.equal(await page.locator('.tab[aria-current="page"]').getAttribute('data-tab'), 'dashboard')
    assert.match(await page.locator('#deviceList').innerText(), /当前节点[\s\S]*在线节点[\s\S]*离线节点/)
    assert.match(await page.locator('#nodeSecuritySummary').innerText(), /访问密钥已启用/)
    await assertNoOverflow('desktop dashboard')
    await screenshot('dashboard-light-desktop')
  })
  await check('all navigation entries open exactly one panel', async () => {
    for (const name of ['notes','files','pads','shares','trash','activity','dashboard']) {
      await page.locator(`.tab[data-tab="${name}"]`).click()
      await page.waitForFunction(tab => document.querySelector(`#${tab}Panel`)?.classList.contains('active'), name)
      assert.equal(await page.locator('.panel-view.active').count(), 1)
      assert.equal(await page.locator('.tab[aria-current="page"]').getAttribute('data-tab'), name)
      await assertNoOverflow(`desktop ${name}`)
    }
  })
  await check('a failed manual device refresh is handled without an unhandled rejection', async () => {
    expectedDeviceConsoleErrors = 1
    failDevices = true
    await page.locator('#refreshDevicesBtn').click()
    await page.waitForFunction(() => document.querySelector('#toast')?.textContent.includes('400'))
    failDevices = false
    await page.locator('#refreshDevicesBtn').click()
  })
  await check('dark theme renders and persists across reload', async () => {
    await page.locator('#themeToggle').click()
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark')
    assert.equal(await page.evaluate(() => localStorage.getItem('omnishare.theme')), 'dark')
    await page.reload({waitUntil:'networkidle'})
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark')
    await screenshot('dashboard-dark-desktop')
    await page.locator('#themeToggle').click()
  })
  await check('settings retain labels, controls and keyboard dismissal', async () => {
    await page.locator('#settingsBtn').click()
    await page.locator('#settingsDialog[open]').waitFor()
    for (const id of ['cfgNodeName','cfgTheme','cfgAllowLAN','cfgAccessKey','cfgMaxUpload','cfgBackupMax']) {
      const count = await page.locator(`#${id}`).evaluate(element => element.labels?.length || 0)
      assert.ok(count > 0, `${id} needs a visible label`)
    }
    await screenshot('settings-light-desktop')
    await page.keyboard.press('Escape')
    assert.equal(await page.locator('#settingsDialog').evaluate(dialog => dialog.open), false)
  })
  await check('clipboard preview, cancel and explicit send work through real browser controls', async () => {
    await context.grantPermissions(['clipboard-read','clipboard-write'], {origin})
    await page.locator('.tab[data-tab="notes"]').click()
    await page.evaluate(() => navigator.clipboard.writeText('剪贴板内容，确认之后再发送。'))
    const before = writes.length
    await page.locator('#pasteBtn').click()
    await page.locator('#clipboardDialog[open]').waitFor()
    assert.equal(await page.locator('#clipboardPreviewText').inputValue(), '剪贴板内容，确认之后再发送。')
    assert.equal(writes.length, before)
    await screenshot('clipboard-confirmation')
    await page.locator('#clipboardCancelBtn').click()
    assert.equal(writes.length, before)
    await page.locator('#pasteBtn').click()
    await page.locator('#clipboardDialog[open]').waitFor()
    await page.locator('#clipboardConfirmBtn').click()
    await page.waitForFunction(() => !document.querySelector('#clipboardDialog').open)
    assert.equal(writes.length, before + 1)
    assert.equal(writes.at(-1).path, '/api/v1/notes')
    assert.equal(JSON.parse(writes.at(-1).body).ttl_seconds, 86400)
  })
  await check('a real PNG clipboard image decodes locally before confirmed upload', async () => {
    await page.evaluate(async () => {
      const canvas = document.createElement('canvas')
      canvas.width = 2
      canvas.height = 2
      const drawing = canvas.getContext('2d')
      drawing.fillStyle = '#4f51d5'
      drawing.fillRect(0, 0, 2, 2)
      const blob = await new Promise(resolveBlob => canvas.toBlob(resolveBlob, 'image/png'))
      await navigator.clipboard.write([new ClipboardItem({'image/png':blob})])
    })
    const before = writes.length
    await page.locator('#pasteBtn').click()
    await page.locator('#clipboardDialog[open]').waitFor()
    const dimensions = await page.locator('#clipboardPreviewImage').evaluate(image => ({width:image.naturalWidth, height:image.naturalHeight, complete:image.complete}))
    assert.deepEqual(dimensions, {width:2, height:2, complete:true})
    assert.equal(writes.length, before)
    await screenshot('clipboard-image-confirmation')
    await page.locator('#clipboardCancelBtn').click()
    assert.equal(writes.length, before)
    await page.locator('#pasteBtn').click()
    await page.locator('#clipboardDialog[open]').waitFor()
    await page.locator('#clipboardConfirmBtn').click()
    await page.waitForFunction(() => [...document.querySelectorAll('#uploadQueue .upload-task')].some(task =>
      task.querySelector('.upload-file-name')?.textContent.startsWith('clipboard-') && task.dataset.uploadState === 'succeeded'))
    assert.equal(writes.length, before + 1)
    assert.equal(writes.at(-1).path, '/api/v1/files/upload')
    assert.match(writes.at(-1).body.toString(), /Content-Type: image\/png/)
  })
  await check('file retention control and upload queue remain operable', async () => {
    await page.locator('.tab[data-tab="files"]').click()
    let chooser = false
    const onChooser = () => { chooser = true }
    page.on('filechooser', onChooser)
    await page.locator('#fileTTL').selectOption('86400')
    assert.equal(chooser, false)
    page.off('filechooser', onChooser)
    const before = writes.filter(write => write.path === '/api/v1/files/upload').length
    await page.locator('#fileInput').setInputFiles({name:'浏览器验证.txt', mimeType:'text/plain', buffer:Buffer.from('synthetic upload fixture')})
    await page.waitForFunction(() => [...document.querySelectorAll('#uploadQueue .upload-task')].some(task =>
      task.querySelector('.upload-file-name')?.textContent === '浏览器验证.txt' && task.dataset.uploadState === 'succeeded'))
    assert.equal(writes.filter(write => write.path === '/api/v1/files/upload').length, before + 1)
    await screenshot('files-and-transfers-desktop')
    await page.locator('#clearFinishedUploadsBtn').click()
    assert.equal(await page.locator('#uploadQueue .progress-card').count(), 0)
  })
  await check('narrow and tablet layouts keep navigation, status and settings usable', async () => {
    for (const width of [320, 390, 768, 1024]) {
      await page.setViewportSize({width, height:844})
      for (const name of ['dashboard', 'notes', 'files', 'pads', 'shares', 'activity']) {
        await page.locator(`.tab[data-tab="${name}"]`).click()
        await assertNoOverflow(`${width}px ${name}`)
      }
      assert.equal(await page.locator('#healthBadge').isVisible(), true)
      await page.locator('#settingsBtn').click()
      await page.locator('#settingsDialog[open]').waitFor()
      await assertNoOverflow(`${width}px settings`)
      await page.keyboard.press('Escape')
      if (width === 390) {
        await page.locator('.tab[data-tab="dashboard"]').click()
        await screenshot('dashboard-light-mobile')
        await page.locator('.tab[data-tab="files"]').click()
        await screenshot('files-light-mobile')
      }
    }
  })
  await check('application emits no uncaught errors or unsolicited external requests', async () => {
    assert.deepEqual(errors, [])
    assert.deepEqual(external, [])
  })
  await writeFile(resolve(output, 'results.json'), JSON.stringify({version, success:true, tests:results, errors, external}, null, 2) + '\n')
} catch (error) {
  if (page) await page.screenshot({path:resolve(output, 'failure.png'), fullPage:true}).catch(() => {})
  await writeFile(resolve(output, 'results.json'), JSON.stringify({version, success:false, tests:results, error:String(error.stack), errors, external}, null, 2) + '\n')
  throw error
} finally {
  if (browser) await browser.close()
  await new Promise(resolveClose => server.close(resolveClose))
}
