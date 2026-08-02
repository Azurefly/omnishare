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
            app.manage(BackendState::default());
            tray::setup(app.handle())?;

            let state = app.state::<BackendState>();
            let window = app.get_webview_window("main");
            match runtime::startup::initialize(app.handle(), state.inner()) {
                Ok(url) => {
                    if let Some(window) = window {
                        let script = format!("window.location.replace({:?});", url);
                        let _ = window.eval(&script);
                    }
                }
                Err(err) => {
                    if let Some(window) = window {
                        let escaped = err.replace('`', "\\`");
                        let script = format!(
                            "document.body.innerHTML = `<main style=\"font-family: system-ui, sans-serif; padding: 32px; line-height: 1.5\"><h1>OmniShare backend failed to start</h1><pre style=\"white-space: pre-wrap; background: #111827; color: #f9fafb; padding: 16px; border-radius: 8px;\">{}</pre><p>Set OMNISHARE_BACKEND_BIN or build the desktop release package.</p></main>`;",
                            escaped
                        );
                        let _ = window.eval(&script);
                    }
                }
            }

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
