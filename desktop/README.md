# OmniShare Desktop

This directory contains the Tauri 2.x desktop application.

## Planned structure

```
desktop/
├── frontend/
├── src-tauri/
│   ├── commands/
│   ├── tray/
│   ├── process/
│   └── updater/
└── tauri.conf.json
```

## Runtime model

1. Tauri application starts.
2. Backend service is launched locally.
3. Desktop WebView loads OmniShare UI.
4. Closing the application manages backend lifecycle safely.

The Go backend remains the source of truth for storage, transfer and discovery.