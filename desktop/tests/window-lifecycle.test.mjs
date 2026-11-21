// Source-contract regressions. These do not replace a native macOS GUI test.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = resolve(process.env.OMNISHARE_TEST_SOURCE_ROOT || dirname(fileURLToPath(import.meta.url)),
  process.env.OMNISHARE_TEST_SOURCE_ROOT ? '.' : '..');
const main = readFileSync(resolve(root, 'src-tauri/src/main.rs'), 'utf8');
const tray = readFileSync(resolve(root, 'src-tauri/src/tray/mod.rs'), 'utf8');
const run = main.slice(main.indexOf('.run(|app, event|'));
const restore = tray.slice(tray.indexOf('pub fn show_main_window('), tray.indexOf('pub fn hide_main_window('));

test('macOS native Reopen restores the existing main window', () => {
  assert.match(run, /#\[cfg\(target_os = "macos"\)\]\s*if matches!\(event, RunEvent::Reopen \{ \.\. \}\)\s*\{\s*tray::show_main_window\(app\);\s*\}/);
});

test('Dock recovery is not gated on other windows being invisible', () => {
  assert.match(run, /RunEvent::Reopen/);
  // An updates window can be visible while the main window is hidden.
  assert.doesNotMatch(run, /has_visible_windows\s*:\s*false|!has_visible_windows/);
});

test('close hides rather than destroying the main WebView or stopping its backend', () => {
  const close = main.match(/if window\.label\(\) == "main" \{([\s\S]*?)\n            \}/)?.[1];
  assert.ok(close, 'main close handler is missing');
  assert.match(close, /api\.prevent_close\(\)/);
  assert.match(close, /window\.hide\(\)/);
  assert.doesNotMatch(close, /shutdown|\.exit\(|\.destroy\(|\.close\(/);
});

test('restore unhides the macOS application before showing and focusing the window', () => {
  assert.match(restore, /#\[cfg\(target_os = "macos"\)\]\s*if let Err\(error\) = app\.show\(\)/);
  const steps = ['app.show()', 'window.show()', 'window.unminimize()', 'window.set_focus()'];
  for (let i = 1; i < steps.length; i++) {
    assert.ok(restore.indexOf(steps[i - 1]) >= 0 && restore.indexOf(steps[i - 1]) < restore.indexOf(steps[i]));
  }
  assert.doesNotMatch(restore, /startup::initialize|WebviewWindowBuilder|window\.navigate|\.reload\(/);
});

test('late reopen cannot interrupt an orderly application shutdown', () => {
  assert.match(restore, /try_state::<crate::runtime::manager::BackendState>\(\)[\s\S]*?state\.is_exiting\(\)[\s\S]*?return;/);
  assert.ok(restore.indexOf('state.is_exiting()') < restore.indexOf('app.show()'));
  assert.match(run, /RunEvent::ExitRequested \{ \.\. \} \| RunEvent::Exit/);
  assert.match(run, /state\.shutdown\(\)/);
  assert.match(tray, /fn quit_application[\s\S]*?app\.exit\(0\)/);
});

test('single-instance, menu and tray paths share the same window restore function', () => {
  assert.match(main, /tauri_plugin_single_instance::init\(\|app, _args, _cwd\| \{\s*tray::show_main_window\(app\);/);
  assert.match(tray, /"app-show" => show_main_window\(app\)/);
  assert.match(tray, /"show" => show_main_window\(app\)/);
  assert.match(tray, /show_main_window\(tray\.app_handle\(\)\)/);
});
