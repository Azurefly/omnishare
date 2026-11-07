import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import {
  ANCHOR, ORIGINAL_NAMES, desiredName, digestOf, buildDesired,
  validateProvenance, planRepair, executeRepair, performOperation,
} from './repair-rc4-release.mjs';

// Synthetic bytes and an in-memory GitHub transport exercise the production
// repair functions. These tests never use credentials, GitHub, or real assets.
const NOTES = 'RELEASE_NOTES_v1.3.0-rc4.md';
const OLD_AMD = 'omnishare_1.3.0-rc4_amd64.deb';
const OLD_ARM = 'omnishare_1.3.0-rc4_arm64.deb';
const NEW_AMD = 'omnishare-server_1.3.0-rc4_amd64.deb';
const NEW_ARM = 'omnishare-server_1.3.0-rc4_arm64.deb';
const DESKTOP_DEB = 'OmniShare_1.3.0-rc4_amd64.deb';
const TEXT_NAMES = ['README.md', NOTES, 'SHA256SUMS.txt'];
const SERVER_NAMES = [
  'OmniShare-v1.3.0-rc4-linux-amd64.tar.gz',
  'OmniShare-v1.3.0-rc4-linux-arm64.tar.gz',
  'OmniShare-v1.3.0-rc4-macos-amd64.zip',
  'OmniShare-v1.3.0-rc4-macos-arm64.zip',
  'OmniShare-v1.3.0-rc4-windows-amd64.zip',
  'OmniShare-v1.3.0-rc4-windows-arm64.zip',
  OLD_AMD, OLD_ARM,
];
const WINDOWS_NAMES = [
  'OmniShare_1.3.0-rc4_x64-setup.exe',
  'OmniShare_1.3.0-rc4_x64_en-US.msi',
];
const LINUX_NAMES = [
  'OmniShare-1.3.0-0.rc4.x86_64.rpm',
  'OmniShare_1.3.0-rc4_amd64.AppImage',
  DESKTOP_DEB,
];
const DOCUMENT_NAMES = ['README.md', NOTES, 'REMEDIATION_v1.3.0-rc1.md'];
const INVENTORY = [
  ...SERVER_NAMES, ...WINDOWS_NAMES, ...LINUX_NAMES,
  'omnishare-linux-sha256.txt', 'omnishare-windows-sha256.json',
  'omnishare-windows-runtime.junit.xml', 'omnishare-windows-watchdog.junit.xml',
  'omnishare-windows-watchdog.json', ...DOCUMENT_NAMES, 'SHA256SUMS.txt',
];
const APPLIED_CONTEXT = {
  repository: 'Azurefly/omnishare', eventName: 'push', ref: 'refs/heads/main',
  // The reviewed repair runs at a new commit; the released binaries do not.
  sha: 'a'.repeat(40),
};
const PR_CONTEXT = { ...APPLIED_CONTEXT, eventName: 'pull_request', ref: 'refs/pull/29/merge' };
const sha256 = bytes => 'sha256:' + createHash('sha256').update(bytes).digest('hex');
const clone = value => structuredClone(value);

function originalAssets() {
  const original = new Map(INVENTORY.map(name => [name, Buffer.from(`verified fixture bytes: ${name}\n`)]));
  original.set('README.md', Buffer.from([
    '# OmniShare v1.3.0-rc4',
    `Server amd64: ${OLD_AMD}`, `Server arm64: ${OLD_ARM}`,
    `Desktop amd64: ${DESKTOP_DEB}`, '',
  ].join('\n')));
  original.set(NOTES, Buffer.from([
    '# RC4 release notes',
    `Server downloads: ${OLD_AMD}, ${OLD_ARM}.`,
    `Desktop download: ${DESKTOP_DEB}.`, '',
  ].join('\n')));
  original.set('omnishare-windows-sha256.json', Buffer.from(JSON.stringify(WINDOWS_NAMES.map(name => ({
    Path: `D:\\build evidence\\bundle\\${name}`,
    Size: original.get(name).length,
    SHA256: sha256(original.get(name)).slice(7).toUpperCase(),
  })))));
  original.set('omnishare-linux-sha256.txt', Buffer.from(LINUX_NAMES.map(name =>
    `${sha256(original.get(name)).slice(7)}  desktop/src-tauri/target/release/bundle/${name}\n`,
  ).join('')));
  for (const name of ['omnishare-windows-runtime.junit.xml', 'omnishare-windows-watchdog.junit.xml']) {
    original.set(name, Buffer.from('<testsuite tests="2" failures="0" errors="0"><testcase name="passed"/></testsuite>\n'));
  }
  original.set('omnishare-windows-watchdog.json', Buffer.from('{"success":true}\n'));
  original.set('SHA256SUMS.txt', Buffer.from([...SERVER_NAMES, ...DOCUMENT_NAMES].map(name =>
    `${sha256(original.get(name)).slice(7)}  ${name}\n`,
  ).join('')));
  return original;
}

function correctedDocument(bytes) {
  return Buffer.from(bytes.toString('utf8')
    .replaceAll(OLD_AMD, NEW_AMD).replaceAll(OLD_ARM, NEW_ARM)
    + '\nRelease attachment names corrected; binary bytes are unchanged.\n');
}

function fixture() {
  const original = originalAssets();
  const readme = correctedDocument(original.get('README.md'));
  const notes = correctedDocument(original.get(NOTES));
  const desired = buildDesired(original, readme, notes);
  let id = 720000000;
  const assets = [...original].filter(([name]) => name !== OLD_AMD).map(([name, bytes]) => ({
    id: name === OLD_ARM ? ANCHOR.armAssetId : name === DESKTOP_DEB ? ANCHOR.desktopDebAssetId : id++,
    name, state: 'uploaded', size: bytes.length, digest: sha256(bytes),
  }));
  return {
    original, desired, readme, notes,
    tagRef: { ref: 'refs/tags/' + ANCHOR.tag, object: { type: 'commit', sha: ANCHOR.sha } },
    release: {
      id: ANCHOR.releaseId, tag_name: ANCHOR.tag, target_commitish: ANCHOR.sha,
      draft: false, prerelease: true, immutable: false,
      body: original.get(NOTES).toString('utf8'), assets,
    },
  };
}

function provenance() {
  const run = {
    id: ANCHOR.runId, workflow_id: ANCHOR.workflowId,
    path: '.github/workflows/publish-desktop-prerelease.yml',
    head_sha: ANCHOR.sha, head_branch: 'main', event: 'push', run_attempt: 1,
    status: 'completed', conclusion: 'failure',
    repository: { id: ANCHOR.repositoryId, full_name: ANCHOR.repo },
  };
  const names = [
    'Read release metadata', 'Build and test server release packages',
    'Build and test Windows release assets', 'Build and test Linux release assets',
    'Verify combined server and desktop assets', 'Create GitHub prerelease',
  ];
  const jobs = names.map((name, index) => ({
    id: 128337000000 + index, name, run_id: ANCHOR.runId,
    head_sha: ANCHOR.sha, status: 'completed', conclusion: index === 5 ? 'failure' : 'success',
    steps: index === 5 ? [
      { name: 'Checkout', conclusion: 'success' },
      { name: 'Download combined verified assets', conclusion: 'success' },
      { name: 'Verify existing tag target', conclusion: 'success' },
      { name: 'Publish GitHub prerelease', conclusion: 'failure' },
      { name: 'Verify published release assets', conclusion: 'skipped' },
    ] : [{ name: 'Verified prerequisite', conclusion: 'success' }],
  }));
  const artifact = {
    id: ANCHOR.artifactId, name: 'verified-prerelease-assets',
    digest: ANCHOR.artifactDigest, size_in_bytes: ANCHOR.artifactSize, expired: false,
    workflow_run: {
      id: ANCHOR.runId, head_sha: ANCHOR.sha, head_branch: 'main',
      repository_id: ANCHOR.repositoryId, head_repository_id: ANCHOR.repositoryId,
    },
  };
  return { run, jobs, artifact };
}

function parseChecksums(bytes) {
  const entries = bytes.toString('utf8').trim().split('\n').map(line => {
    const match = /^([a-f0-9]{64})  (.+)$/.exec(line);
    assert.ok(match, `invalid checksum line: ${line}`);
    return [match[2], 'sha256:' + match[1]];
  });
  assert.equal(new Set(entries.map(([name]) => name)).size, entries.length);
  return new Map(entries);
}

// The HTTP boundary is intentionally strict: the real performOperation must
// preserve asset identity on rename, and may delete only the three text files.
function memoryGitHub(data = fixture()) {
  const state = { release: clone(data.release), tagRef: clone(data.tagRef) };
  const initial = clone(data.release);
  const bytes = new Map([...data.original].filter(([name]) => name !== OLD_AMD));
  const requests = [];
  let nextId = 730000000;
  let reads = 0;
  let failAfter = null;
  let onRead = null;
  let onWrite = null;
  const root = `/repos/${ANCHOR.repo}/releases`;
  async function api(endpoint, options = {}) {
    const { method, json, bytes: upload } = options;
    requests.push({ endpoint, method, json: json && clone(json),
      ...(upload !== undefined ? { size: upload.length, digest: sha256(upload) } : {}) });
    const assetMatch = new RegExp(`^${root}/assets/(\\d+)$`).exec(endpoint);
    if (assetMatch) {
      const id = Number(assetMatch[1]);
      const asset = state.release.assets.find(candidate => candidate.id === id);
      assert.ok(asset, `asset ID ${id} does not exist`);
      if (method === 'PATCH') {
        assert.deepEqual(Object.keys(json), ['name']);
        assert.ok([OLD_ARM, OLD_AMD].includes(asset.name));
        const expected = asset.name === OLD_ARM ? NEW_ARM : NEW_AMD;
        assert.equal(json.name, expected);
        assert.ok(!state.release.assets.some(other => other.name.toLowerCase() === expected.toLowerCase()));
        bytes.set(expected, bytes.get(asset.name));
        bytes.delete(asset.name);
        asset.name = expected;
      } else {
        assert.equal(method, 'DELETE');
        assert.ok(TEXT_NAMES.includes(asset.name), 'binary DELETE is forbidden');
        bytes.delete(asset.name);
        state.release.assets = state.release.assets.filter(candidate => candidate.id !== id);
      }
    } else if (endpoint.startsWith(`${root}/${ANCHOR.releaseId}/assets?name=`)) {
      assert.equal(method, 'POST');
      const name = new URL('https://uploads.example.invalid' + endpoint).searchParams.get('name');
      assert.ok(name === NEW_AMD || TEXT_NAMES.includes(name), `unapproved upload: ${name}`);
      assert.ok(Buffer.isBuffer(upload));
      assert.deepEqual(upload, data.desired.get(name));
      assert.ok(!state.release.assets.some(asset => asset.name.toLowerCase() === name.toLowerCase()), 'would overwrite an existing name');
      state.release.assets.push({ id: nextId++, name, state: 'uploaded', size: upload.length, digest: sha256(upload) });
      bytes.set(name, Buffer.from(upload));
    } else {
      assert.equal(endpoint, `${root}/${ANCHOR.releaseId}`);
      assert.equal(method, 'PATCH');
      assert.deepEqual(Object.keys(json), ['body']);
      assert.equal(json.body, data.notes.toString('utf8'));
      state.release.body = json.body;
    }
    if (onWrite) onWrite(state, requests.length);
    if (requests.length === failAfter) {
      failAfter = null;
      throw new Error('simulated interruption after committed HTTP mutation');
    }
    return {};
  }
  const readState = async () => {
    reads += 1;
    if (onRead) onRead(state, reads);
    return clone(state);
  };
  return {
    data, state, initial, bytes, requests, api, readState,
    get reads() { return reads; },
    failAfterRequest(number) { failAfter = number; },
    beforeRead(callback) { onRead = callback; },
    afterWrite(callback) { onWrite = callback; },
    run({ dryRun = false, context = APPLIED_CONTEXT } = {}) {
      return executeRepair({ original: data.original, desired: data.desired, dryRun, context,
        readState, mutate: operation => performOperation(operation, api, data.desired) });
    },
  };
}

function assertComplete(model) {
  const { release, tagRef } = model.state;
  assert.equal(release.assets.length, 22);
  assert.deepEqual(release.assets.map(asset => asset.name).sort(), [...model.data.desired.keys()].sort());
  assert.equal(new Set(release.assets.map(asset => asset.name.toLowerCase())).size, 22);
  for (const asset of release.assets) {
    const expected = model.data.desired.get(asset.name);
    assert.equal(asset.state, 'uploaded');
    assert.equal(asset.size, expected.length);
    assert.equal(asset.digest, sha256(expected));
    assert.deepEqual(model.bytes.get(asset.name), expected);
  }
  for (const prior of model.initial.assets.filter(asset => !TEXT_NAMES.includes(asset.name))) {
    const now = release.assets.find(asset => asset.id === prior.id);
    assert.ok(now, `existing asset was deleted or replaced: ${prior.name}`);
    assert.deepEqual(now, { ...prior, name: prior.name === OLD_ARM ? NEW_ARM : prior.name });
  }
  assert.equal(release.assets.find(asset => asset.name === NEW_ARM).id, ANCHOR.armAssetId);
  assert.equal(release.assets.find(asset => asset.name === DESKTOP_DEB).id, ANCHOR.desktopDebAssetId);
  assert.equal(release.target_commitish, ANCHOR.sha);
  assert.deepEqual(tagRef, model.data.tagRef);
  assert.equal(release.body, model.data.notes.toString('utf8'));
}

test('repair inventory contains the exact 22 original assets and only the two server names change', () => {
  assert.deepEqual([...ORIGINAL_NAMES].sort(), [...INVENTORY].sort());
  assert.equal(desiredName(OLD_AMD), NEW_AMD);
  assert.equal(desiredName(OLD_ARM), NEW_ARM);
  for (const name of INVENTORY.filter(name => ![OLD_AMD, OLD_ARM].includes(name))) assert.equal(desiredName(name), name);
  assert.equal(digestOf(Buffer.from('fixture')), sha256(Buffer.from('fixture')));
});

test('desired assets preserve binary bytes and rewrite all 11 checksum entries including changed documents', () => {
  const original = originalAssets();
  const snapshot = new Map([...original].map(([name, bytes]) => [name, Buffer.from(bytes)]));
  const readme = correctedDocument(original.get('README.md'));
  const notes = correctedDocument(original.get(NOTES));
  const desired = buildDesired(original, readme, notes);
  assert.deepEqual(original, snapshot, 'planning must not mutate the verified artifact');
  assert.equal(desired.size, 22);
  assert.equal(new Set([...desired.keys()].map(name => name.toLowerCase())).size, 22);
  assert.equal(desired.has(OLD_AMD), false);
  assert.equal(desired.has(OLD_ARM), false);
  assert.deepEqual(desired.get(DESKTOP_DEB), original.get(DESKTOP_DEB));
  for (const [name, bytes] of original) {
    if (!TEXT_NAMES.includes(name)) assert.deepEqual(desired.get(desiredName(name)), bytes);
  }
  assert.deepEqual(desired.get('README.md'), readme);
  assert.deepEqual(desired.get(NOTES), notes);
  const entries = parseChecksums(desired.get('SHA256SUMS.txt'));
  const expectedNames = [...SERVER_NAMES, ...DOCUMENT_NAMES].map(desiredName);
  assert.equal(entries.size, 11);
  assert.deepEqual([...entries.keys()].sort(), expectedNames.sort());
  for (const [name, hash] of entries) assert.equal(hash, sha256(desired.get(name)));
  assert.notEqual(entries.get('README.md'), sha256(original.get('README.md')));
  assert.notEqual(entries.get(NOTES), sha256(original.get(NOTES)));
});

for (const [label, mutate] of [
  ['a missing source asset', source => source.delete(OLD_AMD)],
  ['an extra source asset', source => source.set('unexpected.bin', Buffer.from('extra'))],
  ['empty source bytes', source => source.set(DESKTOP_DEB, Buffer.alloc(0))],
  ['a changed server binary', source => source.set(OLD_ARM, Buffer.from('modified binary'))],
  ['a changed original README', source => source.set('README.md', Buffer.from('unknown edit'))],
  ['an omitted checksum', source => source.set('SHA256SUMS.txt', Buffer.from(source.get('SHA256SUMS.txt').toString().split('\n').slice(1).join('\n')))],
  ['a repeated checksum', source => source.set('SHA256SUMS.txt', Buffer.from(source.get('SHA256SUMS.txt') + source.get('SHA256SUMS.txt').toString().split('\n')[0] + '\n'))],
  ['a malformed checksum', source => source.set('SHA256SUMS.txt', Buffer.from('not a checksum\n'))],
  ['an unsafe checksum path', source => source.set('SHA256SUMS.txt', Buffer.from(source.get('SHA256SUMS.txt').toString().replace(OLD_AMD, '../' + OLD_AMD)))],
]) {
  test(`desired preparation rejects ${label}`, () => {
    const source = originalAssets();
    mutate(source);
    assert.throws(() => buildDesired(source, Buffer.from('corrected readme'), Buffer.from('corrected notes')));
  });
}

test('empty corrected release documents are rejected before mutation', () => {
  assert.throws(() => buildDesired(originalAssets(), Buffer.alloc(0), Buffer.from('notes')));
  assert.throws(() => buildDesired(originalAssets(), Buffer.from('readme'), Buffer.alloc(0)));
});

test('provenance accepts the five successful prerequisite jobs and the single known publication failure', () => {
  const { run, jobs, artifact } = provenance();
  assert.doesNotThrow(() => validateProvenance(run, jobs, artifact));
});

for (const [label, mutate] of [
  ['another source run', data => { data.run.id += 1; }],
  ['another workflow', data => { data.run.workflow_id += 1; }],
  ['another source commit', data => { data.run.head_sha = 'b'.repeat(40); }],
  ['a different workflow path', data => { data.run.path = '.github/workflows/other.yml'; }],
  ['a PR build', data => { data.run.event = 'pull_request'; }],
  ['a non-main source branch', data => { data.run.head_branch = 'feature'; }],
  ['a rerun attempt', data => { data.run.run_attempt = 2; }],
  ['an incomplete source run', data => { data.run.status = 'in_progress'; }],
  ['an unexpected run conclusion', data => { data.run.conclusion = 'cancelled'; }],
  ['another repository name', data => { data.run.repository.full_name = 'other/omnishare'; }],
  ['another repository ID', data => { data.run.repository.id += 1; }],
  ['a failed prerequisite', data => { data.jobs[2].conclusion = 'failure'; }],
  ['a skipped prerequisite', data => { data.jobs[3].conclusion = 'skipped'; }],
  ['a prerequisite from another commit', data => { data.jobs[1].head_sha = 'b'.repeat(40); }],
  ['a prerequisite from another run', data => { data.jobs[1].run_id += 1; }],
  ['a missing prerequisite', data => { data.jobs.splice(1, 1); }],
  ['a duplicated prerequisite', data => { data.jobs[1] = clone(data.jobs[0]); }],
  ['an extra job', data => { data.jobs.push(clone(data.jobs[0])); }],
  ['a failed tag verification', data => { data.jobs[5].steps[2].conclusion = 'failure'; }],
  ['a different failed publication step', data => { data.jobs[5].steps[3].name = 'Download combined verified assets'; }],
  ['a source publication that did not fail', data => { data.jobs[5].conclusion = 'success'; }],
  ['another artifact ID', data => { data.artifact.id += 1; }],
  ['another artifact name', data => { data.artifact.name = 'unverified-prerelease-assets'; }],
  ['another artifact digest', data => { data.artifact.digest = 'sha256:' + '0'.repeat(64); }],
  ['another artifact size', data => { data.artifact.size_in_bytes += 1; }],
  ['an expired artifact', data => { data.artifact.expired = true; }],
  ['an artifact without provenance', data => { delete data.artifact.workflow_run; }],
  ['an artifact from another run', data => { data.artifact.workflow_run.id += 1; }],
  ['an artifact from another commit', data => { data.artifact.workflow_run.head_sha = 'b'.repeat(40); }],
  ['an artifact from another branch', data => { data.artifact.workflow_run.head_branch = 'feature'; }],
  ['an artifact from another repository', data => { data.artifact.workflow_run.repository_id += 1; }],
  ['an artifact from a fork', data => { data.artifact.workflow_run.head_repository_id += 1; }],
]) {
  test(`provenance rejects ${label}`, () => {
    const data = provenance();
    mutate(data);
    assert.throws(() => validateProvenance(data.run, data.jobs, data.artifact));
  });
}

test('the initial plan is limited to the missing server asset, one rename, three text files and body', () => {
  const data = fixture();
  assert.deepEqual(planRepair(data.release, data.tagRef, data.original, data.desired).map(op => [op.kind, op.name]), [
    ['rename', NEW_ARM], ['upload', NEW_AMD], ['replace-text', 'README.md'],
    ['replace-text', NOTES], ['replace-text', 'SHA256SUMS.txt'], ['body', NOTES],
  ]);
});

for (const [label, mutate] of [
  ['a moved tag', data => { data.tagRef.object.sha = 'b'.repeat(40); }],
  ['another tag name', data => { data.tagRef.ref = 'refs/tags/v1.3.0-rc3'; }],
  ['a rewritten tag object', data => { data.tagRef.object.type = 'tag'; }],
  ['another release ID', data => { data.release.id += 1; }],
  ['another release tag', data => { data.release.tag_name = 'v1.3.0-rc3'; }],
  ['another target commit', data => { data.release.target_commitish = 'b'.repeat(40); }],
  ['a draft release', data => { data.release.draft = true; }],
  ['a stable release', data => { data.release.prerelease = false; }],
  ['an immutable release', data => { data.release.immutable = true; }],
  ['unknown body edits', data => { data.release.body += '\nUnrecognized external edit.'; }],
  ['an unknown public filename', data => { data.release.assets[0].name = 'unrecognized.zip'; }],
  ['a duplicate public name', data => { data.release.assets[0].name = data.release.assets[1].name; }],
  ['a duplicate public asset ID', data => { data.release.assets[0].id = data.release.assets[1].id; }],
  ['a missing established binary', data => { data.release.assets = data.release.assets.filter(asset => asset.name !== WINDOWS_NAMES[0]); }],
  ['a binary size mismatch', data => { data.release.assets[0].size += 1; }],
  ['a binary digest mismatch', data => { data.release.assets[0].digest = 'sha256:' + '0'.repeat(64); }],
  ['an unfinished uploaded asset', data => { data.release.assets[0].state = 'starter'; }],
  ['unknown README bytes', data => { data.release.assets.find(asset => asset.name === 'README.md').digest = 'sha256:' + '0'.repeat(64); }],
  ['a changed ARM asset identity', data => { data.release.assets.find(asset => asset.name === OLD_ARM).id += 10000; }],
  ['a changed desktop DEB identity', data => { data.release.assets.find(asset => asset.name === DESKTOP_DEB).id += 10000; }],
  ['both old and repaired ARM names', data => { data.release.assets.push({ ...data.release.assets.find(asset => asset.name === OLD_ARM), id: 750000000, name: NEW_ARM }); }],
  ['neither ARM name', data => { data.release.assets = data.release.assets.filter(asset => asset.name !== OLD_ARM); }],
  ['a case-altered desktop filename', data => { data.release.assets.find(asset => asset.name === DESKTOP_DEB).name = 'OMNISHARE_1.3.0-rc4_amd64.deb'; }],
]) {
  test(`planning rejects ${label} without writing`, async () => {
    const data = fixture();
    mutate(data);
    const model = memoryGitHub(data);
    await assert.rejects(model.run());
    assert.equal(model.requests.length, 0);
  });
}

test('PR dry-run reports the complete plan without invoking a mutation', async () => {
  const model = memoryGitHub();
  const before = clone(model.state);
  const result = await model.run({ dryRun: true, context: PR_CONTEXT });
  assert.equal(result.dryRun, true);
  assert.equal(result.planned.length, 6);
  assert.deepEqual(result.operations, []);
  assert.deepEqual(model.state, before);
  assert.equal(model.requests.length, 0);
});

for (const [label, context] of [
  ['another repository', { ...APPLIED_CONTEXT, repository: 'other/omnishare' }],
  ['a PR event', PR_CONTEXT],
  ['a workflow dispatch', { ...APPLIED_CONTEXT, eventName: 'workflow_dispatch' }],
  ['a non-main ref', { ...APPLIED_CONTEXT, ref: 'refs/heads/feature' }],
  ['a tag ref', { ...APPLIED_CONTEXT, ref: 'refs/tags/v1.3.0-rc4' }],
  ['an invalid execution SHA', { ...APPLIED_CONTEXT, sha: 'not-a-commit' }],
]) {
  test(`apply rejects ${label} before writing`, async () => {
    const model = memoryGitHub();
    await assert.rejects(model.run({ context }));
    assert.equal(model.requests.length, 0);
  });
}

test('apply requires an explicit boolean mode', async () => {
  const data = fixture();
  let writes = 0;
  for (const dryRun of [undefined, 'false', 0]) {
    await assert.rejects(executeRepair({ original: data.original, desired: data.desired, dryRun,
      context: APPLIED_CONTEXT, readState: async () => data, mutate: async () => { writes += 1; } }));
  }
  assert.equal(writes, 0);
});

test('the reviewed main repair commit produces 22 assets while preserving every existing binary', async () => {
  const model = memoryGitHub();
  assert.notEqual(APPLIED_CONTEXT.sha, ANCHOR.sha);
  assert.equal(model.state.release.assets.length, 21);
  const result = await model.run();
  assert.equal(result.success, true);
  assert.equal(result.verifiedAssets, 22);
  assert.equal(result.operations.length, 6);
  assert.equal(model.requests.length, 9);
  assertComplete(model);
  const deletes = model.requests.filter(request => request.method === 'DELETE');
  const deletedNames = deletes.map(request => model.initial.assets.find(asset => request.endpoint.endsWith('/' + asset.id))?.name);
  assert.deepEqual(deletedNames, TEXT_NAMES);
  const uploads = model.requests.filter(request => request.method === 'POST');
  assert.deepEqual(uploads.map(request => new URL('https://uploads.example.invalid' + request.endpoint).searchParams.get('name')), [NEW_AMD, ...TEXT_NAMES]);
  assert.ok(model.requests.every(request => !request.endpoint.includes('/git/') && !request.endpoint.includes('/tags/')));
});

test('repeating a completed repair verifies the final state and writes nothing', async () => {
  const model = memoryGitHub();
  await model.run();
  const previousWrites = model.requests.length;
  const result = await model.run();
  assert.deepEqual(result.planned, []);
  assert.deepEqual(result.operations, []);
  assert.equal(model.requests.length, previousWrites);
  assertComplete(model);
});

// Stop after each actual HTTP mutation, including the gap between DELETE and
// POST for a text attachment. Restart must converge without replaying binaries.
for (let cut = 1; cut <= 9; cut += 1) {
  test(`repair resumes after interruption following HTTP mutation ${cut}`, async () => {
    const model = memoryGitHub();
    model.failAfterRequest(cut);
    await assert.rejects(model.run(), /simulated interruption/);
    assert.equal(model.requests.length, cut);
    const result = await model.run();
    assert.equal(result.success, true);
    assertComplete(model);
    assert.equal(model.requests.filter(request => request.method === 'PATCH' && request.json?.name === NEW_ARM).length, 1);
    assert.equal(model.requests.filter(request => request.method === 'POST' && request.endpoint.endsWith('name=' + NEW_AMD)).length, 1);
    const before = model.requests.length;
    await model.run();
    assert.equal(model.requests.length, before);
  });
}

test('a release change between planning and the first write stops the repair', async () => {
  const model = memoryGitHub();
  model.beforeRead((state, count) => {
    if (count === 2) state.release.assets[0].digest = 'sha256:' + 'f'.repeat(64);
  });
  await assert.rejects(model.run());
  assert.equal(model.requests.length, 0);
});

test('a tag change after an operation prevents all subsequent writes', async () => {
  const model = memoryGitHub();
  model.afterWrite((state, count) => { if (count === 1) state.tagRef.object.sha = 'b'.repeat(40); });
  await assert.rejects(model.run());
  assert.equal(model.requests.length, 1);
});

for (const [label, mutate] of [
  ['a normalized upload name', asset => { asset.name = 'rewritten-server.deb'; }],
  ['a different upload digest', asset => { asset.digest = 'sha256:' + 'e'.repeat(64); }],
  ['a different upload size', asset => { asset.size += 1; }],
  ['an incomplete upload', asset => { asset.state = 'starter'; }],
]) {
  test(`the post-write verification rejects ${label}`, async () => {
    const model = memoryGitHub();
    model.afterWrite((state, count) => {
      if (count === 2) mutate(state.release.assets.find(asset => asset.name === NEW_AMD));
    });
    await assert.rejects(model.run());
    assert.equal(model.requests.length, 2, 'text attachments must not be edited after a binary upload mismatch');
  });
}

test('a change after the final body update is caught by the final full verification', async () => {
  const model = memoryGitHub();
  model.afterWrite((state, count) => {
    if (count === 9) state.release.assets.find(asset => asset.name === DESKTOP_DEB).size += 1;
  });
  await assert.rejects(model.run());
  assert.equal(model.requests.length, 9);
});

test('a transport that reports success without changing state cannot trigger unbounded retries', async () => {
  const data = fixture();
  let writes = 0;
  await assert.rejects(executeRepair({ original: data.original, desired: data.desired, dryRun: false,
    context: APPLIED_CONTEXT, readState: async () => clone(data), mutate: async () => { writes += 1; } }), /converge/);
  assert.ok(writes > 0 && writes <= 12);
});

for (const [label, operation] of [
  ['a desktop binary replacement', { kind: 'replace-text', id: ANCHOR.desktopDebAssetId, name: DESKTOP_DEB }],
  ['an unrelated binary upload', { kind: 'upload', name: WINDOWS_NAMES[0] }],
  ['an ARM reupload', { kind: 'upload', name: NEW_ARM }],
  ['a desktop binary rename', { kind: 'rename', id: ANCHOR.desktopDebAssetId, from: DESKTOP_DEB, name: NEW_AMD }],
  ['an unexpected server destination', { kind: 'rename', id: ANCHOR.armAssetId, from: OLD_ARM, name: 'arbitrary.deb' }],
  ['a different release body', { kind: 'body', id: ANCHOR.releaseId + 1, name: NOTES }],
  ['an unknown operation', { kind: 'delete', id: ANCHOR.armAssetId, name: OLD_ARM }],
]) {
  test(`the HTTP adapter rejects ${label} without making a request`, async () => {
    const data = fixture();
    let calls = 0;
    await assert.rejects(performOperation(operation, async () => { calls += 1; }, data.desired));
    assert.equal(calls, 0);
  });
}
