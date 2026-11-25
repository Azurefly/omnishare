import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { setTimeout as delay } from 'node:timers/promises'
import { chromium, webkit } from '../frontend/node_modules/playwright/index.mjs'

const [binary, engine = 'chromium'] = process.argv.slice(2)
assert.ok(binary && ['chromium', 'webkit'].includes(engine), 'Usage: node scripts/test-file-inputs.mjs BACKEND [chromium|webkit]')
const portProbe = createServer()
portProbe.listen(0, '127.0.0.1')
await once(portProbe, 'listening')
const {port} = portProbe.address()
await new Promise(resolve => portProbe.close(resolve))
const origin = 'http://127.0.0.1:' + port
const root = await mkdtemp(join(tmpdir(), 'omnishare-file-inputs-'))
const folder = join(root, '资料 #1')
const source = join(folder, '子目录', '拷贝 路径.txt')
const uri = pathToFileURL(source).href
const child = spawn(resolve(binary), ['--no-browser','--listen','127.0.0.1','--port',String(port),
  '--data-dir',join(root,'workspace'),'--shutdown-on-stdin-close'], {stdio:['pipe','ignore','inherit']})
const exited = once(child, 'exit')
let browser
try {
  await mkdir(join(folder, '子目录'), {recursive:true})
  await writeFile(source, 'original bytes')
  for (let i = 0; i < 150; i++) {
    if (child.exitCode !== null) throw new Error('Backend stopped before becoming ready')
    if (await fetch(origin + '/api/v1/health').then(r => r.ok).catch(() => false)) break
    await delay(100)
  }
  browser = await ({webkit, chromium})[engine].launch()
  const context = await browser.newContext({serviceWorkers:'block'})
  const page = await context.newPage()
  const errors = []
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(origin)
  await page.locator('#dashUploadFileBtn').click()
  const shortcut = page.locator('#shortcutPath')
  await shortcut.fill(uri)
  const mod = process.platform === 'darwin' ? 'Meta' : 'Control'
  await shortcut.press(mod + '+a')
  await shortcut.press(mod + '+c')
  await shortcut.press('Backspace')
  await shortcut.press(mod + '+v')
  assert.equal(await shortcut.inputValue(), uri, 'System copy and paste must preserve the full file URL')
  await page.locator('#addShortcutBtn').click()
  await page.locator('#shortcutDialog').waitFor({state:'hidden'})
  const files = async () => (await (await fetch(origin + '/api/v1/files')).json()).data
  const link = (await files()).find(f => f.source_shortcut)
  assert.equal(link.file_name, basename(source))
  console.log('PASS keyboard copy/paste and local shortcut registration')

  // Exercise the receiving side of the desktop bridge separately from HTML5 drops.
  const nativeDrop = paths => page.evaluate(paths => window.dispatchEvent(new CustomEvent('omnishare:file-drop', {detail:{paths}})), paths)
  await page.locator('#openShortcutBtn').click()
  await nativeDrop([source])
  assert.equal(await shortcut.inputValue(), source)
  await page.locator('#closeShortcutBtn').click()
  assert.equal((await files()).length, 1, 'Cancelling a drop must leave the workspace untouched')
  await page.locator('#openUploadFileBtn').click()
  await page.locator('#fileUploadFolder').fill('目标')
  await page.locator('#fileTTL').selectOption('86400')
  await nativeDrop([folder])
  await page.locator('#uploadQueue [data-upload-state="succeeded"]').first().waitFor()
  const copy = (await files()).find(f => !f.source_shortcut)
  assert.equal(copy.folder, '目标/资料 #1/子目录')
  assert.ok(copy.expires_at)
  assert.equal(await (await fetch(origin + copy.download_url)).text(), 'original bytes')
  console.log('PASS native path event, nested folder import, TTL and byte-for-byte download')

  await page.locator('#openUploadFileBtn').click()
  await page.locator('#uploadPath').fill(uri)
  await page.locator('#importPathBtn').click()
  await page.waitForFunction(() => document.querySelectorAll('#uploadQueue [data-upload-state="succeeded"]').length === 2)
  await page.locator('#filesPanel').drop({files:{name:'拖入.txt', mimeType:'text/plain', buffer:Buffer.from('browser drop')}})
  await page.waitForFunction(() => document.querySelectorAll('#uploadQueue [data-upload-state="succeeded"]').length === 3)
  assert.equal((await files()).length, 4)
  console.log('PASS paste-path copy and HTML file drop through the real upload queue')

  await page.locator('#openUploadFileBtn').click()
  await page.locator('#dropZone').evaluate(zone => {
    const data = new DataTransfer()
    data.items.add(new File(['pasted bytes'], '已粘贴.txt', {type:'text/plain'}))
    const event = new Event('paste', {bubbles:true, cancelable:true})
    Object.defineProperty(event, 'clipboardData', {value:data})
    zone.dispatchEvent(event)
  })
  await page.waitForFunction(() => document.querySelectorAll('#uploadQueue [data-upload-state="succeeded"]').length === 4)
  assert.equal((await files()).length, 5)
  console.log('PASS clipboard file data enters the upload queue exactly once')

  await page.locator('#settingsBtn').click()
  await page.locator('#restoreBtn').click()
  const archive = join(root, '备份 #1.zip')
  const backup = await fetch(origin + '/api/v1/backup')
  assert.ok(backup.ok)
  await writeFile(archive, new Uint8Array(await backup.arrayBuffer()))
  let restores = 0
  page.on('request', req => { if (new URL(req.url()).pathname === '/api/v1/restore') restores++ })
  await page.locator('#restoreDialog').drop({files:archive})
  assert.equal(await page.locator('#restoreFile').evaluate(input => input.files[0].name), basename(archive))
  await page.locator('#restoreConfirm').check()
  await nativeDrop([archive])
  assert.equal(await page.locator('#restorePath').inputValue(), archive)
  assert.equal(await page.locator('#restoreConfirm').isChecked(), false)
  await page.locator('#restoreSubmitBtn').click()
  assert.equal(restores, 0)
  await page.locator('#restoreConfirm').check()
  await page.locator('#restoreSubmitBtn').click()
  await page.locator('#restoreDialog').waitFor({state:'hidden'})
  assert.equal(restores, 1)
  assert.equal((await files()).length, 5)
  assert.deepEqual(errors, [])
  console.log('PASS backup drop, path replacement, explicit confirmation and restore')
} finally {
  await browser?.close()
  child.stdin.end()
  await exited
  await rm(root, {recursive:true, force:true})
}
