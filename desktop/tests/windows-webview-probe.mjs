import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

// This driver attaches to the actual installed application's WebView2. The
// PowerShell parent enables CDP only in its test child, verifies the listener's
// loopback address and process ancestry, and checks a visible native window.
// No Chromium download, mock page, application test hook, or production flag is used.
export function checkedPort(value) {
  if (!/^\d+$/.test(String(value)) || Number(value) < 1024 || Number(value) > 65535) {
    throw new Error('Expected an unprivileged local TCP port');
  }
  return Number(value);
}

export function checkedDebuggerURL(value, port) {
  const url = new URL(value);
  if (url.protocol !== 'ws:' || !['127.0.0.1', 'localhost', '[::1]'].includes(url.hostname)
      || Number(url.port) !== checkedPort(port) || url.username || url.password
      || url.search || url.hash || !/^\/devtools\/page\/[A-Za-z0-9._-]+$/.test(url.pathname)) {
    throw new Error('Refusing a nonlocal or unexpected WebView debugger endpoint');
  }
  return url.href;
}

export function selectTarget(targets, debugPort, appPort) {
  if (!Array.isArray(targets)) throw new Error('Invalid WebView target inventory');
  const origins = new Set(['tauri://localhost', 'http://tauri.localhost', 'https://tauri.localhost', `http://127.0.0.1:${checkedPort(appPort)}`]);
  const candidates = targets.filter(target => {
    if (target?.type !== 'page' || typeof target.url !== 'string') return false;
    try {
      const url = new URL(target.url);
      const origin = url.protocol === 'tauri:' ? `${url.protocol}//${url.host}` : url.origin;
      return !url.username && !url.password && origins.has(origin);
    } catch { return false; }
  });
  if (candidates.length > 1) throw new Error('Ambiguous installed WebView target');
  if (!candidates.length) return null;
  return checkedDebuggerURL(candidates[0].webSocketDebuggerUrl, debugPort);
}

export class CdpConnection {
  constructor(socket, timeout = 12000) {
    this.socket = socket;
    this.timeout = timeout;
    this.nextId = 0;
    this.pending = new Map();
    socket.addEventListener('message', event => {
      let response;
      try { response = JSON.parse(String(event.data)); } catch { this.failAll(); return; }
      const item = this.pending.get(response.id);
      if (!item) return;
      clearTimeout(item.timer);
      this.pending.delete(response.id);
      // Never copy raw protocol errors/URLs/console events into uploaded evidence:
      // they can contain the first-run token or the test account's access key.
      if (response.error) item.reject(new Error(`WebView command failed: ${item.method}`));
      else item.resolve(response.result);
    });
    socket.addEventListener('close', () => this.failAll());
    socket.addEventListener('error', () => this.failAll());
  }
  failAll() {
    for (const item of this.pending.values()) {
      clearTimeout(item.timer);
      item.reject(new Error(`WebView connection closed: ${item.method}`));
    }
    this.pending.clear();
  }
  send(method, params = {}) {
    const id = ++this.nextId;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`WebView command timed out: ${method}`));
      }, this.timeout);
      this.pending.set(id, { resolve, reject, timer, method });
      try { this.socket.send(JSON.stringify({ id, method, params })); }
      catch { clearTimeout(timer); this.pending.delete(id); reject(new Error(`Cannot send WebView command: ${method}`)); }
    });
  }
  close() { this.failAll(); this.socket.close(); }
}

const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(check, description, timeout = 45000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = await check();
    if (value) return value;
    await pause(250);
  }
  throw new Error(`Timed out waiting for ${description}`);
}

async function connect(debugPort, appPort) {
  const url = await until(async () => {
    let response;
    try { response = await fetch(`http://127.0.0.1:${debugPort}/json/list`, { signal: AbortSignal.timeout(1500) }); }
    catch { return null; }
    if (!response.ok) return null;
    return selectTarget(await response.json(), debugPort, appPort);
  }, 'the installed WebView target');
  const socket = new WebSocket(url);
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => { socket.close(); reject(new Error('WebView connection timed out')); }, 10000);
    socket.addEventListener('open', () => { clearTimeout(timer); resolve(); }, { once: true });
    socket.addEventListener('error', () => { clearTimeout(timer); reject(new Error('WebView connection failed')); }, { once: true });
  });
  return new CdpConnection(socket);
}

async function evaluate(cdp, expression) {
  const result = await cdp.send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true, userGesture: true });
  if (result.exceptionDetails || result.result?.subtype === 'error') throw new Error('Installed WebView script failed');
  return result.result?.value;
}

const visibleFunction = `(element) => {
  if (!element || element.hidden) return false;
  const box = element.getBoundingClientRect();
  const style = getComputedStyle(element);
  return box.width > 0 && box.height > 0 && style.visibility !== 'hidden' && style.display !== 'none';
}`;

export function validatePrivateState(state) {
  if (!state || typeof state.access_key !== 'string' || state.access_key.length < 16
      || typeof state.note_id !== 'string' || !/^[A-Za-z0-9_-]+$/.test(state.note_id)
      || typeof state.note_content !== 'string' || !state.note_content.trim()) {
    throw new Error('Private installed-feature state is incomplete');
  }
  return { access_key: state.access_key, note_id: state.note_id, note_content: state.note_content };
}

export function persistedNoteExpression(state) {
  const checked = validatePrivateState(state);
  const content = checked.note_content.replace(/\s+/g, '');
  return String.raw`(() => {
    const visible = ${visibleFunction};
    const note = document.querySelector('[data-note-id="${checked.note_id}"]');
    if (!visible(document.querySelector('#notesPanel')) || !visible(note)) return false;
    note.scrollIntoView({block:'center'});
    const box = note.getBoundingClientRect();
    return box.bottom > 0 && box.top < innerHeight && note.textContent.replace(/\s+/g, '').includes(${JSON.stringify(content)});
  })()`;
}

export async function probe({ debugPort, appPort, firstRun, state, reportPath, screenshotPath }) {
  debugPort = checkedPort(debugPort); appPort = checkedPort(appPort);
  const report = {
    schema_version: 1, success: false, driver: 'actual-webview2-cdp',
    checks: { first_run_form: false, first_run_submit: false, business_page_visible: false,
      health_badge_visible: false, authenticated_after_restart: false, persisted_note_visible: false },
    first_run_requested: Boolean(firstRun), restart_verification_requested: Boolean(state),
    screenshot: screenshotPath ? resolve(screenshotPath) : null,
  };
  let cdp;
  try {
    cdp = await connect(debugPort, appPort);
    await cdp.send('Page.enable');
    if (firstRun) {
      await until(() => evaluate(cdp, `(() => { const visible = ${visibleFunction}; return document.visibilityState === 'visible' && visible(document.querySelector('#port-form')) && document.querySelector('#title')?.textContent === '首次启动设置'; })()`), 'the visible first-run port form');
      report.checks.first_run_form = true;
      await evaluate(cdp, `(() => { const input = document.querySelector('#port'); input.value = ${JSON.stringify(String(appPort))}; input.dispatchEvent(new Event('input', {bubbles:true})); input.dispatchEvent(new Event('change', {bubbles:true})); document.querySelector('#start').click(); return true; })()`);
      report.checks.first_run_submit = true;
    }
    await until(async () => {
      try {
        return await evaluate(cdp, `(() => { const visible = ${visibleFunction}; return location.origin === ${JSON.stringify(`http://127.0.0.1:${appPort}`)} && document.visibilityState === 'visible' && visible(document.querySelector('#mainContent')) && visible(document.querySelector('#workspaceTitle')); })()`);
      } catch { return false; } // The same WebView changes execution context during navigation.
    }, 'the actual business page in the installed WebView');
    report.checks.business_page_visible = true;
    if (state) {
      const privateState = validatePrivateState(state);
      await until(() => evaluate(cdp, `document.querySelector('#authDialog')?.open === true`), 'the access-key dialog after restart');
      await evaluate(cdp, `(() => { const input = document.querySelector('#authKey'); input.value = ${JSON.stringify(privateState.access_key)}; input.dispatchEvent(new Event('input', {bubbles:true})); document.querySelector('#authSubmitBtn').click(); return true; })()`);
      await until(() => evaluate(cdp, `document.querySelector('#authDialog')?.open === false && document.querySelector('#stats')?.children.length > 0`), 'successful authentication in the restarted WebView');
      await evaluate(cdp, `document.querySelector('#authKey').value = ''; document.querySelector('[data-tab="notes"]').click(); true`);
      report.checks.authenticated_after_restart = true;
      await until(() => evaluate(cdp, persistedNoteExpression(privateState)), 'the persisted note in the visible notes panel');
      report.checks.persisted_note_visible = true;
    }
    await until(() => evaluate(cdp, `(() => { const visible = ${visibleFunction}; const badge = document.querySelector('#healthBadge'); return visible(badge) && badge.getAttribute('aria-label')?.startsWith('已连接到 ') && !document.querySelector('#authDialog')?.open; })()`), 'the connected health badge');
    report.checks.health_badge_visible = true;
    if (screenshotPath) {
      // The query credential has been consumed; never capture before navigation.
      const clean = await evaluate(cdp, `location.search === '' && location.hash === ''`);
      if (!clean) throw new Error('Refusing a screenshot before navigation credentials are removed');
      const capture = await cdp.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      await mkdir(dirname(resolve(screenshotPath)), { recursive: true });
      await writeFile(screenshotPath, Buffer.from(capture.data, 'base64'));
    }
    report.success = true;
  } catch (error) {
    // All errors produced above are bounded descriptions, never response bodies.
    report.failure = error instanceof Error ? error.message : 'WebView probe failed';
    throw error;
  } finally {
    cdp?.close();
    await mkdir(dirname(resolve(reportPath)), { recursive: true });
    await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n');
  }
  return report;
}

async function main(args) {
  const options = Object.create(null);
  for (let i = 0; i < args.length; i++) {
    const name = args[i];
    if (name === '--first-run') { options.firstRun = true; continue; }
    if (!['--debug-port', '--app-port', '--state', '--report', '--screenshot'].includes(name) || !args[i + 1]) throw new Error('Invalid WebView probe arguments');
    options[name.slice(2)] = args[++i];
  }
  let state;
  if (options.state) {
    if (['report', 'screenshot'].some(name => options[name] && resolve(options[name]) === resolve(options.state))) throw new Error('Private state cannot be an evidence output');
    const buffer = await readFile(options.state);
    if (buffer.length > 1048576) throw new Error('Private state is unexpectedly large');
    try { state = validatePrivateState(JSON.parse(buffer.toString('utf8'))); }
    catch { throw new Error('Cannot read private installed-feature state'); }
  }
  if (!options.report) throw new Error('A WebView report path is required');
  await probe({ debugPort: options['debug-port'], appPort: options['app-port'], firstRun: options.firstRun,
    state, reportPath: options.report, screenshotPath: options.screenshot });
  console.log('Installed WebView2 GUI probe passed.');
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2)).catch(() => { console.error('Installed WebView2 GUI probe failed; inspect its sanitized report.'); process.exitCode = 1; });
}
