# OmniShare Desktop

This directory contains the Tauri 2.x desktop application for OmniShare.

## Current status

The desktop shell is wired as a real Tauri application:

1. Tauri starts a local desktop WebView.
2. The runtime resolves the bundled Go backend binary.
3. The backend is launched with `--no-browser --listen 127.0.0.1 --port 8081`.
4. The runtime waits for `127.0.0.1:8081` to become reachable.
5. The WebView navigates to the existing OmniShare UI.
6. Closing the main window hides it to the tray.
7. Quitting from the tray exits the desktop runtime and stops the child backend process.

## Structure

```text
desktop/
├── frontend/                 # Loading page before backend is ready
├── package.json              # Tauri CLI scripts
└── src-tauri/
    ├── tauri.conf.json       # Canonical Tauri config
    ├── build.rs
    ├── resources/            # Backend binary copied here during release builds
    └── src/
        ├── commands/         # IPC commands
        ├── config/           # Path resolution
        ├── process/          # Backend process lifecycle
        ├── runtime/          # Startup and health checks
        ├── tray/             # Native tray behavior
        └── main.rs
```

## Build

Use the repository-level scripts so the frontend, Go backend and desktop wrapper are built in the right order.

### Windows

```powershell
.\scripts\build-tauri-desktop.ps1
```

### Linux/macOS

```bash
./scripts/build-tauri-desktop.sh
```

## Release

See [`docs/DESKTOP_RELEASE.md`](../docs/DESKTOP_RELEASE.md).

## Runtime override

For development, set `OMNISHARE_BACKEND_BIN` to point the desktop shell at an existing backend binary:

```bash
OMNISHARE_BACKEND_BIN=/path/to/omnishare npm --prefix desktop run dev
```

On Windows:

```powershell
$env:OMNISHARE_BACKEND_BIN = 'C:\path\to\omnishare.exe'
npm --prefix desktop run dev
```
