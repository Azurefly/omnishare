'use strict';

const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { mkdtempSync, mkdirSync, readFileSync, renameSync, rmSync, symlinkSync, writeFileSync } = require('node:fs');
const { tmpdir } = require('node:os');
const { join } = require('node:path');
const test = require('node:test');
const { verifyInstallationEvidence } = require('./verify-installation-evidence.cjs');

const digest = bytes => createHash('sha256').update(bytes).digest('hex');
const passed = names => Object.fromEntries(names.map(name => [name, true]));
const windowsName = 'omnishare-windows-installation.json';
const ubuntuName = os => `omnishare-ubuntu-${os}-installation.json`;
// These are the public producer contract, not an import from the verifier under test.
const exerciseChecks = ['health_and_embedded_ui', 'first_run_authentication', 'notes_and_trash',
  'documents_and_conflicts', 'file_upload_download_and_range', 'share_access_and_revocation', 'encrypted_backup_restore'];
const persistenceChecks = ['health_and_embedded_ui', 'configuration_persistence',
  'note_and_counter_persistence', 'file_persistence', 'document_persistence'];
const windowsChecks = ['actual_installation', 'registration_and_shortcut', 'first_run_gui',
  'native_window_visible', 'data_directory', 'core_exercise', 'owned_backend_exited',
  'restart_gui_and_persistence', 'second_normal_exit', 'uninstalled', 'business_data_preserved'];
const aptChecks = ['apt_installed', 'glibc_baseline', 'desktop_entry_valid', 'apt_uninstalled', 'uninstall_preserves_data'];
const webkitChecks = ['first_run_webkit', 'webkit_rendered', 'bundled_backend', 'core_features',
  'core_content_in_webkit', 'normal_quit', 'restart_persistence'];

function features(version) {
  return Object.fromEntries([['exercise', exerciseChecks], ['verify', persistenceChecks]].map(([phase, checks]) =>
    [phase, { schema_version: 1, version, phase, success: true,
      scope: 'installed-backend-http-features', checks: passed(checks) }]));
}

function fixture(callback, version = '0.1.2') {
  const root = mkdtempSync(join(tmpdir(), 'omnishare-install-evidence-'));
  try {
    const names = {
      msi: `OmniShare_${version}_x64_en-US.msi`, nsis: `OmniShare_${version}_x64-setup.exe`,
      deb: `OmniShare_${version}_amd64.deb`, appimage: `OmniShare_${version}_amd64.AppImage`
    };
    const assets = {};
    for (const [kind, name] of Object.entries(names)) {
      const bytes = Buffer.concat([Buffer.from(`Actual fixture bytes for ${kind}\n`), Buffer.from([0, 255, 10, 13]), Buffer.from(name)]);
      writeFileSync(join(root, name), bytes);
      assets[kind] = { name, size: bytes.length, sha256: digest(bytes) };
    }
    const reports = {
      [windowsName]: {
        schema_version: 1, success: true, platform: 'windows', version,
        gui_method: 'visible native HWND + actual WebView2 CDP', quit_method: 'native Ctrl+Q accelerator',
        cold_runtime_install_verified: false, offline_runtime_install_verified: false, non_admin_install_verified: false,
        installers: ['msi', 'nsis'].map(kind => ({ kind, success: true, asset: { ...assets[kind] },
          checks: passed([...windowsChecks, ...(kind === 'msi' ? ['attach_conflict_watchdog'] : [])]), features: features(version) })),
        webview_runtime: {
          schema_version: 1, success: true, mode: 'offlineInstaller', tauri_cli: '2.12.4',
          cold_install_verified: false, offline_install_verified: false,
          scope: 'Native Authenticode verification of cached build inputs; cold/offline installation was not tested.',
          artifacts: ['msi', 'nsis'].map(kind => ({ kind, ...assets[kind] })),
          runtimes: ['msi', 'nsis'].map(kind => ({ kind, sha256: digest(`verified Microsoft payload ${kind}`),
            size: 150 * 1024 * 1024, product_version: '142.0.1000.1',
            signature_status: 'Valid', publisher: 'Microsoft Corporation' }))
        }
      }
    };
    for (const os of ['22.04', '24.04']) {
      reports[ubuntuName(os)] = {
        schema_version: 1, success: true, version, platform: { id: 'ubuntu', version: os }, normal_user: true,
        appimage_scope: 'extract-and-run', fuse_mount_tested: false, checks: passed(aptChecks),
        artifacts: Object.fromEntries(['deb', 'appimage'].map(kind => [kind, { name: assets[kind].name, sha256: assets[kind].sha256 }])),
        ...Object.fromEntries(['deb', 'appimage'].map(kind => [kind, { checks: passed(webkitChecks), features: features(version) }]))
      };
    }
    const save = () => {
      for (const [name, report] of Object.entries(reports)) writeFileSync(join(root, name), JSON.stringify(report));
    };
    save();
    const verify = () => verifyInstallationEvidence({ root, version });
    const reject = pattern => { save(); assert.throws(verify, pattern || /Invalid installation evidence:/); };
    callback({ root, version, names, assets, reports, windows: reports[windowsName], save, verify, reject });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

for (const version of ['0.1.2', '0.2.0-rc1']) {
  test(`all installed platforms are bound to real ${version} bytes and returned report hashes`, () => fixture(({ root, verify }) => {
    assert.deepEqual(verify(), [windowsName, ubuntuName('22.04'), ubuntuName('24.04')]
      .map(name => [name, digest(readFileSync(join(root, name)))]));
  }, version));
}

test('UTF-8 BOM is parsed but remains part of the published report digest', () => fixture(({ root, verify }) => {
  const file = join(root, windowsName);
  writeFileSync(file, Buffer.concat([Buffer.from([0xef, 0xbb, 0xbf]), readFileSync(file)]));
  assert.equal(verify()[0][1], digest(readFileSync(file)));
}));

test('report array order is irrelevant and explicit not-tested Windows fields remain false', () => fixture(({ windows, save, verify }) => {
  windows.installers.reverse();
  windows.webview_runtime.artifacts.reverse();
  windows.webview_runtime.runtimes.reverse();
  windows.webview_runtime.extra_safe_metadata = { inventory_only: true };
  save();
  assert.equal(verify().length, 3);
}));

for (const name of [windowsName, ubuntuName('22.04'), ubuntuName('24.04')]) {
  test(`missing report cannot publish: ${name}`, () => fixture(({ root, verify }) => {
    rmSync(join(root, name));
    assert.throws(verify, /could not be read/);
  }));
  for (const [label, modify] of [
    ['string schema', report => { report.schema_version = '1'; }],
    ['false success', report => { report.success = false; }],
    ['string success', report => { report.success = 'true'; }],
    ['old version', report => { report.version = '0.1.1'; }],
    ['contradictory failure', report => { report.failure = 'installation failed'; }]
  ]) {
    test(`${name} rejects ${label}`, () => fixture(({ reports, reject }) => { modify(reports[name]); reject(); }));
  }
}

for (const kind of ['msi', 'nsis', 'deb', 'appimage']) {
  test(`tampering with actual ${kind} package bytes is rejected`, () => fixture(({ root, names, verify }) => {
    const file = join(root, names[kind]);
    const changed = readFileSync(file);
    changed[0] ^= 1; // Same length: a size-only check would incorrectly pass.
    writeFileSync(file, changed);
    assert.throws(verify, /digest does not match/);
  }));
  test(`${kind} package symlink is rejected even when target bytes match`, () => fixture(({ root, names, verify }) => {
    const file = join(root, names[kind]);
    renameSync(file, `${file}.target`);
    symlinkSync(`${file}.target`, file);
    assert.throws(verify, /regular file/);
  }));
  test(`missing ${kind} package is rejected`, () => fixture(({ root, names, verify }) => {
    rmSync(join(root, names[kind]));
    assert.throws(verify, /could not be read/);
  }));
}

for (const form of ['symlink', 'directory', 'empty', 'malformed', 'too-large']) {
  test(`report ${form} is rejected without echoing raw data`, () => fixture(({ root, verify }) => {
    const file = join(root, windowsName);
    const secret = 'secret-do-not-copy-to-error';
    if (form === 'symlink') { renameSync(file, `${file}.target`); symlinkSync(`${file}.target`, file); }
    if (form === 'directory') { rmSync(file); mkdirSync(file); }
    if (form === 'empty') writeFileSync(file, '');
    if (form === 'malformed') writeFileSync(file, `{"access_key": "${secret}", broken`);
    if (form === 'too-large') writeFileSync(file, ' '.repeat(1024 * 1024 + 1));
    assert.throws(verify, error => /Invalid installation evidence:/.test(error.message) && !error.message.includes(secret));
  }));
}

for (const [label, modify] of [
  ['wrong platform', record => { record.platform = 'linux'; }],
  ['missing installer', record => { record.installers.pop(); }],
  ['duplicate installer', record => { record.installers[1] = structuredClone(record.installers[0]); }],
  ['third installer', record => { record.installers.push(structuredClone(record.installers[0])); }],
  ['installer object instead of list', record => { record.installers = {}; }],
  ['wrong architecture filename', record => { record.installers[0].asset.name = record.installers[0].asset.name.replace('x64', 'arm64'); }],
  ['path traversal filename', record => { record.installers[0].asset.name = '../candidate.msi'; }],
  ['changed asset name case', record => { record.installers[0].asset.name = record.installers[0].asset.name.toLowerCase(); }],
  ['wrong byte count', record => { record.installers[0].asset.size += 1; }],
  ['numeric string byte count', record => { record.installers[0].asset.size = String(record.installers[0].asset.size); }],
  ['uppercase digest', record => { record.installers[0].asset.sha256 = record.installers[0].asset.sha256.toUpperCase(); }],
  ['failed installer', record => { record.installers[1].success = false; }],
  ['string installer success', record => { record.installers[1].success = 'true'; }],
  ['cleanup failure', record => { record.installers[0].cleanup_uninstall_failed = true; }],
  ['HTTP-only GUI method', record => { record.gui_method = 'HTTP status 200'; }],
  ['forced process termination', record => { record.quit_method = 'Stop-Process -Force'; }],
  ['missing watchdog coverage', record => { delete record.installers[0].checks.attach_conflict_watchdog; }]
]) {
  test(`Windows rejects ${label}`, () => fixture(({ windows, reject }) => { modify(windows); reject(); }));
}

for (const name of windowsChecks) {
  test(`Windows requires real ${name} success for both installers`, () => fixture(({ windows, reject }) => {
    windows.installers[1].checks[name] = false;
    reject();
  }));
}
for (const bad of [0, 1, null, 'true', [], {}]) {
  test(`truthy or false-like check ${JSON.stringify(bad)} cannot replace true`, () => fixture(({ windows, reject }) => {
    windows.installers[0].checks.actual_installation = bad;
    reject();
  }));
}
for (const claim of ['cold_runtime_install_verified', 'offline_runtime_install_verified', 'non_admin_install_verified']) {
  test(`Windows refuses unsupported ${claim}`, () => fixture(({ windows, reject }) => { windows[claim] = true; reject(); }));
}

for (const os of ['22.04', '24.04']) {
  for (const [label, modify] of [
    ['different distribution', record => { record.platform.id = 'debian'; }],
    ['wrong Ubuntu version', record => { record.platform.version = os === '22.04' ? '24.04' : '22.04'; }],
    ['root execution', record => { record.normal_user = false; }],
    ['fake normal-user boolean', record => { record.normal_user = 'true'; }],
    ['unsupported FUSE claim', record => { record.fuse_mount_tested = true; }],
    ['fake FUSE boolean', record => { record.fuse_mount_tested = 'false'; }],
    ['unspecified AppImage scope', record => { delete record.appimage_scope; }],
    ['misleading AppImage scope', record => { record.appimage_scope = 'FUSE-mounted'; }],
    ['missing DEB identity', record => { delete record.artifacts.deb; }],
    ['server DEB substituted', record => { record.artifacts.deb.name = 'omnishare-server_0.1.2_amd64.deb'; }],
    ['missing AppImage phase', record => { delete record.appimage; }],
    ['unexpected validation check', record => { record.checks.extra_check = true; }]
  ]) {
    test(`Ubuntu ${os} rejects ${label}`, () => fixture(({ reports, reject }) => { modify(reports[ubuntuName(os)]); reject(); }));
  }
  for (const name of aptChecks) {
    test(`Ubuntu ${os} requires ${name}`, () => fixture(({ reports, reject }) => {
      delete reports[ubuntuName(os)].checks[name]; reject();
    }));
  }
  for (const flavor of ['deb', 'appimage']) {
    for (const name of webkitChecks) {
      test(`Ubuntu ${os} ${flavor} requires ${name}`, () => fixture(({ reports, reject }) => {
        reports[ubuntuName(os)][flavor].checks[name] = false; reject();
      }));
    }
  }
}

const featureTargets = [
  ['Windows MSI', fixture => fixture.windows.installers[0]],
  ['Windows NSIS', fixture => fixture.windows.installers[1]],
  ...['22.04', '24.04'].flatMap(os => ['deb', 'appimage'].map(flavor =>
    [`Ubuntu ${os} ${flavor}`, fixture => fixture.reports[ubuntuName(os)][flavor]]))
];
for (const [label, target] of featureTargets) {
  for (const [phase, names] of [['exercise', exerciseChecks], ['verify', persistenceChecks]]) {
    test(`${label} cannot omit ${phase}`, () => fixture(context => { delete target(context).features[phase]; context.reject(); }));
    for (const name of names) {
      test(`${label} ${phase} requires ${name}`, () => fixture(context => {
        delete target(context).features[phase].checks[name]; context.reject();
      }));
    }
  }
}
for (const [label, modify] of [
  ['fake feature schema', feature => { feature.schema_version = '1'; }],
  ['fake feature success', feature => { feature.success = 1; }],
  ['stale feature version', feature => { feature.version = '0.1.1'; }],
  ['replayed exercise phase', feature => { feature.phase = 'exercise'; }],
  ['wrong feature scope', feature => { feature.scope = 'unit-test'; }],
  ['feature error despite success', feature => { feature.error = 'did not persist'; }],
  ['fake feature check boolean', feature => { feature.checks.file_persistence = 'true'; }],
  ['unexpected feature check', feature => { feature.checks.unit_test_only = true; }]
]) {
  test(`rejects ${label}`, () => fixture(({ windows, reject }) => { modify(windows.installers[0].features.verify); reject(); }));
}

for (const key of ['access_key', 'accessKey', 'init_token', 'init-token', 'X-OmniShare-Key',
  'authorization', 'Cookie', 'password', 'passphrase', 'private_state', 'note_content', 'pad_content']) {
  test(`private field ${key} is rejected even in optional nested metadata`, () => fixture(({ windows, save, verify }) => {
    windows.extra = [{ nested: { [key]: 'private-value-never-logged' } }]; save();
    assert.throws(verify, error => /private field/.test(error.message) && !error.message.includes('private-value-never-logged'));
  }));
}
test('authentication query copied into a diagnostic URL is rejected without leaking it', () => fixture(({ reports, save, verify }) => {
  reports[ubuntuName('24.04')].diagnostic = 'http://127.0.0.1:4000/?init_token=do-not-log-me'; save();
  assert.throws(verify, error => /credential material/.test(error.message) && !error.message.includes('do-not-log-me'));
}));

for (const [label, modify] of [
  ['missing native provenance', report => { delete report.webview_runtime; }],
  ['wrong runtime schema', report => { report.webview_runtime.schema_version = '1'; }],
  ['failed runtime verification', report => { report.webview_runtime.success = false; }],
  ['download bootstrapper mode', report => { report.webview_runtime.mode = 'downloadBootstrapper'; }],
  ['different bundler version', report => { report.webview_runtime.tauri_cli = '2.13.0'; }],
  ['cold-machine claim', report => { report.webview_runtime.cold_install_verified = true; }],
  ['offline-install claim', report => { report.webview_runtime.offline_install_verified = true; }],
  ['fake not-tested boolean', report => { report.webview_runtime.offline_install_verified = 'false'; }],
  ['missing runtime payload', report => { report.webview_runtime.runtimes.pop(); }],
  ['duplicate runtime kind', report => { report.webview_runtime.runtimes[1].kind = 'msi'; }],
  ['missing installer binding', report => { report.webview_runtime.artifacts.pop(); }],
  ['duplicate installer binding', report => { report.webview_runtime.artifacts[1].kind = 'msi'; }],
  ['stale installer digest', report => { report.webview_runtime.artifacts[0].sha256 = '0'.repeat(64); }],
  ['stale installer size', report => { report.webview_runtime.artifacts[1].size += 1; }],
  ['wrong installer filename', report => { report.webview_runtime.artifacts[0].name = 'wrong.msi'; }],
  ['invalid payload digest', report => { report.webview_runtime.runtimes[0].sha256 = 'not-a-sha'; }],
  ['online bootstrapper size', report => { report.webview_runtime.runtimes[0].size = 2 * 1024 * 1024; }],
  ['unreviewed oversized payload', report => { report.webview_runtime.runtimes[0].size = 512 * 1024 * 1024 + 1; }],
  ['string payload size', report => { report.webview_runtime.runtimes[0].size = '150000000'; }],
  ['unsigned payload', report => { report.webview_runtime.runtimes[0].signature_status = 'NotSigned'; }],
  ['wrong publisher', report => { report.webview_runtime.runtimes[0].publisher = 'Other Corporation'; }],
  ['missing product version', report => { delete report.webview_runtime.runtimes[0].product_version; }],
  ['unparseable product version', report => { report.webview_runtime.runtimes[0].product_version = 'unknown'; }]
]) {
  test(`WebView rejects ${label}`, () => fixture(({ windows, reject }) => { modify(windows); reject(); }));
}

test('WebView product-version format follows native metadata verification without requiring a new payload download', () => fixture(({ windows, save, verify }) => {
  windows.webview_runtime.runtimes[0].product_version = '142.0.1000.1 (Official Build)';
  windows.webview_runtime.runtimes[1].product_version = '142.0';
  save(); assert.equal(verify().length, 3);
}));

test('an unsupported input version and a symlink asset directory are rejected', () => fixture(({ root, version }) => {
  assert.throws(() => verifyInstallationEvidence({ root, version: '../../other' }), /invalid version/);
  const link = `${root}-link`;
  try {
    symlinkSync(root, link, 'dir');
    assert.throws(() => verifyInstallationEvidence({ root: link, version }), /real directory/);
  } finally { rmSync(link, { force: true }); }
}));
