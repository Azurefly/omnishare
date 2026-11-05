// Native package managers impose stricter versions than application SemVer.
export function bundleVersions(version) {
  const match = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(alpha|beta|rc)\.?([0-9]+))?(?:\+[0-9A-Za-z.-]+)?$/.exec(version);
  if (!match) throw new Error(`Unsupported desktop release version: ${version}`);
  const [, major, minor, patch, channel, sequence = '0'] = match;
  const rank = channel ? { alpha: 100, beta: 300, rc: 600 }[channel] + Number(sequence) : 999;
  if (Number(major) > 255 || Number(minor) > 255 || Number(patch) > 64 || Number(sequence) > 99) {
    throw new Error(`Desktop version exceeds the MSI mapping limits: ${version}`);
  }
  return {
    msi: `${major}.${minor}.${Number(patch) * 1000 + rank}`,
    rpmVersion: `${major}.${minor}.${patch}`,
    rpmRelease: channel ? `0.${channel}${sequence}` : '1',
    prerelease: Boolean(channel)
  };
}

export function buildPlan(version, platform, args = []) {
  const versions = bundleVersions(version);
  if (platform === 'win32') {
    return [{ args: ['build', ...args], config: { bundle: { windows: { wix: { version: versions.msi } } } } }];
  }
  if (platform !== 'linux' || !versions.prerelease || args.includes('--no-bundle')) {
    return [{ args: ['build', ...args] }];
  }
  // Compile with the real SemVer, then package RPM separately. Tauri currently
  // has no RPM-only version override, and '-' is forbidden in RPM Version.
  const buildArgs = [];
  const bundleArgs = [];
  let targets = ['deb', 'appimage', 'rpm'];
  const sharedValues = new Set(['--target', '-t', '--features', '-f', '--config', '-c']);
  const sharedFlags = new Set(['--debug', '-d', '--ci', '--no-sign', '--no-binary-patching', '--verbose', '-v']);
  for (let i = 0; i < args.length; i++) {
    const arg = args[i];
    if (arg === '--bundles' || arg === '-b') {
      targets = [];
      while (i + 1 < args.length && !args[i + 1].startsWith('-')) targets.push(...args[++i].split(','));
      if (targets.includes('all')) targets = ['deb', 'appimage', 'rpm'];
    } else if (arg.startsWith('--bundles=')) {
      targets = arg.slice('--bundles='.length).split(',');
      if (targets.includes('all')) targets = ['deb', 'appimage', 'rpm'];
    } else {
      buildArgs.push(arg);
      if (sharedValues.has(arg)) {
        if (i + 1 >= args.length) throw new Error(`Missing value for ${arg}`);
        const value = args[++i];
        buildArgs.push(value);
        bundleArgs.push(arg, value);
      } else if (sharedFlags.has(arg) || /^--(target|features|config)=/.test(arg)) {
        bundleArgs.push(arg);
      }
    }
  }
  if (!targets.includes('rpm')) return [{ args: ['build', ...buildArgs, '--bundles', targets.join(',')] }];
  const ordinaryTargets = targets.filter(target => target !== 'rpm');
  return [
    { args: ['build', ...buildArgs, ...(ordinaryTargets.length ? ['--bundles', ordinaryTargets.join(',')] : ['--no-bundle'])] },
    { args: ['bundle', ...bundleArgs, '--bundles', 'rpm'], config: {
      version: versions.rpmVersion,
      bundle: { linux: { rpm: { release: versions.rpmRelease } } }
    } }
  ];
}
