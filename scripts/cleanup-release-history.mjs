// The owner-approved 0.1.0 reset: retire only the reviewed legacy publications.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export const CLEANUP = Object.freeze({
  repo: 'Azurefly/omnishare',
  keepTag: 'v0.1.0',
  keepBranch: 'main',
  releases: [
    { id: 437412874, tag: 'v1.3.0-rc4' },
    { id: 437386229, tag: 'v1.3.0-rc3' },
    { id: 419050857, tag: 'v1.3.0-rc2' },
    { id: 364582649, tag: 'v1.0.1-desktop' },
  ],
  tags: [
    { name: 'v1.0.1-desktop', sha: '008ec80fc32c22e3cfc32ea052750eadaba45d63', commit: '008ec80fc32c22e3cfc32ea052750eadaba45d63' },
    { name: 'v1.3.0-rc2', sha: '8a8fccc0fd25814f26c7ee0ac36ab8f40e58b6fc', commit: '2571b5a7b04dcafbfe0720166d9ad83c4461794b' },
    { name: 'v1.3.0-rc3', sha: '0d1fb8a59662a36068f15e5a4c2e1c911bcd4f52', commit: '0d1fb8a59662a36068f15e5a4c2e1c911bcd4f52' },
    { name: 'v1.3.0-rc4', sha: 'c96c102e7eb0c327b2c8a8248416be12f614d4d3', commit: 'c96c102e7eb0c327b2c8a8248416be12f614d4d3' },
  ],
  branches: [
    { name: 'feat/sqlite-encrypted-backup', sha: 'c121dfa19f5b072097c26c1c40bac62d6cbd6122' },
    { name: 'fix/security-and-backup-hardening', sha: '28516b56d2643ab398066deeb5de200749b6749a' },
    { name: 'fix/integrated-stability-20261107', sha: 'merged-pr-head' },
  ],
});
const shaPattern = /^[a-f0-9]{40}$/;
function check(condition, message) {
  if (!condition) throw new Error(message);
}
export function planCleanup({ releases, refs, context, isAncestor }) {
  check(context?.repository === CLEANUP.repo && ['plan', 'apply'].includes(context.mode),
    'Unexpected repository or cleanup mode');
  for (const field of ['candidateSha', 'mainSha', 'workSha']) {
    check(shaPattern.test(context[field] ?? ''), 'Invalid cleanup commit: ' + field);
  }
  if (context.mode === 'apply') {
    check(context.eventName === 'push' && context.ref === 'refs/heads/main' &&
      context.mainSha === context.candidateSha, 'Cleanup writes require the main push release commit');
  }
  check(refs instanceof Map && Array.isArray(releases), 'Missing remote inventory');
  check(refs.get('refs/heads/main') === context.mainSha, 'Remote main changed');
  check(isAncestor(context.mainSha, context.candidateSha), 'Candidate does not contain the reviewed main');
  const oldRefs = new Map();
  for (const tag of CLEANUP.tags) {
    oldRefs.set('refs/tags/' + tag.name, { sha: tag.sha, commit: tag.commit });
  }
  for (const branch of CLEANUP.branches) {
    const sha = branch.sha === 'merged-pr-head' ? context.workSha : branch.sha;
    oldRefs.set('refs/heads/' + branch.name, { sha, commit: sha });
  }
  const keptTag = 'refs/tags/' + CLEANUP.keepTag;
  const plannedRefs = [];
  for (const [ref, sha] of refs) {
    if (ref === 'refs/heads/main') continue;
    if (ref === keptTag) {
      check(sha === context.candidateSha, 'The new release tag points to another commit');
      continue;
    }
    const expected = oldRefs.get(ref);
    check(expected, 'Unknown remote ref; refusing cleanup: ' + ref);
    check(sha === expected.sha, 'Legacy ref changed: ' + ref);
    check(isAncestor(expected.commit, context.candidateSha), 'Unmerged ref would lose history: ' + ref);
    plannedRefs.push({ ref, sha });
  }
  const ids = new Set();
  const tags = new Set();
  const plannedReleases = [];
  let retained;
  for (const release of releases) {
    check(Number.isSafeInteger(release.id) && release.id > 0 &&
      !ids.has(release.id) && !tags.has(release.tag_name), 'Duplicate or invalid release identity');
    ids.add(release.id);
    tags.add(release.tag_name);
    if (release.tag_name === CLEANUP.keepTag) {
      check(release.target_commitish === context.candidateSha && release.draft === false &&
        release.prerelease === false && refs.get(keptTag) === context.candidateSha,
      'New release is not the expected public 0.1.0 release');
      retained = release;
      continue;
    }
    const expected = CLEANUP.releases.find(item => item.id === release.id && item.tag === release.tag_name);
    check(expected, 'Unknown release; refusing cleanup: ' + release.tag_name);
    check(release.immutable === false, 'Legacy release is immutable: ' + release.tag_name);
    plannedReleases.push({ id: release.id, tag: release.tag_name });
  }
  if (context.mode === 'apply') {
    check(retained && refs.get(keptTag) === context.candidateSha,
      'Verified new release and tag must exist before cleanup');
  }
  return { releases: plannedReleases, refs: plannedRefs };
}
export function verifyPublishedRelease(release, refs, expected, candidateSha) {
  check(release && Number.isSafeInteger(release.id) && release.id > 0 &&
    release.tag_name === CLEANUP.keepTag && release.target_commitish === candidateSha &&
    release.draft === false && release.prerelease === false,
  'The new public release is not ready');
  check(refs.get('refs/heads/main') === candidateSha &&
    refs.get('refs/tags/' + CLEANUP.keepTag) === candidateSha, 'Protected release refs changed');
  check(expected instanceof Map && expected.size === 22, 'Expected the 22 verified publication files');
  check(Array.isArray(release.assets) && release.assets.length === expected.size,
    'Published asset inventory is incomplete');
  const seen = new Set();
  const folded = new Set();
  for (const asset of release.assets) {
    check(typeof asset.name === 'string' && !seen.has(asset.name) &&
      !folded.has(asset.name.toLowerCase()), 'Published names are ambiguous');
    seen.add(asset.name);
    folded.add(asset.name.toLowerCase());
    const file = expected.get(asset.name);
    check(file && file.size > 0 && /^sha256:[a-f0-9]{64}$/.test(file.digest) &&
      asset.state === 'uploaded' && asset.size === file.size && asset.digest === file.digest,
    'Published asset verification failed: ' + asset.name);
  }
  return release.id;
}
export function gitDeleteArgs(refs) {
  check(Array.isArray(refs) && refs.length > 0, 'No refs selected');
  const allowed = new Set([
    ...CLEANUP.tags.map(tag => 'refs/tags/' + tag.name),
    ...CLEANUP.branches.map(branch => 'refs/heads/' + branch.name),
  ]);
  const seen = new Set();
  for (const item of refs) {
    check(allowed.has(item.ref) && !seen.has(item.ref) && shaPattern.test(item.sha),
      'Unapproved or ambiguous ref deletion');
    seen.add(item.ref);
  }
  return ['push', '--atomic',
    ...refs.map(item => '--force-with-lease=' + item.ref + ':' + item.sha),
    'origin', ...refs.map(item => ':' + item.ref)];
}
export async function executeCleanup({
  readState, verify, deleteRelease, deleteRefs, context, isAncestor,
}) {
  let state = await readState();
  const planned = planCleanup({ ...state, context, isAncestor });
  if (context.mode === 'plan') return { success: true, dryRun: true, planned, deleted: { releases: [], refs: [] } };
  const retainedId = await verify(state);
  check(Number.isSafeInteger(retainedId) && retainedId > 0, 'Missing verified retained release');
  const deleted = { releases: [], refs: [] };
  for (const legacy of CLEANUP.releases) {
    state = await readState();
    const current = planCleanup({ ...state, context, isAncestor });
    check(await verify(state) === retainedId, 'Retained release identity changed');
    if (current.releases.some(item => item.id === legacy.id)) {
      await deleteRelease(legacy.id);
      deleted.releases.push(legacy);
    }
  }
  state = await readState();
  const remaining = planCleanup({ ...state, context, isAncestor });
  check(await verify(state) === retainedId, 'Retained release identity changed');
  if (remaining.refs.length) {
    await deleteRefs(remaining.refs);
    deleted.refs.push(...remaining.refs);
  }
  state = await readState();
  const finalPlan = planCleanup({ ...state, context, isAncestor });
  check(await verify(state) === retainedId, 'Final release verification failed');
  check(finalPlan.releases.length === 0 && finalPlan.refs.length === 0 &&
    state.releases.length === 1 && state.refs.size === 2, 'Remote cleanup is not complete');
  return { success: true, dryRun: false, planned, deleted, keepReleaseId: retainedId,
    keepTag: CLEANUP.keepTag, keepBranch: CLEANUP.keepBranch };
}
function git(args) {
  return execFileSync('git', args, { encoding: 'utf8', maxBuffer: 4 * 1024 * 1024 }).trim();
}
function remoteRefs() {
  const refs = new Map();
  for (const line of git(['ls-remote', '--heads', '--tags', 'origin']).split('\n')) {
    if (!line) continue;
    const [sha, ref] = line.split(/\s+/);
    if (ref.endsWith('^{}')) continue;
    check(shaPattern.test(sha) && !refs.has(ref), 'Malformed remote ref inventory');
    refs.set(ref, sha);
  }
  return refs;
}
function ancestor(commit, candidate) {
  const result = spawnSync('git', ['merge-base', '--is-ancestor', commit, candidate], { encoding: 'utf8' });
  check([0, 1].includes(result.status), 'Unable to inspect commit ancestry');
  return result.status === 0;
}
function apiClient(token) {
  check(token, 'GH_TOKEN is required for the repository workflow');
  return async (suffix, method = 'GET') => {
    check(suffix.startsWith('/'), 'Unexpected API path');
    if (method !== 'GET') {
      check(method === 'DELETE' &&
        CLEANUP.releases.some(item => suffix === '/releases/' + item.id), 'Unapproved API mutation');
    }
    const response = await fetch('https://api.github.com/repos/' + CLEANUP.repo + suffix, {
      method, redirect: 'error',
      headers: { Authorization: 'Bearer ' + token, Accept: 'application/vnd.github+json',
        'X-GitHub-Api-Version': '2022-11-28' },
    });
    check(response.ok, 'GitHub ' + method + ' ' + suffix.split('?')[0] + ' returned HTTP ' + response.status);
    return response.status === 204 ? null : response.json();
  };
}
async function listReleases(api) {
  const all = [];
  for (let page = 1; page <= 10; page += 1) {
    const batch = await api('/releases?per_page=100&page=' + page);
    check(Array.isArray(batch), 'Invalid release inventory');
    all.push(...batch);
    if (batch.length < 100) return all;
  }
  throw new Error('Release pagination exceeds the reviewed cleanup scope');
}
export function requireGreenCI(runs, sha) {
  const match = runs.filter(run => run.head_sha === sha && run.event === 'push' &&
    run.path === '.github/workflows/ci.yml' && run.head_branch === 'main')
    .sort((a, b) => b.id - a.id)[0];
  check(match?.status === 'completed' && match.conclusion === 'success',
    'The exact main commit must pass CI before retiring old publications');
}
function expectedAssets(directory) {
  const entries = fs.readdirSync(directory, { withFileTypes: true });
  const expected = new Map();
  for (const entry of entries) {
    check(entry.isFile() && entry.name === path.basename(entry.name), 'Unexpected publication artifact entry');
    const bytes = fs.readFileSync(path.join(directory, entry.name));
    expected.set(entry.name, { size: bytes.length,
      digest: 'sha256:' + crypto.createHash('sha256').update(bytes).digest('hex') });
  }
  return expected;
}
async function main() {
  const args = process.argv.slice(2);
  check(args.length === 1 && ['--plan', '--apply'].includes(args[0]), 'Use --plan or --apply');
  check(process.env.GITHUB_REPOSITORY === CLEANUP.repo &&
    fs.readFileSync('VERSION', 'utf8').trim() === '0.1.0', 'Cleanup is only for the authorized 0.1.0 reset');
  check(/^https:\/\/github\.com\/Azurefly\/omnishare(?:\.git)?$/.test(git(['remote', 'get-url', 'origin'])),
    'Unexpected git remote');
  const isPR = process.env.GITHUB_EVENT_NAME === 'pull_request';
  const candidateSha = isPR ? process.env.PR_HEAD_SHA : process.env.GITHUB_SHA;
  const mainSha = isPR ? process.env.PR_BASE_SHA : process.env.GITHUB_SHA;
  check(shaPattern.test(candidateSha ?? '') && shaPattern.test(mainSha ?? ''), 'Missing workflow commit context');
  const workSha = isPR ? candidateSha : git(['rev-parse', candidateSha + '^2']);
  const context = { repository: process.env.GITHUB_REPOSITORY, mode: args[0] === '--apply' ? 'apply' : 'plan',
    eventName: process.env.GITHUB_EVENT_NAME, ref: process.env.GITHUB_REF, candidateSha, mainSha, workSha };
  const api = apiClient(process.env.GH_TOKEN);
  const readState = async () => ({ releases: await listReleases(api), refs: remoteRefs() });
  const initial = await readState();
  const plan = planCleanup({ ...initial, context, isAncestor: ancestor });
  fs.mkdirSync('release-cleanup-evidence', { recursive: true });
  fs.writeFileSync('release-cleanup-evidence/plan.json', JSON.stringify({ context, plan }, null, 2) + '\n');
  let expected;
  if (context.mode === 'apply') {
    const ci = await api('/actions/runs?head_sha=' + candidateSha + '&event=push&per_page=100');
    requireGreenCI(ci.workflow_runs ?? [], candidateSha);
    expected = expectedAssets('release-assets');
  }
  const result = await executeCleanup({
    readState, context, isAncestor: ancestor,
    verify: state => verifyPublishedRelease(
      state.releases.find(release => release.tag_name === CLEANUP.keepTag), state.refs, expected, candidateSha),
    deleteRelease: async id => {
      await api('/releases/' + id, 'DELETE');
      console.log('Deleted approved legacy release ID ' + id);
    },
    deleteRefs: async refs => {
      git(gitDeleteArgs(refs));
      console.log('Deleted approved refs with atomic compare-and-delete: ' + refs.map(item => item.ref).join(', '));
    },
  });
  const evidence = { ...result, executionSha: candidateSha, runId: process.env.GITHUB_RUN_ID };
  fs.writeFileSync('release-cleanup-evidence/result.json', JSON.stringify(evidence, null, 2) + '\n');
  console.log(JSON.stringify(evidence, null, 2));
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
