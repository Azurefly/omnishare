# OmniShare Tauri 2.x Migration Plan

## Goal

Upgrade OmniShare from a browser-based local application into a true cross-platform desktop application.

## Architecture

- Tauri 2.x provides desktop runtime and native integration.
- Existing Go backend remains the core service layer.
- Existing frontend is embedded through Tauri WebView.

## Migration Strategy

Phase 1: Desktop shell
- Create Tauri application.
- Start and manage OmniShare backend process.
- Embed local frontend.

Phase 2: Native integration
- System tray.
- Startup management.
- Notifications.
- File association.

Phase 3: Release engineering
- Windows installer.
- macOS DMG.
- Linux packages.
- GitHub Actions builds.

## Principles

Do not rewrite stable Go business logic. Keep protocol, storage, security and discovery capabilities while adding native desktop capabilities.