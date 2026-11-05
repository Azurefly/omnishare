import assert from 'node:assert/strict';
import { buildPlan, bundleVersions } from '../scripts/bundle-versions.mjs';

assert.deepEqual(bundleVersions('1.3.0-rc2'), { msi: '1.3.602', rpmVersion: '1.3.0', rpmRelease: '0.rc2', prerelease: true });
const ordered = ['1.3.0-alpha1', '1.3.0-alpha2', '1.3.0-beta1', '1.3.0-rc1', '1.3.0-rc2', '1.3.0', '1.3.1-alpha0', '1.3.1'];
const builds = ordered.map(v => Number(bundleVersions(v).msi.split('.')[2]));
assert.deepEqual(builds, [...new Set(builds)].sort((a, b) => a - b));
assert.equal(bundleVersions('1.3.0-rc.2+build.7').msi, '1.3.602');
assert.equal(bundleVersions('1.3.0').rpmRelease, '1');
for (const v of ['1.3.0-preview1', '1.3.0-rc100', '1.3.65', '256.0.0', '1.256.0', 'invalid']) {
  assert.throws(() => bundleVersions(v));
}
assert.equal(buildPlan('1.3.0-rc2', 'win32')[0].config.bundle.windows.wix.version, '1.3.602');
assert.deepEqual(buildPlan('1.3.0-rc2', 'darwin', ['--target', 'universal-apple-darwin']), [{ args: ['build', '--target', 'universal-apple-darwin'] }]);
const linux = buildPlan('1.3.0-rc2', 'linux');
assert.deepEqual(linux[0], { args: ['build', '--bundles', 'deb,appimage'] });
assert.deepEqual(linux[1], { args: ['bundle', '--bundles', 'rpm'], config: { version: '1.3.0', bundle: { linux: { rpm: { release: '0.rc2' } } } } });
assert.deepEqual(buildPlan('1.3.0', 'linux'), [{ args: ['build'] }]);
assert.deepEqual(buildPlan('1.3.0-rc2', 'linux', ['--no-bundle']), [{ args: ['build', '--no-bundle'] }]);
assert.equal(buildPlan('1.3.0-rc2', 'linux', ['--bundles=deb']).length, 1);
const rpm = buildPlan('1.3.0-rc2', 'linux', ['--bundles', 'rpm', '--target', 'x86_64-unknown-linux-gnu', '--debug']);
assert.ok(rpm[0].args.includes('--no-bundle'));
assert.deepEqual(rpm[1].args, ['bundle', '--target', 'x86_64-unknown-linux-gnu', '--debug', '--bundles', 'rpm']);
console.log('Desktop bundle version mapping and build plans passed');

// Exercise the real wrapper without compiling Rust: inspect exactly what the CLI
// receives, including temporary JSON configs, failure propagation and cleanup.
const { mkdtempSync, mkdirSync, copyFileSync, writeFileSync, readFileSync, existsSync, rmSync } = await import('node:fs');
const { tmpdir } = await import('node:os');
const { join, dirname } = await import('node:path');
const { fileURLToPath } = await import('node:url');
const { spawnSync } = await import('node:child_process');
if (process.platform === 'linux') {
  const root = mkdtempSync(join(tmpdir(), 'omnishare-wrapper-test-'));
  try {
    for (const dir of ['scripts', 'src-tauri', 'node_modules/@tauri-apps/cli']) mkdirSync(join(root, dir), { recursive: true });
    for (const name of ['run-tauri-build.mjs', 'bundle-versions.mjs']) {
      copyFileSync(join(dirname(fileURLToPath(import.meta.url)), '../scripts', name), join(root, 'scripts', name));
    }
    writeFileSync(join(root, 'src-tauri/tauri.conf.json'), JSON.stringify({ version: '1.3.0-rc2' }));
    writeFileSync(join(root, 'node_modules/@tauri-apps/cli/tauri.js'), `
      const fs = require('node:fs');
      const args = process.argv.slice(2);
      const path = args[args.indexOf('--config') + 1];
      fs.appendFileSync(process.env.TEST_LOG, JSON.stringify({ args, config: args.includes('--config') ? JSON.parse(fs.readFileSync(path)) : null }) + '\\n');
      process.exit(Number(process.env.TEST_EXIT || 0));
    `);
    const log = join(root, 'calls.jsonl');
    let result = spawnSync(process.execPath, [join(root, 'scripts/run-tauri-build.mjs')], { env: { ...process.env, TEST_LOG: log } });
    assert.equal(result.status, 0, result.stderr.toString());
    const calls = readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line));
    assert.equal(calls.length, 2);
    assert.equal(calls[0].args[0], 'build');
    assert.equal(calls[1].args[0], 'bundle');
    assert.equal(calls[1].config.bundle.linux.rpm.release, '0.rc2');
    assert.equal(existsSync(calls[1].args.at(-1)), false, 'temporary config must be removed');
    writeFileSync(log, '');
    result = spawnSync(process.execPath, [join(root, 'scripts/run-tauri-build.mjs')], { env: { ...process.env, TEST_LOG: log, TEST_EXIT: '42' } });
    assert.equal(result.status, 42);
    assert.equal(readFileSync(log, 'utf8').trim().split('\n').length, 1, 'bundling must not run after a failed build');
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
  console.log('Desktop build wrapper execution, cleanup and failure propagation passed');
}
