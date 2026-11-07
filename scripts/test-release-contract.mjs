import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { cpSync, mkdtempSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..');
const workflow = readFileSync(join(repo, '.github/workflows/publish-desktop-prerelease.yml'), 'utf8');
const version = '1.3.0-rc3';

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

const manifestScript = stepScript('Read manifest');
const assetsScript = stepScript('Verify staged assets');
const tagScript = stepScript('Verify existing tag target');
const publishedAssetsScript = stepScript('Verify published release assets');

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
    env: { ...process.env, RELEASE_VERSION: version, GITHUB_OUTPUT: join(cwd, 'outputs'), ...env },
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

function writeManifest(root, overrides = {}) {
  mkdirSync(join(root, 'release'));
  mkdirSync(join(root, 'docs'));
  writeFileSync(join(root, 'VERSION'), `${version}\n`);
  writeFileSync(join(root, `docs/RELEASE_NOTES_v${version}.md`), '# Candidate release\n');
  writeFileSync(join(root, 'release/desktop-release.json'), JSON.stringify({
    tag: `v${version}`,
    name: `OmniShare v${version}`,
    notes: `docs/RELEASE_NOTES_v${version}.md`,
    prerelease: true,
    ...overrides
  }));
}

const digest = value => createHash('sha256').update(value).digest('hex');

function writeAssets(root) {
  const assets = join(root, 'release-assets');
  mkdirSync(assets);
  const put = (name, content = `fixture ${name}\n`) => writeFileSync(join(assets, name), content);
  const server = ['amd64', 'arm64'].flatMap(arch => [
    `OmniShare-v${version}-windows-${arch}.zip`,
    `OmniShare-v${version}-linux-${arch}.tar.gz`,
    `omnishare_${version}_${arch}.deb`,
    `OmniShare-v${version}-macos-${arch}.zip`
  ]);
  const windows = [`OmniShare_${version}_x64_en-US.msi`, `OmniShare_${version}_x64-setup.exe`];
  const linux = [`OmniShare_${version}_amd64.AppImage`, `OmniShare_${version}_amd64.deb`, 'OmniShare-1.3.0-0.rc3.x86_64.rpm'];
  for (const name of [...server, ...windows, ...linux]) put(name);
  put('SHA256SUMS.txt', server.map(name => `${digest(readFileSync(join(assets, name)))}  ${name}\n`).join(''));
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
  return { assets, server, windows, linux, put };
}

test('matching prerelease manifest produces versioned outputs', () => fixture(root => {
  writeManifest(root);
  expectSuccess(execute(manifestScript, root));
  assert.equal(readFileSync(join(root, 'outputs'), 'utf8'),
    `version=${version}\ntag=v${version}\nname=OmniShare v${version}\nnotes=docs/RELEASE_NOTES_v${version}.md\n`);
}));

for (const [label, overrides] of [
  ['mismatched tag', { tag: 'v1.3.0-rc2' }],
  ['mismatched notes', { notes: 'docs/other.md' }],
  ['multiline metadata', { name: 'candidate\ntag=v9.0.0' }],
  ['stable publication flag', { prerelease: false }]
]) {
  test(`manifest rejects ${label}`, () => fixture(root => {
    writeManifest(root, overrides);
    expectFailure(execute(manifestScript, root));
  }));
}

test('manifest rejects missing release notes', () => fixture(root => {
  writeManifest(root);
  rmSync(join(root, `docs/RELEASE_NOTES_v${version}.md`));
  expectFailure(execute(manifestScript, root));
}));

test('complete server and desktop assets pass hash and runtime verification', () => fixture(root => {
  writeAssets(root);
  expectSuccess(execute(assetsScript, root));
}));

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

test('publication waits for every build and only runs on main push or dispatch', () => {
  const verify = workflow.slice(workflow.indexOf('\n  verify-assets:\n'), workflow.indexOf('\n  publish:\n'));
  const publish = workflow.slice(workflow.indexOf('\n  publish:\n'));
  assert.match(verify, /needs:\s*\n\s*- release-metadata\s*\n\s*- build-server\s*\n\s*- build-and-test/);
  assert.match(verify, /name: verified-prerelease-assets/);
  assert.match(publish, /needs:\s*\n\s*- release-metadata\s*\n\s*- verify-assets/);
  assert.match(publish, /name: Create GitHub prerelease\s*\n\s*if: github\.ref == 'refs\/heads\/main' && \(github\.event_name == 'push' \|\| github\.event_name == 'workflow_dispatch'\)/);
  assert.match(publish, /name: Download combined verified assets[\s\S]+name: verified-prerelease-assets/);
  assert.match(workflow, /pull_request:\s*\n\s*paths:/);
  assert.doesNotMatch(workflow, /pull_request_target:/);
  assert.match(workflow, /run: \.\/scripts\/build-release\.sh/);
  assert.match(workflow, /name: release-assets-Server/);
  assert.match(workflow, /pattern: release-assets-\*/);
  assert.ok(publish.indexOf('- name: Verify published release assets') > publish.indexOf('- name: Publish GitHub prerelease'));
});

test('packager keeps Debian prerelease ordering while using portable public filenames', () => fixture(root => {
  mkdirSync(join(root, 'scripts'));
  mkdirSync(join(root, 'docs'));
  mkdirSync(join(root, 'binaries'));
  cpSync(join(repo, 'packaging'), join(root, 'packaging'), { recursive: true });
  cpSync(join(repo, 'scripts/package-release.sh'), join(root, 'scripts/package-release.sh'));
  writeFileSync(join(root, 'README.md'), '# Packaging fixture\n');
  for (const name of ['windows-amd64.exe', 'windows-arm64.exe', 'linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64']) {
    writeFileSync(join(root, 'binaries', `omnishare-${name}`), 'binary fixture\n', { mode: 0o755 });
  }
  for (const candidate of ['1.3.0-alpha1', '1.3.0-beta1', version]) {
    writeFileSync(join(root, 'VERSION'), `${candidate}\n`);
    writeFileSync(join(root, `docs/RELEASE_NOTES_v${candidate}.md`), '# Packaging fixture\n');
    expectSuccess(spawnSync('bash', [join(root, 'scripts/package-release.sh'), join(root, 'binaries'), join(root, 'dist')], { encoding: 'utf8' }));
    const packages = join(root, 'dist/packages');
    assert.ok(readdirSync(packages).every(name => !name.includes('~')), 'public filenames must not require GitHub normalization');
    for (const arch of ['amd64', 'arm64']) {
      const deb = join(packages, `omnishare_${candidate}_${arch}.deb`);
      const control = spawnSync('dpkg-deb', ['-f', deb, 'Version'], { encoding: 'utf8' });
      expectSuccess(control);
      assert.equal(control.stdout.trim(), candidate.replace('-', '~'));
      expectSuccess(spawnSync('dpkg', ['--compare-versions', control.stdout.trim(), 'lt', '1.3.0'], { encoding: 'utf8' }));
    }
    expectSuccess(spawnSync('sha256sum', ['-c', 'SHA256SUMS.txt'], { cwd: packages, encoding: 'utf8' }));
  }
}));

function writePublishedRelease(root) {
  const { assets } = writeAssets(root);
  const release = {
    tag_name: `v${version}`,
    draft: false,
    prerelease: true,
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
const expected = ['api', 'repos/fixture/omnishare/releases/tags/v${version}'];
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
        GH_REPO: 'fixture/omnishare', GH_TOKEN: 'fixture-token', TAG: `v${version}`,
        MOCK_RELEASE_RESPONSE: responsePath, ...env
      });
    }
  };
}

test('public verification checks uploaded files without requiring internal Linux evidence paths', () => fixture(root => {
  const published = writePublishedRelease(root);
  expectSuccess(published.verify());
}));

for (const [label, mutate] of [
  ['a filename rewritten by GitHub', release => { release.assets.find(asset => asset.name.startsWith('omnishare_')).name = 'omnishare_1.3.0.rc3_amd64.deb'; }],
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

for (const kind of ['new tag', 'matching lightweight tag', 'matching annotated tag']) {
  test(`tag guard accepts ${kind}`, () => fixture(root => {
    const { current } = tagFixture(root);
    if (kind === 'matching lightweight tag') git(root, 'tag', `v${version}`, current);
    if (kind === 'matching annotated tag') git(root, 'tag', '-a', `v${version}`, current, '-m', 'release');
    expectSuccess(execute(tagScript, root, { TAG: `v${version}`, GITHUB_SHA: current }));
  }));
}

test('tag guard rejects an existing tag from a different commit', () => fixture(root => {
  const { previous, current } = tagFixture(root);
  git(root, 'tag', '-a', `v${version}`, previous, '-m', 'old release');
  expectFailure(execute(tagScript, root, { TAG: `v${version}`, GITHUB_SHA: current }));
}));

test('tag guard does not treat a failed remote lookup as a missing tag', () => fixture(root => {
  const { current } = tagFixture(root);
  git(root, 'remote', 'set-url', 'origin', join(root, 'missing-repository'));
  expectFailure(execute(tagScript, root, { TAG: `v${version}`, GITHUB_SHA: current }));
}));
