# OmniShare Desktop Release Guide

This document describes how to build and publish the Tauri 2 desktop edition of OmniShare.

## Architecture

The desktop edition keeps the existing Go backend as the source of truth for storage, transfer, discovery, security and HTTP APIs. Tauri owns the native desktop lifecycle:

1. Start the desktop WebView.
2. Resolve the bundled OmniShare backend binary.
3. Start the backend with `--no-browser --listen 127.0.0.1 --port 8081`.
4. Wait for `127.0.0.1:8081` to accept TCP connections.
5. Navigate the WebView to the local OmniShare UI.
6. Keep the app in the tray when the main window is closed.
7. Stop the child backend process when the desktop runtime exits.

## Local build

### Windows

```powershell
.\scripts\build-tauri-desktop.ps1
```

### Linux/macOS

```bash
./scripts/build-tauri-desktop.sh
```

The scripts perform the required build order:

1. `npm --prefix frontend run build`
2. `go build` for the local backend binary
3. copy the backend binary into `desktop/src-tauri/resources/`
4. install desktop dependencies
5. `npm --prefix desktop run build`

## CI build

The workflow is defined in:

```text
.github/workflows/desktop-release.yml
```

It builds on:

- `ubuntu-latest`
- `windows-latest`
- `macos-latest`

Artifacts are uploaded as:

```text
omnishare-desktop-Linux
omnishare-desktop-Windows
omnishare-desktop-macOS
```

## Publishing

Create and push a tag that starts with `v`:

```bash
git tag v1.4.0-desktop-alpha.1
git push origin v1.4.0-desktop-alpha.1
```

GitHub Actions will build all desktop bundles and attach generated bundle files to the release.

## Release gate

Before marking a desktop release stable, verify the following on real machines:

- Windows app opens without an external browser.
- macOS app opens without an external browser.
- Linux bundle opens without an external browser.
- The Go backend starts automatically.
- The WebView navigates to `http://127.0.0.1:8081`.
- Closing the main window hides it instead of terminating the app.
- The tray icon can reopen the main window.
- Quit from tray terminates the desktop runtime and child backend.
- Existing OmniShare file upload/download/share workflows still work.

## Known limitations

- The desktop bundle is not code signed or notarized yet.
- The backend is still a child process rather than an in-process library.
- Production installers need real-machine validation for each OS.
- Existing backend data-storage limitations remain unchanged.
