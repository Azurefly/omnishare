# OmniShare Desktop v1.0.1

`v1.0.1-desktop` is a desktop prerelease hotfix for backend availability and duplicate local-device presentation.

## Fixed

### Backend service recovery

- Added a native Tauri watchdog for the bundled Go backend.
- The watchdog requires two consecutive failed health probes before recovery, avoiding restarts on brief transient delays.
- An unexpectedly exited or unhealthy managed backend is detached and restarted automatically.
- An intentional **Stop backend** action remains respected and is not undone by the watchdog.
- The frontend retries idempotent API requests briefly, changes the connection badge to a recovery state, throttles repeated connection messages, and refreshes after service recovery.
- Raw browser `Failed to fetch` errors are replaced by a localized recovery message.

### Local device aggregation

- OmniShare now treats one computer as one product device instead of creating one card for every network-interface address.
- IPv4 APIPA and IPv6 link-local addresses are excluded from the primary device list.
- The preferred address is selected from the configured listener, operating-system default route, private LAN, Tailscale, other routable addresses, and loopback in that order.
- Duplicate node IDs and equivalent URLs are removed.
- The frontend includes a compatibility aggregation guard for cached responses from an older backend.

## Validation

The hotfix is release-blocked by the normal repository CI and enhanced Desktop Release matrix.

Windows installed-layout tests run the application from an administratively extracted MSI package and verify:

1. MSI layout, bundled backend and Windows executable icon.
2. Occupied preferred-port fallback.
3. Attachment to an existing OmniShare backend without spawning a duplicate.
4. Repeated desktop launch leaves one desktop shell.
5. `/api/v1/devices` returns exactly one non-link-local local product device.
6. Force-terminating the managed backend causes the desktop watchdog to start a replacement backend on the same endpoint while retaining one desktop shell and one backend process.

The verified recovery evidence from the implementation run recorded:

- Local product device count: `1`.
- Terminated backend PID: `2176`.
- Replacement backend PID: `3776`.
- Desktop process count after recovery: `1`.
- Backend process count after recovery: `1`.
- Recovery JUnit: `2 tests / 0 failures`.

Linux release checks include frontend and desktop contracts, Rust formatting, Clippy with warnings denied, Rust tests, Tauri bundle generation and SHA-256 evidence.

macOS validation builds a Universal application and verifies both the Tauri executable and bundled backend contain `arm64` and `x86_64` architectures. Public macOS distribution still requires Apple Developer ID signing and notarization credentials.

## Upgrade

Install `v1.0.1-desktop` over `v1.0.0-desktop`. User data is kept outside the installation directory and is not removed by the upgrade.
