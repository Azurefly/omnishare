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
    pub data_dir: Option<PathBuf>,
}

#[derive(Debug, Clone, Serialize)]
pub struct DesktopSettingsView {
    pub port: u16,
    pub configured: bool,
    pub source: String,
    pub data_dir: Option<PathBuf>,
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
    let data_dir = resolve_data_dir(app);
    if let Ok(raw) = env::var(PORT_ENV) {
        if let Ok(parsed) = raw.parse::<u16>() {
            if let Ok(port) = validate_port(parsed) {
                return DesktopSettingsView {
                    port,
                    configured: true,
                    source: PORT_ENV.to_string(),
                    data_dir,
                };
            }
        }
    }

    if let Some(path) = user_settings_path(app) {
        if let Some(port) = read_port(&path) {
            return DesktopSettingsView {
                port,
                configured: true,
                source: path.display().to_string(),
                data_dir,
            };
        }
    }

    if let Some(path) = installation_settings_path() {
        if let Some(port) = read_port(&path) {
            return DesktopSettingsView {
                port,
                configured: true,
                source: path.display().to_string(),
                data_dir,
            };
        }
    }

    DesktopSettingsView {
        port: DEFAULT_BACKEND_PORT,
        configured: false,
        source: "default".to_string(),
        data_dir,
    }
}

pub fn save_user_port(app: &AppHandle, port: u16) -> Result<PathBuf, String> {
    let port = validate_port(port)?;
    let path = user_settings_path(app).ok_or_else(|| {
        "Unable to resolve the OmniShare desktop configuration directory.".to_string()
    })?;
    let mut settings = read_settings(&path).unwrap_or_default();
    settings.port = Some(port);
    write_settings(&path, &settings)?;
    Ok(path)
}

fn write_settings(path: &Path, settings: &DesktopSettings) -> Result<(), String> {
    let content = serde_json::to_string_pretty(settings).map_err(|err| err.to_string())?;
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|err| err.to_string())?;
    }
    fs::write(path, format!("{}\n", content)).map_err(|err| err.to_string())
}

pub fn resolve_data_dir(app: &AppHandle) -> Option<PathBuf> {
    if let Some(path) = env::var_os("OMNISHARE_DATA_DIR").filter(|path| !path.is_empty()) {
        return Some(PathBuf::from(path));
    }
    user_settings_path(app)
        .and_then(|path| read_settings(&path))
        .and_then(|settings| settings.data_dir)
}

pub fn save_user_data_dir(app: &AppHandle, requested: &str) -> Result<(), String> {
    let text = requested.trim();
    let text = ['"', '\'']
        .iter()
        .find_map(|quote| text.strip_prefix(*quote)?.strip_suffix(*quote))
        .unwrap_or(text);
    let directory = if text.to_ascii_lowercase().starts_with("file:") {
        tauri::Url::parse(text)
            .ok()
            .filter(|url| matches!(url.host_str(), None | Some("localhost")))
            .and_then(|url| url.to_file_path().ok())
            .ok_or("请输入本机文件夹的路径。")?
    } else if let Some(relative) = text.strip_prefix("~/") {
        app.path()
            .home_dir()
            .map_err(|e| e.to_string())?
            .join(relative)
    } else {
        PathBuf::from(text)
    };
    if !directory.is_absolute() {
        return Err("请输入当前设备上的完整文件夹路径。".to_string());
    }
    fs::create_dir_all(&directory).map_err(|err| format!("无法打开存储文件夹：{err}"))?;
    if !directory.is_dir() {
        return Err("请选择文件夹作为存储位置。".to_string());
    }
    let path = user_settings_path(app).ok_or("无法打开本机配置目录。")?;
    let mut settings = read_settings(&path).unwrap_or_default();
    settings.data_dir = Some(directory);
    write_settings(&path, &settings)
}

pub fn find_available_port(preferred: u16) -> Result<u16, String> {
    let preferred = validate_port(preferred)?;
    if port_is_available(preferred) {
        return Ok(preferred);
    }

    let upper = preferred.saturating_add(PREFERRED_SCAN_LIMIT);
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
    validate_port(read_settings(path)?.port?).ok()
}

fn read_settings(path: &Path) -> Option<DesktopSettings> {
    serde_json::from_str(&fs::read_to_string(path).ok()?).ok()
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

#[cfg(test)]
mod tests {
    use std::{
        fs,
        net::TcpListener,
        path::PathBuf,
        time::{SystemTime, UNIX_EPOCH},
    };

    use super::{find_available_port, read_port, validate_port};

    fn unique_temp_file(name: &str) -> PathBuf {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock before unix epoch")
            .as_nanos();
        std::env::temp_dir().join(format!(
            "omnishare-desktop-test-{}-{}-{}",
            std::process::id(),
            nonce,
            name
        ))
    }

    #[test]
    fn rejects_reserved_ports() {
        assert!(validate_port(0).is_err());
        assert!(validate_port(1023).is_err());
        assert_eq!(validate_port(1024), Ok(1024));
        assert_eq!(validate_port(65535), Ok(65535));
    }

    #[test]
    fn keeps_an_available_preferred_port() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind ephemeral port");
        let port = listener.local_addr().expect("local addr").port();
        drop(listener);
        assert_eq!(find_available_port(port), Ok(port));
    }

    #[test]
    fn skips_an_occupied_preferred_port() {
        let listener = TcpListener::bind(("127.0.0.1", 0)).expect("bind occupied port");
        let occupied = listener.local_addr().expect("local addr").port();
        let selected = find_available_port(occupied).expect("find fallback port");
        assert_ne!(selected, occupied);
        assert!(selected >= 1024);
    }

    #[test]
    fn reads_only_valid_persisted_ports() {
        let path = unique_temp_file("settings.json");
        fs::write(&path, "{\"port\": 19081}\n").expect("write valid settings");
        assert_eq!(read_port(&path), Some(19081));

        fs::write(&path, "{\"port\": 80}\n").expect("write invalid settings");
        assert_eq!(read_port(&path), None);

        fs::write(&path, "not-json").expect("write malformed settings");
        assert_eq!(read_port(&path), None);
        let _ = fs::remove_file(path);
    }
}
