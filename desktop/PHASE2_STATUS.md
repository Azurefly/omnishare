# Phase 2 Desktop Integration

## Status

Phase 2 is implemented to release-candidate engineering state on branch `feature/tauri-desktop`.

## Completed

- Real backend child-process lifecycle using `std::process::Command`.
- Backend binary resolution from environment override, bundled resources and development paths.
- Backend startup arguments: `--no-browser --listen 127.0.0.1 --port 8081`.
- TCP readiness probe for `127.0.0.1:8081`.
- Desktop startup workflow that launches backend and navigates the WebView to OmniShare UI.
- Native tray menu: open, hide and quit.
- Main-window close behavior changed to hide-to-tray.
- Canonical Tauri 2 configuration under `desktop/src-tauri/tauri.conf.json`.
- Desktop package scripts via `desktop/package.json`.
- Windows and Unix local desktop build scripts.
- GitHub Actions desktop release workflow for Linux, Windows and macOS.
- Desktop release guide.

## Runtime flow

```text
OmniShare Desktop
  -> Tauri starts loading page
  -> Runtime checks 127.0.0.1:8081
  -> If unavailable, runtime starts bundled Go backend
  -> Runtime waits for TCP readiness
  -> WebView navigates to http://127.0.0.1:8081
  -> Window close hides to tray
  -> Tray quit exits runtime and stops child backend
```

## Release command

Local build:

```bash
./scripts/build-tauri-desktop.sh
```

Windows build:

```powershell
.\scripts\build-tauri-desktop.ps1
```

Tag release:

```bash
git tag v1.4.0-desktop-alpha.1
git push origin v1.4.0-desktop-alpha.1
```

## Remaining release gates

These must be validated by CI or real machines before calling the desktop edition stable:

- Linux Tauri dependency installation on GitHub Actions runner.
- Windows bundle starts and locates `omnishare.exe` from resources.
- macOS bundle starts and locates `omnishare` from resources.
- Tray behavior on all three platforms.
- Backend termination on tray quit.
- Existing upload/download/share workflows inside the WebView.
- Code signing and notarization are not implemented yet.
