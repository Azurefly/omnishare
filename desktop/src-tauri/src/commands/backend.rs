//! Tauri commands for OmniShare backend control.

#[tauri::command]
pub fn backend_status() -> String {
    "starting".to_string()
}

#[tauri::command]
pub fn backend_start() -> bool {
    // Backend process spawning will be wired after platform path detection.
    true
}

#[tauri::command]
pub fn backend_stop() -> bool {
    true
}
