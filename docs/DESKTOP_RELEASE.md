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

## Verified release candidate

The first desktop release candidate was validated by GitHub Actions on all three supported desktop platforms.

```text
workflow: Desktop Release
run_id: 30740280360
source_head: fae6e282226a595ea886e2504d3a94ea9c38c50c
merged_to_main: 55e99b7fe0407c74c62451611ccd2c8f7e09fea6
release_branch: release/v1.4.0-desktop
```

Generated artifacts:

| Platform | Artifact | Size | Digest |
| --- | --- | ---: | --- |
| Windows | `omnishare-desktop-Windows` | 14,772,921 bytes | `sha256:cce0e377f0fb220dc229f230fe1f39243e8b0a1c8272eeba349bc5b6876b1315` |
| Linux | `omnishare-desktop-Linux` | 217,009,463 bytes | `sha256:691fb920b0e3480ab406639dcf848e3e80eefdda40d27af6556d45cc2e67c91e` |
| macOS | `omnishare-desktop-macOS` | 16,076,597 bytes | `sha256:ff29de5ddd0946578cfe0596d278095d0edc28260a57b9b4d06a1ef6b637f112` |

This verifies that the Tauri bundle step and artifact upload step succeeded on Windows, Linux and macOS. It does not replace real-device smoke testing, code signing or notarization.

## Publishing

There are two supported publishing paths.

### Option A: publish from GitHub Actions UI

Use this when you want to publish without creating a tag locally.

1. Open **Actions**.
2. Select **Desktop Release**.
3. Click **Run workflow**.
4. Select the branch to build, usually `release/v1.4.0-desktop` or `main`.
5. Enter a release tag, for example:

```text
v1.4.0-desktop
```

6. Choose whether the release should be a draft or prerelease.
7. Run the workflow.

When `release_tag` is provided, the workflow creates or updates the GitHub Release and attaches generated Windows, Linux and macOS bundle files.

### Option B: publish from Git tag

Create and push a tag that starts with `v`:

```bash
git checkout main
git pull origin main
git tag v1.4.0-desktop
git push origin v1.4.0-desktop
```

GitHub Actions will build all desktop bundles and attach generated bundle files to the release.

For a pre-release build, use:

```bash
git checkout release/v1.4.0-desktop
git pull origin release/v1.4.0-desktop
git tag v1.4.0-desktop-alpha.1
git push origin v1.4.0-desktop-alpha.1
```

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
