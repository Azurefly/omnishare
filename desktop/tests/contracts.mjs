import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const desktopRoot = join(here, '..');
const repoRoot = join(desktopRoot, '..');

async function text(path) {
  return readFile(path, 'utf8');
}

function requireMatch(name, content, pattern, message) {
  if (!pattern.test(content)) {
    throw new Error(`${name}: ${message}`);
  }
}

const [cargo, main, tray, startup, manager, health, config, packageJson, commands, process, frontend] = await Promise.all([
  text(join(desktopRoot, 'src-tauri', 'Cargo.toml')),
  text(join(desktopRoot, 'src-tauri', 'src', 'main.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'tray', 'mod.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'runtime', 'startup.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'runtime', 'manager.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'runtime', 'health.rs')),
  text(join(desktopRoot, 'src-tauri', 'tauri.conf.json')),
  text(join(desktopRoot, 'package.json')),
  text(join(desktopRoot, 'src-tauri', 'src', 'commands', 'backend.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'process', 'backend.rs')),
  text(join(desktopRoot, 'frontend', 'index.html'))
]);

requireMatch('Cargo.toml', cargo, /tauri-plugin-single-instance\s*=\s*"2"/, 'single-instance plugin dependency is required');
requireMatch('main.rs', main, /tauri_plugin_single_instance::init/, 'single-instance plugin must be registered before setup');
requireMatch('main.rs', main, /tray::show_main_window/, 'second launch must restore the existing window');
requireMatch('main.rs', main, /if\s+settings\.configured/, 'configured desktop startup must run natively instead of waiting for WebView JavaScript');
requireMatch('main.rs', main, /startup::initialize\(&handle/, 'native worker must initialize the backend');
requireMatch('main.rs', main, /start_backend_watchdog/, 'desktop must supervise the backend after startup');
requireMatch('main.rs', main, /consecutive_failures\s*<\s*2/, 'watchdog must debounce transient health failures');
requireMatch('main.rs', main, /omnishare-backend-watchdog/, 'watchdog must use a named background thread');
requireMatch('tray/mod.rs', tray, /default_window_icon\(\)/, 'tray must use the packaged application icon');
requireMatch('tray/mod.rs', tray, /builder\s*=\s*builder\.icon/, 'tray icon must be explicitly assigned');
requireMatch('tray/mod.rs', tray, /window\.unminimize\(\)/, 'tray restore must unminimize the main window');
requireMatch('runtime/startup.rs', startup, /is_omnishare_backend/, 'startup must attach to an existing OmniShare backend');
requireMatch('runtime/startup.rs', startup, /reset_unhealthy/, 'startup must detach or stop an unhealthy backend before recovery');
requireMatch('runtime/startup.rs', startup, /diagnostics/, 'startup failures must include backend diagnostics');
requireMatch('runtime/startup.rs', startup, /OMNISHARE_DESKTOP_LOG_DIR/, 'test and managed deployments must be able to capture backend logs deterministically');
requireMatch('runtime/manager.rs', manager, /desired_running:\s*AtomicBool/, 'backend state must distinguish an intentional stop from an unexpected exit');
requireMatch('runtime/manager.rs', manager, /pub fn should_run/, 'watchdog must be able to respect intentional backend stops');
requireMatch('commands/backend.rs', commands, /pub fn desktop_boot[\s\S]*?Result<BackendNavigation, String>[\s\S]*?startup::initialize[^;]+\?;\s*state\.inner\(\)\.navigation\(\)/, 'boot must return the navigation credential even when native startup already started the backend');
requireMatch('commands/backend.rs', commands, /pub fn backend_status[^}]+state\.inner\(\)\.status\(\)/, 'ordinary status responses must not include initialization credentials');
requireMatch('runtime/manager.rs', manager, /#\[derive\(Serialize\)\]\s*pub struct BackendNavigation/, 'credential-bearing navigation responses must not derive Debug');
requireMatch('runtime/manager.rs', manager, /bootstrap::navigation_url\(&process\.url\(\), process\.managed_data_dir\(\)\)/, 'navigation must use only the managed backend data directory');
requireMatch('process/backend.rs', process, /pub fn managed_data_dir[^}]+self\.child\.is_some\(\)\s*&&\s*!self\.attached/, 'attached services must never receive a guessed initialization token');
requireMatch('process/backend.rs', process, /\.arg\("--data-dir"\)\s*\.arg\(data_dir\)/, 'the token directory must match the actual child process data directory');
requireMatch('frontend/index.html', frontend, /window\.location\.replace\(backend\.url\)/, 'the WebView must consume the navigation URL returned by desktop boot');
requireMatch('runtime/health.rs', health, /<title>OmniShare<\/title>/, 'health probe must identify OmniShare rather than any open TCP port');
requireMatch('tauri.conf.json', config, /"resources"\s*:\s*\[/, 'backend resources must be bundled');
requireMatch('tauri.conf.json', config, /"icons\/icon\.ico"/, 'Windows application icon must be bundled');
requireMatch('package.json', packageJson, /run-tauri-build\.mjs/, 'cross-platform build wrapper must be used');

console.log(`Desktop release contracts passed for ${repoRoot}`);

requireMatch('main.rs', main, /omnishare-backend-startup[\s\S]*?\.spawn\(move/, 'configured startup must run on a native worker, not the UI thread');
requireMatch('runtime/startup.rs', startup, /startup_guard\(\)\?/, 'all startup callers must serialize readiness and process spawning');
for (const name of ['desktop_boot', 'backend_start', 'backend_stop']) {
  requireMatch('commands/backend.rs', commands, new RegExp('#\\[tauri::command\\(async\\)\\]\\s*pub fn ' + name), name + ' must not block the native UI event loop');
}
requireMatch('process/backend.rs', process, /\.arg\("--no-browser"\)/, 'desktop startup must never open the external browser');
requireMatch('process/backend.rs', process, /CREATE_NO_WINDOW/, 'the Windows backend must not create a console window');

const updates = await text(join(desktopRoot, 'src-tauri', 'src', 'updates', 'mod.rs'));
requireMatch('updates/mod.rs', updates, /require_update_window/, 'updater commands must validate a native bundled window');
requireMatch('updates/mod.rs', updates, /update\.verify\(&bytes\)/, 'downloads and installation must verify signatures');
requireMatch('tauri.conf.json', config, /"requireSignedVersion"\s*:\s*true/, 'update signatures must bind the advertised version');
requireMatch('updates/mod.rs', updates, /pause_for_update\(\)/, 'install must stop the owned backend before replacement');
requireMatch('updates/mod.rs', updates, /MAX_PACKAGE/, 'update downloads must be bounded');
