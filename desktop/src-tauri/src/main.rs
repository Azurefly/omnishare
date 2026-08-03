#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod commands;
mod config;
mod process;
mod runtime;
mod tray;

use tauri::{Manager, WindowEvent};

use runtime::{manager::BackendState, startup};

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            tray::show_main_window(app);
        }))
        .setup(|app| {
            let settings = config::settings::resolve(app.handle());
            app.manage(BackendState::new(settings.port));
            tray::setup(app.handle())?;

            // Do not depend on WebView JavaScript to start the local service.
            // Managed deployments and returning users already have a configured
            // port, so the native runtime brings the backend up before the page
            // asks for status. First launch remains interactive.
            if settings.configured {
                let backend_state = app.state::<BackendState>();
                if let Err(error) =
                    startup::initialize(app.handle(), backend_state.inner(), settings.port)
                {
                    eprintln!("OmniShare native backend startup failed: {error}");
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
