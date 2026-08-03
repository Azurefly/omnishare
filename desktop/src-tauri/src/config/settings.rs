use std::{
    env, fs,
    net::TcpListener,
    path::{Path, PathBuf},
};

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager};

use crate::process::backend::{DEFAULT_BACKEND_HOST, DEFAULT_BACKEND_PORT};

pub const PORT_ENV: &str = "OMNISHARE_PORT";
pub const SETTINGS_FILE_NAME: &str = "omnishare-desktop.json";
const MIN_PORT: u16 = 1024;
const MAX_PORT: u16 = 65535;
const PREFERRED_SCAN_LIMIT: u16 = 100;

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct DesktopSettings {
    pub port: Option<u16>,
}

#[derive(Debug, Clone, Serialize)]
pub struct DesktopSettingsView {
    pub port: u16,
    pub configured: bool,
    pub source: String,
}

pub fn validate_port(port: u16) -> Result<u16, String> {
    if port < MIN_PORT {
        return Err(format!(
            "Port {} is reserved. Choose a port between {} and {}.",
            port, MIN_PORT, MAX_PORT
        ));
    }
    Ok(port)
}

pub fn resolve(app: &AppHandle) -> DesktopSettingsView {
    if let Ok(raw) = env::var(PORT_ENV) {
        if let Ok(port) = raw.parse::<u16>().and_then(|port| {
            validate_port(port).map_err(|_| std::num::ParseIntError::from(std::num::IntErrorKind::InvalidDigit))
        }) {
            return DesktopSettingsView {
                port,
                configured: true,
                source: PORT_ENV.to_string(),
            };
        }
    }

    if let Some(path) = user_settings_path(app) {
        if let Some(port) = read_port(&path) {
            return DesktopSettingsView {
                port,
                configured: true,
                source: path.display().to_string(),
            };
        }
    }

    if let Some(path) = installation_settings_path() {
        if let Some(port) = read_port(&path) {
            return DesktopSettingsView {
                port,
                configured: true,
                source: path.display().to_string(),
            };
        }
    }

    DesktopSettingsView {
        port: DEFAULT_BACKEND_PORT,
        configured: false,
        source: "default".to_string(),
    }
}

pub fn save_user_port(app: &AppHandle, port: u16) -> Result<PathBuf, String> {
    let port = validate_port(port)?;
    let path = user_settings_path(app)
        .ok_or_else(|| "Unable to resolve the OmniShare desktop configuration directory.".to_string())?;
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    let content = serde_json::to_string_pretty(&DesktopSettings { port: Some(port) })
        .map_err(|err| err.to_string())?;
    fs::write(&path, format!("{}\n", content)).map_err(|err| err.to_string())?;
    Ok(path)
}

pub fn find_available_port(preferred: u16) -> Result<u16, String> {
    let preferred = validate_port(preferred)?;
    if port_is_available(preferred) {
        return Ok(preferred);
    }

    let upper = preferred.saturating_add(PREFERRED_SCAN_LIMIT).min(MAX_PORT);
    for port in preferred.saturating_add(1)..=upper {
        if port_is_available(port) {
            return Ok(port);
        }
    }

    for port in 49152..=MAX_PORT {
        if port_is_available(port) {
            return Ok(port);
        }
    }

    Err("No available local TCP port was found for the OmniShare backend.".to_string())
}

fn port_is_available(port: u16) -> bool {
    TcpListener::bind((DEFAULT_BACKEND_HOST, port)).is_ok()
}

fn read_port(path: &Path) -> Option<u16> {
    let content = fs::read_to_string(path).ok()?;
    let settings = serde_json::from_str::<DesktopSettings>(&content).ok()?;
    validate_port(settings.port?).ok()
}

fn user_settings_path(app: &AppHandle) -> Option<PathBuf> {
    app.path()
        .app_config_dir()
        .ok()
        .map(|dir| dir.join(SETTINGS_FILE_NAME))
}

fn installation_settings_path() -> Option<PathBuf> {
    env::current_exe()
        .ok()
        .and_then(|path| path.parent().map(|parent| parent.join(SETTINGS_FILE_NAME)))
}
