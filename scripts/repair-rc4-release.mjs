// One-time, byte-preserving repair of the verified RC4 publication.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { Readable, Transform } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';

export const ANCHOR = Object.freeze({
  repo: 'Azurefly/omnishare',
  repositoryId: 1319116498,
  sha: 'c96c102e7eb0c327b2c8a8248416be12f614d4d3',
  runId: 42350497115,
  workflowId: 326617400,
  artifactId: 13565807241,
  artifactDigest: 'sha256:3d73ac96950c2a6bebe9827aa95675efe961c270334c1bc8e79876ac75c0c47b',
  artifactSize: 182364173,
  releaseId: 437412874,
  tag: 'v1.3.0-rc4',
  version: '1.3.0-rc4',
  armAssetId: 719967246,
  desktopDebAssetId: 719967257,
});
const NOTES = 'RELEASE_NOTES_v1.3.0-rc4.md';
const OLD_AMD = 'omnishare_1.3.0-rc4_amd64.deb';
const OLD_ARM = 'omnishare_1.3.0-rc4_arm64.deb';
const DESKTOP_DEB = 'OmniShare_1.3.0-rc4_amd64.deb';
const TEXT_FILES = new Set(['README.md', NOTES, 'SHA256SUMS.txt']);
const SUCCESSFUL_JOBS = [
  'Read release metadata',
  'Build and test server release packages',
  'Build and test Windows release assets',
  'Build and test Linux release assets',
  'Verify combined server and desktop assets',
];
export const ORIGINAL_NAMES = Object.freeze([
  'OmniShare-v1.3.0-rc4-linux-amd64.tar.gz',
  'OmniShare-v1.3.0-rc4-linux-arm64.tar.gz',
  'OmniShare-v1.3.0-rc4-macos-amd64.zip',
  'OmniShare-v1.3.0-rc4-macos-arm64.zip',
  'OmniShare-v1.3.0-rc4-windows-amd64.zip',
  'OmniShare-v1.3.0-rc4-windows-arm64.zip',
  OLD_AMD, OLD_ARM,
  'OmniShare-1.3.0-0.rc4.x86_64.rpm',
  'OmniShare_1.3.0-rc4_amd64.AppImage',
  DESKTOP_DEB,
  'OmniShare_1.3.0-rc4_x64-setup.exe',
  'OmniShare_1.3.0-rc4_x64_en-US.msi',
  'omnishare-linux-sha256.txt',
  'omnishare-windows-runtime.junit.xml',
  'omnishare-windows-sha256.json',
  'omnishare-windows-watchdog.json',
  'omnishare-windows-watchdog.junit.xml',
  'README.md', NOTES, 'REMEDIATION_v1.3.0-rc1.md', 'SHA256SUMS.txt',
]);
function requireThat(value, message) {
  if (!value) throw new Error(message);
}
export function digestOf(bytes) {
  return 'sha256:' + crypto.createHash('sha256').update(bytes).digest('hex');
}
export function desiredName(name) {
  if (name === OLD_AMD) return 'omnishare-server_1.3.0-rc4_amd64.deb';
  if (name === OLD_ARM) return 'omnishare-server_1.3.0-rc4_arm64.deb';
  return name;
}
function exactNames(actual, expected, label) {
  requireThat(actual.length === expected.length &&
    new Set(actual).size === actual.length &&
    actual.every(name => expected.includes(name)), label + ' names do not match the pinned inventory');
}
export function buildDesired(original, readme, notes) {
  exactNames([...original.keys()], ORIGINAL_NAMES, 'Original artifact');
  for (const [name, bytes] of original) {
    requireThat(Buffer.isBuffer(bytes) && bytes.length > 0, 'Empty or invalid source asset: ' + name);
  }
  requireThat(Buffer.isBuffer(readme) && readme.length > 0 &&
    Buffer.isBuffer(notes) && notes.length > 0, 'Corrected documents must be nonempty buffers');
  const desired = new Map([...original].map(([name, bytes]) => [desiredName(name), bytes]));
  desired.set('README.md', readme);
  desired.set(NOTES, notes);
  const lines = original.get('SHA256SUMS.txt').toString('utf8').trim().split(/\r?\n/);
  const checksumNames = [...ORIGINAL_NAMES.slice(0, 8), 'README.md', NOTES, 'REMEDIATION_v1.3.0-rc1.md'];
  const seen = [];
  const corrected = lines.map(line => {
    const match = /^([a-f0-9]{64}) {2}([^/\\\r\n]+)$/.exec(line);
    requireThat(match, 'Malformed original checksum entry');
    const [, hash, name] = match;
    requireThat(original.has(name) && digestOf(original.get(name)) === 'sha256:' + hash,
      'Original checksum mismatch: ' + name);
    seen.push(name);
    const target = desiredName(name);
    return digestOf(desired.get(target)).slice(7) + '  ' + target;
  });
  exactNames(seen, checksumNames, 'Original checksum');
  desired.set('SHA256SUMS.txt', Buffer.from(corrected.join('\n') + '\n'));
  const folded = [...desired.keys()].map(name => name.toLowerCase());
  requireThat(new Set(folded).size === folded.length, 'Desired names collide when ignoring case');
  return desired;
}
export function validateProvenance(run, jobs, artifact) {
  requireThat(run.id === ANCHOR.runId && run.workflow_id === ANCHOR.workflowId &&
    run.head_sha === ANCHOR.sha && run.head_branch === 'main' && run.event === 'push' &&
    run.path === '.github/workflows/publish-desktop-prerelease.yml' &&
    run.run_attempt === 1 && run.status === 'completed' && run.conclusion === 'failure' &&
    run.repository?.full_name === ANCHOR.repo && run.repository?.id === ANCHOR.repositoryId,
  'Source workflow identity or outcome changed');
  requireThat(Array.isArray(jobs) && jobs.length === 6, 'Source workflow job inventory changed');
  for (const name of SUCCESSFUL_JOBS) {
    const matches = jobs.filter(job => job.name === name);
    requireThat(matches.length === 1 && matches[0].status === 'completed' &&
      matches[0].conclusion === 'success' && matches[0].head_sha === ANCHOR.sha &&
      matches[0].run_id === ANCHOR.runId, 'Unverified source job: ' + name);
  }
  const publisher = jobs.find(job => job.name === 'Create GitHub prerelease');
  requireThat(publisher?.status === 'completed' && publisher.conclusion === 'failure' &&
    publisher.head_sha === ANCHOR.sha && publisher.run_id === ANCHOR.runId &&
    publisher.steps?.filter(step => step.conclusion === 'failure').length === 1 &&
    publisher.steps.some(step => step.name === 'Publish GitHub prerelease' && step.conclusion === 'failure'),
  'Source failure is not limited to the known publication step');
  requireThat(artifact.id === ANCHOR.artifactId && artifact.name === 'verified-prerelease-assets' &&
    artifact.digest === ANCHOR.artifactDigest && artifact.size_in_bytes === ANCHOR.artifactSize &&
    artifact.expired === false && artifact.workflow_run?.id === ANCHOR.runId &&
    artifact.workflow_run?.head_sha === ANCHOR.sha && artifact.workflow_run?.head_branch === 'main' &&
    artifact.workflow_run?.repository_id === ANCHOR.repositoryId &&
    artifact.workflow_run?.head_repository_id === ANCHOR.repositoryId,
  'Verified artifact provenance changed');
}
function matchesBytes(asset, bytes) {
  return bytes && asset.state === 'uploaded' && asset.size === bytes.length && asset.digest === digestOf(bytes);
}
function normalizedBody(body) {
  return String(body ?? '').replace(/\r\n/g, '\n').trim();
}
export function planRepair(release, tagRef, original, desired) {
  requireThat(tagRef.ref === 'refs/tags/' + ANCHOR.tag &&
    tagRef.object?.type === 'commit' && tagRef.object.sha === ANCHOR.sha, 'RC4 tag target changed');
  requireThat(release.id === ANCHOR.releaseId && release.tag_name === ANCHOR.tag &&
    release.target_commitish === ANCHOR.sha && release.draft === false &&
    release.prerelease === true && release.immutable === false, 'RC4 release identity or state changed');
  requireThat(Array.isArray(release.assets), 'Release has no asset inventory');
  const names = new Set();
  const ids = new Set();
  const canonical = new Map();
  const oldByDesired = new Map(ORIGINAL_NAMES.map(name => [desiredName(name), name]));
  for (const asset of release.assets) {
    requireThat(Number.isSafeInteger(asset.id) && asset.id > 0 && !ids.has(asset.id),
      'Invalid or duplicate asset ID');
    ids.add(asset.id);
    requireThat(typeof asset.name === 'string' && !names.has(asset.name), 'Duplicate asset name');
    names.add(asset.name);
    const target = desiredName(asset.name);
    requireThat(desired.has(target) && (asset.name === target || ORIGINAL_NAMES.includes(asset.name)),
      'Unknown public asset: ' + asset.name);
    requireThat(!canonical.has(target), 'Both old and repaired asset names exist: ' + target);
    const old = original.get(oldByDesired.get(target));
    const updated = desired.get(target);
    requireThat(matchesBytes(asset, old) ||
      (TEXT_FILES.has(target) && matchesBytes(asset, updated)), 'Public asset changed: ' + asset.name);
    canonical.set(target, asset);
  }
  for (const name of desired.keys()) {
    requireThat(canonical.has(name) || name === desiredName(OLD_AMD) || TEXT_FILES.has(name),
      'Unexpected missing public asset: ' + name);
  }
  requireThat(canonical.get(desiredName(OLD_ARM))?.id === ANCHOR.armAssetId,
    'Original ARM server asset identity changed');
  requireThat(canonical.get(DESKTOP_DEB)?.id === ANCHOR.desktopDebAssetId,
    'Original desktop DEB asset identity changed');
  const body = normalizedBody(release.body);
  requireThat(body === normalizedBody(original.get(NOTES).toString('utf8')) ||
    body === normalizedBody(desired.get(NOTES).toString('utf8')), 'Release body has unknown edits');
  const operations = [];
  for (const oldName of [OLD_ARM, OLD_AMD]) {
    const name = desiredName(oldName);
    const asset = canonical.get(name);
    if (asset && asset.name !== name) {
      operations.push({ kind: 'rename', id: asset.id, from: asset.name, name });
    } else if (!asset) {
      operations.push({ kind: 'upload', name });
    }
  }
  for (const name of ['README.md', NOTES, 'SHA256SUMS.txt']) {
    const asset = canonical.get(name);
    if (!asset) operations.push({ kind: 'upload', name });
    else if (!matchesBytes(asset, desired.get(name))) {
      operations.push({ kind: 'replace-text', id: asset.id, name });
    }
  }
  if (body !== normalizedBody(desired.get(NOTES).toString('utf8'))) {
    operations.push({ kind: 'body', id: ANCHOR.releaseId, name: NOTES });
  }
  return operations;
}
export async function executeRepair({ readState, mutate, original, desired, dryRun, context }) {
  requireThat(typeof dryRun === 'boolean', 'Explicit dry-run or apply mode is required');
  if (!dryRun) {
    requireThat(context?.repository === ANCHOR.repo && context?.eventName === 'push' &&
      context?.ref === 'refs/heads/main' && /^[a-f0-9]{40}$/.test(context?.sha ?? ''),
    'Writes are only allowed from the protected repository main push workflow');
  }
  let state = await readState();
  const planned = planRepair(state.release, state.tagRef, original, desired);
  if (dryRun) return { success: true, dryRun: true, planned, operations: [], expectedAssets: desired.size };
  const operations = [];
  for (let attempt = 0; attempt < 12; attempt += 1) {
    // Re-read identity, every asset and the body immediately before each change.
    state = await readState();
    const next = planRepair(state.release, state.tagRef, original, desired);
    if (next.length === 0) {
      exactNames(state.release.assets.map(asset => asset.name), [...desired.keys()], 'Repaired release');
      for (const asset of state.release.assets) {
        requireThat(matchesBytes(asset, desired.get(asset.name)), 'Final public digest mismatch: ' + asset.name);
      }
      return { success: true, dryRun: false, planned, operations, verifiedAssets: desired.size };
    }
    await mutate(next[0]);
    operations.push(next[0]);
  }
  throw new Error('Repair did not converge; no unbounded retries are allowed');
}
function apiClient(token) {
  requireThat(token, 'GH_TOKEN is required for the approved repository workflow');
  const headers = {
    Authorization: 'Bearer ' + token,
    Accept: 'application/vnd.github+json',
    'X-GitHub-Api-Version': '2022-11-28',
  };
  return async function api(endpoint, options = {}) {
    const upload = options.bytes !== undefined;
    const base = upload ? 'https://uploads.github.com' : 'https://api.github.com';
    requireThat(endpoint.startsWith('/repos/' + ANCHOR.repo + '/'), 'Unexpected API repository');
    const response = await fetch(base + endpoint, {
      method: options.method ?? 'GET',
      headers: { ...headers, ...(upload ? { 'Content-Type': 'application/octet-stream' } :
        options.json !== undefined ? { 'Content-Type': 'application/json' } : {}) },
      body: upload ? options.bytes : options.json === undefined ? undefined : JSON.stringify(options.json),
      redirect: 'error',
    });
    if (!response.ok) throw new Error('GitHub API ' + (options.method ?? 'GET') + ' ' +
      endpoint.split('?')[0] + ' failed with HTTP ' + response.status);
    return response.status === 204 ? null : response.json();
  };
}
export async function performOperation(operation, api, desired) {
  const root = '/repos/' + ANCHOR.repo + '/releases';
  if (operation.kind === 'rename') {
    requireThat([OLD_AMD, OLD_ARM].includes(operation.from) &&
      operation.name === desiredName(operation.from), 'Unapproved binary rename');
    await api(root + '/assets/' + operation.id, { method: 'PATCH', json: { name: operation.name } });
    return;
  }
  if (operation.kind === 'body') {
    requireThat(operation.id === ANCHOR.releaseId, 'Unexpected release edit');
    await api(root + '/' + ANCHOR.releaseId, {
      method: 'PATCH', json: { body: desired.get(NOTES).toString('utf8') },
    });
    return;
  }
  if (operation.kind === 'replace-text') {
    requireThat(TEXT_FILES.has(operation.name), 'Binary replacement is forbidden');
    await api(root + '/assets/' + operation.id, { method: 'DELETE' });
  } else {
    requireThat(operation.kind === 'upload' &&
      (operation.name === desiredName(OLD_AMD) || TEXT_FILES.has(operation.name)),
    'Unapproved upload');
  }
  await api(root + '/' + ANCHOR.releaseId + '/assets?name=' + encodeURIComponent(operation.name), {
    method: 'POST', bytes: desired.get(operation.name),
  });
}
async function downloadArchive(token, destination) {
  const endpoint = 'https://api.github.com/repos/' + ANCHOR.repo +
    '/actions/artifacts/' + ANCHOR.artifactId + '/zip';
  let response = await fetch(endpoint, {
    headers: { Authorization: 'Bearer ' + token, Accept: 'application/vnd.github+json' },
    redirect: 'manual',
  });
  if (response.status === 302) {
    const location = new URL(response.headers.get('location'));
    requireThat(location.protocol === 'https:' && !location.username && !location.password &&
      (location.hostname.endsWith('.blob.core.windows.net') ||
        location.hostname.endsWith('.githubusercontent.com')), 'Unexpected artifact download host');
    // Never forward the GitHub token to the signed storage URL.
    response = await fetch(location, { redirect: 'error' });
  }
  requireThat(response.ok && response.body, 'Unable to download the pinned artifact');
  let size = 0;
  const hash = crypto.createHash('sha256');
  const check = new Transform({
    transform(chunk, encoding, callback) {
      size += chunk.length;
      if (size > ANCHOR.artifactSize) return callback(new Error('Artifact exceeds pinned size'));
      hash.update(chunk);
      callback(null, chunk);
    },
  });
  await pipeline(Readable.fromWeb(response.body), check, fs.createWriteStream(destination, { flags: 'wx' }));
  requireThat(size === ANCHOR.artifactSize &&
    'sha256:' + hash.digest('hex') === ANCHOR.artifactDigest, 'Artifact archive digest or size mismatch');
}
export function extractArchive(archive, destination) {
  const script = [
    'import json, pathlib, stat, sys, zipfile',
    'archive, target, names = sys.argv[1], pathlib.Path(sys.argv[2]), set(json.loads(sys.argv[3]))',
    'with zipfile.ZipFile(archive) as z:',
    '    entries = z.infolist()',
    '    assert len(entries) == len(names) and {i.filename for i in entries} == names, "Unexpected archive inventory"',
    '    assert sum(i.file_size for i in entries) <= 512 * 1024 * 1024, "Archive too large"',
    '    for i in entries:',
    '        assert pathlib.PurePosixPath(i.filename).name == i.filename, "Unsafe archive path"',
    '        assert not stat.S_ISLNK(i.external_attr >> 16) and not i.is_dir(), "Unsafe archive entry"',
    '        assert 0 < i.file_size <= 256 * 1024 * 1024, "Invalid archive member size"',
    '    target.mkdir()',
    '    for i in entries:',
    '        with z.open(i) as src, (target / i.filename).open("xb") as dst:',
    '            while chunk := src.read(1024 * 1024):',
    '                dst.write(chunk)',
  ].join('\n');
  const result = spawnSync('python3', ['-c', script, archive, destination, JSON.stringify(ORIGINAL_NAMES)],
    { encoding: 'utf8' });
  requireThat(result.status === 0, 'Pinned archive extraction failed: ' + (result.stderr ?? '').slice(-1000));
  return new Map(ORIGINAL_NAMES.map(name => [name, fs.readFileSync(path.join(destination, name))]));
}
async function main() {
  const args = process.argv.slice(2);
  requireThat(args.length === 1 && ['--dry-run', '--apply'].includes(args[0]), 'Use --dry-run or --apply');
  requireThat(process.env.GITHUB_REPOSITORY === ANCHOR.repo, 'Unexpected workflow repository');
  const token = process.env.GH_TOKEN;
  const api = apiClient(token);
  const root = '/repos/' + ANCHOR.repo;
  const [run, jobPage, artifact] = await Promise.all([
    api(root + '/actions/runs/' + ANCHOR.runId),
    api(root + '/actions/runs/' + ANCHOR.runId + '/jobs?per_page=100'),
    api(root + '/actions/artifacts/' + ANCHOR.artifactId),
  ]);
  requireThat(jobPage.total_count === 6, 'Unexpected source job pagination');
  validateProvenance(run, jobPage.jobs, artifact);
  const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'omnishare-rc4-repair-'));
  try {
    const archive = path.join(temp, 'verified.zip');
    await downloadArchive(token, archive);
    const original = extractArchive(archive, path.join(temp, 'assets'));
    const desired = buildDesired(original, fs.readFileSync('README.md'),
      fs.readFileSync('docs/' + NOTES));
    const result = await executeRepair({
      original, desired, dryRun: args[0] === '--dry-run',
      context: { repository: process.env.GITHUB_REPOSITORY, eventName: process.env.GITHUB_EVENT_NAME,
        ref: process.env.GITHUB_REF, sha: process.env.GITHUB_SHA },
      readState: async () => {
        const [release, tagRef] = await Promise.all([
          api(root + '/releases/' + ANCHOR.releaseId),
          api(root + '/git/ref/tags/' + ANCHOR.tag),
        ]);
        return { release, tagRef };
      },
      mutate: operation => performOperation(operation, api, desired),
    });
    const evidence = { ...result, source: ANCHOR, executionSha: process.env.GITHUB_SHA };
    fs.mkdirSync('release-repair-evidence', { recursive: true });
    fs.writeFileSync('release-repair-evidence/result.json', JSON.stringify(evidence, null, 2) + '\n');
    console.log(JSON.stringify(evidence, null, 2));
  } finally {
    fs.rmSync(temp, { recursive: true, force: true });
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
