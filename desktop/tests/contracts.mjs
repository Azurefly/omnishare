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

const [cargo, main, tray, startup, health, config, packageJson] = await Promise.all([
  text(join(desktopRoot, 'src-tauri', 'Cargo.toml')),
  text(join(desktopRoot, 'src-tauri', 'src', 'main.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'tray', 'mod.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'runtime', 'startup.rs')),
  text(join(desktopRoot, 'src-tauri', 'src', 'runtime', 'health.rs')),
  text(join(desktopRoot, 'src-tauri', 'tauri.conf.json')),
  text(join(desktopRoot, 'package.json'))
]);

requireMatch('Cargo.toml', cargo, /tauri-plugin-single-instance\s*=\s*"2"/, 'single-instance plugin dependency is required');
requireMatch('main.rs', main, /tauri_plugin_single_instance::init/, 'single-instance plugin must be registered before setup');
requireMatch('main.rs', main, /tray::show_main_window/, 'second launch must restore the existing window');
requireMatch('main.rs', main, /if\s+settings\.configured/, 'configured desktop startup must run natively instead of waiting for WebView JavaScript');
requireMatch('main.rs', main, /startup::initialize\(app\.handle\(\)/, 'native setup must initialize the backend');
requireMatch('tray/mod.rs', tray, /default_window_icon\(\)/, 'tray must use the packaged application icon');
requireMatch('tray/mod.rs', tray, /builder\s*=\s*builder\.icon/, 'tray icon must be explicitly assigned');
requireMatch('tray/mod.rs', tray, /window\.unminimize\(\)/, 'tray restore must unminimize the main window');
requireMatch('runtime/startup.rs', startup, /is_omnishare_backend/, 'startup must attach to an existing OmniShare backend');
requireMatch('runtime/startup.rs', startup, /diagnostics/, 'startup failures must include backend diagnostics');
requireMatch('runtime/startup.rs', startup, /OMNISHARE_DESKTOP_LOG_DIR/, 'test and managed deployments must be able to capture backend logs deterministically');
requireMatch('runtime/health.rs', health, /<title>OmniShare<\/title>/, 'health probe must identify OmniShare rather than any open TCP port');
requireMatch('tauri.conf.json', config, /"resources"\s*:\s*\[/, 'backend resources must be bundled');
requireMatch('tauri.conf.json', config, /"icons\/icon\.ico"/, 'Windows application icon must be bundled');
requireMatch('package.json', packageJson, /run-tauri-build\.mjs/, 'cross-platform build wrapper must be used');

console.log(`Desktop release contracts passed for ${repoRoot}`);
