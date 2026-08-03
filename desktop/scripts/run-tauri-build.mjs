import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const desktopRoot = join(__dirname, '..');
const tauriExecutable = join(
  desktopRoot,
  'node_modules',
  '.bin',
  process.platform === 'win32' ? 'tauri.cmd' : 'tauri'
);

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

const result = spawnSync(tauriExecutable, ['build', ...process.argv.slice(2)], {
  cwd: desktopRoot,
  env,
  stdio: 'inherit'
});

if (result.error) {
  console.error(`Unable to start Tauri CLI: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status ?? 1);
