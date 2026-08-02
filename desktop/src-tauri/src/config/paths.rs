//! Cross-platform desktop path helpers.

use std::path::PathBuf;

pub fn backend_binary_name() -> &'static str {
    if cfg!(target_os = "windows") {
        "omnishare.exe"
    } else {
        "omnishare"
    }
}

pub fn application_root() -> PathBuf {
    std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."))
}
