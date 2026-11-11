# OmniShare Desktop Release Guide

This document describes Tauri 2 desktop validation and the shared release workflow. Public macOS packages currently run the Go backend and open the browser UI; they are separate from the Tauri Universal validation artifacts.

**Publication status:** the public release is still `v0.1.1`. The installation repairs and acceptance gates described below are source changes for a future release. They do not replace existing downloads or establish a successful native CI run. macOS production signing credentials are still required. See [installation validation and its limits](INSTALLATION_VALIDATION.md) for the evidence required before claiming support.

## Architecture

The desktop edition keeps the existing Go backend as the source of truth for storage, transfer, discovery, security and HTTP APIs. Tauri owns the native desktop lifecycle:

1. Start the desktop WebView.
2. Read the configured local service port.
3. Detect and attach to an existing healthy OmniShare backend on that port.
4. If the configured port is occupied by another service, select and persist an available local port.
5. Resolve the bundled OmniShare backend binary.
6. Start an owned backend with `--no-browser --listen 127.0.0.1 --port <selected-port> --shutdown-on-stdin-close` and a private stdin pipe.
7. Wait until the selected endpoint serves the OmniShare application.
8. Navigate the WebView to the local OmniShare UI.
9. Keep the app in the tray when the main window is closed.
10. Route the native Quit menu, `Ctrl+Q` / `Cmd+Q`, and tray Quit through the same shutdown path. Close the owned child's stdin so it can flush pending writes and exit; prevent the watchdog from restarting it during shutdown. An already running backend that the desktop attached to remains running.

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

Current Linux CI builds desktop binaries on Ubuntu 22.04. The DEB declares `libc6 (>= 2.35)` in addition to Tauri's automatically detected runtime dependencies; installation acceptance inspects both installed executables for a glibc requirement no higher than 2.35. Ubuntu 22.04 and 24.04 are the explicit acceptance targets. This is a baseline for the new build, not a compatibility claim for existing `v0.1.1` binaries or every Linux distribution. macOS requires version 12 or newer.

## Local validation

Desktop source contracts:

```bash
npm --prefix desktop ci
npm --prefix desktop run test:contracts
```

Rust formatting, static analysis and unit tests:

```bash
npm --prefix desktop run prebuild
cargo fmt --manifest-path desktop/src-tauri/Cargo.toml --check
cargo clippy --manifest-path desktop/src-tauri/Cargo.toml --all-targets --all-features -- -D warnings
cargo test --manifest-path desktop/src-tauri/Cargo.toml --all-features
```

Windows validation installs and uninstalls the real MSI and NSIS packages. Use a disposable Windows test machine with PowerShell 7, Node.js and Python, an interactive desktop session, and administrator privileges for MSI. It refuses a pre-existing OmniShare installation, desktop settings file or default data directory. On a clean local test machine, after building both bundles:

```powershell
.\desktop\tests\windows-webview-runtime-evidence.ps1 `
  -BundleRoot .\desktop\src-tauri\target\release\bundle `
  -EvidenceRoot .\desktop-test-evidence\Windows
.\desktop\tests\windows-test-support.test.ps1
node --test .\desktop\tests\windows-webview-probe.test.mjs
.\desktop\tests\windows-install-smoke.ps1 `
  -BundleRoot .\desktop\src-tauri\target\release\bundle `
  -EvidenceRoot .\desktop-test-evidence\Windows `
  -AllowLocalInstall
```

CI invokes the same checks through `windows-runtime-smoke-ci.ps1`. The actual installation path uses MSI `/i`, not administrative extraction with `/a`. The older runtime and watchdog scripts can still inspect an extracted layout when invoked alone, but that fallback is not the actual installation evidence accepted by the release gate.

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

The Windows job verifies the complete offline WebView2 payloads, then separately installs the MSI and NSIS packages. MSI must register a machine installation in `%ProgramFiles%\OmniShare`; NSIS must register a current-user installation in `%LOCALAPPDATA%\OmniShare`. The test checks the product registration, executable layout and shortcuts, a visible process-owned native window, and the actual WebView2 page through a private loopback CDP connection.

Each package must complete first-run port configuration, the shared core feature exercise, native `Ctrl+Q`, disappearance of its owned backend, a fresh WebView session with authenticated persisted content, the post-restart feature checks, another normal Quit, and real uninstall without deleting business data. The MSI case additionally runs port-conflict fallback, existing-backend attachment, single-instance, icon and watchdog checks. Attachment tests require an independently started backend to survive desktop Quit. Emergency cleanup cannot satisfy a normal-Quit check.

The shared core probe executes seven exercise checks and five restart checks. Private access keys and feature state remain in a temporary directory; public reports embed only the sanitized check results. Windows evidence includes `windows-install-smoke.json`, `webview-offline-runtime.json`, screenshots and the existing runtime/watchdog reports. See [the check definitions and evidence scope](INSTALLATION_VALIDATION.md).

The publication workflow separately downloads this run's Ubuntu 22.04-built DEB and AppImage into fresh Ubuntu 22.04 and 24.04 jobs. These jobs install the DEB through APT, validate its desktop entry and glibc baseline, and use real GTK/WebKit under Xvfb and D-Bus. Both the DEB and the AppImage must render first-run and persisted content, exercise the real backend, exit through the native menu and restart with their data intact. The AppImage test explicitly uses `--appimage-extract-and-run`; it does not establish FUSE mounting. DEB removal must preserve business data. RPM remains a build artifact without an equivalent native installation acceptance test.

## Public macOS distribution

The published `v0.1.1` macOS ZIPs contain a Go server application sealed with an ad-hoc signature. They have no Developer ID identity or Apple notarization, and can still be blocked after a browser download. Their native packaging and launch tests do not establish first-download Gatekeeper acceptance. See [macOS installation and diagnostics](MACOS.md).

The next release pipeline prepares a Developer ID signed, notarized and stapled DMG for drag-to-Applications installation, with a ZIP of the same application as a fallback. This is an unreleased pipeline change: it does not replace `v0.1.1` assets, change its tag, or mean that a new notarized package is already available. The package remains the Go backend with a browser UI; Tauri macOS artifacts remain validation-only.

Configure these five repository Actions secrets before preparing the next release:

```text
APPLE_CERTIFICATE
APPLE_CERTIFICATE_PASSWORD
APPLE_TEAM_ID
APPLE_ID
APPLE_APP_SPECIFIC_PASSWORD
```

`APPLE_CERTIFICATE` is the Base64 encoding of a password-protected P12 containing the Developer ID Application certificate and its private key. Create and export that identity on the maintainer's own Mac. The last secret is an Apple app-specific password, not the account's primary password. Do not send secret values through chat, commits, issues or logs. The complete operator procedure is in [macOS signing and notarization](MACOS_SIGNING.md).

The main-only credential readiness check reports missing secret names. It does not import a certificate, authenticate with Apple, sign code or publish a release. Passing this check proves that configuration values are present; the release job still has to validate the actual identity and credentials.

Native Intel and Apple silicon jobs use:

```text
bash scripts/package-macos.sh <binary> <output-dir> <amd64|arm64> --mode release
```

Release mode is the default and must stop if signing or notarization cannot complete. It requires a matching Developer ID Application identity, Hardened Runtime, a secure timestamp, accepted app and DMG notarizations, and valid stapled tickets. The final ZIP is recreated after stapling its app. The exact final ZIP and DMG are extracted or mounted, verified and launched; Gatekeeper checks, resource-tampering rejection, health/version, UI and shutdown results are tied to their SHA-256 digests.

PR jobs explicitly use `--mode rehearsal`. Those ad-hoc artifacts are internal validation outputs and do not receive production signing credentials. The publication gate rejects rehearsal evidence for an actual release.

Command-line policy checks, including `syspolicy_check distribution` on macOS 14 or newer, are useful release gates but are not a real first-download test. Follow [Apple's notarized-product test procedure](https://developer.apple.com/forums/thread/130560): use a fresh Mac or restored VM, download with Safari, preserve quarantine, install as a user would, and separately test offline ticket availability. Native CI evidence always keeps `first_download_verified: false`; record real Safari acceptance separately. A normal first-open confirmation is expected; the acceptance target does not require an Open Anyway exception and cannot override managed-device policy.

## Publishing

`Publish Release`, defined in `.github/workflows/publish-desktop-prerelease.yml`, is the single publication workflow. Keep `VERSION`, application metadata, `release/desktop-release.json`, and the matching release notes consistent. Merge the reviewed manifest change to `main` after its checks pass; that push starts the release build.

Keep the current `0.1.1` version and manifest unchanged while configuring and reviewing the new signing pipeline. Once credentials and validation are ready, prepare a new patch release, such as `0.1.2`, with its own version, manifest and release notes. Do not rerun the old version on a different commit to test the new pipeline. An existing release tag must point to the exact source commit or publication stops; tags and previously published assets are not rewritten to relabel a new build.

The workflow builds Windows/Linux packages and all six Go targets. Native macOS jobs produce `OmniShare-v<version>-macos-<arch>.zip` and `.dmg` for each supported architecture. The aggregation job checks all required assets, packaging and runtime evidence, release signing mode, notarization and policy gates before publishing. It also validates the actual Windows and Ubuntu installation reports against this run's installer bytes and their embedded exercise/restart checks. Missing, unsuccessful, mismatched or credential-bearing reports stop publication.

The next release manifest requires **29 public assets**. Its final `SHA256SUMS.txt` contains **28 entries**, covering every other public file, including the Windows/Linux desktop installers and the three installation reports: `omnishare-windows-installation.json`, `omnishare-ubuntu-22.04-installation.json` and `omnishare-ubuntu-24.04-installation.json`. Checksums are written only after all platform gates pass. These counts describe the new pipeline; the existing `v0.1.1` release retains its original attachments.

The publisher then verifies public asset names, sizes and SHA-256 digests. A manual run on `main` is available for the same exact release commit when a retry is appropriate; it does not bypass these gates.

`Desktop Validation` builds the Tauri Windows/Linux/macOS Universal artifacts and runs its desktop checks for pull requests. It does not publish GitHub Releases, and pushing an arbitrary tag does not trigger publication.

## Additional Tauri desktop acceptance

The following real-device checks remain necessary before claiming complete desktop distribution readiness. The new automated install/GUI/restart checks are prerequisites; their results must come from the exact candidate run and must not be inferred from a build succeeding:

- Windows 10 and 11: notification-area interaction, close-to-tray, restore and hide on a user's physical desktop.
- Windows: a clean machine without WebView2, a disconnected installation, and a full current-user install/run/uninstall under a non-administrator account. A hosted runner with WebView2 already installed cannot establish these claims.
- macOS Intel and Apple silicon: actual Safari download, quarantine retention, drag-to-Applications and first launch of the signed/notarized distribution. Tauri Universal build artifacts are not that public distribution.
- Linux: FUSE-mounted AppImage startup, Wayland/X11 desktop and tray differences, plus RPM and distributions outside the Ubuntu 22.04/24.04 matrix.

The GitHub Actions run must include SHA-256 evidence and platform test-evidence artifacts. A green package build without the runtime evidence is not sufficient for stable publication.

## Remaining release considerations

- Windows Authenticode signing is still recommended for a polished public release.
- Physical notification-area interaction and Gatekeeper first-launch behavior require real-device validation.
- The backend remains a child process rather than an in-process library.
- Existing backend data-storage limitations remain unchanged.

### Pinned desktop packaging tools

Desktop CI uses `@tauri-apps/cli` 2.12.4 and the committed npm lockfile. Before Linux AppImage packaging, `scripts/prepare-linux-appimage-tools.py` downloads AppRun, linuxdeploy, the AppImage plugin and its AppImage runtime from their official GitHub Release Asset APIs. Each asset is pinned by asset ID, byte count and SHA-256; only verified bytes are atomically moved into the tool cache. GTK and GStreamer helper scripts remain embedded in the pinned CLI.

The Linux preparation step exposes the verified runtime's absolute path as `runtime_path`. CI passes it through `LDAI_RUNTIME_FILE`, and fails before the build if it is empty, relative or missing. The plugin forwards this local file to `appimagetool --runtime-file`, so final AppImage packaging does not download a runtime from the moving `continuous` release. Local maintainers should set `LDAI_RUNTIME_FILE` to the absolute runtime path printed by preparation before invoking the desktop build.

The default cache is `${XDG_CACHE_HOME:-$HOME/.cache}/tauri`. The preparation step rejects unsupported CLI versions or a different local-tools-directory configuration. Tauri deliberately changes three bytes of the cached linuxdeploy before execution; a later preparation run therefore replaces a changed cache entry with a freshly verified original. Upgrading the CLI requires checking its tool versions and cache layout and updating this pinned list together. The existing build, runtime and release checks still run after preparation.

Windows CI runs `scripts/prepare-windows-bundle-tools.py` before packaging. It obtains WiX 3.14, NSIS 3.13 and the Tauri NSIS utility plugin through pinned official GitHub asset APIs, verifies byte counts and SHA-256, and stages the expected `WixTools314` and `NSIS` trees below `%LOCALAPPDATA%/tauri`. ZIP extraction rejects path escapes, links and Windows filename collisions; incomplete trees cannot replace an installed cache. The CLI's required files and NSIS compatibility hashes are checked before use. The downloader is shared with Linux, while platform and cache-layout checks stay separate.

Windows bundles now explicitly use `webviewInstallMode: { type: "offlineInstaller", silent: true }`, while NSIS uses `installMode: "currentUser"`. The complete WebView2 runtime still needs to be downloaded during a connected build. It is embedded for installation rather than relying on a small bootstrapper to fetch the runtime at the user's machine; see [Tauri's Windows installer modes](https://v2.tauri.app/distribute/windows-installer/) and [Microsoft's WebView2 distribution guidance](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution).

After bundling, `windows-webview-runtime-evidence.ps1` checks the exact CLI 2.12.4 cache layouts: MSI uses `target/release/wix/x64/x64/<GUID>/MicrosoftEdgeWebView2RuntimeInstallerX64.exe`, while NSIS uses `%LOCALAPPDATA%/tauri/x64/<GUID>/MicrosoftEdgeWebView2RuntimeInstallerX64.exe`. It rejects missing, ambiguous or linked payloads, requires a 64–512 MiB complete executable, and checks native Authenticode `Valid` with publisher `Microsoft Corporation`. The report records SHA-256, size and product version. Before any installer is executed, the installation test rechecks these signatures and unchanged cache/package bytes.

This evidence verifies the cached runtime payloads selected by the pinned bundler; it does not independently extract them from each finished installer. It also does not establish a cold-machine or offline installation, and Microsoft's runtime signature does not sign the OmniShare installer. The report preserves those limits explicitly. If the official runtime download or signature verification fails, the build/acceptance run stops; there is no automatic switch back to an online bootstrapper.

Pinned asset IDs and digests protect the expected contents; they cannot guarantee that an upstream project will retain an asset forever. If an asset is removed, preparation fails and a maintainer must verify and update the corresponding pins. There is no fallback to an unverified mirror or moving release tag.

CI supplies its built-in GitHub token only to the preparation step through `OMNISHARE_TOOL_GITHUB_TOKEN`, under the build job's existing `contents: read` permissions. Authentication is limited to the initial `api.github.com` release-asset request and is never forwarded to redirects. Local runs without this explicit variable use anonymous public requests. Authentication, permission and rate-limit errors stop immediately with a sanitized classification; only transient server or network failures are retried. No token or raw API error body is logged. This follows [GitHub's API authentication](https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api) and [rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api); no additional repository secret is needed for tool downloads.
