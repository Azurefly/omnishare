//! Tauri commands for OmniShare backend control.

use tauri::{AppHandle, State};

use crate::{
    config::paths,
    runtime::manager::{BackendState, BackendStatus},
};

#[tauri::command]
pub fn backend_status(state: State<'_, BackendState>) -> BackendStatus {
    state.inner().status()
}

#[tauri::command]
pub fn backend_start(
    app: AppHandle,
    state: State<'_, BackendState>,
) -> Result<BackendStatus, String> {
    let executable = paths::resolve_backend_executable(&app).ok_or_else(|| {
        "OmniShare backend binary was not found. Set OMNISHARE_BACKEND_BIN or bundle the backend in src-tauri/resources.".to_string()
    })?;
    state.inner().start(&executable)
}

#[tauri::command]
pub fn backend_stop(state: State<'_, BackendState>) -> BackendStatus {
    state.inner().stop()
}
