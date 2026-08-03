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

## macOS Apple Silicon distribution

The release workflow builds a Universal macOS package containing both:

- `arm64` for Apple Silicon, including M1, M2, M3 and M4.
- `x86_64` for Intel Macs.

Both the Tauri desktop executable and the bundled Go backend are verified with `lipo` before artifacts are uploaded.

### Required GitHub Actions secrets

A public macOS release must be signed with a `Developer ID Application` certificate and notarized by Apple. Configure these repository secrets:

| Secret | Purpose |
| --- | --- |
| `APPLE_CERTIFICATE` | Base64-encoded exported Developer ID `.p12` certificate |
| `APPLE_CERTIFICATE_PASSWORD` | Password used when exporting the `.p12` certificate |
| `APPLE_SIGNING_IDENTITY` | Optional explicit Developer ID signing identity; Tauri can infer it from the certificate |
| `APPLE_ID` | Apple ID used for notarization |
| `APPLE_PASSWORD` | App-specific password for the Apple ID |
| `APPLE_TEAM_ID` | Apple Developer team identifier |

The release workflow intentionally fails before publishing when mandatory macOS signing or notarization secrets are missing. Pull-request builds may remain unsigned because they are validation artifacts, not public releases.

For a formal release, the workflow verifies:

```bash
codesign --verify --deep --strict --verbose=2 OmniShare.app
spctl --assess --type execute --verbose=2 OmniShare.app
```

A successful public artifact must pass both checks. This prevents publishing a package that macOS Gatekeeper reports as damaged or unable to open.

### Existing unsigned build workaround

For diagnosis only, a user can remove quarantine from a trusted locally-built application:

```bash
xattr -dr com.apple.quarantine /Applications/OmniShare.app
```

This is not a release fix and must not be presented as the normal installation process. The correct fix is Developer ID signing and Apple notarization.
