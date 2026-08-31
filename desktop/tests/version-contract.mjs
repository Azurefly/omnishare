import { readFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const desktopRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const repoRoot = join(desktopRoot, '..');
const [rootVersionText, packageText, cargoText, tauriText] = await Promise.all([
  readFile(join(repoRoot, 'VERSION'), 'utf8'),
  readFile(join(desktopRoot, 'package.json'), 'utf8'),
  readFile(join(desktopRoot, 'src-tauri', 'Cargo.toml'), 'utf8'),
  readFile(join(desktopRoot, 'src-tauri', 'tauri.conf.json'), 'utf8')
]);

const rootVersion = rootVersionText.trim();
const packageVersion = JSON.parse(packageText).version;
const tauriVersion = JSON.parse(tauriText).version;
const cargoMatch = cargoText.match(/^version\s*=\s*"([^"]+)"/m);
if (!cargoMatch) {
  throw new Error('Cargo package version was not found');
}
const cargoVersion = cargoMatch[1];
const versions = { rootVersion, packageVersion, cargoVersion, tauriVersion };

if (!/^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$/.test(rootVersion)) {
  throw new Error(`Invalid root semantic version: ${rootVersion}`);
}
if (new Set(Object.values(versions)).size !== 1) {
  throw new Error(`Desktop version sources are inconsistent: ${JSON.stringify(versions)}`);
}

console.log(`Desktop release version contract passed: ${rootVersion}`);
