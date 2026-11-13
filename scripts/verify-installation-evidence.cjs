'use strict';

const { createHash } = require('node:crypto');
const { constants, closeSync, fstatSync, lstatSync, openSync, readSync } = require('node:fs');
const { join, resolve } = require('node:path');

const REPORT_LIMIT = 1024 * 1024;
const SHA256 = /^[0-9a-f]{64}$/;
const FEATURE_CHECKS = {
  exercise: ['health_and_embedded_ui', 'first_run_authentication', 'notes_and_trash',
    'documents_and_conflicts', 'file_upload_download_and_range', 'share_access_and_revocation', 'encrypted_backup_restore'],
  verify: ['health_and_embedded_ui', 'configuration_persistence', 'note_and_counter_persistence',
    'file_persistence', 'document_persistence']
};
const WINDOWS_CHECKS = ['actual_installation', 'registration_and_shortcut', 'first_run_gui',
  'native_window_visible', 'data_directory', 'core_exercise', 'owned_backend_exited',
  'restart_gui_and_persistence', 'second_normal_exit', 'uninstalled', 'business_data_preserved'];
const UBUNTU_CHECKS = ['apt_installed', 'glibc_baseline', 'desktop_entry_valid',
  'apt_uninstalled', 'uninstall_preserves_data'];
const LINUX_APP_CHECKS = ['first_run_webkit', 'webkit_rendered', 'bundled_backend',
  'core_features', 'core_content_in_webkit', 'normal_quit', 'restart_persistence'];
// Public reports contain results, never the private feature state or credentials.
// Normalize spelling so accessKey / access_key / access-key are equally rejected.
const PRIVATE_FIELDS = new Set(['accesskey', 'inittoken', 'xomnisharekey', 'xomnishareinit',
  'authorization', 'cookie', 'setcookie', 'password', 'passphrase', 'secret', 'token',
  'privatestate', 'featurestate', 'noteid', 'notecontent', 'notereadcount',
  'fileid', 'filesha256', 'padid', 'padcontent', 'padversion']);

function fail(message) { throw new Error(`Invalid installation evidence: ${message}`); }
function requireValue(condition, message) { if (!condition) fail(message); }
function object(value, label) {
  requireValue(value !== null && typeof value === 'object' && !Array.isArray(value), `${label} must be an object`);
  return value;
}
function exactKeys(value, keys, label) {
  object(value, label);
  const actual = Object.keys(value).sort();
  const expected = [...keys].sort();
  requireValue(actual.length === expected.length && actual.every((key, index) => key === expected[index]),
    `${label} has missing or unexpected fields`);
}
function passedChecks(value, names, label) {
  exactKeys(value, names, label);
  for (const name of names) requireValue(value[name] === true, `${label}.${name} did not pass`);
}
function installerPair(value, label) {
  requireValue(Array.isArray(value) && value.length === 2, `${label} must contain both Windows installers`);
  const kinds = value.map(record => object(record, label).kind);
  requireValue(kinds.includes('msi') && kinds.includes('nsis'), `${label} must contain exactly msi and nsis`);
  return value;
}
function rejectPrivateFields(value, depth = 0) {
  requireValue(depth <= 32, 'report nesting exceeds its limit');
  if (value !== null && typeof value === 'object') {
    for (const [key, child] of Object.entries(value)) {
      const normalized = key.toLowerCase().replace(/[^a-z0-9]/g, '');
      requireValue(!PRIVATE_FIELDS.has(normalized), 'report contains a private field');
      rejectPrivateFields(child, depth + 1);
    }
  } else if (typeof value === 'string') {
    // Catch accidentally copied authentication URLs/headers as well as fields.
    requireValue(!/(?:access[_-]?key|init[_-]?token|x-omnishare-(?:key|init))\s*(?:[=:]|%3[dD])/i.test(value),
      'report contains credential material');
  }
}
function header(report, version, label) {
  object(report, label);
  requireValue(report.schema_version === 1 && report.success === true, `${label} has an unsupported schema or did not succeed`);
  requireValue(report.version === version, `${label} version mismatch`);
  for (const field of ['error', 'failure', 'failed_check', 'cleanup_uninstall_failed', 'cleanup_failed']) {
    requireValue(!Object.hasOwn(report, field), `${label} contains failure evidence`);
  }
}
function featureEvidence(features, version, label) {
  exactKeys(features, ['exercise', 'verify'], `${label}.features`);
  for (const [phase, checks] of Object.entries(FEATURE_CHECKS)) {
    const record = features[phase];
    header(record, version, `${label}.${phase}`);
    requireValue(record.phase === phase && record.scope === 'installed-backend-http-features',
      `${label}.${phase} has the wrong phase or validation scope`);
    passedChecks(record.checks, checks, `${label}.${phase}.checks`);
  }
}

/** Validate the installed binaries' real reports before any release upload.
 * Returns only the three report names and hashes; binary hashes are independently
 * checked here and remain part of the publisher's existing asset manifest.
 */
function verifyInstallationEvidence({ root, version }) {
  requireValue(typeof root === 'string' && root.length > 0, 'asset directory is required');
  requireValue(typeof version === 'string' && /^\d+\.\d+\.\d+(?:-(?:alpha|beta|rc)\.?\d+)?$/.test(version), 'invalid version');
  root = resolve(root);
  try {
    const stat = lstatSync(root);
    requireValue(stat.isDirectory() && !stat.isSymbolicLink(), 'asset root must be a real directory');
  } catch (error) {
    if (error.message.startsWith('Invalid installation evidence:')) throw error;
    fail('asset directory is unavailable');
  }
  const read = (name, json = false) => {
    requireValue(typeof name === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(name), 'unsafe asset name');
    let descriptor;
    try {
      const path = join(root, name);
      const before = lstatSync(path);
      requireValue(before.isFile() && !before.isSymbolicLink() && before.size > 0, `${name} is not a nonempty regular file`);
      requireValue(!json || before.size <= REPORT_LIMIT, `${name} exceeds the report size limit`);
      descriptor = openSync(path, constants.O_RDONLY | (constants.O_NOFOLLOW || 0));
      const opened = fstatSync(descriptor);
      requireValue(opened.isFile() && opened.dev === before.dev && opened.ino === before.ino && opened.size === before.size,
        `${name} changed while opening`);
      const hash = createHash('sha256');
      const chunk = Buffer.alloc(128 * 1024);
      const chunks = [];
      let size = 0;
      for (;;) {
        const length = readSync(descriptor, chunk, 0, chunk.length, null);
        if (!length) break;
        size += length;
        requireValue(size <= opened.size && (!json || size <= REPORT_LIMIT), `${name} changed while reading`);
        const bytes = chunk.subarray(0, length);
        hash.update(bytes);
        if (json) chunks.push(Buffer.from(bytes));
      }
      const after = fstatSync(descriptor);
      requireValue(size === opened.size && after.size === opened.size && after.mtimeMs === opened.mtimeMs && after.ctimeMs === opened.ctimeMs,
        `${name} changed while reading`);
      return { sha256: hash.digest('hex'), size, bytes: json ? Buffer.concat(chunks) : undefined };
    } catch (error) {
      if (error.message.startsWith('Invalid installation evidence:')) throw error;
      fail(`${name} could not be read as a regular file`);
    } finally {
      if (descriptor !== undefined) closeSync(descriptor);
    }
  };
  const readReport = name => {
    const { bytes, sha256 } = read(name, true);
    let report;
    try { report = JSON.parse(bytes.toString('utf8').replace(/^\uFEFF/, '')); }
    catch { fail(`${name} is not valid JSON`); }
    rejectPrivateFields(report);
    header(report, version, name);
    return { report, sha256 };
  };
  const asset = (record, expectedName, label, requireSize = false) => {
    object(record, label);
    requireValue(record.name === expectedName && typeof record.sha256 === 'string' && SHA256.test(record.sha256),
      `${label} has an unexpected name or digest`);
    const actual = read(expectedName);
    requireValue(record.sha256 === actual.sha256, `${label} digest does not match the actual file`);
    if (requireSize || Object.hasOwn(record, 'size')) {
      requireValue(Number.isSafeInteger(record.size) && record.size === actual.size, `${label} size does not match the actual file`);
    }
  };

  const records = [];
  const windowsName = 'omnishare-windows-installation.json';
  const { report: windows, sha256: windowsHash } = readReport(windowsName);
  requireValue(windows.platform === 'windows', 'Windows platform mismatch');
  requireValue(windows.gui_method === 'visible native HWND + actual WebView2 CDP' &&
    windows.quit_method === 'native Ctrl+Q accelerator', 'Windows GUI or normal-Quit validation scope is missing');
  for (const claim of ['cold_runtime_install_verified', 'offline_runtime_install_verified', 'non_admin_install_verified']) {
    requireValue(windows[claim] === false, `Windows ${claim} must not claim an untested installation scenario`);
  }
  const windowsAssetName = kind => kind === 'msi' ? `OmniShare_${version}_x64_en-US.msi` : `OmniShare_${version}_x64-setup.exe`;
  for (const record of installerPair(windows.installers, 'Windows installers')) {
    const label = `Windows ${record.kind}`;
    requireValue(record.success === true, `${label} did not succeed`);
    for (const field of ['error', 'failure', 'cleanup_uninstall_failed', 'cleanup_failed']) {
      requireValue(!Object.hasOwn(record, field), `${label} contains failure evidence`);
    }
    asset(record.asset, windowsAssetName(record.kind), label, true);
    passedChecks(record.checks, record.kind === 'msi' ? [...WINDOWS_CHECKS, 'attach_conflict_watchdog'] : WINDOWS_CHECKS, `${label}.checks`);
    featureEvidence(record.features, version, label);
  }
  const webview = object(windows.webview_runtime, 'Windows WebView runtime');
  requireValue(webview.schema_version === 1 && webview.success === true && webview.mode === 'offlineInstaller' &&
    webview.tauri_cli === '2.12.4', 'Windows WebView runtime metadata does not match the packaging contract');
  requireValue(webview.cold_install_verified === false && webview.offline_install_verified === false,
    'Windows WebView runtime metadata must not claim cold-machine or offline installation was tested');
  for (const runtime of installerPair(webview.runtimes, 'Windows WebView runtime payloads')) {
    requireValue(typeof runtime.sha256 === 'string' && SHA256.test(runtime.sha256), 'Windows WebView payload digest is missing');
    requireValue(Number.isSafeInteger(runtime.size) && runtime.size >= 64 * 1024 * 1024 && runtime.size <= 512 * 1024 * 1024,
      'Windows WebView payload must be a complete 64–512 MiB offline installer');
    requireValue(runtime.signature_status === 'Valid' && runtime.publisher === 'Microsoft Corporation',
      'Windows WebView payload lacks a valid Microsoft signature');
    requireValue(typeof runtime.product_version === 'string' && runtime.product_version.length <= 128 &&
      /^\d+(?:\.\d+){1,3}(?:\s[^\r\n]*)?$/.test(runtime.product_version), 'Windows WebView payload version is invalid');
  }
  for (const entry of installerPair(webview.artifacts, 'Windows WebView installer bindings')) {
    asset(entry, windowsAssetName(entry.kind), `Windows WebView ${entry.kind} binding`, true);
  }
  // Authenticode and package bindings are evidence of the actual build inputs;
  // they cannot establish successful installation on a cold/offline machine.
  records.push([windowsName, windowsHash]);

  for (const ubuntu of ['22.04', '24.04']) {
    const name = `omnishare-ubuntu-${ubuntu}-installation.json`;
    const { report, sha256 } = readReport(name);
    requireValue(report.platform?.id === 'ubuntu' && report.platform.version === ubuntu, `${name} platform mismatch`);
    requireValue(report.normal_user === true, `${name} was not validated as a normal user`);
    requireValue(report.appimage_scope === 'extract-and-run' && report.fuse_mount_tested === false,
      `${name} must accurately report the FUSE-free AppImage scope`);
    passedChecks(report.checks, UBUNTU_CHECKS, `${name}.checks`);
    exactKeys(report.artifacts, ['deb', 'appimage'], `${name}.artifacts`);
    for (const [flavor, extension] of [['deb', 'deb'], ['appimage', 'AppImage']]) {
      asset(report.artifacts[flavor], `OmniShare_${version}_amd64.${extension}`, `${name}.${flavor}`);
      object(report[flavor], `${name}.${flavor}`);
      passedChecks(report[flavor].checks, LINUX_APP_CHECKS, `${name}.${flavor}.checks`);
      featureEvidence(report[flavor].features, version, `${name}.${flavor}`);
    }
    records.push([name, sha256]);
  }
  return records;
}

module.exports = { verifyInstallationEvidence };
