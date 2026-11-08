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

### Native installer version mapping

The application, Cargo, package.json and Tauri source version remain the same
SemVer (for example `1.3.0-rc2`). Use `npm run build`, not a direct `tauri build`,
to apply native package-manager version rules:

- MSI uses `major.minor.(patch * 1000 + rank)`. Alpha rank is `100 + N`, beta is
  `300 + N`, RC is `600 + N`, and stable is `999`. Thus `1.3.0-rc2` installs as
  MSI version `1.3.602`, followed by stable `1.3.999`. This intentionally uses
  three fields: Windows Installer ignores a fourth field for upgrade ordering.
  Existing older stable installers such as `1.2.0` sort before the new series.
  Major/minor must be at most 255, patch at most 64, and prerelease sequence at
  most 99. Supported prereleases are alphaN/betaN/rcN (also alpha.N, etc.).
  Unsupported versions fail before compilation rather than colliding silently.
- Linux builds DEB/AppImage using the original SemVer. RPM is bundled separately
  from the same compiled executable with the numeric core as RPM Version and
  `0.rc2` (or the corresponding alpha/beta suffix) as Release. Stable uses Release
  `1`, so prerelease RPMs upgrade to stable correctly. The application binary
  still reports the original version. Build metadata is not an upgrade counter.
- macOS keeps the original version and build flow.

`Desktop Validation` validates native installers without publishing them.
The shared `Publish Release` workflow uses its reviewed manifest to distinguish
stable releases from prereleases. macOS Tauri builds remain validation-only;
the public Go application signing and notarization requirements are documented
in [`docs/MACOS_SIGNING.md`](../docs/MACOS_SIGNING.md).
Run `npm run test:contracts` to check version mapping and platform build plans.
