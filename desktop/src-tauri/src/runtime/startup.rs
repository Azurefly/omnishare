use std::time::Duration;

use tauri::AppHandle;

use crate::{
    config::paths,
    process::backend::{DEFAULT_BACKEND_HOST, DEFAULT_BACKEND_PORT},
    runtime::{health, manager::BackendState},
};

pub fn initialize(app: &AppHandle, backend_state: &BackendState) -> Result<String, String> {
    if health::wait_backend(
        DEFAULT_BACKEND_HOST,
        DEFAULT_BACKEND_PORT,
        Duration::from_millis(750),
    ) {
        return Ok(format!("http://{}:{}", DEFAULT_BACKEND_HOST, DEFAULT_BACKEND_PORT));
    }

    let executable = paths::resolve_backend_executable(app).ok_or_else(|| {
        let candidates = paths::backend_executable_candidates(app)
            .into_iter()
            .map(|path| format!("- {}", path.display()))
            .collect::<Vec<_>>()
            .join("\n");
        format!("OmniShare backend binary was not found. Checked:\n{}", candidates)
    })?;

    let status = backend_state.start(&executable)?;

    if health::wait_backend(
        DEFAULT_BACKEND_HOST,
        DEFAULT_BACKEND_PORT,
        Duration::from_secs(20),
    ) {
        Ok(status.url)
    } else {
        Err(format!(
            "OmniShare backend started from '{}' but did not become ready on {}:{} within 20 seconds.",
            executable.display(),
            DEFAULT_BACKEND_HOST,
            DEFAULT_BACKEND_PORT
        ))
    }
}
