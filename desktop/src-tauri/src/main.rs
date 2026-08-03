#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod commands;
mod config;
mod process;
mod runtime;
mod tray;

use tauri::{Manager, WindowEvent};

use runtime::manager::BackendState;

fn main() {
    tauri::Builder::default()
        .setup(|app| {
            let settings = config::settings::resolve(app.handle());
            app.manage(BackendState::new(settings.port));
            tray::setup(app.handle())?;
            Ok(())
        })
        .on_window_event(|window, event| {
            if let WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .invoke_handler(tauri::generate_handler![
            health,
            commands::backend::desktop_settings,
            commands::backend::desktop_boot,
            commands::backend::backend_status,
            commands::backend::backend_start,
            commands::backend::backend_stop
        ])
        .run(tauri::generate_context!())
        .expect("error while running OmniShare desktop");
}

#[tauri::command]
fn health() -> String {
    "omnishare-desktop-ok".to_string()
}
