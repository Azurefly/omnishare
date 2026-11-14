#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod commands;
mod config;
mod process;
mod runtime;
mod tray;
mod updates;

use std::{thread, time::Duration};

use tauri::{AppHandle, Manager, RunEvent, WindowEvent};

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
                if backend_state.is_exiting() {
                    break;
                }
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

                if !backend_state.should_run() {
                    continue;
                }

                eprintln!(
                    "OmniShare backend watchdog detected an unhealthy service on port {}; restarting.",
                    status.port
                );

                if let Err(error) = startup::initialize(&app, backend_state.inner(), status.port) {
                    eprintln!("OmniShare backend watchdog restart failed: {error}");
                    // Keep escalating the backoff after a failed restart;
                    // otherwise the exponent stays at zero and the watchdog
                    // retries (and logs) every few seconds forever.
                    consecutive_failures = consecutive_failures.saturating_add(1);
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
        .plugin(tauri_plugin_updater::Builder::new().build())
        .setup(|app| {
            app.manage(updates::UpdateState::default());
            let settings = config::settings::resolve(app.handle());
            app.manage(BackendState::new(settings.port));
            tray::setup(app.handle())?;

            // Keep both platforms native: startup I/O must not freeze the
            // Windows WebView2 / macOS WebKit event loop. The page and this
            // worker share a serialized startup path, so they cannot race to
            // restart the same backend. First-run port selection stays local.
            if settings.configured {
                let handle = app.handle().clone();
                thread::Builder::new()
                    .name("omnishare-backend-startup".to_string())
                    .spawn(move || {
                        let state = handle.state::<BackendState>();
                        if let Err(error) =
                            startup::initialize(&handle, state.inner(), settings.port)
                        {
                            eprintln!("OmniShare native backend startup failed: {error}");
                        }
                    })?;
            }

            start_backend_watchdog(app.handle().clone());
            Ok(())
        })
        .on_window_event(|window, event| {
            if window.label() == "main" {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    api.prevent_close();
                    let _ = window.hide();
                }
            }
        })
        .invoke_handler(tauri::generate_handler![
            health,
            updates::open_updates,
            updates::updater_info,
            updates::check_update,
            updates::download_update,
            updates::install_update,
            commands::backend::desktop_settings,
            commands::backend::desktop_boot,
            commands::backend::backend_status,
            commands::backend::backend_start,
            commands::backend::backend_stop
        ])
        .build(tauri::generate_context!())
        .expect("error while building OmniShare desktop")
        .run(|app, event| {
            if matches!(event, RunEvent::ExitRequested { .. } | RunEvent::Exit) {
                // Tauri exits the process after this callback; managed state
                // destructors alone are not a reliable child cleanup hook.
                if let Some(state) = app.try_state::<BackendState>() {
                    state.shutdown();
                }
            }
        });
}

#[tauri::command]
fn health() -> String {
    "omnishare-desktop-ok".to_string()
}
