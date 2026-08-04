#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod commands;
mod config;
mod process;
mod runtime;
mod tray;

use std::{thread, time::Duration};

use tauri::{AppHandle, Manager, WindowEvent};

use process::backend::DEFAULT_BACKEND_HOST;
use runtime::{health, manager::BackendState, startup};

fn start_backend_watchdog(app: AppHandle) {
    let _ = thread::Builder::new()
        .name("omnishare-backend-watchdog".to_string())
        .spawn(move || {
            let mut consecutive_failures = 0u32;

            loop {
                thread::sleep(Duration::from_secs(5));

                let backend_state = app.state::<BackendState>();
                if !backend_state.should_run() {
                    consecutive_failures = 0;
                    continue;
                }

                let status = backend_state.status();
                if health::is_omnishare_backend(DEFAULT_BACKEND_HOST, status.port) {
                    consecutive_failures = 0;
                    continue;
                }

                consecutive_failures = consecutive_failures.saturating_add(1);
                if consecutive_failures < 2 {
                    continue;
                }

                eprintln!(
                    "OmniShare backend watchdog detected an unhealthy service on port {}; restarting.",
                    status.port
                );

                if let Err(error) = startup::initialize(&app, backend_state.inner(), status.port) {
                    eprintln!("OmniShare backend watchdog restart failed: {error}");
                } else {
                    consecutive_failures = 0;
                }

                let exponent = consecutive_failures.saturating_sub(2).min(5);
                let backoff_seconds = 1u64 << exponent;
                thread::sleep(Duration::from_secs(backoff_seconds));
            }
        });
}

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

            start_backend_watchdog(app.handle().clone());
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
