//! Cross-platform desktop path helpers.

use std::path::PathBuf;

use tauri::{AppHandle, Manager};

pub fn backend_binary_name() -> &'static str {
    if cfg!(target_os = "windows") {
        "omnishare.exe"
    } else {
        "omnishare"
    }
}

pub fn backend_executable_candidates(app: &AppHandle) -> Vec<PathBuf> {
    let binary_name = backend_binary_name();
    let mut candidates = Vec::new();

    if let Ok(value) = std::env::var("OMNISHARE_BACKEND_BIN") {
        if !value.trim().is_empty() {
            candidates.push(PathBuf::from(value));
        }
    }

    if let Ok(resource_dir) = app.path().resource_dir() {
        candidates.push(resource_dir.join(binary_name));
        candidates.push(resource_dir.join("resources").join(binary_name));
        candidates.push(resource_dir.join("bin").join(binary_name));
    }

    if let Ok(current_exe) = std::env::current_exe() {
        if let Some(parent) = current_exe.parent() {
            candidates.push(parent.join(binary_name));
            candidates.push(parent.join("resources").join(binary_name));
            candidates.push(parent.join("bin").join(binary_name));
        }
    }

    if let Ok(cwd) = std::env::current_dir() {
        candidates.push(cwd.join("resources").join(binary_name));
        candidates.push(cwd.join("src-tauri").join("resources").join(binary_name));
        candidates.push(cwd.join("..").join("backend").join(binary_name));
        candidates.push(cwd.join("..").join("..").join("backend").join(binary_name));
    }

    candidates
}

pub fn resolve_backend_executable(app: &AppHandle) -> Option<PathBuf> {
    backend_executable_candidates(app)
        .into_iter()
        .find(|path| path.is_file())
}
