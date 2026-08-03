# OmniShare Desktop Release Guide

This document describes how to build and publish the Tauri 2 desktop edition of OmniShare.

## Architecture

The desktop edition keeps the existing Go backend as the source of truth for storage, transfer, discovery, security and HTTP APIs. Tauri owns the native desktop lifecycle:

1. Start the desktop WebView.
2. Read the configured local service port.
3. Detect and attach to an existing healthy OmniShare backend on that port.
4. If the configured port is occupied by another service, select and persist an available local port.
5. Resolve the bundled OmniShare backend binary.
6. Start the backend with `--no-browser --listen 127.0.0.1 --port <selected-port>`.
7. Wait until the selected endpoint serves the OmniShare application.
8. Navigate the WebView to the local OmniShare UI.
9. Keep the app in the tray when the main window is closed.
10. Stop the managed child backend process when the desktop runtime exits.

Port configuration and managed-deployment options are documented in:

```text
docs/DESKTOP_PORT_AND_MACOS.md
```

The complete automated and real-device test process is documented in:

```text
docs/DESKTOP_TEST_PLAN.md
```

## Local build

### Windows

```powershell
.\scripts\build-tauri-desktop.ps1
```

### Linux/macOS

```bash
./scripts/build-tauri-desktop.sh
```

On macOS, the script builds both the Go backend and Tauri application as Universal binaries containing `arm64` and `x86_64`.

## Local validation

Desktop source contracts:

```bash
npm --prefix desktop install
npm --prefix desktop run test:contracts
```

Rust formatting, static analysis and unit tests:

```bash
npm --prefix desktop run prebuild
cargo fmt --manifest-path desktop/src-tauri/Cargo.toml --check
cargo clippy --manifest-path desktop/src-tauri/Cargo.toml --all-targets --all-features -- -D warnings
cargo test --manifest-path desktop/src-tauri/Cargo.toml --all-features
```

Windows installed-layout runtime smoke test after building bundles:

```powershell
.\desktop\tests\windows-runtime-smoke.ps1 `
  -BundleRoot .\desktop\src-tauri\target\release\bundle `
  -EvidenceRoot .\desktop-test-evidence\Windows
```

## CI build and test

The workflow is defined in:

```text
.github/workflows/desktop-release.yml
```

It builds and validates:

- Linux x86_64
- Windows x86_64
- macOS Universal (`arm64` + `x86_64`)

Release packages are uploaded as:

```text
omnishare-desktop-Linux
omnishare-desktop-Windows
omnishare-desktop-macOS-universal
```

Independent test evidence is uploaded as:

```text
omnishare-test-evidence-Linux
omnishare-test-evidence-Windows
omnishare-test-evidence-macOS-universal
```

The Windows job extracts the MSI package and executes the desktop from the extracted installed layout. It validates port conflict fallback, existing-backend attachment, single-instance enforcement, process counts and Windows icon resources.

## macOS release requirement

Pull-request builds may be unsigned validation artifacts. A formal GitHub Release is different: the workflow requires a Developer ID certificate and Apple notarization credentials before the macOS job can proceed.

Mandatory repository secrets:

```text
APPLE_CERTIFICATE
APPLE_CERTIFICATE_PASSWORD
APPLE_ID
APPLE_PASSWORD
APPLE_TEAM_ID
```

Optional explicit identity:

```text
APPLE_SIGNING_IDENTITY
```

The release gate verifies both Universal architectures and executes:

```bash
codesign --verify --deep --strict --verbose=2 OmniShare.app
spctl --assess --type execute --verbose=2 OmniShare.app
```

If these checks fail, the release publishing job does not run. This prevents an unsigned or non-notarized macOS package from being published as a stable release.

## Publishing

There are two supported publishing paths.

### Option A: publish from GitHub Actions UI

1. Open **Actions**.
2. Select **Desktop Release**.
3. Click **Run workflow**.
4. Select `main`.
5. Enter a new tag such as `v1.4.1-desktop`.
6. Choose whether the release is a draft or prerelease.
7. Run the workflow.

The workflow runs source checks, Rust tests, platform builds, installed-layout smoke tests, architecture checks and integrity hashing first. Only after every required job succeeds does a separate publishing job attach the verified release packages.

### Option B: publish from Git tag

```bash
git checkout main
git pull origin main
git tag v1.4.1-desktop
git push origin v1.4.1-desktop
```

## Release gate

A stable release requires all automated gates in `docs/DESKTOP_TEST_PLAN.md` to pass and the real-device evidence below to be recorded:

- Windows 10 or 11: tray icon visible and interactive.
- Windows: repeated launch leaves one desktop shell and one backend.
- Windows: close-to-tray, restore, hide and quit behavior verified.
- macOS Apple Silicon, including M4: signed/notarized download opens without a damaged-app warning.
- macOS Intel: the same Universal package opens.
- Linux: application and tray/AppIndicator start correctly.
- Existing file upload, download, delete and share workflows work.

The GitHub Actions run must include SHA-256 evidence and platform test-evidence artifacts. A green package build without the runtime evidence is not sufficient for stable publication.

## Remaining release considerations

- Windows Authenticode signing is still recommended for a polished public release.
- Physical notification-area interaction and Gatekeeper first-launch behavior require real-device validation.
- The backend remains a child process rather than an in-process library.
- Existing backend data-storage limitations remain unchanged.
