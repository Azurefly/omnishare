'use strict';

const { createHash } = require('node:crypto');
const { lstatSync, readFileSync } = require('node:fs');
const { join } = require('node:path');

const COMMON_CHECKS = [
  'archive_layout', 'signature_valid', 'signature_survives_archive',
  'resource_tamper_rejected', 'launch_services', 'health_version', 'http_ui',
  'clean_shutdown', 'dmg_layout', 'dmg_mount_readonly', 'dmg_signature_valid',
  'dmg_app_matches_zip', 'dmg_copy_survives_detach', 'dmg_launch_services',
  'dmg_health_version', 'dmg_http_ui', 'dmg_clean_shutdown'
];
const DISTRIBUTION_CHECKS = [
  'developer_id', 'hardened_runtime', 'secure_timestamp', 'app_staple_valid',
  'dmg_staple_valid', 'quarantine_policy'
];
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SHA256 = /^[0-9a-f]{64}$/;
const digest = data => createHash('sha256').update(data).digest('hex');

function verifyMacEvidence({ root, version, arch, mode, expectedTeam }) {
  const fail = message => { throw new Error(`Invalid macOS ${arch} release evidence: ${message}`); };
  const requireValue = (condition, message) => { if (!condition) fail(message); };
  requireValue(['release', 'rehearsal'].includes(mode), 'explicit release or rehearsal mode is required');
  requireValue(['amd64', 'arm64'].includes(arch), 'unsupported architecture');
  requireValue(/^\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.?\d+)?$/.test(version), 'invalid version');
  const release = mode === 'release';
  if (release) requireValue(/^[A-Z0-9]{10}$/.test(expectedTeam || ''), 'configured Team ID is required');
  const read = name => {
    const path = join(root, name);
    const stat = lstatSync(path);
    requireValue(stat.isFile() && !stat.isSymbolicLink() && stat.size > 0, `missing regular file ${name}`);
    return readFileSync(path);
  };
  const reportName = `omnishare-macos-${arch}-verification.json`;
  const reportBytes = read(reportName);
  const report = JSON.parse(reportBytes.toString('utf8'));
  requireValue(report.schema_version === 2 && report.success === true, 'unsupported schema or unsuccessful verification');
  requireValue(report.version === version && report.arch === arch && report.mode === mode, 'version, architecture or mode mismatch');
  requireValue(report.minimum_macos === '12.0', 'unexpected minimum macOS');
  requireValue(report.production_ready === release, 'production readiness does not match verification mode');
  requireValue(report.signing_mode === (release ? 'developer-id' : 'ad-hoc'), 'wrong signing mode');
  requireValue(report.team_id === (release ? expectedTeam : null), 'developer team does not match independent configuration');
  for (const check of COMMON_CHECKS) requireValue(report.checks?.[check] === true, `check did not pass: ${check}`);
  for (const check of DISTRIBUTION_CHECKS) {
    requireValue(report.checks?.[check] === release, `distribution check does not match mode: ${check}`);
  }
  requireValue(report.gatekeeper?.first_download_verified === false,
    'automated policy checks must not claim a real browser first download');
  requireValue(report.gatekeeper?.simulation === 'explicit-quarantine-on-isolated-copies', 'missing policy test scope');
  requireValue(report.gatekeeper?.notarized === release && report.gatekeeper?.quarantine_policy_verified === release,
    'notarization or quarantine policy state does not match mode');

  const records = [];
  for (const kind of ['zip', 'dmg']) {
    const name = `OmniShare-v${version}-macos-${arch}.${kind}`;
    const bytes = read(name);
    const hash = digest(bytes);
    const entry = report.artifacts?.[kind];
    requireValue(entry?.name === name && entry.sha256 === hash && entry.size === bytes.length,
      `${kind} final bytes do not match native verification`);
    if (kind === 'zip') requireValue(report.asset === name && report.sha256 === hash, 'legacy ZIP identity mismatch');
    records.push([name, hash]);
  }
  requireValue(report.packaging?.name === `omnishare-macos-${arch}-packaging.json` &&
    typeof report.packaging.sha256 === 'string' && SHA256.test(report.packaging.sha256), 'missing packaging provenance');
  for (const kind of ['app', 'dmg']) {
    const notary = report.notarization?.[kind];
    requireValue(notary?.status === (release ? 'Accepted' : 'NotSubmitted') && notary.stapled === release,
      `${kind} did not complete required notarization and stapling`);
    requireValue(release ? typeof notary.submission_id === 'string' && UUID.test(notary.submission_id) : notary.submission_id === null,
      `${kind} has an invalid notarization submission identifier`);
  }
  if (release) {
    requireValue(report.notarization.app.submission_id !== report.notarization.dmg.submission_id,
      'app and DMG must have independent notarization submissions');
    requireValue(report.gatekeeper.policy_enabled === true, 'Gatekeeper was disabled during validation');
    for (const kind of ['zip_app', 'dmg', 'dmg_app']) {
      const assessment = report.gatekeeper[kind];
      requireValue(assessment?.assessment_exit_code === 0 && assessment.assessment_result === 'accepted' &&
        assessment.assessment_source === 'Notarized Developer ID' && assessment.quarantine_verified === true, `${kind} was not accepted with quarantine retained`);
      if (kind !== 'dmg') requireValue(assessment.syspolicy_check?.available === true &&
        assessment.syspolicy_check.exit_code === 0 && assessment.syspolicy_check.result === 'accepted',
        `${kind} did not pass syspolicy_check on the native runner`);
    }
  }
  for (const kind of ['zip', 'dmg']) {
    const runtime = report.runtime?.[kind];
    requireValue(runtime?.launch_method === 'open -n -W' && runtime.loopback_only === true &&
      runtime.browser_suppressed === true && runtime.quarantined === false,
      `${kind} runtime validation scope is missing or misleading`);
  }
  records.push([reportName, digest(reportBytes)]);
  return records;
}

module.exports = { COMMON_CHECKS, DISTRIBUTION_CHECKS, verifyMacEvidence };
