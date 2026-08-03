use std::time::Duration;

use tauri::{AppHandle, Manager};

use crate::{
    config::{paths, settings},
    process::backend::DEFAULT_BACKEND_HOST,
    runtime::{
        health,
        manager::{BackendState, BackendStatus},
    },
};

pub fn initialize(
    app: &AppHandle,
    backend_state: &BackendState,
    preferred_port: u16,
) -> Result<BackendStatus, String> {
    let current = backend_state.status();
    if current.running {
        return Ok(current);
    }

    if health::is_omnishare_backend(DEFAULT_BACKEND_HOST, preferred_port) {
        settings::save_user_port(app, preferred_port)?;
        return backend_state.attach(preferred_port);
    }

    let selected_port = settings::find_available_port(preferred_port)?;
    backend_state.configure_port(selected_port)?;
    settings::save_user_port(app, selected_port)?;

    let executable = paths::resolve_backend_executable(app).ok_or_else(|| {
        let candidates = paths::backend_executable_candidates(app)
            .into_iter()
            .map(|path| format!("- {}", path.display()))
            .collect::<Vec<_>>()
            .join("\n");
        format!(
            "OmniShare backend binary was not found. Checked:\n{}",
            candidates
        )
    })?;

    let config_dir = app
        .path()
        .app_config_dir()
        .map_err(|error| format!("Unable to resolve desktop configuration directory: {error}"))?;
    let log_path = config_dir.join("logs").join("backend.log");
    let status = backend_state.start(&executable, &log_path)?;

    if health::wait_backend(DEFAULT_BACKEND_HOST, status.port, Duration::from_secs(20)) {
        Ok(status)
    } else {
        let diagnostics = backend_state.diagnostics();
        let _ = backend_state.stop();
        Err(format!(
            "OmniShare backend started from '{}' but did not become ready on {}:{} within 20 seconds.\n\n{}",
            executable.display(),
            DEFAULT_BACKEND_HOST,
            status.port,
            diagnostics
        ))
    }
}
