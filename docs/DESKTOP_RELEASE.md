# OmniShare Desktop Release Guide

This document describes how to build and publish the Tauri 2 desktop edition of OmniShare.

## Architecture

The desktop edition keeps the existing Go backend as the source of truth for storage, transfer, discovery, security and HTTP APIs. Tauri owns the native desktop lifecycle:

1. Start the desktop WebView.
2. Read the configured local service port.
3. If the configured port is occupied, select and persist an available local port.
4. Resolve the bundled OmniShare backend binary.
5. Start the backend with `--no-browser --listen 127.0.0.1 --port <selected-port>`.
6. Wait for the selected port to accept TCP connections.
7. Navigate the WebView to the local OmniShare UI.
8. Keep the app in the tray when the main window is closed.
9. Stop the child backend process when the desktop runtime exits.

Port configuration and managed-deployment options are documented in:

```text
docs/DESKTOP_PORT_AND_MACOS.md
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

## CI build

The workflow is defined in:

```text
.github/workflows/desktop-release.yml
```

It builds:

- Linux x86_64
- Windows x86_64
- macOS Universal (`arm64` + `x86_64`)

Artifacts are uploaded as:

```text
omnishare-desktop-Linux
omnishare-desktop-Windows
omnishare-desktop-macOS-universal
```

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

The workflow builds and validates all platforms first. Only after every build succeeds does a separate publishing job attach the verified assets to the GitHub Release.

### Option B: publish from Git tag

```bash
git checkout main
git pull origin main
git tag v1.4.1-desktop
git push origin v1.4.1-desktop
```

## Release gate

Before marking a desktop release stable, verify the following on real machines:

- First launch allows the local service port to be selected.
- A collision on the preferred port selects another available port.
- Windows opens without an external browser.
- macOS Apple Silicon opens without a damaged-app warning.
- macOS Intel opens from the same Universal package.
- Linux opens without an external browser.
- The Go backend starts automatically.
- The WebView navigates to the selected local URL.
- Closing the main window hides it instead of terminating the app.
- The tray icon can reopen the main window.
- Quit from tray terminates the desktop runtime and child backend.
- Existing OmniShare file upload/download/share workflows still work.

## Remaining release considerations

- Windows Authenticode signing is still recommended for a polished public release.
- Real-device smoke testing remains required even after CI packaging succeeds.
- The backend remains a child process rather than an in-process library.
- Existing backend data-storage limitations remain unchanged.
