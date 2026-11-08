# OmniShare Desktop Release Guide

This document describes Tauri 2 desktop validation and the shared release workflow. Public macOS packages currently run the Go backend and open the browser UI; they are separate from the Tauri Universal validation artifacts.

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

The workflow builds Windows/Linux packages and all six Go targets. Native macOS jobs produce `OmniShare-v<version>-macos-<arch>.zip` and `.dmg` for each supported architecture. The aggregation job checks all required assets, packaging and runtime evidence, release signing mode, notarization and policy gates before publishing. The publisher then verifies public asset names, sizes and SHA-256 digests. A manual run on `main` is available for the same exact release commit when a retry is appropriate; it does not bypass these gates.

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

### Pinned desktop packaging tools

Desktop CI uses `@tauri-apps/cli` 2.12.4 and the committed npm lockfile. Before Linux AppImage packaging, `scripts/prepare-linux-appimage-tools.py` downloads AppRun, linuxdeploy, the AppImage plugin and its AppImage runtime from their official GitHub Release Asset APIs. Each asset is pinned by asset ID, byte count and SHA-256; only verified bytes are atomically moved into the tool cache. GTK and GStreamer helper scripts remain embedded in the pinned CLI.

The Linux preparation step exposes the verified runtime's absolute path as `runtime_path`. CI passes it through `LDAI_RUNTIME_FILE`, and fails before the build if it is empty, relative or missing. The plugin forwards this local file to `appimagetool --runtime-file`, so final AppImage packaging does not download a runtime from the moving `continuous` release. Local maintainers should set `LDAI_RUNTIME_FILE` to the absolute runtime path printed by preparation before invoking the desktop build.

The default cache is `${XDG_CACHE_HOME:-$HOME/.cache}/tauri`. The preparation step rejects unsupported CLI versions or a different local-tools-directory configuration. Tauri deliberately changes three bytes of the cached linuxdeploy before execution; a later preparation run therefore replaces a changed cache entry with a freshly verified original. Upgrading the CLI requires checking its tool versions and cache layout and updating this pinned list together. The existing build, runtime and release checks still run after preparation.

Windows CI runs `scripts/prepare-windows-bundle-tools.py` before packaging. It obtains WiX 3.14, NSIS 3.13 and the Tauri NSIS utility plugin through pinned official GitHub asset APIs, verifies byte counts and SHA-256, and stages the expected `WixTools314` and `NSIS` trees below `%LOCALAPPDATA%/tauri`. ZIP extraction rejects path escapes, links and Windows filename collisions; incomplete trees cannot replace an installed cache. The CLI's required files and NSIS compatibility hashes are checked before use. The downloader is shared with Linux, while platform and cache-layout checks stay separate.

Pinned asset IDs and digests protect the expected contents; they cannot guarantee that an upstream project will retain an asset forever. If an asset is removed, preparation fails and a maintainer must verify and update the corresponding pins. There is no fallback to an unverified mirror or moving release tag.

CI supplies its built-in GitHub token only to the preparation step through `OMNISHARE_TOOL_GITHUB_TOKEN`, under the build job's existing `contents: read` permissions. Authentication is limited to the initial `api.github.com` release-asset request and is never forwarded to redirects. Local runs without this explicit variable use anonymous public requests. Authentication, permission and rate-limit errors stop immediately with a sanitized classification; only transient server or network failures are retried. No token or raw API error body is logged. This follows [GitHub's API authentication](https://docs.github.com/en/rest/authentication/authenticating-to-the-rest-api) and [rate-limit guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api); no additional repository secret is needed for tool downloads.
