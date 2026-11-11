import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { cpSync, mkdtempSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const workflow = readFileSync(join(repo, '.github/workflows/publish-desktop-prerelease.yml'), 'utf8');
const desktopWorkflow = readFileSync(join(repo, '.github/workflows/desktop-release.yml'), 'utf8');
const version = '1.3.0-rc3';
const stableVersion = '0.1.1';
const { COMMON_CHECKS, DISTRIBUTION_CHECKS } = createRequire(import.meta.url)('./verify-macos-evidence.cjs');
const macChecks = [...COMMON_CHECKS, ...DISTRIBUTION_CHECKS];
const testTeam = 'AB12CD34EF';

// Execute the actual workflow scripts against fixtures. This catches publishing
// gate regressions without contacting GitHub or creating a tag/release.
function stepScript(name) {
  const lines = workflow.split('\n');
  const start = lines.findIndex(line => line.trim() === `- name: ${name}`);
  assert.ok(start >= 0, `workflow step missing: ${name}`);
  let run = start + 1;
  while (run < lines.length && !lines[run].trim().startsWith('- name:') && lines[run].trim() !== 'run: |') run++;
  assert.equal(lines[run]?.trim(), 'run: |', `script missing from ${name}`);
  const indent = lines[run].length - lines[run].trimStart().length + 2;
  const script = [];
  for (const line of lines.slice(run + 1)) {
    if (line.trim() && line.length - line.trimStart().length < indent) break;
    script.push(line.slice(indent));
  }
  return script.join('\n');
}

function jobSource(name) {
  const start = workflow.indexOf(`\n  ${name}:\n`);
  assert.ok(start >= 0, `workflow job missing: ${name}`);
  return workflow.slice(start + 1).split(/\n(?=  [a-zA-Z0-9_-]+:\n)/)[0];
}

function jobNeeds(job) {
  const lines = job.split('\n');
  const index = lines.findIndex(line => /^    needs:/.test(line));
  assert.ok(index >= 0, 'job dependency list is missing');
  const inline = lines[index].replace(/^    needs:\s*/, '').trim();
  if (inline) return inline.replace(/^\[|\]$/g, '').split(',').map(value => value.trim());
  const dependencies = [];
  for (const line of lines.slice(index + 1)) {
    const match = /^      - ([a-zA-Z0-9_-]+)$/.exec(line);
    if (!match) break;
    dependencies.push(match[1]);
  }
  return dependencies;
}

const manifestScript = stepScript('Read manifest');
const assetsScript = stepScript('Verify staged assets');
const tagScript = stepScript('Verify existing tag target');
const publishedAssetsScript = stepScript('Verify published release assets');
const macVerificationScript = stepScript('Verify and launch the actual macOS release ZIP and DMG');
const publishMacGateScript = stepScript('Require notarized macOS evidence before publishing');
const modeScript = stepScript('Select macOS validation mode');

function fixture(run) {
  const root = mkdtempSync(join(tmpdir(), 'omnishare-release-contract-'));
  try {
    return run(root);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

function execute(script, cwd, env = {}) {
  return spawnSync('bash', ['-e', '-o', 'pipefail', '-c', script], {
    cwd,
    env: { ...process.env, RELEASE_VERSION: version, GITHUB_OUTPUT: join(cwd, 'outputs'),
      MACOS_VERIFICATION_MODE: 'release', EXPECTED_TEAM_ID: testTeam, ...env },
    encoding: 'utf8'
  });
}

function expectSuccess(result) {
  assert.equal(result.status, 0, result.error?.message || result.stderr || result.stdout);
}

function expectFailure(result) {
  assert.notEqual(result.status, 0, 'invalid release inputs were accepted');
  assert.equal(result.error, undefined, 'validation could not be executed');
}

function writeManifest(root, overrides = {}, candidate = version) {
  mkdirSync(join(root, 'release'));
  mkdirSync(join(root, 'docs'));
  writeFileSync(join(root, 'VERSION'), `${candidate}\n`);
  writeFileSync(join(root, `docs/RELEASE_NOTES_v${candidate}.md`), '# Candidate release\n');
  writeFileSync(join(root, 'release/desktop-release.json'), JSON.stringify({
    tag: `v${candidate}`,
    name: `OmniShare v${candidate}`,
    notes: `docs/RELEASE_NOTES_v${candidate}.md`,
    prerelease: candidate.includes('-'),
    macos: { signing_mode: 'developer-id', notarized: true, server_zip_signed: true,
      dmg_published: true, minimum_system_version: '12.0', tauri_published: false },
    ...overrides
  }));
}

const digest = value => createHash('sha256').update(value).digest('hex');
const featureChecks = {
  exercise: ['health_and_embedded_ui', 'first_run_authentication', 'notes_and_trash',
    'documents_and_conflicts', 'file_upload_download_and_range', 'share_access_and_revocation', 'encrypted_backup_restore'],
  verify: ['health_and_embedded_ui', 'configuration_persistence', 'note_and_counter_persistence',
    'file_persistence', 'document_persistence']
};
const featureEvidence = candidate => Object.fromEntries(Object.entries(featureChecks).map(([phase, checks]) =>
  [phase, { schema_version: 1, version: candidate, phase, success: true, scope: 'installed-backend-http-features',
    checks: Object.fromEntries(checks.map(check => [check, true])) }]));

function writeAssets(root, candidate = version, mode = 'release') {
  mkdirSync(join(root, 'scripts'), { recursive: true });
  cpSync(join(repo, 'scripts/verify-macos-evidence.cjs'), join(root, 'scripts/verify-macos-evidence.cjs'));
  cpSync(join(repo, 'scripts/verify-installation-evidence.cjs'), join(root, 'scripts/verify-installation-evidence.cjs'));
  const assets = join(root, 'release-assets');
  mkdirSync(assets);
  const put = (name, content = `fixture ${name}\n`) => writeFileSync(join(assets, name), content);
  const server = ['amd64', 'arm64'].flatMap(arch => [
    `OmniShare-v${candidate}-windows-${arch}.zip`,
    `OmniShare-v${candidate}-linux-${arch}.tar.gz`,
    `omnishare-server_${candidate}_${arch}.deb`
  ]);
  const macos = ['amd64', 'arm64'].map(arch => `OmniShare-v${candidate}-macos-${arch}.zip`);
  const macDmgs = ['amd64', 'arm64'].map(arch => `OmniShare-v${candidate}-macos-${arch}.dmg`);
  const macEvidence = ['amd64', 'arm64'].map(arch => `omnishare-macos-${arch}-verification.json`);
  const windows = [`OmniShare_${candidate}_x64_en-US.msi`, `OmniShare_${candidate}_x64-setup.exe`];
  const [baseVersion, suffix] = candidate.split('-');
  const rpmRelease = suffix ? `0.${suffix.replace('.', '')}` : '1';
  const linux = [`OmniShare_${candidate}_amd64.AppImage`, `OmniShare_${candidate}_amd64.deb`, `OmniShare-${baseVersion}-${rpmRelease}.x86_64.rpm`];
  const docs = ['README.md', `RELEASE_NOTES_v${candidate}.md`, 'SECURE_SHARING.md'];
  for (const name of [...server, ...macos, ...macDmgs, ...windows, ...linux, ...docs]) put(name);
  put('SHA256SUMS.txt', [...server, ...docs].map(name => `${digest(readFileSync(join(assets, name)))}  ${name}\n`).join(''));
  for (const [index, arch] of ['amd64', 'arm64'].entries()) {
    const release = mode === 'release';
    const assessment = app => ({
      assessment_exit_code: release ? 0 : 3, assessment_result: release ? 'accepted' : 'rejected',
      assessment_source: release ? 'Notarized Developer ID' : null,
      quarantine_verified: release,
      ...(app ? { syspolicy_check: { available: true, exit_code: release ? 0 : 3, result: release ? 'accepted' : 'rejected' } } : {})
    });
    const notary = kind => ({ submission_id: release ? `12345678-abcd-1234-abcd-123456789ab${kind === 'app' ? 'c' : 'd'}` : null,
      status: release ? 'Accepted' : 'NotSubmitted', stapled: release });
    put(macEvidence[index], JSON.stringify({
      schema_version: 2, success: true, version: candidate, arch, asset: macos[index],
      sha256: digest(readFileSync(join(assets, macos[index]))),
      mode, production_ready: release, signing_mode: release ? 'developer-id' : 'ad-hoc',
      team_id: release ? testTeam : null, minimum_macos: '12.0',
      artifacts: Object.fromEntries([['zip', macos[index]], ['dmg', macDmgs[index]]].map(([kind, name]) =>
        [kind, { name, sha256: digest(readFileSync(join(assets, name))), size: readFileSync(join(assets, name)).length }])),
      packaging: { name: `omnishare-macos-${arch}-packaging.json`, sha256: '1'.repeat(64) },
      notarization: { app: notary('app'), dmg: notary('dmg') },
      checks: Object.fromEntries(macChecks.map(check => [check, release || COMMON_CHECKS.includes(check)])),
      gatekeeper: { notarized: release, first_download_verified: false, quarantine_policy_verified: release,
        simulation: 'explicit-quarantine-on-isolated-copies', policy_enabled: true,
        zip_app: assessment(true), dmg: assessment(false), dmg_app: assessment(true) },
      runtime: Object.fromEntries(['zip', 'dmg'].map(kind => [kind, { launch_method: 'open -n -W',
        loopback_only: true, browser_suppressed: true, quarantined: false, features: featureEvidence(candidate) }]))
    }));
  }
  put('omnishare-windows-sha256.json', '\uFEFF' + JSON.stringify(windows.map(name => ({
    Path: `D:\\build evidence\\bundle\\${name}`,
    SHA256: digest(readFileSync(join(assets, name))).toUpperCase()
  }))));
  put('omnishare-linux-sha256.txt', linux.map(name =>
    `${digest(readFileSync(join(assets, name)))}  desktop/src-tauri/target/release/bundle/${name}\n`
  ).join('') + `${'0'.repeat(64)}  desktop/src-tauri/target/release/bundle/appimage/OmniShare.AppDir/usr/lib/not-a-public-asset.so\n`);
  for (const name of ['omnishare-windows-runtime.junit.xml', 'omnishare-windows-watchdog.junit.xml']) {
    put(name, '\uFEFF<testsuite tests="2" failures="0"><testcase name="passed"/></testsuite>\n');
  }
  put('omnishare-windows-watchdog.json', '\uFEFF' + JSON.stringify({ success: true }));
  const windowsChecks = ['actual_installation', 'registration_and_shortcut', 'first_run_gui',
    'native_window_visible', 'data_directory', 'core_exercise', 'owned_backend_exited',
    'restart_gui_and_persistence', 'second_normal_exit', 'uninstalled', 'business_data_preserved'];
  put('omnishare-windows-installation.json', JSON.stringify({
    schema_version: 1, version: candidate, platform: 'windows', success: true,
    gui_method: 'visible native HWND + actual WebView2 CDP', quit_method: 'native Ctrl+Q accelerator',
    cold_runtime_install_verified: false, offline_runtime_install_verified: false, non_admin_install_verified: false,
    webview_runtime: {
      schema_version: 1, success: true, mode: 'offlineInstaller', tauri_cli: '2.12.4',
      cold_install_verified: false, offline_install_verified: false,
      artifacts: windows.map((name, index) => ({kind: index === 0 ? 'msi' : 'nsis', name,
        size: readFileSync(join(assets, name)).length, sha256: digest(readFileSync(join(assets, name)))})),
      runtimes: ['msi', 'nsis'].map(kind => ({kind, sha256: '9'.repeat(64), size: 100 * 1024 * 1024,
        product_version: '157.0.0.0', signature_status: 'Valid', publisher: 'Microsoft Corporation'}))
    },
    installers: windows.map((name, index) => ({kind: index === 0 ? 'msi' : 'nsis', success: true,
      asset: {name, size: readFileSync(join(assets, name)).length, sha256: digest(readFileSync(join(assets, name)))},
      checks: Object.fromEntries([...windowsChecks, ...(index === 0 ? ['attach_conflict_watchdog'] : [])].map(check => [check, true])),
      features: featureEvidence(candidate)
    }))
  }));
  for (const ubuntu of ['22.04', '24.04']) {
    const appChecks = ['first_run_webkit', 'webkit_rendered', 'bundled_backend', 'core_features',
      'core_content_in_webkit', 'normal_quit', 'restart_persistence'];
    const app = () => ({checks: Object.fromEntries(appChecks.map(check => [check, true])), features: featureEvidence(candidate)});
    put(`omnishare-ubuntu-${ubuntu}-installation.json`, JSON.stringify({
      schema_version: 1, version: candidate, success: true, platform: {id: 'ubuntu', version: ubuntu},
      normal_user: true, appimage_scope: 'extract-and-run', fuse_mount_tested: false,
      artifacts: Object.fromEntries([['deb', linux[1]], ['appimage', linux[0]]].map(([kind, name]) =>
        [kind, {name, sha256: digest(readFileSync(join(assets, name)))}])),
      checks: Object.fromEntries(['apt_installed', 'glibc_baseline', 'desktop_entry_valid', 'apt_uninstalled',
        'uninstall_preserves_data'].map(check => [check, true])), deb: app(), appimage: app()
    }));
  }
  const mutateMacEvidence = (arch, mutate) => {
    const name = `omnishare-macos-${arch}-verification.json`;
    const evidence = JSON.parse(readFileSync(join(assets, name), 'utf8'));
    mutate(evidence);
    put(name, JSON.stringify(evidence));
  };
  return { assets, server, macos, macDmgs, macEvidence, windows, linux, docs, put, mutateMacEvidence };
}

for (const candidate of [stableVersion, '0.1.0-rc1', version]) {
  test(`matching ${candidate} manifest produces the correct release type and latest policy`, () => fixture(root => {
    writeManifest(root, {}, candidate);
    expectSuccess(execute(manifestScript, root));
    const prerelease = candidate.includes('-');
    assert.equal(readFileSync(join(root, 'outputs'), 'utf8'),
      `version=${candidate}\ntag=v${candidate}\nname=OmniShare v${candidate}\nnotes=docs/RELEASE_NOTES_v${candidate}.md\nprerelease=${prerelease}\nmake_latest=${!prerelease}\n`);
  }));
}

for (const [label, overrides] of [
  ['mismatched tag', { tag: 'v1.3.0-rc2' }],
  ['mismatched notes', { notes: 'docs/other.md' }],
  ['multiline metadata', { name: 'candidate\ntag=v9.0.0' }],
  ['stable publication flag for an RC version', { prerelease: false }]
]) {
  test(`manifest rejects ${label}`, () => fixture(root => {
    writeManifest(root, overrides);
    expectFailure(execute(manifestScript, root));
  }));
}

test(`manifest rejects a prerelease flag for stable ${stableVersion}`, () => fixture(root => {
  writeManifest(root, { prerelease: true }, stableVersion);
  const result = execute(manifestScript, root);
  expectFailure(result);
  assert.match(result.stderr, /boolean matching the version prerelease suffix/);
}));

for (const candidate of [stableVersion, version]) {
  for (const [label, value] of [['string true', 'true'], ['string false', 'false'], ['zero', 0], ['one', 1], ['null', null], ['missing', undefined]]) {
    test(`${candidate} manifest rejects ${label} instead of a prerelease boolean`, () => fixture(root => {
      writeManifest(root, { prerelease: value }, candidate);
      const result = execute(manifestScript, root);
      expectFailure(result);
      assert.match(result.stderr, /boolean matching the version prerelease suffix/);
    }));
  }
}

test('manifest rejects missing release notes', () => fixture(root => {
  writeManifest(root);
  rmSync(join(root, `docs/RELEASE_NOTES_v${version}.md`));
  expectFailure(execute(manifestScript, root));
}));

for (const candidate of [stableVersion, version]) {
  test(`${candidate} verifies 29 assets and binds all installers and native evidence to final checksums`, () => fixture(root => {
    const { assets, server, docs, macos, macDmgs, macEvidence } = writeAssets(root, candidate);
    assert.equal(readdirSync(assets).length, 29);
    const checksums = () => readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8');
    assert.equal(checksums().trim().split('\n').length, 9);
    assert.ok(macos.every(name => !checksums().includes(name)), 'Linux staging must not certify macOS archives');
    expectSuccess(execute(assetsScript, root, { RELEASE_VERSION: candidate }));
    const names = checksums().trim().split('\n').map(line => line.slice(66));
    assert.deepEqual(names.sort(), readdirSync(assets).filter(name => name !== 'SHA256SUMS.txt').sort());
    assert.equal(new Set(names).size, 28, 'final checksum entries must not be duplicated');
    expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: assets, encoding: 'utf8' }));
    const verified = checksums();
    expectSuccess(execute(assetsScript, root, { RELEASE_VERSION: candidate }));
    assert.equal(checksums(), verified, 'rechecking staged assets must not append duplicate macOS checksums');
  }));
}

for (const name of ['README.md', `RELEASE_NOTES_v${stableVersion}.md`, 'SECURE_SHARING.md']) {
  test(`stable release requires ${name} in the server checksum list`, () => fixture(root => {
    const { assets, put } = writeAssets(root, stableVersion);
    put('SHA256SUMS.txt', readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8')
      .split('\n').filter(line => !line.endsWith(`  ${name}`)).join('\n'));
    const result = execute(assetsScript, root, { RELEASE_VERSION: stableVersion });
    expectFailure(result);
    assert.ok(result.stderr.includes(`Missing or incorrect SHA256 for ${name}`), result.stderr);
  }));
}

for (const [label, existing, collision, source] of [
  ['legacy server and desktop Debian basenames', `OmniShare_${version}_amd64.deb`, `omnishare_${version}_amd64.deb`, `omnishare-server_${version}_amd64.deb`],
  ['checksum filenames differing only by case', 'SHA256SUMS.txt', 'sha256sums.TXT', 'SHA256SUMS.txt']
]) {
  test(`asset verification rejects ${label}`, () => fixture(root => {
    const { assets, put } = writeAssets(root);
    // Keep every required package intact. Only the extra case-variant filename
    // is invalid, so a missing target or checksum cannot hide a broken guard.
    put(collision, readFileSync(join(assets, source)));
    expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: assets, encoding: 'utf8' }));
    const result = execute(assetsScript, root);
    expectFailure(result);
    assert.match(result.stderr, /Release asset names must be unique ignoring case:/);
    assert.ok(result.stderr.includes(existing), result.stderr);
    assert.ok(result.stderr.includes(collision), result.stderr);
  }));
}

for (const [label, mutate] of [
  ['missing server target', ({ assets, server }) => rmSync(join(assets, server[0]))],
  ['server package omitted from checksum list', ({ assets, put }) => put('SHA256SUMS.txt', readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8').split('\n').slice(1).join('\n'))],
  ['modified Windows installer', ({ windows, put }) => put(windows[0], 'corrupted')],
  ['modified Linux package', ({ linux, put }) => put(linux[2], 'corrupted')],
  ['missing Linux format', ({ assets, linux }) => rmSync(join(assets, linux[0]))],
  ['missing watchdog evidence', ({ assets }) => rmSync(join(assets, 'omnishare-windows-watchdog.json'))],
  ['failed runtime evidence', ({ put }) => put('omnishare-windows-runtime.junit.xml', '<testsuite tests="1" failures="1"><failure/></testsuite>')],
  ['failed watchdog recovery', ({ put }) => put('omnishare-windows-watchdog.json', JSON.stringify({ success: false }))],
  ['unsigned macOS Tauri installer', ({ put }) => put('OmniShare_1.3.0-rc3_universal.dmg')]
]) {
  test(`asset verification rejects ${label}`, () => fixture(root => {
    const assets = writeAssets(root);
    mutate(assets);
    expectFailure(execute(assetsScript, root));
  }));
}

for (const arch of ['amd64', 'arm64']) {
  for (const kind of ['archive', 'installer', 'evidence']) {
    test(`asset verification requires the ${arch} macOS ${kind}`, () => fixture(root => {
      const { assets } = writeAssets(root);
      const name = kind === 'archive' ? `OmniShare-v${version}-macos-${arch}.zip` :
        kind === 'installer' ? `OmniShare-v${version}-macos-${arch}.dmg` : `omnishare-macos-${arch}-verification.json`;
      rmSync(join(assets, name));
      expectFailure(execute(assetsScript, root));
    }));
  }
}

for (const [label, mutate] of [
  ['unsupported schema', evidence => { evidence.schema_version = 1; }],
  ['string schema version', evidence => { evidence.schema_version = '2'; }],
  ['unsuccessful verification', evidence => { evidence.success = false; }],
  ['string success flag', evidence => { evidence.success = 'true'; }],
  ['missing success flag', evidence => { delete evidence.success; }],
  ['different release version', evidence => { evidence.version = '0.1.0'; }],
  ['different architecture', evidence => { evidence.arch = 'amd64'; }],
  ['different archive basename', evidence => { evidence.asset = `OmniShare-v${version}-macos-amd64.zip`; }],
  ['archive path instead of basename', evidence => { evidence.asset = `../${evidence.asset}`; }],
  ['different archive digest', evidence => { evidence.sha256 = '0'.repeat(64); }],
  ['uppercase digest', evidence => { evidence.sha256 = evidence.sha256.toUpperCase(); }],
  ['missing digest', evidence => { delete evidence.sha256; }],
  ['unsigned bundle', evidence => { evidence.signing_mode = 'unsigned'; }],
  ['ad-hoc signing fallback', evidence => { evidence.signing_mode = 'ad-hoc'; }],
  ['unvalidated minimum macOS version', evidence => { evidence.minimum_macos = '11.0'; }],
  ['missing checks', evidence => { delete evidence.checks; }],
  ['missing Gatekeeper limits', evidence => { delete evidence.gatekeeper; }],
  ['missing notarization', evidence => { evidence.gatekeeper.notarized = false; }],
  ['false first-download approval claim', evidence => { evidence.gatekeeper.first_download_verified = true; }],
  ['string notarization flag', evidence => { evidence.gatekeeper.notarized = 'true'; }],
  ['missing first-download status', evidence => { delete evidence.gatekeeper.first_download_verified; }],
  ['wrong signing team', evidence => { evidence.team_id = 'ZZ99ZZ99ZZ'; }],
  ['rehearsal mode', evidence => { evidence.mode = 'rehearsal'; }],
  ['not production ready', evidence => { evidence.production_ready = false; }],
  ['wrong DMG digest', evidence => { evidence.artifacts.dmg.sha256 = '0'.repeat(64); }],
  ['wrong DMG byte count', evidence => { evidence.artifacts.dmg.size += 1; }],
  ['missing packaging provenance', evidence => { delete evidence.packaging; }],
  ['notarization still in progress', evidence => { evidence.notarization.app.status = 'In Progress'; }],
  ['DMG notarization rejected', evidence => { evidence.notarization.dmg.status = 'Invalid'; }],
  ['missing app ticket', evidence => { evidence.notarization.app.stapled = false; }],
  ['missing DMG ticket', evidence => { evidence.notarization.dmg.stapled = false; }],
  ['malformed notary submission', evidence => { evidence.notarization.app.submission_id = 'accepted'; }],
  ['reused notary submission', evidence => { evidence.notarization.dmg.submission_id = evidence.notarization.app.submission_id; }],
  ['Gatekeeper disabled', evidence => { evidence.gatekeeper.policy_enabled = false; }],
  ['missing quarantine policy proof', evidence => { evidence.gatekeeper.quarantine_policy_verified = false; }],
  ['misleading runtime scope', evidence => { evidence.runtime.zip.quarantined = true; }],
  ['unverified DMG runtime', evidence => { delete evidence.runtime.dmg; }]
]) {
  test(`macOS evidence rejects ${label} without partially updating checksums`, () => fixture(root => {
    const { assets, mutateMacEvidence } = writeAssets(root);
    // Fail the second architecture after the first one is valid. No macOS
    // archive or evidence may be certified before the complete gate succeeds.
    mutateMacEvidence('arm64', mutate);
    const before = readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8');
    expectFailure(execute(assetsScript, root));
    assert.equal(readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8'), before);
  }));
}

for (const check of macChecks) {
  for (const [label, value] of [['failed', false], ['missing', undefined], ['string', 'true']]) {
    test(`macOS evidence rejects a ${label} ${check} result`, () => fixture(root => {
      const { mutateMacEvidence } = writeAssets(root);
      mutateMacEvidence('amd64', evidence => { evidence.checks[check] = value; });
      expectFailure(execute(assetsScript, root));
    }));
  }
}

test('modified macOS archive is rejected even if the old checksum list certifies its new bytes', () => fixture(root => {
  const { assets, macos, put } = writeAssets(root);
  put(macos[1], 'modified after native verification');
  const checksums = readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8');
  put('SHA256SUMS.txt', checksums + macos.map(name =>
    `${digest(readFileSync(join(assets, name)))}  ${name}\n`).join(''));
  expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: assets, encoding: 'utf8' }));
  expectFailure(execute(assetsScript, root));
}));

test('a legacy combined checksum list cannot replace native bundle verification', () => fixture(root => {
  const { assets, macos, put, mutateMacEvidence } = writeAssets(root);
  const checksums = readFileSync(join(assets, 'SHA256SUMS.txt'), 'utf8');
  put('SHA256SUMS.txt', checksums + macos.map(name =>
    `${digest(readFileSync(join(assets, name)))}  ${name}\n`).join(''));
  mutateMacEvidence('amd64', evidence => { evidence.checks.resource_tamper_rejected = false; });
  expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: assets, encoding: 'utf8' }));
  expectFailure(execute(assetsScript, root));
}));

for (const name of ['omnishare-darwin-arm64', `OmniShare-v${version}-macos-unverified.zip`]) {
  test(`asset verification rejects unexpected release payload ${name}`, () => fixture(root => {
    const { put } = writeAssets(root);
    put(name, 'not a verified public release asset');
    expectFailure(execute(assetsScript, root));
  }));
}

test('publication waits for every build and only runs on main push or dispatch', () => {
  const verify = jobSource('verify-assets');
  const publish = jobSource('publish');
  assert.deepEqual(jobNeeds(verify).sort(), ['release-metadata', 'build-server', 'verify-macos', 'build-and-test', 'verify-ubuntu'].sort());
  assert.match(verify, /name: verified-prerelease-assets/);
  assert.match(publish, /needs:\s*\n\s*- release-metadata\s*\n\s*- verify-assets/);
  assert.match(publish, /name: Create GitHub release\s*\n\s*if: github\.ref == 'refs\/heads\/main' && \(github\.event_name == 'push' \|\| github\.event_name == 'workflow_dispatch'\)/);
  assert.match(publish, /name: Download combined verified assets[\s\S]+name: verified-prerelease-assets/);
  assert.match(workflow, /pull_request:\s*\n\s*paths:/);
  assert.doesNotMatch(workflow, /pull_request_target:/);
  assert.match(workflow, /run: \.\/scripts\/build-release\.sh/);
  assert.match(workflow, /name: release-assets-Server/);
  assert.match(workflow, /pattern: release-assets-\*/);
  assert.match(workflow, /prerelease: \$\{\{ steps\.metadata\.outputs\.prerelease \}\}/);
  assert.match(workflow, /make_latest: \$\{\{ steps\.metadata\.outputs\.make_latest \}\}/);
  assert.match(publish, /prerelease: \$\{\{ needs\.release-metadata\.outputs\.prerelease \}\}/);
  assert.match(publish, /make_latest: \$\{\{ needs\.release-metadata\.outputs\.make_latest \}\}/);
  assert.match(publish, /RELEASE_PRERELEASE: \$\{\{ needs\.release-metadata\.outputs\.prerelease \}\}/);
  assert.ok(publish.indexOf('- name: Verify published release assets') > publish.indexOf('- name: Publish GitHub release'));
});

test('native macOS signing and independent verification run on matching architectures', () => {
  const server = jobSource('build-server');
  const macos = jobSource('build-macos');
  const verification = jobSource('verify-macos');
  assert.deepEqual(jobNeeds(macos).sort(), ['release-metadata', 'build-server'].sort());
  assert.deepEqual(jobNeeds(verification).sort(), ['release-metadata', 'build-macos'].sort());
  for (const job of [macos, verification]) {
    assert.match(job, /runs-on: \$\{\{ matrix\.os \}\}/);
    assert.match(job, /arch: amd64\s*\n\s*os: macos-15-intel\s*\n/);
    assert.match(job, /arch: arm64\s*\n\s*os: macos-15\s*\n/);
    assert.deepEqual([...job.matchAll(/- arch: ([^\s]+)/g)].map(match => match[1]).sort(), ['amd64', 'arm64']);
    assert.doesNotMatch(job, /continue-on-error:\s*true/);
  }
  assert.match(server, /name: unsigned-macos-binaries\s*\n\s*path: dist\/binaries\/omnishare-darwin-\*/);
  assert.match(macos, /name: macos-candidates-\$\{\{ matrix\.arch \}\}/);
  assert.match(verification, /name: macos-candidates-\$\{\{ matrix\.arch \}\}/);
  assert.ok(verification.indexOf('scripts/test-macos-release.py') < verification.indexOf('name: release-assets-macOS-'));
  assert.doesNotMatch(verification, /secrets\.APPLE_(?:CERTIFICATE|CERTIFICATE_PASSWORD|ID|APP_SPECIFIC_PASSWORD)/);
  const rehearsal = macos.slice(macos.indexOf('- name: Package rehearsal'), macos.indexOf('- name: Sign and notarize'));
  assert.doesNotMatch(rehearsal, /secrets\./);
  assert.match(rehearsal, /--mode rehearsal/);
  assert.match(macos, /macos_mode == 'release' && github\.ref == 'refs\/heads\/main' && github\.event_name != 'pull_request'/);
  assert.match(macos, /--mode release/);
  assert.match(verification, /--team-id "\$EXPECTED_TEAM_ID"/);
  const rawArtifact = /name: (unsigned-macos-binaries)/.exec(server)?.[1];
  assert.ok(rawArtifact && !rawArtifact.startsWith('release-assets-'));
  const build = readFileSync(join(repo, 'scripts/build-release.sh'), 'utf8');
  const targets = /for target in ([^;\n]+); do/.exec(build)?.[1].split(/\s+/).sort();
  assert.deepEqual(targets, ['windows/amd64', 'windows/arm64', 'linux/amd64', 'linux/arm64', 'darwin/amd64', 'darwin/arm64'].sort());
});

for (const failed of [false, true]) {
  test(`native macOS verification ${failed ? 'failure cannot stage release evidence' : 'receives the actual ZIP and stages its evidence'}`, () => fixture(root => {
    const mockBin = join(root, 'mock-bin');
    mkdirSync(mockBin);
    mkdirSync(join(root, 'release-assets'));
    mkdirSync(join(root, 'macos-candidates'));
    for (const ext of ['zip', 'dmg']) writeFileSync(join(root, `macos-candidates/OmniShare-v${stableVersion}-macos-arm64.${ext}`), 'candidate');
    writeFileSync(join(mockBin, 'python3'), `#!/usr/bin/env node
const fs = require('node:fs');
const path = require('node:path');
const args = process.argv.slice(2);
fs.writeFileSync('mac-verifier-arguments.json', JSON.stringify(args));
const evidence = args[args.indexOf('--evidence') + 1];
fs.mkdirSync(path.dirname(evidence), { recursive: true });
fs.writeFileSync(evidence, JSON.stringify({ success: process.env.MOCK_MAC_FAILURE !== '1' }));
process.exit(process.env.MOCK_MAC_FAILURE === '1' ? 42 : 0);
`, { mode: 0o755 });
    const result = execute(macVerificationScript, root, {
      PATH: `${mockBin}:${process.env.PATH}`, ARCH: 'arm64', RELEASE_VERSION: stableVersion,
      MOCK_MAC_FAILURE: failed ? '1' : '0'
    });
    assert.deepEqual(JSON.parse(readFileSync(join(root, 'mac-verifier-arguments.json'), 'utf8')), [
      'scripts/test-macos-release.py', '--archive', `macos-candidates/OmniShare-v${stableVersion}-macos-arm64.zip`,
      '--dmg', `macos-candidates/OmniShare-v${stableVersion}-macos-arm64.dmg`,
      '--packaging-evidence', 'macos-candidates/omnishare-macos-arm64-packaging.json',
      '--mode', 'release', '--arch', 'arm64', '--version', stableVersion,
      '--evidence', 'macos-test-evidence/arm64/verification.json', '--team-id', testTeam
    ]);
    if (failed) {
      expectFailure(result);
      assert.equal(result.status, 42);
      assert.deepEqual(readdirSync(join(root, 'release-assets')), [], 'a failed native gate must not expose its report as a successful release artifact');
    } else {
      expectSuccess(result);
      assert.deepEqual(JSON.parse(readFileSync(join(root, 'release-assets/omnishare-macos-arm64-verification.json'), 'utf8')), { success: true });
    }
  }));
}

test('desktop workflow validates three platforms with read-only permissions and no publication path', () => {
  assert.match(desktopWorkflow, /^name: Desktop Validation$/m);
  assert.match(desktopWorkflow, /^permissions:\n  contents: read$/m);
  assert.match(desktopWorkflow, /^  workflow_dispatch:$/m);
  assert.match(desktopWorkflow, /^  pull_request:$/m);
  assert.doesNotMatch(desktopWorkflow, /^  (?:push|publish-release):/m);
  assert.doesNotMatch(desktopWorkflow, /contents:\s*write|action-gh-release@|release_tag|APPLE_|inputs:/);
  for (const name of ['Linux', 'Windows', 'macOS-universal']) {
    assert.ok(desktopWorkflow.includes(`- name: ${name}\n`), `missing desktop target ${name}`);
  }
  assert.match(desktopWorkflow, /cargo test --all-features/);
  assert.match(desktopWorkflow, /windows-runtime-smoke-ci\.ps1/);
  assert.match(desktopWorkflow, /name: Build unsigned universal Tauri bundle \(macOS validation\)\n\s*if: runner\.os == 'macOS'/);
  assert.match(desktopWorkflow, /name: Verify macOS universal architectures/);
  assert.match(desktopWorkflow, /name: Upload desktop bundle artifact/);
  assert.match(desktopWorkflow, /name: Upload desktop test evidence/);
});

test('packager separates server filenames while preserving Debian package identity and prerelease ordering', () => fixture(root => {
  mkdirSync(join(root, 'scripts'));
  mkdirSync(join(root, 'docs'));
  mkdirSync(join(root, 'binaries'));
  cpSync(join(repo, 'packaging'), join(root, 'packaging'), { recursive: true });
  rmSync(join(root, 'packaging/macos'), { recursive: true, force: true });
  cpSync(join(repo, 'scripts/package-release.sh'), join(root, 'scripts/package-release.sh'));
  writeFileSync(join(root, 'README.md'), '# Packaging fixture\n');
  writeFileSync(join(root, 'docs/SECURE_SHARING.md'), '# Sharing fixture\n');
  for (const name of ['windows-amd64.exe', 'windows-arm64.exe', 'linux-amd64', 'linux-arm64']) {
    writeFileSync(join(root, 'binaries', `omnishare-${name}`), 'binary fixture\n', { mode: 0o755 });
  }
  for (const candidate of [stableVersion, '0.1.0-rc1', '1.3.0-alpha1', '1.3.0-beta1', version, '1.3.0-rc4']) {
    writeFileSync(join(root, 'VERSION'), `${candidate}\n`);
    writeFileSync(join(root, `docs/RELEASE_NOTES_v${candidate}.md`), '# Packaging fixture\n');
    expectSuccess(spawnSync('bash', [join(root, 'scripts/package-release.sh'), join(root, 'binaries'), join(root, 'dist')], { encoding: 'utf8' }));
    const packages = join(root, 'dist/packages');
    const names = readdirSync(packages);
    const expected = ['amd64', 'arm64'].flatMap(arch => [
      `OmniShare-v${candidate}-windows-${arch}.zip`,
      `OmniShare-v${candidate}-linux-${arch}.tar.gz`,
      `omnishare-server_${candidate}_${arch}.deb`
    ]).concat(['README.md', `RELEASE_NOTES_v${candidate}.md`, 'SECURE_SHARING.md', 'SHA256SUMS.txt']);
    assert.deepEqual(names.sort(), expected.sort(),
      'server packaging requires only four binaries and emits six Windows/Linux packages, three documents and checksums');
    const checksums = readFileSync(join(packages, 'SHA256SUMS.txt'), 'utf8');
    for (const name of ['README.md', `RELEASE_NOTES_v${candidate}.md`, 'SECURE_SHARING.md']) {
      assert.ok(checksums.includes(`  ${name}\n`), `missing package documentation checksum for ${name}`);
    }
    assert.ok(names.every(name => !name.includes('~')), 'public filenames must not require GitHub normalization');
    const combined = [...names, `OmniShare_${candidate}_amd64.deb`];
    assert.equal(new Set(combined.map(name => name.toLowerCase())).size, combined.length,
      'server and desktop assets must have distinct names even without case sensitivity');
    for (const arch of ['amd64', 'arm64']) {
      const deb = join(packages, `omnishare-server_${candidate}_${arch}.deb`);
      const packageName = spawnSync('dpkg-deb', ['-f', deb, 'Package'], { encoding: 'utf8' });
      expectSuccess(packageName);
      assert.equal(packageName.stdout.trim(), 'omnishare');
      const control = spawnSync('dpkg-deb', ['-f', deb, 'Version'], { encoding: 'utf8' });
      expectSuccess(control);
      assert.equal(control.stdout.trim(), candidate.replace('-', '~'));
      expectSuccess(spawnSync('dpkg', ['--compare-versions', control.stdout.trim(),
        candidate.includes('-') ? 'lt' : 'eq', candidate.split('-')[0]], { encoding: 'utf8' }));
    }
    expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: packages, encoding: 'utf8' }));
  }
}));

function writePublishedRelease(root, candidate = version) {
  const { assets } = writeAssets(root, candidate);
  expectSuccess(execute(assetsScript, root, { RELEASE_VERSION: candidate }));
  const release = {
    tag_name: `v${candidate}`,
    draft: false,
    prerelease: candidate.includes('-'),
    assets: readdirSync(assets).map(name => {
      const contents = readFileSync(join(assets, name));
      return { name, state: 'uploaded', size: contents.length, digest: `sha256:${digest(contents)}` };
    })
  };
  const mockBin = join(root, 'mock-bin');
  const responsePath = join(root, 'release-response.json');
  mkdirSync(mockBin);
  writeFileSync(join(mockBin, 'gh'), `#!/usr/bin/env node
const fs = require('node:fs');
const expected = ['api', 'repos/fixture/omnishare/releases/tags/v${candidate}'];
if (JSON.stringify(process.argv.slice(2)) !== JSON.stringify(expected)) process.exit(2);
if (process.env.MOCK_GH_EXIT) process.exit(Number(process.env.MOCK_GH_EXIT));
process.stdout.write(fs.readFileSync(process.env.MOCK_RELEASE_RESPONSE, 'utf8'));
`, { mode: 0o755 });
  return {
    release,
    verify(env = {}) {
      writeFileSync(responsePath, JSON.stringify(release));
      return execute(publishedAssetsScript, root, {
        PATH: `${mockBin}:${process.env.PATH}`,
        GH_REPO: 'fixture/omnishare', GH_TOKEN: 'fixture-token', TAG: `v${candidate}`,
        RELEASE_PRERELEASE: String(candidate.includes('-')),
        MOCK_RELEASE_RESPONSE: responsePath, ...env
      });
    }
  };
}

for (const candidate of [stableVersion, version]) {
  test(`${candidate} public verification checks release type and all 29 assets without internal Linux paths`, () => fixture(root => {
    const published = writePublishedRelease(root, candidate);
    assert.equal(published.release.assets.length, 29);
    expectSuccess(published.verify());
  }));
}

test(`stable ${stableVersion} public verification rejects a release marked as prerelease`, () => fixture(root => {
  const published = writePublishedRelease(root, stableVersion);
  published.release.prerelease = true;
  const result = published.verify();
  expectFailure(result);
  assert.match(result.stderr, /release type does not match/);
}));

for (const value of [undefined, '', '0', 'TRUE']) {
  test(`public verification rejects an invalid expected release type ${JSON.stringify(value)}`, () => fixture(root => {
    const result = writePublishedRelease(root).verify({ RELEASE_PRERELEASE: value });
    expectFailure(result);
    assert.match(result.stderr, /Expected release type must be explicitly true or false/);
  }));
}

for (const [label, mutate] of [
  ['a filename rewritten by GitHub', release => { release.assets.find(asset => asset.name.startsWith('omnishare-server_')).name = 'omnishare-server_1.3.0.rc3_amd64.deb'; }],
  ['a missing published asset', release => { release.assets.pop(); }],
  ['an unexpected published asset', release => { release.assets.push({ name: 'unexpected.zip', state: 'uploaded', size: 1, digest: `sha256:${'0'.repeat(64)}` }); }],
  ['an unfinished upload', release => { release.assets[0].state = 'starter'; }],
  ['a public size mismatch', release => { release.assets[0].size += 1; }],
  ['a public digest mismatch', release => { release.assets[0].digest = `sha256:${'0'.repeat(64)}`; }],
  ['the wrong release tag', release => { release.tag_name = 'v1.3.0-rc2'; }],
  ['a draft release', release => { release.draft = true; }],
  ['a stable release', release => { release.prerelease = false; }]
]) {
  test(`public verification rejects ${label}`, () => fixture(root => {
    const published = writePublishedRelease(root);
    mutate(published.release);
    expectFailure(published.verify());
  }));
}

test('public verification rejects a failed GitHub lookup', () => fixture(root => {
  expectFailure(writePublishedRelease(root).verify({ MOCK_GH_EXIT: '42' }));
}));

function git(root, ...args) {
  const result = spawnSync('git', ['-C', root, ...args], { encoding: 'utf8' });
  expectSuccess(result);
  return result.stdout.trim();
}

function tagFixture(root) {
  git(root, 'init', '-q');
  git(root, 'config', 'user.name', 'release-contract');
  git(root, 'config', 'user.email', 'release-contract@example.invalid');
  git(root, 'remote', 'add', 'origin', root);
  git(root, 'commit', '-q', '--allow-empty', '-m', 'previous release');
  const previous = git(root, 'rev-parse', 'HEAD');
  git(root, 'commit', '-q', '--allow-empty', '-m', 'current release');
  return { previous, current: git(root, 'rev-parse', 'HEAD') };
}

for (const candidate of [stableVersion, version]) {
  for (const kind of ['new tag', 'matching lightweight tag', 'matching annotated tag']) {
    test(`${candidate} tag guard accepts ${kind}`, () => fixture(root => {
      const { current } = tagFixture(root);
      if (kind === 'matching lightweight tag') git(root, 'tag', `v${candidate}`, current);
      if (kind === 'matching annotated tag') git(root, 'tag', '-a', `v${candidate}`, current, '-m', 'release');
      expectSuccess(execute(tagScript, root, { TAG: `v${candidate}`, GITHUB_SHA: current }));
    }));
  }
  test(`${candidate} tag guard rejects an existing tag from a different commit`, () => fixture(root => {
    const { previous, current } = tagFixture(root);
    git(root, 'tag', '-a', `v${candidate}`, previous, '-m', 'old release');
    expectFailure(execute(tagScript, root, { TAG: `v${candidate}`, GITHUB_SHA: current }));
  }));
}

test('tag guard does not treat a failed remote lookup as a missing tag', () => fixture(root => {
  const { current } = tagFixture(root);
  git(root, 'remote', 'set-url', 'origin', join(root, 'missing-repository'));
  expectFailure(execute(tagScript, root, { TAG: `v${version}`, GITHUB_SHA: current }));
}));

for (const [event, ref, expected] of [
  ['pull_request', 'refs/pull/32/merge', 'rehearsal'],
  ['push', 'refs/heads/main', 'release'],
  ['workflow_dispatch', 'refs/heads/main', 'release']
]) {
  test(`macOS mode is explicitly ${expected} for ${event}`, () => fixture(root => {
    expectSuccess(execute(modeScript, root, { EVENT_NAME: event, REF_NAME: ref }));
    assert.equal(readFileSync(join(root, 'outputs'), 'utf8'), `macos_mode=${expected}\n`);
  }));
}
for (const [event, ref] of [['workflow_dispatch', 'refs/heads/fix/unreviewed'], ['push', 'refs/heads/fix/unreviewed'], ['pull_request_target', 'refs/heads/main']]) {
  test(`macOS mode rejects ${event} on ${ref}`, () => fixture(root => {
    expectFailure(execute(modeScript, root, { EVENT_NAME: event, REF_NAME: ref }));
  }));
}

test('rehearsal assets pass only rehearsal validation and never the final publication gate', () => fixture(root => {
  writeAssets(root, version, 'rehearsal');
  expectSuccess(execute(assetsScript, root, { MACOS_VERIFICATION_MODE: 'rehearsal', EXPECTED_TEAM_ID: '' }));
  expectFailure(execute(assetsScript, root));
  expectFailure(execute(publishMacGateScript, root));
}));

test('formal assets pass the independent final publication gate', () => fixture(root => {
  writeAssets(root);
  expectSuccess(execute(publishMacGateScript, root));
}));

for (const mode of ['', 'unsigned', 'auto']) {
  test(`aggregation never guesses signing mode from ${JSON.stringify(mode)}`, () => fixture(root => {
    writeAssets(root);
    expectFailure(execute(assetsScript, root, { MACOS_VERIFICATION_MODE: mode }));
  }));
}
for (const team of ['', 'AB12CD34E', 'ZZ99ZZ99ZZ']) {
  test(`formal assets reject independent team configuration ${JSON.stringify(team)}`, () => fixture(root => {
    writeAssets(root);
    expectFailure(execute(assetsScript, root, { EXPECTED_TEAM_ID: team }));
    expectFailure(execute(publishMacGateScript, root, { EXPECTED_TEAM_ID: team }));
  }));
}
for (const kind of ['zip_app', 'dmg', 'dmg_app']) {
  for (const [label, mutate] of [
    ['rejected', assessment => { assessment.assessment_exit_code = 3; assessment.assessment_result = 'rejected'; }],
    ['unquarantined', assessment => { assessment.quarantine_verified = false; }],
    ['non-notarized trust source', assessment => { assessment.assessment_source = 'Developer ID'; }],
    ['string exit status', assessment => { assessment.assessment_exit_code = '0'; }]
  ]) {
    test(`formal gate rejects ${label} ${kind}`, () => fixture(root => {
      const { mutateMacEvidence } = writeAssets(root);
      mutateMacEvidence('arm64', evidence => mutate(evidence.gatekeeper[kind]));
      expectFailure(execute(publishMacGateScript, root));
    }));
  }
}
for (const kind of ['zip_app', 'dmg_app']) {
  for (const [label, mutate] of [
    ['failed', policy => { policy.exit_code = 1; policy.result = 'rejected'; }],
    ['unavailable', policy => { policy.available = false; }]
  ]) {
    test(`formal gate rejects ${label} syspolicy_check for ${kind}`, () => fixture(root => {
      const { mutateMacEvidence } = writeAssets(root);
      mutateMacEvidence('amd64', evidence => mutate(evidence.gatekeeper[kind].syspolicy_check));
      expectFailure(execute(publishMacGateScript, root));
    }));
  }
}

test('formal gate runs before tag mutation or release creation', () => {
  const publish = jobSource('publish');
  assert.ok(publish.indexOf('- name: Require notarized macOS evidence before publishing') < publish.indexOf('- name: Verify existing tag target'));
  assert.ok(publish.indexOf('- name: Require notarized macOS evidence before publishing') < publish.indexOf('- name: Publish GitHub release'));
  assert.match(publishMacGateScript, /mode: 'release'/);
  assert.doesNotMatch(publish, /continue-on-error:\s*true/);
});

test('credential readiness runs only trusted main and cannot publish', () => {
  const readiness = readFileSync(join(repo, '.github/workflows/macos-signing-readiness.yml'), 'utf8');
  assert.match(readiness, /branches: \[main\]/);
  assert.match(readiness, /github\.ref == 'refs\/heads\/main'/);
  assert.doesNotMatch(readiness, /pull_request|action-gh-release|notarytool submit|--mode release/);
  assert.match(readiness, /run: bash scripts\/package-macos\.sh --check-credentials/);
  const credentialsJob = readiness.slice(readiness.indexOf('  credentials:'), readiness.indexOf('  cleanup-reviewed-branch:'));
  assert.doesNotMatch(credentialsJob, /contents:\s*write|continue-on-error:\s*true/);
  for (const secret of ['APPLE_CERTIFICATE', 'APPLE_CERTIFICATE_PASSWORD', 'APPLE_TEAM_ID', 'APPLE_ID', 'APPLE_APP_SPECIFIC_PASSWORD']) {
    assert.ok(credentialsJob.includes(`secrets.${secret}`));
  }
  assert.match(readiness, /branch_ref=refs\/heads\/fix\/cross-platform-install-20261111/);
  assert.match(readiness, /--force-with-lease="\$branch_ref:\$expected"/);
  assert.match(readiness, /git merge-base --is-ancestor "\$actual" "\$GITHUB_SHA"/);
});

for (const [key, value] of [
  ['signing_mode', 'ad-hoc'], ['notarized', false], ['server_zip_signed', false],
  ['dmg_published', false], ['minimum_system_version', '11.0'], ['tauri_published', true]
]) {
  test(`formal manifest rejects invalid macOS ${key}`, () => fixture(root => {
    writeManifest(root);
    const path = join(root, 'release/desktop-release.json');
    const manifest = JSON.parse(readFileSync(path, 'utf8'));
    manifest.macos[key] = value;
    writeFileSync(path, JSON.stringify(manifest));
    expectFailure(execute(manifestScript, root));
  }));
}
test('rehearsal can validate historical ad-hoc source without authorizing another formal publication', () => fixture(root => {
  writeManifest(root, { macos: { signing_mode: 'ad-hoc', notarized: false } }, stableVersion);
  expectSuccess(execute(manifestScript, root, { MACOS_VERIFICATION_MODE: 'rehearsal' }));
  expectFailure(execute(manifestScript, root));
}));


test('desktop packaging uses the pinned CLI and verified platform tools before builds', () => {
  const manifest = JSON.parse(readFileSync(join(repo, 'desktop/package.json'), 'utf8'));
  const lock = JSON.parse(readFileSync(join(repo, 'desktop/package-lock.json'), 'utf8'));
  assert.equal(manifest.devDependencies['@tauri-apps/cli'], '2.12.4');
  assert.equal(lock.packages[''].devDependencies['@tauri-apps/cli'], '2.12.4');
  assert.equal(lock.packages['node_modules/@tauri-apps/cli'].version, '2.12.4');
  for (const source of [desktopWorkflow, jobSource('build-and-test')]) {
    assert.match(source, /name: Install locked desktop dependencies\n\s*working-directory: desktop\n\s*run: npm ci/);
    assert.match(source, /name: Prepare verified Linux AppImage tools\n\s*id: linux-tools\n\s*if: runner\.os == 'Linux'\n\s*env:\n\s*OMNISHARE_TOOL_GITHUB_TOKEN: \$\{\{ github\.token \}\}\n\s*run: python3 scripts\/prepare-linux-appimage-tools\.py/);
    assert.match(source, /LDAI_RUNTIME_FILE: \$\{\{ steps\.linux-tools\.outputs\.runtime_path \}\}/);
    assert.match(source, /test -n "\$LDAI_RUNTIME_FILE"\n\s*\[\[ "\$LDAI_RUNTIME_FILE" = \/\* \]\]\n\s*test -f "\$LDAI_RUNTIME_FILE"\n\s*npm run build -- --verbose/);
    assert.ok(source.indexOf('Prepare verified Linux AppImage tools') < source.indexOf('- name: Build Tauri bundle'));
    assert.match(source, /name: Prepare verified Windows installer tools\n\s*if: runner\.os == 'Windows'\n\s*env:\n\s*OMNISHARE_TOOL_GITHUB_TOKEN: \$\{\{ github\.token \}\}\n\s*run: python scripts\/prepare-windows-bundle-tools\.py/);
    assert.ok(source.indexOf('Prepare verified Windows installer tools') < source.indexOf('- name: Build Tauri bundle'));
    assert.doesNotMatch(source, /run: npm install\s*\n/);
  }
});
