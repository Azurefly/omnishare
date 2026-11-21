import test from 'node:test';
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { devNull, tmpdir } from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import {
  CLEANUP, planCleanup, verifyPublishedRelease, gitDeleteArgs,
  executeCleanup, requireGreenCI,
} from './cleanup-release-history.mjs';

const candidateSha = 'c'.repeat(40);
const workSha = 'b'.repeat(40);
const keptReleaseId = 900000001;
const mainRef = 'refs/heads/' + CLEANUP.keepBranch;
const keptTagRef = 'refs/tags/' + CLEANUP.keepTag;
const context = {
  repository: CLEANUP.repo, mode: 'apply', eventName: 'push', ref: mainRef,
  candidateSha, mainSha: candidateSha, workSha,
};
const legacyRefs = [
  ...CLEANUP.tags.map(tag => ({ ref: 'refs/tags/' + tag.name, sha: tag.sha })),
  ...CLEANUP.branches.map(branch => ({
    ref: 'refs/heads/' + branch.name,
    sha: branch.sha === 'merged-pr-head' ? workSha : branch.sha,
  })),
];
const ancestors = new Set([
  candidateSha, workSha,
  ...CLEANUP.tags.map(tag => tag.commit),
  ...CLEANUP.branches.filter(branch => branch.sha !== 'merged-pr-head').map(branch => branch.sha),
]);
const isAncestor = (commit, candidate) => candidate === candidateSha && ancestors.has(commit);
const expectedAssets = new Map(Array.from({ length: 22 }, (_, index) => {
  const bytes = Buffer.from('Verified publication fixture ' + index + '\n');
  return ['fixture-' + String(index).padStart(2, '0') + '.bin', {
    size: bytes.length,
    digest: 'sha256:' + createHash('sha256').update(bytes).digest('hex'),
  }];
}));

function fixture() {
  return {
    refs: new Map([[mainRef, candidateSha], [keptTagRef, candidateSha],
      ...legacyRefs.map(item => [item.ref, item.sha])]),
    releases: [
      ...CLEANUP.releases.map(item => ({
        id: item.id, tag_name: item.tag, immutable: false, draft: false,
        prerelease: item.tag.includes('-rc'), assets: [],
      })),
      {
        id: keptReleaseId, tag_name: CLEANUP.keepTag, target_commitish: candidateSha,
        draft: false, prerelease: false, immutable: false,
        assets: [...expectedAssets].map(([name, meta], index) => ({
          id: 100000 + index, name, state: 'uploaded', ...meta,
        })),
      },
    ],
  };
}
function keptRelease(state) {
  return state.releases.find(release => release.tag_name === CLEANUP.keepTag);
}
function plan(state, overrides = {}) {
  return planCleanup({ ...state, context, isAncestor, ...overrides });
}
function verify(state) {
  return verifyPublishedRelease(keptRelease(state), state.refs, expectedAssets, candidateSha);
}
function harness(options = {}) {
  const state = options.state ?? fixture();
  const writes = [];
  let reads = 0;
  let verifications = 0;
  async function wrote(operation) {
    writes.push(operation);
    options.afterWrite?.(state, writes.length, operation);
    if (options.stopAfterWrite === writes.length) throw new Error('Simulated response loss after write');
  }
  return {
    state, writes,
    get reads() { return reads; },
    get verifications() { return verifications; },
    run(overrides = {}) {
      return executeCleanup({
        context, isAncestor,
        readState: async () => { reads += 1; return structuredClone(state); },
        verify: async current => { verifications += 1; return verify(current); },
        deleteRelease: async id => {
          const index = state.releases.findIndex(release => release.id === id);
          assert.notEqual(index, -1, 'must not delete an absent release again');
          state.releases.splice(index, 1);
          await wrote({ kind: 'release', id });
        },
        deleteRefs: async refs => {
          for (const item of refs) assert.equal(state.refs.get(item.ref), item.sha, 'lease must match');
          for (const item of refs) state.refs.delete(item.ref);
          await wrote({ kind: 'refs', refs: structuredClone(refs) });
        },
        ...overrides,
      });
    },
  };
}
function assertOnlyProtectedRemain(state) {
  assert.deepEqual(state.releases.map(release => release.id), [keptReleaseId]);
  assert.deepEqual(state.refs, new Map([[mainRef, candidateSha], [keptTagRef, candidateSha]]));
  assert.equal(verify(state), keptReleaseId);
}

test('the reviewed inventory plans exactly four releases and seven refs, preserving main and the new tag', () => {
  const state = fixture();
  const before = structuredClone(state);
  const result = plan(state);
  assert.equal(result.releases.length, 4);
  assert.equal(result.refs.length, 7);
  assert.deepEqual(new Set(result.releases.map(item => item.id)), new Set(CLEANUP.releases.map(item => item.id)));
  assert.deepEqual(result.refs, legacyRefs);
  assert.ok(!result.refs.some(item => item.ref === mainRef || item.ref === keptTagRef));
  assert.deepEqual(state, before, 'planning must not mutate its inventory');
  // The annotated legacy tag has a distinct raw object SHA and commit SHA.
  // isAncestor deliberately accepts only its peeled commit, never its tag object.
  assert.ok(CLEANUP.tags.some(tag => tag.sha !== tag.commit));
});

test('unknown releases/refs, identity changes, moved refs and unmerged history fail closed', () => {
  const cases = [
    ['unknown release', state => state.releases.push({ id: 800000001, tag_name: 'v9.9.9', immutable: false })],
    ['replacement release ID', state => { state.releases[0].id += 1; }],
    ['unknown branch', state => state.refs.set('refs/heads/unreviewed-work', workSha)],
    ['unknown tag', state => state.refs.set('refs/tags/v9.9.9', workSha)],
    ['moved old tag', state => state.refs.set(legacyRefs[0].ref, candidateSha)],
    ['moved old branch', state => state.refs.set(legacyRefs.at(-1).ref, candidateSha)],
    ['moved main', state => state.refs.set(mainRef, workSha)],
    ['moved protected tag', state => state.refs.set(keptTagRef, workSha)],
    ['immutable old release', state => { state.releases[0].immutable = true; }],
  ];
  for (const [name, change] of cases) {
    const state = fixture();
    change(state);
    assert.throws(() => plan(state), undefined, name);
  }
  const unmerged = CLEANUP.tags[0].commit;
  assert.throws(() => plan(fixture(), {
    isAncestor: (commit, candidate) => commit !== unmerged && isAncestor(commit, candidate),
  }), /Unmerged/);
});

test('publication verification requires the complete exact uploaded inventory and protected refs', () => {
  assert.equal(verify(fixture()), keptReleaseId);
  const cases = [
    ['missing file', state => keptRelease(state).assets.pop()],
    ['unexpected file', state => { keptRelease(state).assets[0].name = 'unverified.bin'; }],
    ['changed bytes', state => { keptRelease(state).assets[0].digest = 'sha256:' + '0'.repeat(64); }],
    ['incomplete upload', state => { keptRelease(state).assets[0].state = 'starter'; }],
    ['case-insensitive collision', state => {
      keptRelease(state).assets[1].name = keptRelease(state).assets[0].name.toUpperCase();
    }],
    ['draft release', state => { keptRelease(state).draft = true; }],
    ['wrong release commit', state => { keptRelease(state).target_commitish = workSha; }],
    ['missing protected tag', state => state.refs.delete(keptTagRef)],
    ['main moved after publishing', state => state.refs.set(mainRef, workSha)],
  ];
  for (const [name, change] of cases) {
    const state = fixture();
    change(state);
    assert.throws(() => verify(state), undefined, name);
  }
});

test('PR planning before publication performs no writes and does not require a new release', async () => {
  const state = fixture();
  state.releases = state.releases.filter(release => release.tag_name !== CLEANUP.keepTag);
  state.refs.delete(keptTagRef);
  const oldMain = CLEANUP.tags[0].commit;
  state.refs.set(mainRef, oldMain);
  const currentWork = CLEANUP.branches.find(branch => branch.sha === 'merged-pr-head');
  state.refs.set('refs/heads/' + currentWork.name, candidateSha);
  const before = structuredClone(state);
  const run = harness({ state });
  const result = await run.run({ context: {
    ...context, mode: 'plan', eventName: 'pull_request', ref: 'refs/pull/42/merge',
    mainSha: oldMain, workSha: candidateSha,
  } });
  assert.equal(result.dryRun, true);
  assert.equal(result.planned.releases.length, 4);
  assert.equal(result.planned.refs.length, 7);
  assert.deepEqual(run.writes, []);
  assert.equal(run.verifications, 0);
  assert.deepEqual(state, before);
});

test('apply requires the authorized main push and a complete new release before any deletion', async () => {
  for (const override of [
    { repository: 'someone/omnishare' },
    { eventName: 'pull_request' },
    { ref: 'refs/heads/another-branch' },
    { mainSha: workSha },
  ]) {
    const run = harness();
    await assert.rejects(run.run({ context: { ...context, ...override } }));
    assert.deepEqual(run.writes, []);
  }
  for (const change of [
    state => { state.releases = state.releases.filter(release => release.tag_name !== CLEANUP.keepTag); },
    state => keptRelease(state).assets.pop(),
  ]) {
    const state = fixture();
    change(state);
    const run = harness({ state });
    await assert.rejects(run.run());
    assert.deepEqual(run.writes, []);
  }
});

test('successful cleanup removes only the reviewed objects and a completed rerun writes nothing', async () => {
  const run = harness();
  const result = await run.run();
  assert.equal(result.success, true);
  assert.deepEqual(new Set(run.writes.filter(item => item.kind === 'release').map(item => item.id)),
    new Set(CLEANUP.releases.map(item => item.id)));
  const refWrites = run.writes.filter(item => item.kind === 'refs');
  assert.equal(refWrites.length, 1, 'all refs must be passed to one atomic deletion');
  assert.deepEqual(refWrites[0].refs, legacyRefs);
  assertOnlyProtectedRemain(run.state);
  const before = structuredClone(run.state);
  const rerun = harness({ state: run.state });
  await rerun.run();
  assert.deepEqual(rerun.writes, []);
  assert.deepEqual(rerun.state, before);
});

test('a lost response after a release or atomic ref deletion resumes without repeating completed writes', async () => {
  for (const stopAfterWrite of [1, 3, 5]) {
    const interrupted = harness({ stopAfterWrite });
    await assert.rejects(interrupted.run(), /Simulated response loss/);
    const deletedIds = new Set(interrupted.writes.filter(item => item.kind === 'release').map(item => item.id));
    const resumed = harness({ state: interrupted.state });
    await resumed.run();
    assert.ok(resumed.writes.filter(item => item.kind === 'release').every(item => !deletedIds.has(item.id)));
    assertOnlyProtectedRemain(resumed.state);
    if (stopAfterWrite === 5) assert.deepEqual(resumed.writes, []);
  }
});

test('each next mutation rechecks inventory and the retained release after an earlier deletion', async () => {
  for (const mutate of [
    state => state.refs.set('refs/heads/new-unreviewed-work', workSha),
    state => { keptRelease(state).assets[0].digest = 'sha256:' + 'f'.repeat(64); },
    state => { keptRelease(state).id += 1; },
  ]) {
    const run = harness({ afterWrite: (state, count) => { if (count === 1) mutate(state); } });
    await assert.rejects(run.run());
    assert.equal(run.writes.length, 1, 'must stop before deleting another object');
    assert.equal(run.state.refs.get(mainRef), candidateSha);
    assert.equal(run.state.refs.get(keptTagRef), candidateSha);
    assert.ok(legacyRefs.every(item => run.state.refs.get(item.ref) === item.sha));
    assert.ok(run.reads >= 3, 'must reread after a mutation');
  }
});

test('the final state is verified even after the last atomic ref write', async () => {
  const run = harness({ afterWrite: (state, _count, operation) => {
    if (operation.kind === 'refs') keptRelease(state).assets.pop();
  } });
  await assert.rejects(run.run(), /incomplete/);
  assert.equal(run.writes.length, 5);
  assert.equal(run.state.refs.get(mainRef), candidateSha);
  assert.equal(run.state.refs.get(keptTagRef), candidateSha);
});

test('Git deletion args contain only explicit leases and refspecs, rejecting protected or ambiguous input', () => {
  const args = gitDeleteArgs(legacyRefs);
  assert.deepEqual(args, ['push', '--atomic',
    ...legacyRefs.map(item => '--force-with-lease=' + item.ref + ':' + item.sha),
    'origin', ...legacyRefs.map(item => ':' + item.ref)]);
  for (const input of [
    [],
    [legacyRefs[0], { ref: mainRef, sha: candidateSha }],
    [legacyRefs[0], { ref: keptTagRef, sha: candidateSha }],
    [legacyRefs[0], { ref: 'refs/heads/unreviewed', sha: workSha }],
    [legacyRefs[0], legacyRefs[0]],
    [{ ...legacyRefs[0], sha: '--all' }],
  ]) assert.throws(() => gitDeleteArgs(input));
});

test('only the latest exact main-push CI result can authorize cleanup', () => {
  const success = { id: 100, head_sha: candidateSha, event: 'push',
    path: '.github/workflows/ci.yml', head_branch: 'main', status: 'completed', conclusion: 'success' };
  assert.doesNotThrow(() => requireGreenCI([success], candidateSha));
  assert.throws(() => requireGreenCI([{ ...success, head_sha: workSha }], candidateSha));
  assert.throws(() => requireGreenCI([{ ...success, event: 'pull_request' }], candidateSha));
  assert.throws(() => requireGreenCI([success, { ...success, id: 101, conclusion: 'failure' }], candidateSha));
  assert.throws(() => requireGreenCI([success, { ...success, id: 101, status: 'in_progress', conclusion: null }], candidateSha));
});

function git(cwd, args, { allowFailure = false } = {}) {
  const result = spawnSync('git', args, {
    cwd, encoding: 'utf8', timeout: 15000,
    env: { ...process.env, GIT_TERMINAL_PROMPT: '0', GIT_CONFIG_NOSYSTEM: '1',
      GIT_CONFIG_GLOBAL: devNull, GIT_ALLOW_PROTOCOL: 'file', LC_ALL: 'C', LANG: 'C' },
  });
  if (result.error) throw result.error;
  if (!allowFailure) assert.equal(result.status, 0, result.stderr || result.stdout);
  return result;
}
function remoteSnapshot(directory) {
  const output = git(directory, ['for-each-ref', '--format=%(refname)%09%(objectname)']).stdout.trim();
  return new Map(output ? output.split('\n').map(line => line.split('\t')) : []);
}

test('real Git atomically rejects stale leases without losing refs, then deletes only reviewed refs', t => {
  const root = mkdtempSync(path.join(tmpdir(), 'omnishare-cleanup-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const remote = path.join(root, 'remote.git');
  const work = path.join(root, 'work');
  git(root, ['init', '--bare', remote]);
  git(root, ['init', '--initial-branch=main', work]);
  git(work, ['config', 'user.name', 'Cleanup Test']);
  git(work, ['config', 'user.email', 'cleanup-test@example.invalid']);
  git(work, ['config', 'commit.gpgSign', 'false']);
  git(work, ['remote', 'add', 'origin', remote]);
  git(work, ['commit', '--allow-empty', '-m', 'Legacy baseline']);
  const baseline = git(work, ['rev-parse', 'HEAD']).stdout.trim();
  git(work, ['commit', '--allow-empty', '-m', 'Published candidate']);
  const published = git(work, ['rev-parse', 'HEAD']).stdout.trim();
  git(work, ['tag', CLEANUP.keepTag, published]);
  for (const [index, tag] of CLEANUP.tags.entries()) {
    if (index === 0) git(work, ['tag', '-a', tag.name, baseline, '-m', 'Annotated legacy tag']);
    else git(work, ['tag', tag.name, baseline]);
  }
  for (const branch of CLEANUP.branches) git(work, ['branch', branch.name, baseline]);
  const allRefs = [mainRef, keptTagRef, ...legacyRefs.map(item => item.ref)];
  git(work, ['push', '--atomic', 'origin', ...allRefs.map(ref => ref + ':' + ref)]);
  const initial = remoteSnapshot(remote);
  const reviewed = legacyRefs.map(item => ({ ref: item.ref, sha: initial.get(item.ref) }));
  const changedRef = 'refs/heads/' + CLEANUP.branches[0].name;
  git(remote, ['update-ref', changedRef, published, baseline]);
  const afterConcurrentWrite = remoteSnapshot(remote);
  const rejected = git(work, gitDeleteArgs(reviewed), { allowFailure: true });
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /stale info/);
  assert.deepEqual(remoteSnapshot(remote), afterConcurrentWrite,
    'a stale lease must preserve every ref, including other deletable refs');
  assert.equal(afterConcurrentWrite.get(mainRef), published);
  assert.equal(afterConcurrentWrite.get(keptTagRef), published);
  // Restore the known reviewed state, rather than silently refreshing a failed lease.
  git(remote, ['update-ref', changedRef, baseline, published]);
  git(work, gitDeleteArgs(reviewed));
  assert.deepEqual(remoteSnapshot(remote), new Map([[mainRef, published], [keptTagRef, published]]));
});
