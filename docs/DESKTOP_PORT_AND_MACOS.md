# OmniShare Desktop Port and macOS Distribution

## Configurable local service port

OmniShare Desktop no longer assumes that TCP port `8081` is always available.

The desktop runtime resolves the preferred port in this order:

1. `OMNISHARE_PORT` environment variable.
2. Current-user `omnishare-desktop.json` in the Tauri application configuration directory.
3. `omnishare-desktop.json` next to the installed desktop executable.
4. Default port `8081`.

On first launch, when no configuration exists, OmniShare displays a port setup screen. The selected value is stored in the current-user configuration file.

Example configuration:

```json
{
  "port": 18081
}
```

The valid range is `1024` through `65535`.

If the preferred port is occupied, OmniShare scans the next 100 ports and then the dynamic/private port range. The actual selected port is persisted for the next launch.

### Enterprise or unattended installation

For managed deployments, use either method:

```text
OMNISHARE_PORT=18081
```

or deploy `omnishare-desktop.json` next to the installed executable. A per-user configuration overrides the installation-level file, and the environment variable overrides both.

## macOS distribution and validation

`Desktop Validation` checks the Tauri application and its bundled Go backend as Universal binaries containing `arm64` and `x86_64`. Those macOS artifacts are for validation; the public macOS download runs the Go backend and opens a browser, with separate Intel and Apple silicon packages.

The published `v0.1.1` ZIPs use ad-hoc integrity signatures and are not notarized. They can still be blocked after a browser download. Current installation status and read-only diagnostics are documented in [MACOS.md](MACOS.md).

The next formal distribution pipeline requires Developer ID Application signing, Hardened Runtime, secure timestamps, accepted Apple notarization, stapled tickets and Gatekeeper checks. Its target is a DMG that installs by dragging the app to Applications, plus a ZIP fallback. This pipeline change does not mean a new notarized version has already been published.

Configure exactly these repository Actions secrets using the local certificate-export procedure in [MACOS_SIGNING.md](MACOS_SIGNING.md):

- `APPLE_CERTIFICATE`: Base64-encoded P12 containing the Developer ID Application certificate and private key.
- `APPLE_CERTIFICATE_PASSWORD`: P12 export password.
- `APPLE_TEAM_ID`: the matching Apple Developer team.
- `APPLE_ID`: the account authorized for notarization.
- `APPLE_APP_SPECIFIC_PASSWORD`: that account's app-specific password.

Do not put their values in source, chat, issue reports or logs. A missing credential blocks formal publication; PR rehearsal artifacts remain ad-hoc and cannot pass the production publication gate.

Native automated tests check the final ZIP and DMG, signatures, policy assessments, actual application startup and shutdown. They keep `first_download_verified: false`. Real first-download acceptance requires a fresh Mac or restored VM, a Safari download with quarantine retained, and the normal installation path. A standard first-open confirmation can appear; formal acceptance does not use Open Anyway, and managed-device policy can impose additional restrictions.
