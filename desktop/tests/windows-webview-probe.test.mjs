import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { checkedPort, checkedDebuggerURL, selectTarget, CdpConnection, validatePrivateState, persistedNoteExpression } from './windows-webview-probe.mjs';

const debugPort = 29999;
const appPort = 18881;
const endpoint = `ws://127.0.0.1:${debugPort}/devtools/page/ABC-123`;
const target = (url, socket = endpoint) => ({ type: 'page', url, webSocketDebuggerUrl: socket });

test('only unprivileged integer ports and exact loopback debugger URLs are accepted', () => {
  assert.equal(checkedPort('29999'), 29999);
  for (const value of [0, 80, 1023, 65536, '2e4', '20000\ninjected=x', NaN]) assert.throws(() => checkedPort(value));
  assert.equal(checkedDebuggerURL(endpoint, debugPort), endpoint);
  for (const value of [
    `ws://example.com:${debugPort}/devtools/page/ABC`,
    `wss://127.0.0.1:${debugPort}/devtools/page/ABC`,
    `ws://user:password@127.0.0.1:${debugPort}/devtools/page/ABC`,
    `ws://127.0.0.1:30000/devtools/page/ABC`,
    endpoint + '?secret=x', endpoint + '#fragment',
    `ws://127.0.0.1:${debugPort}/devtools/browser/ABC`,
  ]) assert.throws(() => checkedDebuggerURL(value, debugPort));
});

test('target inventory accepts the real bootstrap/business page and never exposes the init URL', () => {
  for (const url of ['tauri://localhost/', 'http://tauri.localhost/', `http://127.0.0.1:${appPort}/?init_token=never-log-this`]) {
    assert.equal(selectTarget([target(url)], debugPort, appPort), endpoint);
  }
  assert.equal(selectTarget([target('https://example.com/'), { ...target('http://tauri.localhost/'), type: 'service_worker' }], debugPort, appPort), null);
  assert.equal(selectTarget([target(`http://127.0.0.1:${appPort + 1}/`)], debugPort, appPort), null);
});

test('ambiguous targets and a debugger endpoint from another process are rejected', () => {
  assert.throws(() => selectTarget([target('http://tauri.localhost/'), target(`http://127.0.0.1:${appPort}/`)], debugPort, appPort), /Ambiguous/);
  assert.throws(() => selectTarget([target('http://tauri.localhost/', 'ws://untrusted.example/devtools/page/id')], debugPort, appPort));
  assert.throws(() => selectTarget({}, debugPort, appPort));
});

class FakeSocket extends EventTarget {
  sent = [];
  send(value) { this.sent.push(JSON.parse(value)); }
  respond(value) { this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(value) })); }
  close() { this.dispatchEvent(new Event('close')); }
}

test('CDP request correlation handles out-of-order responses without mistaking events for results', async () => {
  const socket = new FakeSocket();
  const client = new CdpConnection(socket, 1000);
  const first = client.send('Page.enable');
  const second = client.send('Runtime.evaluate', { expression: '1+1' });
  socket.respond({ method: 'Runtime.consoleAPICalled', params: { secret: 'never-log' } });
  socket.respond({ id: 2, result: { result: { value: 2 } } });
  socket.respond({ id: 1, result: {} });
  assert.deepEqual(await first, {});
  assert.equal((await second).result.value, 2);
  assert.equal(client.pending.size, 0);
  client.close();
});

test('CDP protocol errors are sanitized instead of copying credentials into test reports', async () => {
  const socket = new FakeSocket();
  const client = new CdpConnection(socket);
  const request = client.send('Runtime.evaluate', { expression: 'credential-input' });
  socket.respond({ id: 1, error: { message: 'URL?init_token=secret-and-access-key' } });
  await assert.rejects(request, error => error.message === 'WebView command failed: Runtime.evaluate');
  client.close();
});

test('closing a WebView rejects pending operations immediately', async () => {
  const socket = new FakeSocket();
  const client = new CdpConnection(socket, 10000);
  const request = client.send('Page.captureScreenshot');
  socket.close();
  await assert.rejects(request, /WebView connection closed: Page.captureScreenshot/);
  assert.equal(client.pending.size, 0);
});

test('a nonresponsive CDP command is bounded and late responses cannot resurrect it', async () => {
  const socket = new FakeSocket();
  const client = new CdpConnection(socket, 10);
  await assert.rejects(client.send('Runtime.evaluate'), /timed out/);
  socket.respond({ id: 1, result: { result: { value: true } } });
  assert.equal(client.pending.size, 0);
  client.close();
});

const state = { access_key: 'private-test-key-123456789', note_id: 'note-abc_123', note_content: 'OmniShare 安装验收：中文、空格 #100% — abc123' };

test('private state validation rejects selector injection and incomplete login state', () => {
  assert.deepEqual(validatePrivateState(state), state);
  for (const bad of [{ ...state, access_key: 'short' }, { ...state, note_id: 'id\"] ; alert(1)' }, { ...state, note_content: '' }, null]) {
    assert.throws(() => validatePrivateState(bad), /incomplete/);
  }
});

test('persisted note check executes against visible DOM state and handles Unicode/whitespace safely', () => {
  let scrolled = false;
  const geometry = { width: 600, height: 180, top: 150, bottom: 330 };
  const note = { hidden: false, textContent: `置顶 ${state.note_content.replaceAll(' ', '\n  ')} 复制`, getBoundingClientRect: () => geometry, scrollIntoView() { scrolled = true; } };
  const panel = { hidden: false, getBoundingClientRect: () => geometry };
  const context = {
    innerHeight: 800,
    getComputedStyle: () => ({ visibility: 'visible', display: 'block' }),
    document: { querySelector: selector => selector === '#notesPanel' ? panel : note },
  };
  const expression = persistedNoteExpression(state);
  assert.equal(vm.runInNewContext(expression, context), true);
  assert.equal(scrolled, true);
  note.textContent = 'A different note';
  assert.equal(vm.runInNewContext(expression, context), false);
  note.textContent = state.note_content; note.hidden = true;
  assert.equal(vm.runInNewContext(expression, context), false);
  note.hidden = false; geometry.top = 900; geometry.bottom = 1080;
  assert.equal(vm.runInNewContext(expression, context), false);
});
