//! Tauri commands for OmniShare backend control.

use tauri::{AppHandle, State};

use crate::{
    config::settings::{self, DesktopSettingsView},
    runtime::{
        manager::{BackendNavigation, BackendState, BackendStatus},
        startup,
    },
};

#[tauri::command]
pub fn desktop_settings(app: AppHandle, state: State<'_, BackendState>) -> DesktopSettingsView {
    let mut settings = settings::resolve(&app);
    settings.port = state.inner().status().port;
    settings
}

#[tauri::command(async)]
pub fn desktop_boot(
    app: AppHandle,
    state: State<'_, BackendState>,
    requested_port: Option<u16>,
) -> Result<BackendNavigation, String> {
    let preferred = requested_port.unwrap_or_else(|| settings::resolve(&app).port);
    startup::initialize(&app, state.inner(), preferred)?;
    state.inner().navigation()
}

#[tauri::command]
pub fn backend_status(state: State<'_, BackendState>) -> BackendStatus {
    state.inner().status()
}

#[tauri::command(async)]
pub fn backend_start(
    app: AppHandle,
    state: State<'_, BackendState>,
) -> Result<BackendStatus, String> {
    let preferred = settings::resolve(&app).port;
    startup::initialize(&app, state.inner(), preferred)
}

#[tauri::command(async)]
pub fn backend_stop(state: State<'_, BackendState>) -> BackendStatus {
    state.inner().stop()
}
