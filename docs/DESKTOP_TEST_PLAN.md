# OmniShare Desktop Complete Test Process

This document defines the mandatory validation process for every desktop release. A release is not considered complete merely because Tauri bundles were generated.

## 1. Test objectives

The process validates five layers:

1. Source and contract correctness.
2. Rust desktop runtime logic.
3. Go backend quality and cross-platform compilation.
4. Real packaged-layout desktop startup and process behavior.
5. Real-device GUI, signing and operating-system acceptance.

## 2. Automated CI test layers

The `Desktop Release` workflow executes the following tests for every pull request that changes desktop, frontend, backend or release workflow files.

### 2.1 Existing application quality checks

The existing repository CI remains mandatory:

- Frontend syntax and contract checks.
- Windows launcher contract checks.
- `gofmt` and `go vet`.
- Go unit and integration tests.
- Go cross-builds for Windows, Linux and macOS architectures.

### 2.2 Desktop static release contracts

Command:

```bash
npm --prefix desktop run test:contracts
```

The contract test fails when any release-critical wiring is removed, including:

- Tauri single-instance dependency and plugin registration.
- Existing-window restore on a second launch.
- Explicit tray icon assignment.
- Restore from minimized state.
- Existing OmniShare backend detection.
- Backend startup diagnostics.
- Bundled backend resources and Windows icon.
- Cross-platform Tauri build wrapper.

### 2.3 Rust formatting and static analysis

Executed on Linux after Tauri system dependencies are installed:

```bash
cargo fmt --check
cargo clippy --all-targets --all-features -- -D warnings
```

Any formatting difference or compiler warning blocks the release.

### 2.4 Rust unit tests

Command:

```bash
cargo test --all-features
```

Current desktop unit coverage includes:

- Reserved port rejection.
- Valid port boundary acceptance.
- Preferred free port retention.
- Occupied preferred port fallback.
- Valid, invalid and malformed persisted settings.
- Positive OmniShare HTTP identification.
- Rejection of unrelated HTTP services.
- Backend readiness timeout behavior.

### 2.5 Three-platform package builds

The `Desktop Validation` workflow builds:

- Windows x86_64 MSI and NSIS bundles.
- Linux desktop bundles.
- macOS Universal bundles containing both `arm64` and `x86_64`.

The Go backend resource is built before Tauri packaging and included in the application bundle.

### 2.6 Windows installed-layout runtime smoke test

Script:

```powershell
./desktop/tests/windows-runtime-smoke.ps1 `
  -BundleRoot desktop/src-tauri/target/release/bundle `
  -EvidenceRoot desktop-test-evidence/Windows
```

The test does not run only the development executable. It performs an MSI administrative extraction and runs the application from the extracted installed layout.

Automated scenarios:

#### Scenario A: package contents and icon

- MSI can be extracted successfully.
- Desktop executable exists.
- Bundled `omnishare.exe` backend exists.
- Desktop executable exposes a valid Windows associated icon.

#### Scenario B: preferred port occupied by another service

- A non-OmniShare HTTP server occupies the requested port.
- Desktop starts from the installed layout.
- Desktop does not connect to the unrelated service.
- A fallback port is selected within the supported scan range.
- The fallback endpoint serves the OmniShare page.
- Exactly one desktop shell and one managed backend exist.

#### Scenario C: existing OmniShare backend

- The packaged backend is started independently on an available port.
- Desktop launches with the same port and data directory.
- Desktop attaches to the existing backend instead of starting a duplicate.
- Backend process count remains one.

#### Scenario D: single desktop instance

- The desktop executable is launched a second time.
- The second process exits after forwarding activation to the original instance.
- Exactly one desktop shell remains.
- The backend remains healthy.


#### Scenario E: managed backend recovery and local-device summary

- Start the desktop application from the MSI installed layout.
- Confirm `/api/v1/devices` contains exactly one local product device.
- Reject APIPA/IPv6 link-local addresses as the selected local endpoint.
- Force-terminate the managed backend process.
- Confirm the endpoint becomes unavailable, then is restored by the desktop watchdog.
- Confirm the replacement backend has a different process ID.
- Confirm exactly one desktop shell and one backend process remain after recovery.

### 2.7 macOS Universal validation

Both the Tauri application executable and bundled Go backend are inspected using `lipo`.

Required architectures:

```text
arm64
x86_64
```

These Tauri macOS artifacts are validation-only. They are separate from the public Go backend application packages, which have individual Intel and Apple silicon builds.

The next formal Go application release requires Developer ID signing, Hardened Runtime, secure timestamps, app and DMG notarization and stapling, and Gatekeeper acceptance. Native verification checks the exact final ZIP and DMG, including:

```bash
codesign --verify --deep --strict --verbose=2 OmniShare.app
spctl --assess --type execute --verbose=2 OmniShare.app
```

A release workflow without valid Developer ID and notarization credentials must fail before publishing. The final packages must pass all 23 native checks and agree with their verification reports and public SHA-256 digests. See [MACOS_SIGNING.md](MACOS_SIGNING.md) for the complete release gates and configuration. The currently published `0.1.1` is still ad-hoc and unnotarized; the pipeline change does not retrospectively certify it.

### 2.8 Bundle integrity evidence

Every generated bundle file is hashed with SHA-256. Complete test artifacts are uploaded independently; only the explicitly selected, sanitized verification reports and checksum files are staged as public release assets. Private signing materials and raw backend initialization logs must never be included.

## 3. CI evidence artifacts

Each workflow run uploads platform-specific test evidence:

```text
omnishare-test-evidence-Windows
omnishare-test-evidence-Linux
omnishare-test-evidence-macOS-universal
```

Expected Windows evidence:

```text
windows-runtime-smoke.json
windows-runtime-smoke.junit.xml
windows-watchdog-recovery.json
windows-watchdog-recovery.junit.xml
msi-administrative-extract.log
desktop-associated-icon.ico
standalone-backend.log
bundle-sha256.json
```

Expected Linux evidence:

```text
cargo-fmt.log
cargo-clippy.log
cargo-test.log
bundle-sha256.txt
```

Expected macOS evidence:

```text
backend-architectures.txt
app-architectures.txt
bundled-backend-architectures.txt
bundle-sha256.txt
codesign-verify.log             # formal release only
gatekeeper-assess.log           # formal release only
```

## 4. Mandatory real-device acceptance

CI cannot reliably prove notification-area visibility, physical mouse interaction or Gatekeeper behavior on a newly downloaded application. The following real-device tests are mandatory before declaring a stable release.

### 4.1 Windows 10 and Windows 11

Use a clean user profile or uninstall the previous build first.

1. Install the NSIS package.
2. Confirm one visible OmniShare tray icon appears.
3. Double-click the desktop shortcut five times.
4. Confirm only one desktop shell and one backend process remain.
5. Close the main window.
6. Confirm the process remains and the tray icon remains visible.
7. Left-click the tray icon and confirm the window restores and receives focus.
8. Use **Hide Window**, then **Open OmniShare**.
9. Use **Quit OmniShare** and confirm both desktop and managed backend terminate.
10. Occupy the configured port with another program and confirm OmniShare selects a different port.
11. Restart after a simulated orphan backend and confirm the desktop reconnects without a duplicate backend.
12. Upload, download and delete a test file.

Capture:

- Screenshot of the tray icon.
- Screenshot of Task Manager process list after repeated launch.
- Selected port and endpoint.
- Backend log when any startup issue occurs.

### 4.2 macOS Apple Silicon, including M4

This is the acceptance target for the next signed and notarized Go application release. The published `0.1.1` and PR rehearsal artifacts do not meet that prerequisite.

1. On a fresh Mac or restored VM snapshot, download the matching `arm64` DMG from GitHub Release using Safari and compare its SHA-256 with the release manifest.
2. Drag OmniShare to Applications.
3. Eject the disk image and open the installed app with the original download quarantine retained.
4. Confirm Gatekeeper does not report the application as damaged.
5. Confirm `spctl -a -vv /Applications/OmniShare.app` reports acceptance.
6. Confirm the Go executable reports the expected version and `arm64` architecture; the public app is not the Tauri Universal build.
7. Confirm the browser UI opens, then verify shutdown and restart. A normal first-open confirmation is acceptable; Open Anyway is not part of acceptance.
8. Verify upload and download.

Repeat from a clean snapshot without networking to check stapled-ticket availability, and test the ZIP fallback separately. Do not count a cached approval or simulated quarantine assessment as this first-download test. Native CI evidence keeps `first_download_verified: false`; record these real-device results separately.

Capture:

- `spctl` output.
- `codesign -dv --verbose=4` output.
- `lipo -archs` for the packaged Go executable.
- Screenshot of successful first launch.

### 4.3 macOS Intel

Install the matching `amd64` DMG and ZIP and repeat the same fresh-download, installation, launch, shutdown and transfer tests. When Tauri macOS distribution is separately enabled, also test its Universal executable, bundled backend and tray/menu-bar lifecycle; those are not claims made by the current Go application release.

### 4.4 Linux

Test the distributed package on at least one supported desktop environment.

1. Install or launch the produced package.
2. Confirm WebKit runtime startup.
3. Confirm tray/AppIndicator behavior.
4. Confirm one desktop shell and one backend.
5. Confirm upload and download.

## 5. Failure handling

### Backend startup failure

Record the full error shown by the desktop. The error must include:

- Backend executable path.
- Selected host and port.
- Process exit status when available.
- Backend log path.
- Recent backend log output.

### Windows duplicate process failure

Record:

```powershell
Get-CimInstance Win32_Process |
  Where-Object { $_.Name -match 'OmniShare|omnishare' } |
  Select-Object ProcessId, Name, ExecutablePath, CommandLine
```

### macOS damaged application failure

Do not publish a workaround that asks users to disable Gatekeeper or remove quarantine. Check Developer ID signing, hardened runtime, notarization and stapling evidence instead.

## 6. Release decision

A stable release requires all of the following:

- Repository CI is green.
- Desktop Validation build-and-test matrix is green.
- Windows installed-layout runtime smoke is green.
- Tauri macOS Universal architecture validation is green, and both public Go application architectures pass native ZIP/DMG validation.
- Formal macOS signing, notarization, stapling and Gatekeeper checks are green.
- SHA-256 evidence exists for every package.
- Windows real-device tray and lifecycle acceptance is signed off.
- macOS M4 first-launch acceptance is signed off.
- No release-blocking issue remains open.

Ad-hoc pull-request rehearsal artifacts are test artifacts only and must not be presented as production macOS downloads. These are acceptance requirements, not a statement that the published `0.1.1` already satisfies them.
