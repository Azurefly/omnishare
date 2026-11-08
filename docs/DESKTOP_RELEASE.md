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

## macOS Tauri desktop release requirement

This section describes the requirements for future public Tauri desktop installers. Current macOS downloads are Go server application ZIPs that open the Web interface; they are not the Tauri Universal validation artifacts. From v0.1.1, those ZIPs are assembled and sealed with ad-hoc signatures on native Intel and Apple silicon runners, then extracted and launched for verification. They still lack Developer ID trust and notarization; see [macOS installation and diagnostics](MACOS.md).

Public Tauri desktop installers require a Developer ID certificate and Apple notarization credentials before distribution. Pull-request Tauri builds remain unsigned validation artifacts and are not published by the release workflow.

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

These checks are required before enabling public Tauri macOS distribution. The current Publish Release workflow rejects Tauri DMG assets and separately verifies the ad-hoc signed Go server ZIPs; that verification does not claim Gatekeeper approval for browser downloads.

## Publishing

`Publish Release` is the single publication workflow. Keep `VERSION`, application metadata, `release/desktop-release.json`, and the matching release notes consistent. Merge the reviewed manifest change to `main` after its checks pass; that push starts the release build. A manual run of `Publish Release` on `main` is also available for retrying the same release commit.

The workflow builds Windows/Linux packages and all six Go targets. Separate native Intel and Apple silicon jobs assemble, seal, unpack, and launch the actual macOS server ZIPs. The aggregation job validates every required asset and the macOS evidence before publishing; the publisher verifies public asset names, sizes, and SHA-256 digests. An existing release tag must already point to the exact source commit or the workflow stops rather than replacing unrelated assets.

`Desktop Validation` builds the Tauri Windows/Linux/macOS Universal artifacts and runs its desktop checks for pull requests. It does not publish GitHub Releases, and pushing an arbitrary tag does not trigger publication.

## Additional Tauri desktop acceptance

The following real-device checks remain necessary before claiming complete Tauri desktop distribution readiness. They supplement the current automated release gates and are not claimed by the Go server ZIP smoke tests:

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
