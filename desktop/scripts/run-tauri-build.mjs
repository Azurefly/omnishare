import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { buildPlan } from './bundle-versions.mjs';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const desktopRoot = join(__dirname, '..');
const tauriExecutable = join(desktopRoot, 'node_modules', '@tauri-apps', 'cli', 'tauri.js');

const env = { ...process.env };
const appleVariables = [
  'APPLE_CERTIFICATE',
  'APPLE_CERTIFICATE_PASSWORD',
  'APPLE_SIGNING_IDENTITY',
  'APPLE_ID',
  'APPLE_PASSWORD',
  'APPLE_TEAM_ID',
  'APPLE_API_ISSUER',
  'APPLE_API_KEY',
  'APPLE_API_KEY_PATH'
];

for (const name of appleVariables) {
  if (typeof env[name] === 'string' && env[name].trim() === '') {
    delete env[name];
  }
}

const version = JSON.parse(readFileSync(join(desktopRoot, 'src-tauri', 'tauri.conf.json'), 'utf8')).version;
const plan = buildPlan(version, process.platform, process.argv.slice(2));
const configDir = mkdtempSync(join(tmpdir(), 'omnishare-bundle-'));
let status = 0;
try {
  for (const [index, step] of plan.entries()) {
    const args = [...step.args];
    if (step.config) {
      const configPath = join(configDir, `${index}.json`);
      writeFileSync(configPath, JSON.stringify(step.config));
      args.push('--config', configPath);
    }
    // Invoke the JS entry point directly: no cmd.exe quoting of JSON or paths.
    const result = spawnSync(process.execPath, [tauriExecutable, ...args], {
      cwd: desktopRoot, env, stdio: 'inherit'
    });
    if (result.error) console.error(`Unable to start Tauri CLI: ${result.error.message}`);
    status = result.status ?? 1;
    if (status !== 0) break;
  }
} finally {
  rmSync(configDir, { recursive: true, force: true });
}
process.exit(status);
